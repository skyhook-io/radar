package openobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"

	"github.com/skyhook-io/radar/pkg/prom"
)

// ErrUnscopable is returned on a store with the old filter behaviour
// (OpenObserve before v1.2) for a metric whose stream has never had one of
// the forced cluster labels: such a store ignores the matcher and would answer
// with every cluster's data. It wraps ErrNotCollected, so callers that show
// "not collected" (or an empty result) for refused metrics keep working.
var ErrUnscopable = fmt.Errorf("openobserve: this OpenObserve version can't scope the metric to one cluster (upgrade to v1.2 or later): %w", ErrNotCollected)

// unscopableError names what could not be scoped. Its text starts like an
// ErrNotCollected error followed by the metric name, which callers parse.
type unscopableError struct{ what string }

func (e *unscopableError) Error() string {
	return ErrNotCollected.Error() + ": " + e.what + " (this OpenObserve version can't scope it to one cluster; upgrade to v1.2 or later)"
}

func (e *unscopableError) Is(target error) bool {
	return target == ErrUnscopable || target == ErrNotCollected
}

// SchemaFetcher returns the field (label column) names of a metric's stream
// in the OpenObserve organization. It returns an error wrapping
// ErrStreamNotFound when the organization has no stream for the metric.
type SchemaFetcher func(ctx context.Context, metric string) ([]string, error)

// ErrStreamNotFound is returned by a SchemaFetcher for a metric no one has
// written to the organization.
var ErrStreamNotFound = errors.New("openobserve: no stream for this metric")

// ErrSchemaUnreachable is returned by NewSchemaFetcher's fetcher when the
// schema endpoint answers 404 without OpenObserve's "stream not found" body,
// as a reverse proxy that exposes only the Prometheus API does. Errors
// wrapping it also wrap the *prom.HTTPError.
var ErrSchemaUnreachable = errors.New("openobserve: the stream schema endpoint isn't reachable at this address (HTTP 404); this OpenObserve version needs /api/<org>/streams as well as the Prometheus API")

type schemaUnreachableError struct{ http *prom.HTTPError }

func (e *schemaUnreachableError) Error() string   { return ErrSchemaUnreachable.Error() }
func (e *schemaUnreachableError) Unwrap() []error { return []error{ErrSchemaUnreachable, e.http} }

const (
	legacyScopableTTL   = 10 * time.Minute
	legacyUnscopableTTL = 2 * time.Minute
	// A metric without a stream can't leak today, but another cluster may
	// start writing it without the label at any time.
	legacyNoStreamTTL = 30 * time.Second
	legacyCacheMax    = 2048
	maxSchemaBytes    = 4 << 20
	// schemaFetchTimeout bounds a shared schema lookup, which runs detached
	// from the caller that started it.
	schemaFetchTimeout = 10 * time.Second
)

type legacyKey struct{ store, metric, label string }

type legacyEntry struct {
	scopable bool
	expires  time.Time
}

type legacyCall struct {
	done     chan struct{}
	scopable bool
	ttl      time.Duration
	err      error
}

// legacyCache is shared by every RewritingTransport, keyed by the store's
// address (which names the organization), so a transport built per request
// (Hub's relay) still reuses schema lookups. Whether a label column exists
// for a metric is a property of the store, not of the credentials or the
// cluster asking.
var legacyCache = struct {
	mu       sync.Mutex
	entries  map[legacyKey]legacyEntry
	inflight map[legacyKey]*legacyCall
}{entries: map[legacyKey]legacyEntry{}, inflight: map[legacyKey]*legacyCall{}}

// legacyNow is replaced in tests.
var legacyNow = time.Now

// checkLegacyScopable refuses, with ErrUnscopable, any selector in exprs (the
// rewritten queries or series selectors) that the store can't confine to the
// forced labels. A schema lookup that fails refuses the request with that
// error.
func (t *RewritingTransport) checkLegacyScopable(ctx context.Context, exprs []string) error {
	s, err := prepare(t.Options)
	if err != nil {
		return err
	}
	type pair struct{ metric, label string }
	var pairs []pair
	seen := map[pair]bool{}
	for _, q := range exprs {
		expr, err := parser.ParseExpr(q)
		if err != nil {
			return fmt.Errorf("openobserve: rewritten query does not parse: %w", err)
		}
		var bad error
		parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
			vs, ok := node.(*parser.VectorSelector)
			if !ok || bad != nil {
				return nil
			}
			name := selectorMetric(vs)
			if name == "" {
				bad = &unscopableError{what: vs.String()}
				return bad
			}
			for _, f := range s.required(name) {
				p := pair{name, f.Name}
				if !seen[p] {
					seen[p] = true
					pairs = append(pairs, p)
				}
			}
			return nil
		})
		if bad != nil {
			return bad
		}
	}
	for _, p := range pairs {
		ok, err := t.labelHonoured(ctx, p.metric, p.label)
		if err != nil {
			return err
		}
		if !ok {
			return &unscopableError{what: p.metric}
		}
	}
	return nil
}

func selectorMetric(vs *parser.VectorSelector) string {
	if vs.Name != "" {
		return vs.Name
	}
	for _, m := range vs.LabelMatchers {
		if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			return m.Value
		}
	}
	return ""
}

