package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const rightsizingScanQueryTimeout = 30 * time.Second

// Leave room for the request line on proxies with an 8 KiB limit.
const scanGETRequestTargetBytes = 7 * 1024

var rightsizingScanHTTPClient = &http.Client{}

// Keep ordinary scan reads on GET. For long URLs both query APIs accept
// the same parameters in a POST form.
type scanQueryTransport struct{ *prom.HTTPTransport }

func (t scanQueryTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if method == http.MethodGet {
		endpoint, err := url.Parse(t.Address() + path)
		if err != nil {
			return nil, err
		}
		if len(endpoint.RequestURI())+1+len(params.Encode()) > scanGETRequestTargetBytes {
			method = http.MethodPost
		}
	}
	return t.HTTPTransport.Do(ctx, method, path, params)
}

// scanQuerier runs a scan's queries on one store handle; conn is the
// connection it was taken from, which the scan's cluster scope check must
// have run on.
type scanQuerier struct {
	client *prom.Client
	at     time.Time
	conn   ConnectionSnapshot
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
	transport := c.newTransportLocked(c.baseURL, c.basePath, rightsizingScanHTTPClient, 0)
	if direct, ok := transport.(*prom.HTTPTransport); ok {
		transport = scanQueryTransport{direct}
	}
	return &scanQuerier{client: prom.NewClient(transport), at: at, conn: c.snapshotLocked()}, nil
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
