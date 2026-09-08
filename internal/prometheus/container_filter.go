package prometheus

import (
	"context"
	"log"
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
		return result, query, nil
	}
	log.Printf("[prometheus] %s, fallback without container filter succeeded", logPrefix)
	return fallbackResult, fallbackQuery, nil
}
