package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	metricsSourcePath         = "/api/agent/metrics/source"
	metricsRelayPathPrefix    = "/api/agent/metrics/"
	maxMetricsSourceBodyBytes = 64 << 10

	MetricsSourcePrometheus  = "prometheus"
	MetricsSourceOpenObserve = "openobserve"
)

// The agent asks Hub for its metrics source only when something can have
// changed it: at start, on each tunnel (re)connect, when Hub nudges it after a
// settings change, when the relay answers that this cluster is no longer
// selected, and when a user asks for a check from an empty metrics screen.
// Hub checks every relay request itself, so a missed change costs chart
// errors until one of those moments, never another cluster's data. A failed
// check is retried with backoff until one succeeds.
var (
	metricsSourceMinPollGap = 2 * time.Second
	metricsSourceRetryMin   = 30 * time.Second
	metricsSourceRetryMax   = 5 * time.Minute
	metricsSourceTimeout    = 10 * time.Second
)

var relayLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// MetricsSourceMatcher is one label matcher that selects this cluster's series.
type MetricsSourceMatcher struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MetricsSourceResponse is Hub's answer to GET /api/agent/metrics/source.
type MetricsSourceResponse struct {
	Source          string                 `json:"source"`
	RelayPath       string                 `json:"relayPath,omitempty"`
	Matchers        []MetricsSourceMatcher `json:"matchers,omitempty"`
	MatchedBy       string                 `json:"matchedBy,omitempty"`
	Dialect         string                 `json:"dialect,omitempty"`
	Warning         string                 `json:"warning,omitempty"`
	VersionWarning  string                 `json:"versionWarning,omitempty"`
	MissingFamilies []string               `json:"missingFamilies,omitempty"`
	Revision        string                 `json:"revision,omitempty"`
	// OpenObserve says this Hub offers OpenObserve metrics, whichever source
	// it selected. Hubs without OpenObserve, and older ones, leave it out.
	OpenObserve bool `json:"openobserve,omitempty"`
}

// MetricsSourceDecision is what the agent should do with its metrics client.
// For MetricsSourceOpenObserve, NewTransport builds relay transports and Key
// changes whenever the relay connection must be rebuilt.
type MetricsSourceDecision struct {
	Response MetricsSourceResponse
	Key      string
	// Endpoint is the relay's URL without credentials.
	Endpoint     string
	NewTransport func(timeout time.Duration, maxResponseBytes int64) prom.Transport
	Health       func() (unavailable bool, message string)
}

// metricsSourcePollers holds every running poller, so a refresh request from
// Hub reaches whichever tunnel is current.
var metricsSourcePollers = struct {
	sync.Mutex
	set map[*metricsSourcePoller]struct{}
}{set: map[*metricsSourcePoller]struct{}{}}

// RequestMetricsSourceRefresh makes every running poller ask Hub for the
// metrics source now.
func RequestMetricsSourceRefresh() {
	metricsSourcePollers.Lock()
	defer metricsSourcePollers.Unlock()
	for p := range metricsSourcePollers.set {
		p.requestPoll()
	}
}

// HubOffersOpenObserve reports whether Hub's last successful answer to a
// running poller said it offers OpenObserve metrics.
func HubOffersOpenObserve() bool {
	metricsSourcePollers.Lock()
	defer metricsSourcePollers.Unlock()
	for p := range metricsSourcePollers.set {
		if p.offersOpenObserve.Load() {
			return true
		}
	}
	return false
}

// CheckMetricsSourceNow makes every running poller ask Hub for the metrics
// source and waits until each has applied the answer of a check that started
// after this call, or ctx ends. The check keeps the poller's minimum gap
// between requests. It reports whether any poller finished such a check.
func CheckMetricsSourceNow(ctx context.Context) bool {
	metricsSourcePollers.Lock()
	var waits []<-chan struct{}
	for p := range metricsSourcePollers.set {
		waits = append(waits, p.waitForPoll())
	}
	metricsSourcePollers.Unlock()
	for _, w := range waits {
		select {
		case <-w:
		case <-ctx.Done():
			return false
		}
	}
	return len(waits) > 0
}

