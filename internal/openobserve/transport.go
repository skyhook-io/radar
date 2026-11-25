package openobserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// UnavailablePrefix starts every error that means OpenObserve could not answer.
const UnavailablePrefix = "OpenObserve unavailable"

// health remembers the latest failure talking to OpenObserve, for status,
// and how long OpenObserve asked Radar to wait after a 429. It is shared by
// every transport of one relay, health probes included.
type health struct {
	mu      sync.Mutex
	message string
	until   time.Time
	// now is replaced in tests.
	now func() time.Time
}

func (h *health) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// coolingDown reports how much of OpenObserve's requested wait is left.
func (h *health) coolingDown() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	left := h.until.Sub(h.clock())
	return left, left > 0
}

// coolFor holds every request back for d, at least a second, as Radar Hub's
// relay does.
func (h *health) coolFor(d time.Duration) {
	h.mu.Lock()
	h.until = h.clock().Add(max(d, time.Second))
	h.mu.Unlock()
}

func (h *health) set(msg string) {
	h.mu.Lock()
	h.message = msg
	h.mu.Unlock()
}

func (h *health) get() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.message != "", h.message
}

// upstreamError is what the user sees of an OpenObserve failure: why, without
// the request's URL or query (those go to the log). It unwraps to the
// upstream error. When OpenObserve could not answer, its text starts with
// UnavailablePrefix.
type upstreamError struct {
	msg string
	err error
}

func (e *upstreamError) Error() string { return e.msg }
func (e *upstreamError) Unwrap() error { return e.err }

// retryTransport sits under the RewritingTransport, next to the network. It
// retries once what OpenObserve answers under load ("bad_data: transport
// error"), and turns other upstream failures into "OpenObserve unavailable".
type retryTransport struct {
	inner  prom.Transport
	health *health
	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error
}

func (t *retryTransport) Address() string { return t.inner.Address() }

