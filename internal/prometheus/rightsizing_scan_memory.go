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

func recoverScanQuery(ctx context.Context, client rightsizingScanQuerier, batch []scanWorkload, scope ViewScope, family string, start, end time.Time, refusal error) (*prom.QueryResult, error) {
	parts := scanMemoryParts(refusal)
	if parts == 0 {
		return nil, refusal
	}
	var result prom.QueryResult
	calls := 0
	queryFor := func(workloads []scanWorkload) string { return buildScanQueries(workloads, scope, true)[family] }
	run := func(workloads []scanWorkload, from, to time.Time) error {
		part, err := runScanQueryParts(ctx, workloads, queryFor(workloads), queryFor, func(ctx context.Context, q string) (*prom.QueryResult, error) {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < rightsizingScanQueryTimeout {
				return nil, refusal
			}
			if calls >= maxScanMemoryRecoveryRequests {
				return nil, refusal
			}
			calls++
			return queryScanFamily(ctx, client, family, q, from, to)
		})
		if err == nil && part != nil {
			result.Series = append(result.Series, part.Series...)
		}
		return err
	}
	if parts == 1 {
		if err := run(batch, start, end); err != nil {
			return nil, err
		}
	} else if !instantScanFamily(family) {
		points := int(end.Sub(start)/rightsizingStep) + 1
		chunk := (points + parts - 1) / parts
		for i := 0; i < points; i += chunk {
			from := start.Add(time.Duration(i) * rightsizingStep)
			to := start.Add(time.Duration(min(i+chunk, points)-1) * rightsizingStep)
			if err := run(batch, from, to); err != nil {
				return nil, err
			}
		}
	} else {
		// Increasing a counter over shorter windows changes its reset and
		// extrapolation semantics. Keep the full window for safety signals.
		namespaces := batchNamespaces(batch)
		if len(namespaces) < parts {
			return nil, refusal
		}
		for group := range slices.Chunk(namespaces, (len(namespaces)+parts-1)/parts) {
			var workloads []scanWorkload
			for _, w := range batch {
				if slices.Contains(group, w.namespace) {
					workloads = append(workloads, w)
				}
			}
			if err := run(workloads, start, end); err != nil {
				return nil, err
			}
		}
	}
	return &result, nil
}