type metricsSourcePoller struct {
	hubOrigin string
	token     string
	client    *http.Client
	relay     *http.Transport
	apply     func(MetricsSourceDecision)
	nudge     chan struct{}

	waitMu  sync.Mutex
	waiters []chan struct{}

	applied     *MetricsSourceResponse
	appliedKey  string
	health      *relayHealth
	lastFailure string
	// offersOpenObserve is read by HubOffersOpenObserve from other goroutines.
	offersOpenObserve atomic.Bool
}

func newMetricsSourcePoller(cfg Config) (*metricsSourcePoller, error) {
	origin, err := HubOriginFromWebSocketURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = hubTLSConfig(cfg.CA, cfg.InsecureSkipVerify)
	return &metricsSourcePoller{
		hubOrigin: origin,
		token:     cfg.Token,
		client:    &http.Client{Timeout: metricsSourceTimeout, Transport: transport},
		relay:     transport,
		apply:     cfg.OnMetricsSource,
		nudge:     make(chan struct{}, 1),
	}, nil
}

func (p *metricsSourcePoller) run(ctx context.Context) {
	metricsSourcePollers.Lock()
	metricsSourcePollers.set[p] = struct{}{}
	metricsSourcePollers.Unlock()
	defer func() {
		metricsSourcePollers.Lock()
		delete(metricsSourcePollers.set, p)
		metricsSourcePollers.Unlock()
	}()

	retry := metricsSourceRetryMin
	ok := p.pollForWaiters(ctx)
	last := time.Now()
	for {
		var retryTimer *time.Timer
		var retryAfter <-chan time.Time
		if !ok {
			retryTimer = time.NewTimer(retry)
			retryAfter = retryTimer.C
		}
		select {
		case <-ctx.Done():
			if retryTimer != nil {
				retryTimer.Stop()
			}
			return
		case <-retryAfter:
			retry = min(retry*2, metricsSourceRetryMax)
		case <-p.nudge:
			if retryTimer != nil {
				retryTimer.Stop()
			}
			if wait := metricsSourceMinPollGap - time.Since(last); wait > 0 && !sleep(ctx, wait) {
				return
			}
		}
		if ok = p.pollForWaiters(ctx); ok {
			retry = metricsSourceRetryMin
		}
		last = time.Now()
	}
}

func (p *metricsSourcePoller) requestPoll() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// waitForPoll requests a check and returns a channel closed once a check that
// started after this call has finished.
func (p *metricsSourcePoller) waitForPoll() <-chan struct{} {
	ch := make(chan struct{})
	p.waitMu.Lock()
	p.waiters = append(p.waiters, ch)
	p.waitMu.Unlock()
	p.requestPoll()
	return ch
}

func (p *metricsSourcePoller) pollForWaiters(ctx context.Context) bool {
	p.waitMu.Lock()
	waiters := p.waiters
	p.waiters = nil
	p.waitMu.Unlock()
	ok := p.poll(ctx)
	for _, ch := range waiters {
		close(ch)
	}
	return ok
}

