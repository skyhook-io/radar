// Package argocd manages Radar's connection to the Argo CD API server:
// configuration, endpoint discovery, reachability/auth probing, and a small
// response cache. It mirrors internal/prometheus's manager-around-a-pure-client
// shape, with pkg/argoapi doing the actual HTTP calls.
package argocd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

var (
	// ErrUnreachable means the Argo CD API server could not be reached (or
	// no server was found by discovery).
	ErrUnreachable = errors.New("argocd: server unreachable")
	// ErrTokenInvalid means the server responded but rejected the configured
	// token (or no token is configured and the server requires one).
	ErrTokenInvalid = errors.New("argocd: token invalid")
)

const managedResourcesTTL = 15 * time.Second

// probeTimeout bounds the background probe of persisted settings;
// probeRetryInterval throttles retries after a failed probe so a dead
// argocd-server doesn't get hammered on every insights request.
const (
	probeTimeout       = 15 * time.Second
	probeRetryInterval = 30 * time.Second
)

type cacheEntry struct {
	items   []argoapi.ResourceDiff
	expires time.Time
}

// Manager holds the Argo CD connection state. Use NewManager (or the
// package-level functions backed by the default instance).
type Manager struct {
	mu sync.Mutex

	seeded      bool
	manualURL   string
	token       string
	insecureTLS bool

	// generation bumps on every SetConfig/Reset. A Probe captures it before
	// its (unlocked) network I/O and commits the resolved connection only if
	// generation is unchanged — so a slow probe for stale settings can't
	// resurrect a dropped connection after a config change or context switch.
	generation uint64

	probing   bool
	nextProbe time.Time

	baseURL string
	client  *argoapi.Client
	forward *activeForward

	cache map[string]cacheEntry

	// tokenContext is the kubeconfig context the stored token is bound to when
	// the URL is empty (auto-discovery). Discovery resolves whatever
	// argocd-server exists in the CURRENT cluster — and the in-cluster Service
	// DNS is identical across clusters — so a token must never be sent to a
	// discovered server in a DIFFERENT cluster than it was configured for.
	// Empty means "not bound" (explicit-URL mode, where the origin guard
	// governs instead).
	tokenContext string

	k8sClient   func() kubernetes.Interface
	k8sConfig   func() *rest.Config
	inCluster   func() bool
	contextName func() string
	loadConfig  func() config.Config
}

// NewManager builds a Manager wired to the live internal/k8s connection.
// The k8s accessors are resolved lazily per call, so the Manager follows
// kubeconfig context switches without a reinit step.
func NewManager() *Manager {
	return &Manager{
		k8sClient: func() kubernetes.Interface {
			// k8s.GetClient returns a concrete *Clientset; convert nil to a
			// nil interface so callers can compare against nil.
			if c := k8s.GetClient(); c != nil {
				return c
			}
			return nil
		},
		k8sConfig:   k8s.GetConfig,
		inCluster:   k8s.IsInCluster,
		contextName: k8s.GetContextName,
		loadConfig:  config.Load,
	}
}

var defaultManager = NewManager()

// SetConfig applies new connection settings on the default manager.
func SetConfig(url, token string, insecureTLS bool) {
	defaultManager.SetConfig(url, token, insecureTLS)
}

// Reset clears the default manager's connection state (used on context switch).
func Reset() { defaultManager.Reset() }

// Get returns the default manager's connected client, or (nil, false).
func Get() (*argoapi.Client, bool) { return defaultManager.Get() }

// Probe resolves and verifies the default manager's connection.
func Probe(ctx context.Context) error { return defaultManager.Probe(ctx) }

// Address returns the default manager's resolved base URL ("" when not connected).
func Address() string { return defaultManager.Address() }

// ManagedResourcesCached fetches managed resources via the default manager.
func ManagedResourcesCached(ctx context.Context, q argoapi.ManagedResourcesQuery) ([]argoapi.ResourceDiff, error) {
	return defaultManager.ManagedResourcesCached(ctx, q)
}

