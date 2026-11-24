package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const maxScanMemoryRecoveryRequests = 4

// VictoriaMetrics reports both estimates in bytes. A single oversized request
// cannot succeed by waiting for its peers to finish.
var scanMemoryEstimate = regexp.MustCompile(`total available memory for concurrent requests: ([0-9]+) bytes; requested memory: ([0-9]+) bytes`)

func scanMemoryParts(err error) int {
	var upstream *prom.HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnprocessableEntity {
		return 0
	}
	var apiError struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(upstream.Body, &apiError) != nil || !strings.Contains(apiError.Error, "not enough memory for processing") {
		return 0
	}
	estimate := scanMemoryEstimate.FindStringSubmatch(apiError.Error)
	if estimate == nil {
		return 0
	}
	available, availableErr := strconv.ParseUint(estimate[1], 10, 64)
	requested, requestedErr := strconv.ParseUint(estimate[2], 10, 64)
	if availableErr != nil || requestedErr != nil || available == 0 || requested == 0 {
		return 0
	}
	parts := 1 + (requested-1)/available
	if parts > maxScanMemoryRecoveryRequests {
		return 0
	}
	return int(parts)
}

func instantScanFamily(family string) bool {
	return family == "restart_activity" || family == "termination_history"
}

func queryScanFamily(ctx context.Context, client rightsizingScanQuerier, family, query string, start, end time.Time) (*prom.QueryResult, error) {
	if instantScanFamily(family) {
		return client.Query(ctx, query)
	}
	return client.QueryRange(ctx, query, start, end, rightsizingStep)
}

// Recoveries run serially between normal query batches. Across a scan they may
// consume at most one normal query timeout, leaving time for later workloads.
type scanMemoryBudget struct{ spent time.Duration }

func (b *scanMemoryBudget) recover(ctx context.Context, client rightsizingScanQuerier, batch []scanWorkload, scope ViewScope, family string, start, end time.Time, refusal error) (*prom.QueryResult, error) {
	parts := scanMemoryParts(refusal)
	allowance := rightsizingScanQueryTimeout - b.spent
	if deadline, ok := ctx.Deadline(); ok {
		allowance = min(allowance, time.Until(deadline)-rightsizingScanQueryTimeout)
	}
	if parts == 0 || allowance <= 0 {
		return nil, refusal
	}
	started := time.Now()
	defer func() { b.spent += time.Since(started) }()
	retryCtx, cancel := context.WithTimeout(ctx, allowance)
	defer cancel()
	type request struct {
		query    string
		from, to time.Time
	}
	var plan []request
	queryFor := func(workloads []scanWorkload) string { return buildScanQueries(workloads, scope, true)[family] }
	// Plan the byte-limit splits too: never spend four requests on a family
	// whose fifth physical request would already exceed our recovery budget.
	queue := func(group []scanWorkload, from, to time.Time) error {
		_, err := runScanQueryParts(retryCtx, group, queryFor(group), queryFor, func(_ context.Context, q string) (*prom.QueryResult, error) {
			if len(plan) >= maxScanMemoryRecoveryRequests {
				return nil, refusal
			}
			plan = append(plan, request{q, from, to})
			return nil, nil
		})
		return err
	}
	if parts == 1 {
		if err := queue(batch, start, end); err != nil {
			return nil, err
		}
	} else if !instantScanFamily(family) {
		points := int(end.Sub(start)/rightsizingStep) + 1
		for i := range parts {
			first := i * points / parts
			last := (i+1)*points/parts - 1
			if err := queue(batch, start.Add(time.Duration(first)*rightsizingStep), start.Add(time.Duration(last)*rightsizingStep)); err != nil {
				return nil, err
			}
		}
	} else {
		// Counter resets and extrapolation depend on the full window. Split
		// whole workloads, preferring disjoint namespaces to shrink source reads.
		namespaces := batchNamespaces(batch)
		parts = min(parts, len(batch))
		for i := range parts {
			group := batch[i*len(batch)/parts : (i+1)*len(batch)/parts]
			if len(namespaces) >= parts {
				selected := namespaces[i*len(namespaces)/parts : (i+1)*len(namespaces)/parts]
				group = nil
				for _, w := range batch {
					if slices.Contains(selected, w.namespace) {
						group = append(group, w)
					}
				}
			}
			if err := queue(group, start, end); err != nil {
				return nil, err
			}
		}
	}
	var result prom.QueryResult
	for _, req := range plan {
		part, err := queryScanFamily(retryCtx, client, family, req.query, req.from, req.to)
		if err != nil {
			if retryCtx.Err() != nil && ctx.Err() == nil {
				return nil, refusal
			}
			return nil, err
		}
		if part != nil {
			result.Series = append(result.Series, part.Series...)
		}
	}
	return &result, nil
}
