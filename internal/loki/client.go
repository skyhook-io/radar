package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
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
	inflight    chan struct{} // closed when the running connect ends
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
	client.baseURL, client.source, client.contextName, client.labels, client.lastErr = "", "", "", nil, lastErr
	portforward.Stop(portforward.OwnerLoki)
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
// context is already known. Concurrent callers share one attempt.
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
		if wait := client.inflight; wait != nil {
			client.mu.Unlock()
			select {
			case <-wait:
				client.mu.Lock()
				ok, lastErr := connectedLocked(), client.lastErr
				client.mu.Unlock()
				if ok {
					return nil
				}
				return fmt.Errorf("%w: %s", ErrUnavailable, lastErr)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		done := make(chan struct{})
		client.inflight = done
		manual, headers, contextName := client.manualURL, maps.Clone(client.headers), currentContext()
		client.mu.Unlock()

		// The attempt outlives a cancelled caller: others may be waiting on it.
		attemptCtx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
		base, source, found, err := connect(attemptCtx, manual, headers, contextName)
		cancel()

		client.mu.Lock()
		client.inflight = nil
		close(done)
		// A Configure or Retire during the attempt wins over its result.
		stale := !client.enabled || client.manualURL != manual || !maps.Equal(client.headers, headers)
		if err == nil && !stale {
			client.baseURL, client.source, client.contextName, client.labels, client.lastErr = base, source, contextName, &found, ""
		} else if err != nil && !stale {
			client.lastErr = scrubLocked(err.Error())
		}
		lastErr := client.lastErr
		client.mu.Unlock()
		if stale {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: %s", ErrUnavailable, lastErr)
		}
		return nil
	}
}

func connect(ctx context.Context, manual string, headers map[string]string, contextName string) (base, source string, found StreamLabels, err error) {
	if manual != "" {
		found, err = probe(ctx, manual, headers)
		return manual, "manual", found, err
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
		portforward.Stop(portforward.OwnerLoki)
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

// scrubLocked removes every configured header value and URL from text.
func scrubLocked(text string) string {
	for _, v := range client.headers {
		if v != "" {
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
