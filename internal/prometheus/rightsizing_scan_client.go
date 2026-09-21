package prometheus

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const rightsizingScanQueryTimeout = 30 * time.Second

var rightsizingScanHTTPClient = &http.Client{}

type scanQuerier struct {
	client *prom.Client
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
	transport := prom.NewHTTPTransport(c.baseURL, c.basePath, rightsizingScanHTTPClient)
	transport.Headers = copyHeaders(c.headers)
	return &scanQuerier{client: prom.NewClient(transport), at: at}, nil
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
	ctx, cancel := context.WithTimeout(ctx, rightsizingScanQueryTimeout)
	defer cancel()
	return run(ctx)
}
