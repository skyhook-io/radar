package openobserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// RewritingTransport scopes every request to the configured cluster: queries
// and series selectors are rewritten before Inner sees them, and series
// lacking the cluster matchers are dropped from the answers.
type RewritingTransport struct {
	Inner     prom.Transport
	Options   RewriteOptions
	OnDropped func(n int)
	// LegacyStore turns on the scoping guard for a store that may ignore a
	// matcher on a label the metric's stream has never had (OpenObserve
	// before v1.2; see LegacyFilterBehaviour). Set it unless the store is
	// positively known to filter correctly. Every metric a request reads is
	// then checked, through SchemaFetcher, to have a column for each forced
	// label before the request is sent, and refused with ErrUnscopable when
	// it doesn't. Label lookups must name at least one metric in match[].
	LegacyStore bool
	// SchemaFetcher reads a metric's stream columns (NewSchemaFetcher). With
	// LegacyStore set and no SchemaFetcher, every metric is refused.
	SchemaFetcher SchemaFetcher
}

var _ prom.Transport = (*RewritingTransport)(nil)

// ErrRateLimited is returned when OpenObserve answers 429. RetryAfter is zero
// when the upstream did not say how long to wait.
type ErrRateLimited struct {
	RetryAfter time.Duration
	Err        error
}

func (e ErrRateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("openobserve: rate limited, retry after %s", e.RetryAfter)
	}
	return "openobserve: rate limited"
}

func (e ErrRateLimited) Unwrap() error { return e.Err }

// ParseRetryAfter reads a Retry-After header value in seconds or as an HTTP
// date; anything unreadable or in the past is zero.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

type pathKind int

const (
	pathRefused pathKind = iota
	pathQuery
	pathSeries
	pathLabels
	pathPassthrough
	pathRules
)

// ErrRulesUnscoped refuses alerting and recording rules on a shared
// OpenObserve: they can't be limited to one cluster.
var ErrRulesUnscoped = errors.New("alerting and recording rules aren't available from a shared OpenObserve: they can't be limited to one cluster")

func classifyPath(path string) (pathKind, string) {
	switch path {
	case "/api/v1/query", "/api/v1/query_range":
		return pathQuery, path
	case "/api/v1/series":
		return pathSeries, path
	case "/api/v1/labels":
		return pathLabels, path
	case "/api/v1/metadata":
		return pathPassthrough, path
	case "/api/v1/rules":
		return pathRules, path
	}
	const prefix, suffix = "/api/v1/label/", "/values"
	if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix) && len(path) > len(prefix)+len(suffix) {
		escaped := path[len(prefix) : len(path)-len(suffix)]
		name, err := url.PathUnescape(escaped)
		if err != nil || name == "" || strings.ContainsAny(name, "/?#") || name == "." || name == ".." {
			return pathRefused, ""
		}
		return pathLabels, prefix + url.PathEscape(strings.ToLower(name)) + suffix
	}
	return pathRefused, ""
}

func (t *RewritingTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if t.Inner == nil {
		return nil, errors.New("openobserve: no inner transport")
	}
	if len(t.Options.Matchers) == 0 {
		return nil, errNoMatchers
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, fmt.Errorf("openobserve: method %s not allowed", method)
	}
	kind, outPath := classifyPath(path)
	if kind == pathRefused {
		return nil, fmt.Errorf("openobserve: path %q is not allowed", path)
	}
	// Rule groups and their alerts come back for every cluster in the
	// organization, with nothing Radar can filter on reliably.
	if kind == pathRules {
		return nil, &prom.RefusedError{Message: ErrRulesUnscoped.Error(), Err: ErrRulesUnscoped}
	}

	// Old stores answer a label-name listing from every series of the named
	// streams, ignoring the cluster matcher; Radar itself never lists names.
	if t.LegacyStore && path == "/api/v1/labels" {
		return nil, &unscopableError{what: "label names (this OpenObserve version can't list them for one cluster)"}
	}
	if t.LegacyStore && kind == pathLabels && len(params["match[]"]) == 0 {
		return nil, &unscopableError{what: SelectorForLabelValues(t.Options.Matchers) + " (a label lookup must name a metric in match[])"}
	}

	out := cloneValues(params)
	switch kind {
	case pathQuery:
		qs := out["query"]
		if len(qs) != 1 {
			return nil, &prom.RefusedError{Err: errors.New("openobserve: exactly one query parameter is required")}
		}
		rewritten, err := Rewrite(qs[0], t.Options)
		if err != nil {
			return nil, refused(err)
		}
		out["query"] = []string{rewritten}
	case pathSeries, pathLabels:
		if err := t.rewriteMatches(out); err != nil {
			return nil, refused(err)
		}
		defaultLookupRange(out)
	}

	if t.LegacyStore {
		var exprs []string
		switch kind {
		case pathQuery:
			exprs = out["query"]
		case pathSeries, pathLabels:
			exprs = out["match[]"]
		}
		if err := t.checkLegacyScopable(ctx, exprs); err != nil {
			return nil, rateLimited(err)
		}
	}

	body, err := t.Inner.Do(ctx, method, outPath, out)
	if err != nil {
		return nil, rateLimited(err)
	}
	if kind != pathQuery && kind != pathSeries {
		return body, nil
	}
	filtered, dropped, err := FilterResponse(body, t.Options.Matchers)
	if err != nil {
		return nil, err
	}
	if dropped > 0 && t.OnDropped != nil {
		t.OnDropped(dropped)
	}
	return filtered, nil
}