// TokenFromCLI reads the auth token from the user's Argo CD CLI config
// (~/.config/argocd/config) for the given server URL. Empty serverURL uses
// the CLI's current-context.
func TokenFromCLI(serverURL string) (string, error) {
	return argoapi.TokenFromCLIConfig("", serverURL)
}

// SetConfig re-points the manager immediately: connection state and cache are
// dropped so the next Probe/Get resolves against the new settings. Empty url
// enables auto-discovery.
func (m *Manager) SetConfig(url, token string, insecureTLS bool) {
	m.mu.Lock()
	m.seeded = true
	m.manualURL = strings.TrimRight(strings.TrimSpace(url), "/")
	m.token = token
	m.insecureTLS = insecureTLS
	// Bind the token to the current context for the auto-discovery case; empty
	// URL means the token will be used against whatever argocd-server the
	// current cluster exposes, so it's only valid while that cluster is active.
	m.tokenContext = m.currentContextName()
	m.generation++
	fwd := m.dropConnectionLocked()
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
}

func (m *Manager) currentContextName() string {
	if m.contextName == nil {
		return ""
	}
	return m.contextName()
}

// Reset drops connection state and cache but keeps the configured
// URL/token — after a kubeconfig context switch the next Probe rediscovers
// against the new cluster.
func (m *Manager) Reset() {
	m.mu.Lock()
	m.generation++
	fwd := m.dropConnectionLocked()
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
}

func (m *Manager) dropConnectionLocked() *activeForward {
	m.baseURL = ""
	m.client = nil
	m.cache = nil
	fwd := m.forward
	m.forward = nil
	return fwd
}

// Get returns the connected client without any network I/O. Returns
// (nil, false) when unconfigured or not yet (successfully) probed.
func (m *Manager) Get() (*argoapi.Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		// Settings persisted by a previous run seed lazily, but nothing would
		// ever probe them — a restart would silently disable the integration
		// until the user re-saved Settings. Kick one throttled background
		// probe; callers see connected=false until it lands.
		m.maybeProbeInBackgroundLocked()
		return nil, false
	}
	return m.client, true
}

// maybeProbeInBackgroundLocked starts a background Probe when the manager has
// persisted settings (an explicit URL or a token) but no live client yet.
// Deliberately does nothing when neither is configured — auto-discovery
// probing on every Get would run service discovery on each insights request
// for users who never enabled the integration. Failures throttle retries to
// once per probeRetryInterval. Caller must hold m.mu.
func (m *Manager) maybeProbeInBackgroundLocked() {
	m.ensureSeededLocked()
	if m.manualURL == "" && m.token == "" {
		return
	}
	if m.probing || time.Now().Before(m.nextProbe) {
		return
	}
	m.probing = true
	tok := m.token
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		err := m.Probe(ctx)
		m.mu.Lock()
		m.probing = false
		if err != nil {
			m.nextProbe = time.Now().Add(probeRetryInterval)
		}
		m.mu.Unlock()
		if err != nil && !errors.Is(err, errStaleProbe) {
			log.Printf("[argocd] background probe of persisted settings failed (retrying in %s): %v", probeRetryInterval, redactToken(err.Error(), tok))
		}
	}()
}

