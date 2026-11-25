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

// Hub has no channel to push configuration to the agent, so the agent asks.
// A refresh nudge over the tunnel shortens the wait after a settings change.
var (
	metricsSourcePollInterval = 30 * time.Second
	metricsSourceMinPollGap   = 2 * time.Second
	metricsSourceTimeout      = 10 * time.Second
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

// metricsSourceNudges holds one channel per running poller, so a refresh
// request from Hub reaches whichever tunnel is current.
var metricsSourceNudges = struct {
	sync.Mutex
	set map[chan struct{}]struct{}
}{set: map[chan struct{}]struct{}{}}

// RequestMetricsSourceRefresh makes every running poller ask Hub for the
// metrics source now instead of at its next interval.
func RequestMetricsSourceRefresh() {
	metricsSourceNudges.Lock()
	defer metricsSourceNudges.Unlock()
	for ch := range metricsSourceNudges.set {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type metricsSourcePoller struct {
	hubOrigin string
	token     string
	client    *http.Client
	relay     *http.Transport
	apply     func(MetricsSourceDecision)
	nudge     chan struct{}

	applied     *MetricsSourceResponse
	appliedKey  string
	health      *relayHealth
	lastFailure string
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
	metricsSourceNudges.Lock()
	metricsSourceNudges.set[p.nudge] = struct{}{}
	metricsSourceNudges.Unlock()
	defer func() {
		metricsSourceNudges.Lock()
		delete(metricsSourceNudges.set, p.nudge)
		metricsSourceNudges.Unlock()
	}()

	ticker := time.NewTicker(metricsSourcePollInterval)
	defer ticker.Stop()
	p.poll(ctx)
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.nudge:
			if wait := metricsSourceMinPollGap - time.Since(last); wait > 0 && !sleep(ctx, wait) {
				return
			}
		}
		p.poll(ctx)
		last = time.Now()
	}
}

func (p *metricsSourcePoller) requestPoll() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// poll reads the source and applies it when it changed. A failed read keeps
// whatever is in effect: in particular it never turns an OpenObserve relay
// back into the cluster's own Prometheus.
func (p *metricsSourcePoller) poll(ctx context.Context) {
	resp, err := p.fetch(ctx)
	if err == nil {
		var decision MetricsSourceDecision
		decision, err = p.decide(resp)
		if err == nil {
			p.lastFailure = ""
			if p.applied != nil && p.appliedKey == decision.Key && reflect.DeepEqual(*p.applied, decision.Response) {
				return
			}
			p.applied, p.appliedKey = &decision.Response, decision.Key
			p.apply(decision)
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	if msg := err.Error(); msg != p.lastFailure {
		p.lastFailure = msg
		log.Printf("[cloud] metrics source check failed; keeping the current source: %s", msg)
	}
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