// labelHonoured reports whether the store filters metric on label: the
// metric's stream must have a column for it. A store before v1.2 ignores a
// matcher on a missing column and returns every cluster's series, whatever
// the time range asked for, so only the schema (every column the stream has
// ever had) can tell.
func (t *RewritingTransport) labelHonoured(ctx context.Context, metric, label string) (bool, error) {
	if t.SchemaFetcher == nil {
		return false, errors.New("openobserve: this OpenObserve version needs a stream schema check that isn't configured")
	}
	key := legacyKey{store: t.Inner.Address(), metric: strings.ToLower(metric), label: strings.ToLower(label)}
	c := &legacyCache
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && legacyNow().Before(e.expires) {
		c.mu.Unlock()
		return e.scopable, nil
	}
	call, shared := c.inflight[key]
	if !shared {
		call = &legacyCall{done: make(chan struct{})}
		c.inflight[key] = call
		// The lookup is detached from the caller that started it, so its
		// cancellation never fails the callers waiting on the same answer.
		go t.runSchemaCall(context.WithoutCancel(ctx), key, call)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if call.err != nil && shared {
		// A failure can belong to the credentials that looked (another
		// tenant on the same organization), so it is not handed on: this
		// caller looks for itself, uncached.
		fctx, cancel := context.WithTimeout(ctx, schemaFetchTimeout)
		defer cancel()
		scopable, _, err := t.schemaHasLabel(fctx, key.metric, key.label)
		return scopable, err
	}
	return call.scopable, call.err
}

// runSchemaCall performs the shared lookup for call and caches a successful
// answer.
func (t *RewritingTransport) runSchemaCall(ctx context.Context, key legacyKey, call *legacyCall) {
	ctx, cancel := context.WithTimeout(ctx, schemaFetchTimeout)
	defer cancel()
	call.scopable, call.ttl, call.err = t.schemaHasLabel(ctx, key.metric, key.label)
	c := &legacyCache
	c.mu.Lock()
	delete(c.inflight, key)
	if call.err == nil {
		now := legacyNow()
		if len(c.entries) >= legacyCacheMax {
			for k, e := range c.entries {
				if !now.Before(e.expires) {
					delete(c.entries, k)
				}
			}
			for k := range c.entries {
				if len(c.entries) < legacyCacheMax {
					break
				}
				delete(c.entries, k)
			}
		}
		c.entries[key] = legacyEntry{scopable: call.scopable, expires: now.Add(call.ttl)}
	}
	c.mu.Unlock()
	close(call.done)
}

// schemaHasLabel reads metric's stream schema and reports whether it has a
// column named label, and for how long that answer may be cached.
func (t *RewritingTransport) schemaHasLabel(ctx context.Context, metric, label string) (bool, time.Duration, error) {
	fields, err := t.SchemaFetcher(ctx, metric)
	switch {
	case errors.Is(err, ErrStreamNotFound):
		return false, legacyNoStreamTTL, nil
	case err != nil:
		return false, 0, fmt.Errorf("openobserve: check whether %s can be scoped: %w", metric, err)
	case len(fields) == 0:
		// A stream with no columns yet is no better known than a missing one.
		return false, legacyNoStreamTTL, nil
	}
	for _, f := range fields {
		if strings.ToLower(f) == label {
			return true, legacyScopableTTL, nil
		}
	}
	return false, legacyUnscopableTTL, nil
}

// NewSchemaFetcher reads stream schemas with the client, headers (the basic
// auth) and organization of t, a transport rooted at the organization's
// Prometheus API (<base>/api/<org>/prometheus, as Connection.NewHTTPTransport
// builds). The schema endpoint is <base>/api/<org>/streams/<metric>/schema.
func NewSchemaFetcher(t *prom.HTTPTransport) SchemaFetcher {
	root := strings.TrimSuffix(strings.TrimRight(t.BaseURL+t.BasePath, "/"), "/prometheus")
	return func(ctx context.Context, metric string) ([]string, error) {
		u := root + "/streams/" + url.PathEscape(strings.ToLower(metric)) + "/schema?type=metrics"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("openobserve: build schema request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range t.Headers {
			req.Header.Set(k, v)
		}
		client := t.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}
		resp, err := prom.SameOriginRedirectClient(client).Do(req)
		if err != nil {
			return nil, fmt.Errorf("openobserve: schema request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxSchemaBytes+1))
		if err != nil {
			return nil, fmt.Errorf("openobserve: read schema: %w", &prom.ResponseError{StatusCode: resp.StatusCode, Err: err})
		}
		if len(body) > maxSchemaBytes {
			return nil, fmt.Errorf("openobserve: read schema: %w", &prom.ResponseError{StatusCode: resp.StatusCode, Err: prom.ErrResponseTooLarge})
		}
		if resp.StatusCode == http.StatusNotFound {
			if isStreamNotFound(body) {
				return nil, ErrStreamNotFound
			}
			return nil, &schemaUnreachableError{http: &prom.HTTPError{StatusCode: resp.StatusCode, URL: u, Body: body}}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &prom.HTTPError{StatusCode: resp.StatusCode, URL: u, Body: body}
		}
		return parseSchemaFields(body)
	}
}

// isStreamNotFound reports whether a 404 body is OpenObserve's own answer for
// a missing stream, {"code":404,"message":"stream not found"} in v1.1 and
// v1.2, rather than some other server's 404.
func isStreamNotFound(body []byte) bool {
	var doc struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body, &doc) == nil && strings.EqualFold(strings.TrimSpace(doc.Message), "stream not found")
}

// parseSchemaFields reads the field names of a stream schema answer, which
// OpenObserve v1.1 and v1.2 both shape as {"schema":[{"name":...},...]}.
func parseSchemaFields(body []byte) ([]string, error) {
	var doc struct {
		Schema *[]struct {
			Name string `json:"name"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("openobserve: parse schema: %w", err)
	}
	if doc.Schema == nil {
		return nil, errors.New("openobserve: schema answer has no schema field")
	}
	fields := make([]string, 0, len(*doc.Schema))
	for _, f := range *doc.Schema {
		if f.Name != "" {
			fields = append(fields, f.Name)
		}
	}
	return fields, nil
}