// Address returns the resolved base URL, or "" when not connected.
func (m *Manager) Address() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// Probe resolves the Argo CD endpoint (manual URL or discovery), verifies
// reachability, and — when a token is configured — verifies the token via
// session/userinfo. Errors wrap ErrUnreachable or ErrTokenInvalid so callers
// can map them to distinct messages.
func (m *Manager) Probe(ctx context.Context) error {
	m.mu.Lock()
	m.ensureSeededLocked()
	// Bracket the client/config capture with two context reads. The kubeconfig
	// context lives in the k8s package, not under m.mu, so a switch could race
	// this capture; if the name changed across the bracket, the captured
	// client/config may be inconsistent with tokenContext — fail closed.
	ctxBefore := m.currentContextName()
	snap := m.snapshotLocked()
	ctxAfter := m.currentContextName()
	m.mu.Unlock()

	// Auto-discovery (empty URL) resolves whatever argocd-server the CURRENT
	// cluster exposes. A token bound to a different context must not be sent to
	// it — the in-cluster Service DNS is identical across clusters, so the URL
	// can't distinguish them. Drop the token when it isn't bound to the captured
	// context, OR when a switch raced the capture (ctxBefore != ctxAfter). The
	// probe then fails auth (or connects unauthenticated) rather than leak the
	// token to another cluster's Argo. Explicit-URL tokens are governed by the
	// origin guard, not this check.
	contextStable := ctxBefore == ctxAfter
	if snap.manualURL == "" && snap.token != "" && (!contextStable || snap.tokenContext != ctxAfter) {
		snap.token = ""
	}

	// All network I/O below uses `snap` — a single coherent (url-intent, token,
	// tls) capture — never live m.* fields. So a concurrent SetConfig/Reset can
	// never make this probe send snap's token to a different origin, or a
	// different token to snap's origin; a changed config just invalidates the
	// whole result at the generation check.
	url, err := m.resolve(ctx, snap)
	if err != nil {
		return err
	}
	if err := m.verifyAuth(ctx, url, snap); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.staleLocked(snap) {
		// Config changed while we were probing off-lock. Discard rather than
		// resurrect a connection to a target the user has moved away from.
		return errStaleProbe
	}
	if m.baseURL != url {
		m.baseURL = url
		m.cache = nil
	}
	m.client = newClient(url, snap.token, snap.insecureTLS)
	return nil
}

// probeSnapshot is an immutable capture of the config a single Probe uses, so
// concurrent config changes can't make one probe mix credentials or origins.
type probeSnapshot struct {
	generation   uint64
	manualURL    string
	token        string
	insecureTLS  bool
	tokenContext string
	// k8sClient + k8sConfig are captured at snapshot time so BOTH candidate
	// discovery and the port-forward dial target the SAME cluster the token was
	// captured against. If a context switch swaps the live client/config
	// mid-probe, these frozen handles keep the token pointed at its own
	// cluster's Argo — never the new one — and the generation check then
	// discards the superseded result.
	k8sClient kubernetes.Interface
	k8sConfig *rest.Config
}

func (m *Manager) snapshotLocked() probeSnapshot {
	var k8sc kubernetes.Interface
	if m.k8sClient != nil {
		k8sc = m.k8sClient()
	}
	var cfg *rest.Config
	if m.k8sConfig != nil {
		cfg = m.k8sConfig()
	}
	return probeSnapshot{
		generation:   m.generation,
		manualURL:    m.manualURL,
		token:        m.token,
		insecureTLS:  m.insecureTLS,
		tokenContext: m.tokenContext,
		k8sClient:    k8sc,
		k8sConfig:    cfg,
	}
}

// staleLocked reports whether a config change (SetConfig/Reset) has superseded
// the probe that captured snap. Caller must hold m.mu.
func (m *Manager) staleLocked(snap probeSnapshot) bool {
	return m.generation != snap.generation
}

// errStaleProbe is an internal sentinel: a Probe found the manager's config had
// changed under it and refused to commit. Not surfaced to users — callers treat
// a Probe as "not connected yet" and retry.
var errStaleProbe = errors.New("argocd: probe superseded by a config change")