// defaultLookupWindow is the range a series or label lookup reads when it
// names none. Prometheus reads all of its data then; OpenObserve refuses the
// lookup with HTTP 500 ("invalid time range"), so a missing bound is filled.
const defaultLookupWindow = time.Hour

// lookupNow is replaced in tests.
var lookupNow = time.Now

func defaultLookupRange(out url.Values) {
	end := lookupNow()
	if out.Get("end") == "" {
		out.Set("end", strconv.FormatInt(end.Unix(), 10))
	} else if t, ok := parsePromTime(out.Get("end")); ok {
		end = t
	}
	if out.Get("start") == "" {
		out.Set("start", strconv.FormatInt(end.Add(-defaultLookupWindow).Unix(), 10))
	}
}

// parsePromTime reads a Prometheus API time: Unix seconds (possibly
// fractional) or RFC 3339.
func parsePromTime(v string) (time.Time, bool) {
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		sec := int64(f)
		return time.Unix(sec, int64((f-float64(sec))*1e9)), true
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// refused marks a rewrite failure as the query's fault (prom.ErrQueryRefused),
// keeping its text. A metric this source doesn't collect is an answer, not a
// refusal.
func refused(err error) error {
	if errors.Is(err, ErrNotCollected) {
		return err
	}
	return &prom.RefusedError{Err: err}
}

func (t *RewritingTransport) rewriteMatches(out url.Values) error {
	matches := out["match[]"]
	if len(matches) == 0 {
		if _, err := RewriteSeriesSelector(SelectorForLabelValues(t.Options.Matchers), t.Options); err != nil {
			return err
		}
		out["match[]"] = []string{SelectorForLabelValues(t.Options.Matchers)}
		return nil
	}
	rewritten := make([]string, 0, len(matches))
	for _, m := range matches {
		r, err := RewriteSeriesSelector(m, t.Options)
		if err != nil {
			return err
		}
		rewritten = append(rewritten, r)
	}
	out["match[]"] = rewritten
	return nil
}

func (t *RewritingTransport) Address() string {
	if t.Inner == nil {
		return ""
	}
	return t.Inner.Address()
}

// retryAfterError is implemented by transport errors that carry the
// upstream's Retry-After; prom.HTTPError does not keep response headers.
type retryAfterError interface {
	RetryAfter() time.Duration
}

func rateLimited(err error) error {
	var limited ErrRateLimited
	if errors.As(err, &limited) {
		return limited
	}
	var httpErr *prom.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		return err
	}
	var wait time.Duration
	var ra retryAfterError
	if errors.As(err, &ra) {
		wait = ra.RetryAfter()
	}
	return ErrRateLimited{RetryAfter: wait, Err: err}
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// RateLimitRoundTripper turns a 429 answer into ErrRateLimited carrying the
// Retry-After header, which prom.HTTPTransport would otherwise discard.
type RateLimitRoundTripper struct {
	Next http.RoundTripper
}

func (rt RateLimitRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := rt.Next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	wait := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return nil, ErrRateLimited{RetryAfter: wait}
}