func (t *retryTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	var body []byte
	err := t.call(ctx, func() error {
		var err error
		body, err = t.inner.Do(ctx, method, path, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

// schemaFetcher routes a legacy store's stream schema lookups through the
// same retry and health handling as queries. A missing stream is an answer,
// not a failure.
func (t *retryTransport) schemaFetcher(fetch oo.SchemaFetcher) oo.SchemaFetcher {
	return func(ctx context.Context, metric string) ([]string, error) {
		var fields []string
		var notFound error
		err := t.call(ctx, func() error {
			var err error
			fields, err = fetch(ctx, metric)
			if errors.Is(err, oo.ErrStreamNotFound) {
				notFound, err = err, nil
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if notFound != nil {
			return nil, notFound
		}
		return fields, nil
	}
}

// call runs do, retrying once an overload answer, records the outcome in
// health and turns an upstream failure into an upstreamError.
func (t *retryTransport) call(ctx context.Context, do func() error) error {
	if left, ok := t.health.coolingDown(); ok {
		// OpenObserve asked for a pause: answer as it would, without asking.
		return oo.ErrRateLimited{RetryAfter: left}
	}
	err := do()
	if err != nil && isOverloaded(err) {
		sleep := t.sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if serr := sleep(ctx, 150*time.Millisecond+rand.N(100*time.Millisecond)); serr != nil {
			return serr
		}
		err = do()
	}
	if err == nil {
		t.health.set("")
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	var limited oo.ErrRateLimited
	if errors.As(err, &limited) {
		t.health.coolFor(limited.RetryAfter)
		t.health.set(UnavailablePrefix + ": " + limited.Error())
		return limited
	}
	var httpErr *prom.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest && !isOverloaded(err) {
		// A query OpenObserve refused says nothing about its health.
		msg := "OpenObserve refused the query: " + upstreamPromError(httpErr.Body)
		log.Printf("[openobserve] %s: %v", msg, err)
		return &prom.RefusedError{Message: msg, Err: err}
	}
	msg := prom.RedactURLs(UnavailablePrefix + ": " + failureReason(err))
	log.Printf("[openobserve] %s: %v", msg, err)
	t.health.set(msg)
	return &upstreamError{msg: msg, err: err}
}

// failureReason names an upstream failure, in the words Radar Hub's relay
// uses for the same failures.
func failureReason(err error) string {
	var httpErr *prom.HTTPError
	switch {
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized:
		return "it rejected Radar's user or token"
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden:
		// The login passed setup's check; a 403 now is missing read access.
		return "it doesn't let this account read metrics; give it a role that can read the metrics streams"
	case errors.Is(err, oo.ErrSchemaUnreachable):
		return trimPkgPrefix(oo.ErrSchemaUnreachable)
	case isOverloaded(err):
		return "it failed the query twice with a transport error"
	case errors.As(err, &httpErr):
		return fmt.Sprintf("it answered HTTP %d", httpErr.StatusCode)
	case errors.Is(err, prom.ErrResponseTooLarge):
		return "the answer was too large. Narrow the query"
	case errors.Is(err, context.DeadlineExceeded):
		return "the query timed out"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "Radar could not reach it: " + urlErr.Err.Error()
	}
	return trimPkgPrefix(err)
}

// upstreamPromError is the error a Prometheus-style answer body names,
// bounded, or "bad request".
func upstreamPromError(body []byte) string {
	var answer struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &answer) == nil && answer.Error != "" {
		msg := prom.RedactURLs(answer.Error)
		if r := []rune(msg); len(r) > 300 {
			msg = string(r[:300]) + "…"
		}
		return msg
	}
	return "bad request"
}

func isOverloaded(err error) bool {
	var httpErr *prom.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest &&
		bytes.Contains(httpErr.Body, []byte("bad_data")) && bytes.Contains(httpErr.Body, []byte("transport error"))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// notCollectedTransport sits above the RewritingTransport. A query on a
// metric family this source does not ship answers an empty success, so
// charts show no data rather than an error; status lists the family as
// missing. A metric an older OpenObserve can't scope to this cluster
// (ErrUnscopable, which wraps ErrNotCollected) answers the same way.
type notCollectedTransport struct {
	inner prom.Transport
}

func (t notCollectedTransport) Address() string { return t.inner.Address() }

func (t notCollectedTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	body, err := t.inner.Do(ctx, method, path, params)
	if err == nil {
		return body, nil
	}
	if !errors.Is(err, oo.ErrNotCollected) {
		// A failed schema lookup comes back wrapped by the rewriter; the
		// user sees why OpenObserve was unavailable, not the wrapping.
		var upstream *upstreamError
		if errors.As(err, &upstream) {
			return nil, upstream
		}
		var refused *prom.RefusedError
		if errors.As(err, &refused) {
			return nil, refused
		}
		return nil, err
	}
	switch path {
	case "/api/v1/query":
		return []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
	case "/api/v1/query_range":
		return []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
	default:
		return []byte(`{"status":"success","data":[]}`), nil
	}
}

// unavailableTransport answers every request with the reason OpenObserve
// can't be used right now, so no other backend is read in its place.
type unavailableTransport struct {
	address string
	reason  string
}

func (t unavailableTransport) Address() string { return t.address }

func (t unavailableTransport) Do(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, errors.New(t.reason)
}

// storeGuards are the checks a store's version calls for.
type storeGuards struct {
	legacy              bool
	labelValuesUnscoped bool
}

// transportFactory builds the scoped transport stack for one use.
func transportFactory(conn oo.Connection, opts oo.RewriteOptions, guards storeGuards, h *health) func(time.Duration, int64) prom.Transport {
	return func(timeout time.Duration, maxResponseBytes int64) prom.Transport {
		httpTr, err := conn.NewHTTPTransport(timeout)
		if err != nil {
			msg := UnavailablePrefix + ": " + err.Error()
			h.set(msg)
			return unavailableTransport{address: conn.PrometheusURL(), reason: msg}
		}
		if timeout <= 0 && httpTr.HTTPClient != nil {
			// The caller bounds the request with its context (MCP queries).
			httpTr.HTTPClient.Timeout = 0
		}
		httpTr.MaxResponseBytes = maxResponseBytes
		retry := &retryTransport{inner: httpTr, health: h}
		return notCollectedTransport{inner: &oo.RewritingTransport{
			Inner:               retry,
			Options:             opts,
			LegacyStore:         guards.legacy,
			LabelValuesUnscoped: guards.labelValuesUnscoped,
			SchemaFetcher:       retry.schemaFetcher(oo.NewSchemaFetcher(httpTr)),
		}}
	}
}