// ManagedResourcesCached returns the app's managed-resource diffs, serving
// from a 15s TTL cache keyed by (appNamespace, appName). Queries carrying
// per-resource filters bypass the cache — mixing filtered results into the
// app-level key would poison it for unfiltered callers.
func (m *Manager) ManagedResourcesCached(ctx context.Context, q argoapi.ManagedResourcesQuery) ([]argoapi.ResourceDiff, error) {
	filtered := q.Group != "" || q.Kind != "" || q.Namespace != "" || q.Name != ""
	key := q.AppNamespace + "\x00" + q.AppName

	if !filtered {
		m.mu.Lock()
		if e, ok := m.cache[key]; ok && time.Now().Before(e.expires) {
			items := e.items
			m.mu.Unlock()
			return items, nil
		}
		m.mu.Unlock()
	}

	client, ok := m.Get()
	if !ok {
		if err := m.Probe(ctx); err != nil {
			return nil, err
		}
		if client, ok = m.Get(); !ok {
			return nil, fmt.Errorf("%w: connection was reset", ErrUnreachable)
		}
	}

	items, err := client.ManagedResources(ctx, q)
	if err != nil {
		return nil, err
	}

	if !filtered {
		m.mu.Lock()
		now := time.Now()
		for k, e := range m.cache {
			if now.After(e.expires) {
				delete(m.cache, k)
			}
		}
		if m.cache == nil {
			m.cache = make(map[string]cacheEntry)
		}
		m.cache[key] = cacheEntry{items: items, expires: now.Add(managedResourcesTTL)}
		m.mu.Unlock()
	}
	return items, nil
}

// resolve returns a reachable base URL: the already-connected one if it still
// responds, else the snapshot's manual URL, else discovery. All reachability
// clients use the snapshot's credentials. Errors wrap ErrUnreachable.
func (m *Manager) resolve(ctx context.Context, snap probeSnapshot) (string, error) {
	m.mu.Lock()
	baseURL := m.baseURL
	m.mu.Unlock()

	if baseURL != "" {
		if m.probeEndpoint(ctx, baseURL, snap) == nil {
			return baseURL, nil
		}
		m.mu.Lock()
		if m.baseURL == baseURL {
			fwd := m.dropConnectionLocked()
			m.mu.Unlock()
			if fwd != nil {
				fwd.stop()
			}
		} else {
			m.mu.Unlock()
		}
	}

	if snap.manualURL != "" {
		if err := m.probeEndpoint(ctx, snap.manualURL, snap); err != nil {
			return "", fmt.Errorf("%w: Argo CD at %s: %v", ErrUnreachable, snap.manualURL, err)
		}
		return snap.manualURL, nil
	}

	return m.discover(ctx, snap)
}

// verifyAuth checks the snapshot's token against session/userinfo. With no
// token configured, reachability is the only possible check.
func (m *Manager) verifyAuth(ctx context.Context, url string, snap probeSnapshot) error {
	if snap.token == "" {
		return nil
	}
	client := newClient(url, snap.token, snap.insecureTLS)
	info, err := client.UserInfo(ctx)
	if err != nil {
		if errors.Is(err, argoapi.ErrUnauthorized) {
			return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
		}
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if !info.LoggedIn {
		return fmt.Errorf("%w: session/userinfo reports loggedIn=false", ErrTokenInvalid)
	}
	return nil
}

// probeEndpoint checks that an Argo CD API server answers at url. A 401/403
// still proves reachability — auth is verifyAuth's concern.
func (m *Manager) probeEndpoint(ctx context.Context, url string, snap probeSnapshot) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := newClient(url, snap.token, snap.insecureTLS).Version(probeCtx)
	if err == nil || errors.Is(err, argoapi.ErrUnauthorized) {
		return nil
	}
	return err
}

func newClient(url, token string, insecureTLS bool) *argoapi.Client {
	return argoapi.New(argoapi.Options{
		BaseURL:               url,
		Token:                 token,
		InsecureSkipTLSVerify: insecureTLS,
	})
}

// ensureSeededLocked adopts the persisted config on first use, so settings
// saved by a previous run apply without explicit startup wiring.
func (m *Manager) ensureSeededLocked() {
	if m.seeded {
		return
	}
	m.seeded = true
	c := m.loadConfig()
	m.manualURL = strings.TrimRight(strings.TrimSpace(c.ArgoCDURL), "/")
	m.token = c.ArgoCDToken
	m.insecureTLS = c.ArgoCDInsecureTLS
}

// redactToken masks the bearer token if it appears verbatim in a string
// (e.g. an upstream error or misconfigured echo-proxy body) before it is
// logged, so the credential never lands in logs.
func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted-token>")
}
