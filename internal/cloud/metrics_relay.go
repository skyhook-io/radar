package cloud

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

	"github.com/skyhook-io/radar/pkg/prom"
)

// MetricsRelayAddress is how status names the relay in place of a URL.
const MetricsRelayAddress = "Radar Hub relay"

const (
	defaultRelayMaxResponseBytes = 10 << 20
	defaultRelayRetryAfter       = 30 * time.Second
	maxRelayRetryAfter           = 5 * time.Minute
)

// MetricsRelayError is a relay answer that concerns the metrics source as a
// whole rather than one query. Its message is complete and user-facing.
type MetricsRelayError struct {
	StatusCode  int
	Code        string
	Message     string
	Unavailable bool
	RetryAfter  time.Duration
}

func (e *MetricsRelayError) Error() string { return e.Message }

// relayHealth is shared by every transport built for one relay configuration,
// so status reflects the most recent relay answer on any query path.
type relayHealth struct {
	mu          sync.Mutex
	unavailable bool
	message     string
	retryUntil  time.Time
}

func (h *relayHealth) Status() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unavailable, h.message
}

func (h *relayHealth) ok() {
	h.mu.Lock()
	h.unavailable, h.message = false, ""
	h.mu.Unlock()
}

func (h *relayHealth) fail(err *MetricsRelayError) {
	h.mu.Lock()
	h.unavailable, h.message = err.Unavailable, err.Message
	if err.RetryAfter > 0 {
		h.retryUntil = time.Now().Add(err.RetryAfter)
	}
	h.mu.Unlock()
}

// backoff returns how long requests must still wait after a rate limit.
func (h *relayHealth) backoff(now time.Time) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retryUntil.Sub(now)
}

// HubRelayTransport is a prom.Transport that sends each Prometheus API request
// to Radar Hub's metrics relay, authenticated by the cluster token. Hub adds
// the backend's credentials and the cluster filter; the agent never holds them.
type HubRelayTransport struct {
	// HubOrigin is the Hub's http(s) origin, without a path.
	HubOrigin string
	// RelayPath is the relay's path prefix as Hub reported it, e.g.
	// /api/agent/metrics/openobserve.
	RelayPath        string
	Token            string
	HTTPClient       *http.Client
	MaxResponseBytes int64

	health *relayHealth
	// onNotSelected asks the source poller to re-read the source: the relay
	// says Hub no longer routes this cluster to it.
	onNotSelected func()
}

func (t *HubRelayTransport) Address() string { return MetricsRelayAddress }

func (t *HubRelayTransport) Do(ctx context.Context, method, path string, values url.Values) ([]byte, error) {
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "..") || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("metrics relay: unsupported path %q", path)
	}
	if t.health != nil {
		if wait := t.health.backoff(time.Now()); wait > 0 {
			return nil, rateLimitedError(wait)
		}
	}
	full := t.HubOrigin + t.RelayPath + path
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(values.Encode())
	} else if len(values) > 0 {
		full += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("metrics relay: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.Token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	client := http.Client{Timeout: 10 * time.Second}
	if t.HTTPClient != nil {
		client = *t.HTTPClient
	}
	// The request carries the cluster token: never follow a redirect with it.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Hub API redirects are not allowed")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, t.failed(&MetricsRelayError{Unavailable: true, Message: "OpenObserve unavailable: Radar Hub could not be reached: " + prom.RedactURLs(err.Error())})
	}
	defer func() { _ = resp.Body.Close() }()

	limit := t.MaxResponseBytes
	if limit <= 0 {
		limit = defaultRelayMaxResponseBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, t.failed(&MetricsRelayError{StatusCode: resp.StatusCode, Unavailable: true, Message: "OpenObserve unavailable: the Radar Hub relay answer could not be read"})
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("metrics relay: %w (%d bytes)", &prom.ResponseError{StatusCode: resp.StatusCode, Err: prom.ErrResponseTooLarge}, limit)
	}
	if resp.StatusCode == http.StatusOK {
		if t.health != nil {
			t.health.ok()
		}
		return data, nil
	}
	return nil, t.statusError(resp, data)
}

func (t *HubRelayTransport) statusError(resp *http.Response, data []byte) error {
	var answer struct {
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(data, &answer)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return t.failed(rateLimitedError(relayRetryAfter(resp.Header.Get("Retry-After"), time.Now())))
	case resp.StatusCode == http.StatusForbidden && answer.ErrorType == "not_selected":
		err := t.failed(&MetricsRelayError{StatusCode: resp.StatusCode, Code: answer.ErrorType, Unavailable: true,
			Message: "OpenObserve unavailable: Radar Hub no longer routes this cluster's metrics to OpenObserve"})
		if t.onNotSelected != nil {
			t.onNotSelected()
		}
		return err
	case resp.StatusCode == http.StatusUnauthorized:
		return t.failed(&MetricsRelayError{StatusCode: resp.StatusCode, Unavailable: true,
			Message: "OpenObserve unavailable: Radar Hub rejected this cluster's token"})
	case resp.StatusCode == http.StatusBadGateway || answer.ErrorType == "openobserve_unavailable" || resp.StatusCode >= 500:
		message := strings.TrimSpace(answer.Error)
		if !strings.HasPrefix(message, "OpenObserve unavailable") {
			message = fmt.Sprintf("OpenObserve unavailable: Radar Hub answered %d", resp.StatusCode)
		}
		return t.failed(&MetricsRelayError{StatusCode: resp.StatusCode, Code: answer.ErrorType, Unavailable: true, Message: message})
	}
	// A refused query (400 bad_data and the like) concerns that query only.
	httpErr := &prom.HTTPError{StatusCode: resp.StatusCode, URL: MetricsRelayAddress, Body: data}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		message := strings.TrimSpace(answer.Error)
		if message == "" {
			message = fmt.Sprintf("Radar Hub refused the query (HTTP %d)", resp.StatusCode)
		}
		return &prom.RefusedError{Message: message, Err: httpErr}
	}
	return httpErr
}

func (t *HubRelayTransport) failed(err *MetricsRelayError) error {
	if t.health != nil {
		t.health.fail(err)
	}
	return err
}

func rateLimitedError(wait time.Duration) *MetricsRelayError {
	seconds := int((wait + time.Second - 1) / time.Second)
	return &MetricsRelayError{
		StatusCode: http.StatusTooManyRequests,
		Code:       "rate_limited",
		RetryAfter: wait,
		Message:    fmt.Sprintf("OpenObserve is rate limited by Radar Hub; retry in %ds", seconds),
	}
}

// relayRetryAfter bounds the Retry-After header so a bad value can neither
// busy-loop the relay nor silence metrics for long.
func relayRetryAfter(raw string, now time.Time) time.Duration {
	d, ok := parseRetryAfter(raw, now)
	if !ok {
		d = defaultRelayRetryAfter
	}
	return min(max(d, time.Second), maxRelayRetryAfter)
}
