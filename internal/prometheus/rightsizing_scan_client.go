package prometheus

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const rightsizingScanQueryTimeout = 30 * time.Second

var rightsizingScanHTTPClient = &http.Client{Timeout: 45 * time.Second}

type scanQuerier struct {
	client *prom.Client
	valid  func() bool
	at     time.Time
}

func (c *Client) newScanQuerier(ctx context.Context, at time.Time) (*scanQuerier, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.retired || c.baseURL == "" {
		return nil, errors.New("metrics connection changed")
	}
	generation, epoch := c.discoveryGen, c.connectionEpoch
	transport := prom.NewHTTPTransport(c.baseURL, c.basePath, rightsizingScanHTTPClient)
	transport.Headers = copyHeaders(c.headers)
	return &scanQuerier{client: prom.NewClient(transport), at: at, valid: func() bool {
		return GetClient() == c && c.DiscoveryGeneration() == generation && c.backendEpoch() == epoch
	}}, nil
}

func (q *scanQuerier) Query(ctx context.Context, query string) (*prom.QueryResult, error) {
	return q.query(ctx, func(ctx context.Context) (*prom.QueryResult, error) { return q.client.QueryAt(ctx, query, q.at) })
}

func (q *scanQuerier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	return q.query(ctx, func(ctx context.Context) (*prom.QueryResult, error) {
		return q.client.QueryRange(ctx, query, start, end, step)
	})
}

func (q *scanQuerier) query(ctx context.Context, run func(context.Context) (*prom.QueryResult, error)) (*prom.QueryResult, error) {
	if !q.valid() {
		return nil, ErrRightsizingScanScopeChanged
	}
	ctx, cancel := context.WithTimeout(ctx, rightsizingScanQueryTimeout)
	defer cancel()
	start := time.Now()
	result, err := run(ctx)
	log.Printf("[rightsizing] query duration=%s failed=%t", time.Since(start).Round(time.Millisecond), err != nil)
	if !q.valid() {
		return nil, ErrRightsizingScanScopeChanged
	}
	return result, err
}
