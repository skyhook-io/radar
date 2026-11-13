package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// ErrNotConfigured: Loki is turned off for this context, or was retired.
var ErrNotConfigured = errors.New("Loki is not connected")

// ErrHeadersRequireURL: credentials are never sent to an auto-discovered
// endpoint, where anyone who can create a Service could receive them.
var ErrHeadersRequireURL = errors.New("Loki headers are set but no Loki URL; set the URL, or remove the headers to discover Loki")

// ErrUnavailable: Loki could not be found or reached.
var ErrUnavailable = errors.New("Loki is unavailable")

// UpstreamError is Loki answering with an error status. Message is redacted.
type UpstreamError struct {
	StatusCode int
	Message    string
}

func (e *UpstreamError) Error() string { return e.Message }

// Status is what may leave the server about the Loki connection: never a
// header value or a full URL.
type Status struct {
	Configured    bool          `json:"configured"`
	Connected     bool          `json:"connected"`
	Discovering   bool          `json:"discovering"`
	Address       string        `json:"address,omitempty"`
	Source        string        `json:"source,omitempty"` // "manual" | "discovered"
	Labels        *StreamLabels `json:"labels,omitempty"`
	HistoryReason string        `json:"historyReason,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// Seams for tests.
var (
	currentContext = k8s.GetContextName
	listServices   = cachedServices
	startForward   = func(ctx context.Context, ns, name string, port int, contextName string) (string, error) {
		info, err := portforward.Start(portforward.OwnerLoki, ctx, ns, name, port, contextName)
		if err != nil {
			return "", err
		}
		return info.Address, nil
	}
	stopForward = func() { portforward.Stop(portforward.OwnerLoki) }
)

const (
	directProbeTimeout = 2 * time.Second
	discoveryTimeout   = 30 * time.Second
	requestTimeout     = 30 * time.Second
	maxResponseBytes   = 32 << 20
)

// client is the process-wide Loki connection for the current context.
var client struct {
	mu          sync.Mutex
	enabled     bool
	manualURL   string
	headers     map[string]string
	baseURL     string
	source      string
	contextName string
	labels      *StreamLabels
	lastErr     string
	inflight    chan struct{} // closed when the running attempt ends
	generation  int           // bumped by Configure and Retire
}

// Configure replaces the connection: rawURL set means that Loki, empty means
// find it in the cluster. It drops what was found before.
func Configure(rawURL string, headers map[string]string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.enabled = true
	client.manualURL = strings.TrimRight(rawURL, "/")
	client.headers = maps.Clone(headers)
	dropLocked("")
}

// Retire turns Loki off; err (redacted) becomes the status error.
func Retire(err error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.enabled = false
	text := ""
	if err != nil {
		text = scrubLocked(err.Error())
	}
	dropLocked(text)
}

func dropLocked(lastErr string) {
	client.generation++
	client.baseURL, client.source, client.contextName, client.labels, client.lastErr = "", "", "", nil, lastErr
	stopForward()
}

// CurrentStatus describes the connection for the status endpoint.
func CurrentStatus() Status {
	client.mu.Lock()
	defer client.mu.Unlock()
	st := Status{Configured: client.enabled, Discovering: client.inflight != nil, Error: client.lastErr}
	address := client.manualURL
	if connectedLocked() {
		st.Connected, st.Source, address = true, client.source, client.baseURL
		st.Labels = client.labels
		if client.labels != nil {
			st.HistoryReason = client.labels.HistoryReason()
		}
	}
	if address != "" {
		st.Address = prom.SafeAddress(address)
	}
	return st
}

func connectedLocked() bool {
	return client.enabled && client.baseURL != "" && client.contextName == currentContext()
}

// EnsureConnected finds and probes Loki unless a connection for the current
// context is already known. Concurrent callers share one attempt, which runs
// on its own and outlives a caller whose ctx ends first.
func EnsureConnected(ctx context.Context) error {
	for {
		client.mu.Lock()
		if !client.enabled {
			client.mu.Unlock()
			return ErrNotConfigured
		}
		if connectedLocked() {
			client.mu.Unlock()
			return nil
		}
		wait, generation, contextName := client.inflight, client.generation, currentContext()
		if wait == nil {
			wait = make(chan struct{})
			client.inflight = wait
			go attempt(wait, generation, client.manualURL, maps.Clone(client.headers), contextName)
		}
		client.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		client.mu.Lock()
		ok, enabled, lastErr := connectedLocked(), client.enabled, client.lastErr
		changed := client.generation != generation || currentContext() != contextName
		client.mu.Unlock()
		switch {
		case ok:
			return nil
		case !enabled:
			return ErrNotConfigured
		case changed:
			continue // reconfigured, or the context switched: try again for what is current now
		}
		return fmt.Errorf("%w: %s", ErrUnavailable, lastErr)
	}
}

// attempt connects with the settings of one generation on one context. A
// Configure, Retire or context switch during it wins: its result is dropped,
// and a forward it opened is stopped (no newer attempt can own one while this
// one is in flight).
func attempt(done chan struct{}, generation int, manual string, headers map[string]string, contextName string) {
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	base, source, found, err := connect(ctx, manual, headers, contextName)
	cancel()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.inflight = nil
	close(done)
	switch {
	case client.generation != generation || currentContext() != contextName:
		if err == nil && source == "discovered" {
			stopForward()
		}
	case err == nil:
		client.baseURL, client.source, client.contextName, client.labels, client.lastErr = base, source, contextName, &found, ""
	default:
		client.lastErr = scrubLocked(err.Error())
	}
}

func connect(ctx context.Context, manual string, headers map[string]string, contextName string) (base, source string, found StreamLabels, err error) {
	if manual != "" {
		found, err = probe(ctx, manual, headers)
		return manual, "manual", found, err
	}
	if len(headers) > 0 {
		return "", "", found, ErrHeadersRequireURL
	}
	candidates := Candidates(listServices())
	if len(candidates) == 0 {
		return "", "", found, errors.New("no Loki Service found in the cluster")
	}
	var lastErr error
	for _, c := range candidates {
		direct := fmt.Sprintf("http://%s.%s.svc:%d", c.Name, c.Namespace, c.Port)
		probeCtx, cancel := context.WithTimeout(ctx, directProbeTimeout)
		found, err = probe(probeCtx, direct, headers)
		cancel()
		if err == nil {
			return direct, "discovered", found, nil
		}
		address, ferr := startForward(ctx, c.Namespace, c.Name, c.Port, contextName)
		if ferr != nil {
			lastErr = fmt.Errorf("%s/%s: %w", c.Namespace, c.Name, ferr)
			continue
		}
		if found, err = probe(ctx, address, headers); err == nil {
			return address, "discovered", found, nil
		}
		stopForward()
		lastErr = fmt.Errorf("%s/%s: %w", c.Namespace, c.Name, err)
	}
	return "", "", found, lastErr
}

// probe asks Loki for its label names, which both proves it answers and
// tells how its streams are labelled.
func probe(ctx context.Context, base string, headers map[string]string) (StreamLabels, error) {
	body, err := transport(base, headers).Do(ctx, http.MethodGet, "/loki/api/v1/labels", nil)
	if err != nil {
		return StreamLabels{}, err
	}
	var resp struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Status != "success" {
		return StreamLabels{}, errors.New("the endpoint did not answer like Loki")
	}
	return DetectLabels(resp.Data), nil
}

func transport(base string, headers map[string]string) *prom.HTTPTransport {
	t := prom.NewHTTPTransport(base, "", &http.Client{Timeout: requestTimeout})
	t.Headers = headers
	t.MaxResponseBytes = maxResponseBytes
	return t
}

// Get runs one read against Loki. A Loki error status comes back as
// *UpstreamError; an unreachable Loki as ErrUnavailable, and a discovered
// address is dropped so the next read finds Loki again.
func Get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if err := EnsureConnected(ctx); err != nil {
		return nil, err
	}
	client.mu.Lock()
	if !connectedLocked() { // changed since EnsureConnected returned
		client.mu.Unlock()
		return nil, fmt.Errorf("%w: the connection changed; try again", ErrUnavailable)
	}
	base, headers := client.baseURL, maps.Clone(client.headers)
	client.mu.Unlock()
	body, err := transport(base, headers).Do(ctx, http.MethodGet, path, params)
	if err == nil {
		return body, nil
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	var httpErr *prom.HTTPError
	if errors.As(err, &httpErr) {
		return nil, &UpstreamError{StatusCode: httpErr.StatusCode, Message: scrubLocked(httpErr.Error())}
	}
	if client.source == "discovered" && client.baseURL == base {
		client.baseURL = ""
	}
	return nil, fmt.Errorf("%w: %s", ErrUnavailable, scrubLocked(err.Error()))
}

// Labels is the detected stream labelling, once connected.
func Labels() (StreamLabels, bool) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if !connectedLocked() || client.labels == nil {
		return StreamLabels{}, false
	}
	return *client.labels, true
}

// minScrubbedLength: shorter header values (a numeric tenant ID) aren't
// credentials, and replacing them would garble every number in a message.
const minScrubbedLength = 4

// scrubLocked removes every configured header value and URL from text.
func scrubLocked(text string) string {
	for _, v := range client.headers {
		if len(v) >= minScrubbedLength {
			text = strings.ReplaceAll(text, v, "<redacted>")
		}
	}
	return prom.RedactURLs(text)
}

// RegisterSignals subscribes Loki to lost port-forwards. Call once at startup.
func RegisterSignals() {
	portforward.SetOnForwardLost(portforward.OwnerLoki, forwardLost)
}

// forwardLost drops a discovered address the lost forward served (address is
// the forward's URL); every subscriber hears every loss, so it matches on it.
func forwardLost(_ portforward.Owner, address string) {
	// A replacement forward may already serve the same local port; the loss
	// then describes the old tunnel only, as for Prometheus.
	if portforward.IsActiveAddress(address) {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.source == "discovered" && address != "" && client.baseURL == address {
		client.baseURL = ""
	}
}

func cachedServices() []corev1.Service {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Services() == nil {
		return nil
	}
	list, err := cache.Services().List(labels.Everything())
	if err != nil {
		return nil
	}
	out := make([]corev1.Service, 0, len(list))
	for _, s := range list {
		out = append(out, *s)
	}
	return out
}

// DefaultLimit is the most lines one read returns.
const DefaultLimit = 5000

// Line is one log line with the pod and container it came from.
type Line struct {
	Time           time.Time
	Pod, Container string
	Text           string
}

// Lines is a read's result, oldest first. Truncated: Loki returned the limit,
// so older lines in the window were left out.
type Lines struct {
	Items     []Line
	Truncated bool
}

// QueryRange reads the newest lines of selector in w, up to limit, and
// returns them oldest first. selector must come from PodSelector or
// JobSelector: nothing else reaches the query.
func QueryRange(ctx context.Context, selector string, w Window, limit int) (Lines, error) {
	labels, ok := Labels()
	if !ok {
		if err := EnsureConnected(ctx); err != nil {
			return Lines{}, err
		}
		labels, _ = Labels()
	}
	params := url.Values{
		"query":     {selector},
		"start":     {strconv.FormatInt(w.Start.UnixNano(), 10)},
		"end":       {strconv.FormatInt(w.End.UnixNano(), 10)},
		"limit":     {strconv.Itoa(limit)},
		"direction": {"backward"},
	}
	body, err := Get(ctx, "/loki/api/v1/query_range", params)
	if err != nil {
		return Lines{}, err
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Status != "success" || resp.Data.ResultType != "streams" {
		return Lines{}, fmt.Errorf("%w: Loki's answer was not a list of streams", ErrUnavailable)
	}
	var out Lines
	for _, stream := range resp.Data.Result {
		for _, v := range stream.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			out.Items = append(out.Items, Line{Time: time.Unix(0, ns).UTC(), Pod: stream.Stream[labels.Pod], Container: stream.Stream[labels.Container], Text: v[1]})
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].Time.Before(out.Items[j].Time) })
	out.Truncated = len(out.Items) >= limit
	return out, nil
}
