package prometheus

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// RangeQuerier is the range-query surface QueryWithContainerFilterFallback
// needs. Both this package's Client and prom.Client satisfy it, so the HTTP
// handlers and the MCP diagnose bundle share one fallback behaviour.
type RangeQuerier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error)
}

// QueryWithContainerFilterFallback re-runs an empty query without the
// container!="" filter, which cri-docker and other setups need because their
// cAdvisor metrics carry no container label. It returns the result and the
// query that produced it.
//
// A fallback error is returned rather than swallowed: on a cluster whose
// cAdvisor lacks the label the retry IS the query that carries the data, so
// its failure is the caller's failure and not an empty window.
//
// Whichever answer is returned carries both queries' warnings and partial
// marks: an empty partial primary may be empty only because a shard was
// missing, and an empty partial fallback says the same of the retry.
func QueryWithContainerFilterFallback(
	ctx context.Context,
	client RangeQuerier,
	result *prom.QueryResult,
	query string,
	category prom.MetricCategory,
	start, end time.Time,
	step time.Duration,
	buildFallback func() string,
	logPrefix string,
) (*prom.QueryResult, string, error) {
	if len(result.Series) > 0 || !prom.CategoryUsesContainerFilter(category) {
		return result, query, nil
	}
	fallbackQuery := buildFallback()
	if fallbackQuery == "" || fallbackQuery == query {
		return result, query, nil
	}
	fallbackResult, err := client.QueryRange(ctx, fallbackQuery, start, end, step)
	if err != nil {
		log.Printf("[prometheus] %s, fallback query also failed: %v", logPrefix, err)
		return result, query, err
	}
	if len(fallbackResult.Series) == 0 {
		return withAnnotationsOf(result, fallbackResult), query, nil
	}
	log.Printf("[prometheus] %s, fallback without container filter succeeded", logPrefix)
	return withAnnotationsOf(fallbackResult, result), fallbackQuery, nil
}

// withAnnotationsOf returns a copy of res that also carries other's warnings,
// infos and partial mark.
func withAnnotationsOf(res, other *prom.QueryResult) *prom.QueryResult {
	if other == nil {
		return res
	}
	a, b := res.Annotations, other.Annotations
	if !b.Partial && len(b.Warnings) == 0 && len(b.Infos) == 0 {
		return res
	}
	out := *res
	out.Annotations = prom.QueryAnnotations{
		Warnings: append(slices.Clone(a.Warnings), b.Warnings...),
		Infos:    append(slices.Clone(a.Infos), b.Infos...),
		Partial:  a.Partial || b.Partial,
	}
	return &out
}