// poll reads the source and applies it when it changed, and reports whether
// the read succeeded. A failed read keeps whatever is in effect: in particular
// it never turns an OpenObserve relay back into the cluster's own Prometheus.
func (p *metricsSourcePoller) poll(ctx context.Context) bool {
	resp, err := p.fetch(ctx)
	if err == nil {
		var decision MetricsSourceDecision
		decision, err = p.decide(resp)
		if err == nil {
			p.lastFailure = ""
			p.offersOpenObserve.Store(resp.OpenObserve || resp.Source == MetricsSourceOpenObserve)
			if p.applied != nil && p.appliedKey == decision.Key && reflect.DeepEqual(*p.applied, decision.Response) {
				return true
			}
			p.applied, p.appliedKey = &decision.Response, decision.Key
			p.apply(decision)
			return true
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if msg := err.Error(); msg != p.lastFailure {
		p.lastFailure = msg
		log.Printf("[cloud] metrics source check failed; keeping the current source: %s", msg)
	}
	return false
}

func (p *metricsSourcePoller) fetch(ctx context.Context) (MetricsSourceResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.hubOrigin+metricsSourcePath, nil)
	if err != nil {
		return MetricsSourceResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Hub API redirects are not allowed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return MetricsSourceResponse{}, fmt.Errorf("reach Radar Hub: %s", prom.RedactURLs(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// A Hub that predates the endpoint has no other source to offer.
		return MetricsSourceResponse{Source: MetricsSourcePrometheus}, nil
	case http.StatusUnauthorized:
		return MetricsSourceResponse{}, errors.New("Radar Hub rejected this cluster's token")
	default:
		return MetricsSourceResponse{}, fmt.Errorf("Radar Hub answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetricsSourceBodyBytes+1))
	if err != nil {
		return MetricsSourceResponse{}, fmt.Errorf("read metrics source: %w", err)
	}
	if len(body) > maxMetricsSourceBodyBytes {
		return MetricsSourceResponse{}, errors.New("read metrics source: response is too large")
	}
	var out MetricsSourceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return MetricsSourceResponse{}, fmt.Errorf("decode metrics source: %w", err)
	}
	return out, nil
}

func (p *metricsSourcePoller) decide(resp MetricsSourceResponse) (MetricsSourceDecision, error) {
	switch resp.Source {
	case MetricsSourcePrometheus:
		return MetricsSourceDecision{Response: resp, Key: MetricsSourcePrometheus}, nil
	case MetricsSourceOpenObserve:
	default:
		return MetricsSourceDecision{}, fmt.Errorf("unknown metrics source %q", resp.Source)
	}
	if err := validateRelayPath(resp.RelayPath); err != nil {
		return MetricsSourceDecision{}, err
	}
	// Without a matcher the agent could not check that answers are this
	// cluster's; Hub never selects OpenObserve without one.
	if len(resp.Matchers) == 0 {
		return MetricsSourceDecision{}, errors.New("metrics source has no cluster matcher")
	}
	seen := map[string]bool{}
	for _, m := range resp.Matchers {
		if !relayLabelName.MatchString(m.Name) || strings.HasPrefix(m.Name, "__") || m.Value == "" || seen[m.Name] {
			return MetricsSourceDecision{}, fmt.Errorf("metrics source has an invalid cluster matcher %q", m.Name)
		}
		seen[m.Name] = true
	}

	key := p.relayKey(resp)
	if key != p.appliedKey || p.health == nil {
		p.health = &relayHealth{}
	}
	health := p.health
	origin, relayPath, token, shared := p.hubOrigin, resp.RelayPath, p.token, p.relay
	return MetricsSourceDecision{
		Response: resp,
		Key:      key,
		Endpoint: origin + relayPath,
		Health:   health.Status,
		NewTransport: func(timeout time.Duration, maxResponseBytes int64) prom.Transport {
			return &HubRelayTransport{
				HubOrigin:        origin,
				RelayPath:        relayPath,
				Token:            token,
				HTTPClient:       &http.Client{Transport: shared, Timeout: timeout},
				MaxResponseBytes: maxResponseBytes,
				health:           health,
				onNotSelected:    p.requestPoll,
			}
		},
	}, nil
}

// relayKey covers everything the relay connection depends on. The token is
// hashed in, never stored. Hub's connection revision is in it because Hub
// can move to another OpenObserve (endpoint or organization) and keep the
// cluster's matchers: a new key is what invalidates work done against the
// old store, such as a running rightsizing scan.
func (p *metricsSourcePoller) relayKey(resp MetricsSourceResponse) string {
	matchers := make([]string, 0, len(resp.Matchers))
	for _, m := range resp.Matchers {
		matchers = append(matchers, m.Name+"="+m.Value)
	}
	slices.Sort(matchers)
	h := sha256.New()
	for _, part := range append([]string{p.hubOrigin, p.token, resp.RelayPath, resp.Revision}, matchers...) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validateRelayPath keeps the cluster token on Hub's relay routes: the path
// comes from Hub's answer and is joined onto Hub's origin.
func validateRelayPath(p string) error {
	if !strings.HasPrefix(p, metricsRelayPathPrefix) || len(p) == len(metricsRelayPathPrefix) ||
		path.Clean(p) != p || strings.ContainsAny(p, "?#%\\") {
		return fmt.Errorf("metrics source has an invalid relay path %q", p)
	}
	return nil
}
