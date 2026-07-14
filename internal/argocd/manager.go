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
	"net/url"
	"os"
	"strconv"
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

// Git commit metadata is small and per-revision; a longer TTL makes browsing
// deploy history cheap while still refreshing drifting tags/signature. The cap
// bounds memory across many revisions.
const (
	revisionMetadataTTL      = 5 * time.Minute
	revisionMetadataCacheCap = 256
)

type revMetaEntry struct {
	meta    *argoapi.RevisionMetadata
	expires time.Time
}

// repositoriesTTL keeps the (global, per-connection) repository list cached long
// enough that the 2s insights poll hits the cache instead of re-listing repos
// upstream on every request.
const repositoriesTTL = 60 * time.Second

// probeTimeout bounds the background probe of persisted settings;
// probeRetryInterval throttles retries after a failed probe so a dead
// argocd-server doesn't get hammered on every insights request.
const (
	probeTimeout       = 15 * time.Second
	probeRetryInterval = 30 * time.Second

	// repoErrorBackoff throttles repo-list refetches after a failure (commonly a
	// token without `repositories, get`), so insights' 2s poll doesn't hammer
	// argocd-server. Longer than probeRetryInterval since repo-scope denials are
	// usually persistent until the operator widens the token.
	repoErrorBackoff = 2 * time.Minute
)

type cacheEntry struct {
	items   []argoapi.ResourceDiff
	expires time.Time
}

// Manager holds the Argo CD connection state. Use NewManager (or the
// package-level functions backed by the default instance).
type Manager struct {
	mu sync.Mutex

	// probeMu serializes Probe executions (see Probe). Always acquired BEFORE
	// mu within a probe; never held together with mu across network I/O.
	probeMu sync.Mutex

	seeded      bool
	manualURL   string
	token       string
	insecureTLS bool
	// envManaged is set when the integration was provisioned from the environment
	// (RADAR_ARGOCD_TOKEN[_FILE]) rather than the Settings UI — including the case
	// where that provisioning was ATTEMPTED but failed (see envError). Such a
	// config is the declarative source of truth: the UI renders it read-only, the
	// apply handler refuses to change it, and it is never written to disk.
	envManaged bool
	// envError, when non-empty, is the sanitized reason environment provisioning
	// failed (bad token file, invalid URL, etc.). It keeps envManaged=true with no
	// token so the integration fails closed — it must NOT fall back to a stale
	// on-disk token — while the UI surfaces "the env config is invalid".
	envError string

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

	// revMetaCache holds Git commit metadata keyed by
	// (appNamespace, app, sourceIndex, revision). Cleared alongside `cache` on
	// reconnect/context switch so it never serves another cluster's data.
	revMetaCache map[string]revMetaEntry

	// repoCache holds the (global) repository list + connection state. Single
	// entry with a TTL; repoFetchMu serializes the upstream list so a burst of
	// insights requests triggers one fetch, not one per request. Cleared on
	// reconnect/context switch.
	repoCache        []argoapi.Repository
	repoCacheExpires time.Time
	repoFetchMu      sync.Mutex
	// repoRetryAfter throttles the background repo-list refetch after a failure,
	// so a token that lacks `repositories, get` (the documented default) doesn't
	// re-hit argocd-server on every 2s insights poll. Reset on reconnect/switch.
	repoRetryAfter time.Time

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
func SetConfig(url, token string, insecureTLS bool, tokenIsFresh bool) {
	defaultManager.SetConfig(url, token, insecureTLS, tokenIsFresh)
}

// RestoreConfig rolls the default manager back to a captured state, including
// the token's context binding.
func RestoreConfig(url, token string, insecureTLS bool, tokenContext string) {
	defaultManager.RestoreConfig(url, token, insecureTLS, tokenContext)
}

// Env var names for declarative (headless / in-cluster / Cloud) provisioning.
const (
	envArgoToken     = "RADAR_ARGOCD_TOKEN"
	envArgoTokenFile = "RADAR_ARGOCD_TOKEN_FILE"
	envArgoURL       = "RADAR_ARGOCD_URL"
	envArgoInsecure  = "RADAR_ARGOCD_INSECURE_TLS"
)

// SeedFromEnvVars provisions the Argo CD integration from environment variables,
// for deployments with no interactive Settings session (helm/in-cluster, Cloud
// tunnel). RADAR_ARGOCD_TOKEN_FILE (a mounted-secret path) takes precedence over
// the inline RADAR_ARGOCD_TOKEN — file mounts don't expose the value via
// /proc/<pid>/environ. When a token is present the integration becomes
// environment-managed (read-only in the UI, never persisted). Returns
// (false, nil) when no token env is set, leaving the interactive path unchanged.
// Must run before the server serves so the lazy config seed can't clobber it.
func SeedFromEnvVars() (bool, error) {
	url, token, insecure, ok, err := resolveEnvArgo(os.Getenv, os.ReadFile)
	if err != nil {
		// The operator set the Argo env vars but they're invalid. Mark the manager
		// env-managed-but-errored so it fails closed (no stale disk token) and the
		// UI can show the reason. The error is already sanitized (no token/raw URL).
		if envArgoAttempted(os.Getenv) {
			defaultManager.SeedFromEnvFailed(sanitizeEnvErr(err))
		}
		return false, err
	}
	if !ok {
		// No token resolved. If env provisioning was clearly attempted (a URL/token
		// var is set but incomplete), still fail closed rather than fall back to a
		// stale on-disk token; a truly empty environment leaves the disk path alone.
		if envArgoAttempted(os.Getenv) {
			defaultManager.SeedFromEnvFailed("no token provided — set " + envArgoToken + " or " + envArgoTokenFile)
		}
		return false, nil
	}
	defaultManager.SeedFromEnv(url, token, insecure)
	return true, nil
}

// envArgoAttempted reports whether the operator clearly tried to provision Argo CD
// from the environment — a token, token-file, or URL is set. It gates the
// fail-closed suppression of the on-disk fallback so a truly empty environment
// still uses the interactive/disk config unchanged. (Insecure-TLS alone is too
// weak a signal — it's a modifier, not an intent to configure.)
func envArgoAttempted(getenv func(string) string) bool {
	return strings.TrimSpace(getenv(envArgoToken)) != "" ||
		strings.TrimSpace(getenv(envArgoTokenFile)) != "" ||
		strings.TrimSpace(getenv(envArgoURL)) != ""
}

// sanitizeEnvErr strips the "argocd: " prefix for a UI-facing message. The
// resolveEnvArgo errors are already token- and raw-URL-free.
func sanitizeEnvErr(err error) string {
	return strings.TrimPrefix(err.Error(), "argocd: ")
}

// resolveEnvArgo parses the Argo CD env config with all precedence + validation
// rules, without touching the manager — so the source-precedence and validation
// paths are unit-testable. ok is false (with nil err) when no token is set, which
// leaves the interactive path untouched. getenv/readFile are injected for tests.
func resolveEnvArgo(getenv func(string) string, readFile func(string) ([]byte, error)) (url, token string, insecure, ok bool, err error) {
	if f := strings.TrimSpace(getenv(envArgoTokenFile)); f != "" {
		// A mounted-secret file takes precedence over the inline env var — the file
		// value isn't exposed via /proc/<pid>/environ.
		b, rerr := readFile(f)
		if rerr != nil {
			return "", "", false, false, fmt.Errorf("argocd: read %s=%q: %w", envArgoTokenFile, f, rerr)
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return "", "", false, false, fmt.Errorf("argocd: %s=%q is empty", envArgoTokenFile, f)
		}
	} else {
		token = strings.TrimSpace(getenv(envArgoToken))
	}

	rawURL := strings.TrimSpace(getenv(envArgoURL))
	rawInsecure := strings.TrimSpace(getenv(envArgoInsecure))

	if token == "" {
		// A URL or TLS flag without a token can't authenticate the deep-diff — warn
		// so a half-set env config doesn't silently do nothing.
		if rawURL != "" || rawInsecure != "" {
			log.Printf("[argocd] %s/%s is set but no token (%s or %s) — ignoring the environment Argo CD config",
				envArgoURL, envArgoInsecure, envArgoToken, envArgoTokenFile)
		}
		return "", "", false, false, nil
	}

	// The env URL is a non-UI input that ends up in status/config/logs, so validate
	// it at least as strictly as the Settings PUT: http(s) scheme, a host, and no
	// embedded userinfo credentials (which would leak into those surfaces).
	if rawURL != "" {
		if verr := validateEnvArgoURL(rawURL); verr != nil {
			return "", "", false, false, fmt.Errorf("argocd: invalid %s: %w", envArgoURL, verr)
		}
	}

	if rawInsecure != "" {
		v, perr := strconv.ParseBool(rawInsecure)
		if perr != nil {
			// Don't echo the value — a miswired Secret key could point this at the
			// token, which would then land in the logs.
			return "", "", false, false, fmt.Errorf("argocd: %s is not a boolean (use true/false)", envArgoInsecure)
		}
		insecure = v
	}

	return rawURL, token, insecure, true, nil
}

// validateEnvArgoURL strictly validates an argocd-server URL: http(s) scheme, a
// host, and no embedded userinfo / query / fragment. The env URL flows into
// /api/config, the status address, and logs, so credentials in userinfo or a
// query (e.g. ?token=…) must be rejected. This is stricter than the Settings PUT
// (which checks only the scheme); see the divergence note in argocd_integration.go.
func validateEnvArgoURL(raw string) error {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		// Don't wrap the parse error: url.Parse echoes the raw input, which could
		// carry embedded credentials (e.g. a malformed userinfo) into the logs.
		return errors.New("could not be parsed as a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must be an http(s) URL (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	if u.User != nil {
		return errors.New("must not embed userinfo credentials")
	}
	// A query or fragment can carry credentials (e.g. ?token=…) that would then
	// surface in /api/config and logs — an argocd-server URL has neither. A path
	// is allowed (ingress prefixes like /argocd are legitimate).
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not include a query or fragment")
	}
	return nil
}

// SeedFromEnv provisions the default manager from an already-resolved
// environment token (see SeedFromEnvVars).
func SeedFromEnv(url, token string, insecureTLS bool) {
	defaultManager.SeedFromEnv(url, token, insecureTLS)
}

// SeedFromEnvFailed marks the default manager env-managed-but-errored (see the
// Manager method) — used when env provisioning was attempted but didn't resolve.
func SeedFromEnvFailed(reason string) { defaultManager.SeedFromEnvFailed(reason) }

// IsEnvManaged reports whether the token was provisioned from the environment.
func IsEnvManaged() bool { return defaultManager.IsEnvManaged() }

// EnvManagedConfig returns the effective env-managed URL + TLS mode (ok=false when
// not env-managed). The token is never returned.
func EnvManagedConfig() (url string, insecureTLS bool, ok bool) {
	return defaultManager.EnvManagedConfig()
}

// EnvManagedError returns the sanitized reason environment provisioning failed
// ("" when it succeeded or isn't env-managed).
func EnvManagedError() string { return defaultManager.EnvManagedError() }

// ValidateServerURL strictly validates an argocd-server URL: http(s) scheme, a
// host, and no embedded userinfo / query / fragment. Used for BOTH the env input
// and the Settings PUT so a credential-bearing URL (e.g. https://user:pass@host
// or ?token=…) can't be persisted and then surfaced in /api/config, the status
// address, or logs. Returns a UI-safe error (never echoes the raw input).
func ValidateServerURL(raw string) error { return validateEnvArgoURL(raw) }

// IsConfigured reports whether the default manager has connection settings.
func IsConfigured() bool { return defaultManager.IsConfigured() }

// TokenContext returns the context the current token is bound to.
func TokenContext() string { return defaultManager.TokenContext() }

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

func RevisionMetadataCached(ctx context.Context, q argoapi.RevisionMetadataQuery) (*argoapi.RevisionMetadata, error) {
	return defaultManager.RevisionMetadataCached(ctx, q)
}

func RepositoriesCached() []argoapi.Repository {
	return defaultManager.RepositoriesCached()
}

// TokenFromCLI reads the auth token from the user's Argo CD CLI config
// (~/.config/argocd/config) for the given server URL. Empty serverURL uses
// the CLI's current-context.
func TokenFromCLI(serverURL string) (string, error) {
	return argoapi.TokenFromCLIConfig("", serverURL)
}

// CLISession returns the detected Argo CD CLI login (server + user, token-free),
// or nil when there is none — so the UI can offer "use your CLI session" only
// when it will actually work. Machine-local; no connection state involved.
func CLISession() (*argoapi.CLISession, error) {
	return argoapi.CLISessionFromConfig("")
}

// SetConfig re-points the manager immediately: connection state and cache are
// dropped so the next Probe/Get resolves against the new settings. Empty url
// enables auto-discovery. tokenIsFresh must be true ONLY when the token was
// explicitly provided by the user (typed or CLI-adopted) — a genuine "this
// token is for this cluster" action — and false when it's the stored token
// being carried forward. That distinction is load-bearing: re-saving other
// settings after a context switch must NOT rebind a preserved token to the new
// context (which would defeat the auto-discovery context guard), so tokenContext
// is only re-stamped for a fresh token.
func (m *Manager) SetConfig(url, token string, insecureTLS bool, tokenIsFresh bool) {
	m.mu.Lock()
	m.seeded = true
	// An explicit interactive config supersedes environment provisioning. In
	// practice the apply handler refuses the PUT while env-managed, so this path
	// isn't reached then; clearing the flag here keeps the "env is source of truth"
	// invariant inside the manager rather than resting solely on that HTTP gate.
	m.envManaged = false
	m.envError = ""
	m.manualURL = strings.TrimRight(strings.TrimSpace(url), "/")
	m.token = token
	m.insecureTLS = insecureTLS
	if tokenIsFresh {
		m.tokenContext = m.currentContextName()
	}
	m.bumpAndReset()
}

// SeedFromEnv provisions the manager from an environment-provided token. Like a
// fresh SetConfig it binds the token to the current context (so the
// auto-discovery cross-cluster guard accepts it for the cluster it's deployed
// in), and additionally marks the integration environment-managed: the apply
// handler refuses UI changes and the token is never written to disk. Runs at
// startup before the first Get, so the lazy config seed won't override it.
func (m *Manager) SeedFromEnv(url, token string, insecureTLS bool) {
	m.mu.Lock()
	m.seeded = true
	m.envManaged = true
	m.envError = ""
	m.manualURL = strings.TrimRight(strings.TrimSpace(url), "/")
	m.token = token
	m.insecureTLS = insecureTLS
	m.tokenContext = m.currentContextName()
	m.bumpAndReset()
}

// SeedFromEnvFailed marks the manager environment-managed but errored: the
// operator set the Argo CD env vars, but they didn't resolve to a usable token
// (unreadable/empty file, invalid URL, non-boolean TLS, or a URL with no token).
// It seeds NO credential and sets m.seeded so the lazy config load can't fall
// back to a stale on-disk token — the operator declared the environment as the
// source of truth, so honoring old disk creds would be wrong (potentially the
// wrong Argo, with the UI silently editable). The integration is left unconfigured
// (fails closed to annotation drift) with the reason surfaced for the UI.
func (m *Manager) SeedFromEnvFailed(reason string) {
	m.mu.Lock()
	m.seeded = true
	m.envManaged = true
	m.envError = reason
	m.manualURL = ""
	m.token = ""
	m.insecureTLS = false
	m.tokenContext = ""
	m.bumpAndReset()
}

// IsEnvManaged reports whether the token was provisioned from the environment.
func (m *Manager) IsEnvManaged() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSeededLocked()
	return m.envManaged
}

// EnvManagedConfig returns the effective URL + TLS mode when the integration is
// environment-managed, so callers (e.g. GET /api/config) can present the real
// endpoint instead of the stale on-disk values, which env-managed mode ignores.
// ok is false when not env-managed. The token is never returned.
func (m *Manager) EnvManagedConfig() (url string, insecureTLS bool, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSeededLocked()
	if !m.envManaged {
		return "", false, false
	}
	return m.manualURL, m.insecureTLS, true
}

// EnvManagedError returns the sanitized reason environment provisioning failed,
// or "" when it succeeded (or isn't env-managed). The UI surfaces this so a
// misconfigured declarative credential isn't invisible behind one startup log.
func (m *Manager) EnvManagedError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSeededLocked()
	return m.envError
}

// RestoreConfig rolls the manager back to a previously-captured state, INCLUDING
// the token's context binding. It exists for the connect-rollback path: the
// candidate SetConfig may have re-stamped tokenContext for a fresh token, and a
// plain SetConfig(fresh=false) rollback leaves that stamp in place — which would
// mark the restored (older) token as valid for the wrong cluster and defeat the
// auto-discovery cross-cluster guard. Restoring tokenContext explicitly keeps the
// guard intact.
func (m *Manager) RestoreConfig(url, token string, insecureTLS bool, tokenContext string) {
	m.mu.Lock()
	m.seeded = true
	// A rollback restores a previously-captured state faithfully; it must not
	// change env-managed-ness. (It's only reached from the apply handler, which
	// refuses the PUT while env-managed, so this only ever runs with it already
	// false — but hard-coding false here would be a latent way to drop the
	// read-only marker if that ever changed.)
	m.manualURL = strings.TrimRight(strings.TrimSpace(url), "/")
	m.token = token
	m.insecureTLS = insecureTLS
	m.tokenContext = tokenContext
	m.bumpAndReset()
}

// IsConfigured reports whether the Argo CD integration has connection settings
// (an explicit URL or a token) — i.e. the user has set it up, even if a live
// probe hasn't landed yet. Used to offer the deep-diff capability immediately
// after a restart while the background reconnect is still in flight, instead of
// showing "not connected" until the first probe completes.
func (m *Manager) IsConfigured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSeededLocked()
	return m.manualURL != "" || m.token != ""
}

func (m *Manager) currentContextName() string {
	if m.contextName == nil {
		return ""
	}
	return m.contextName()
}

// bumpAndReset invalidates in-flight probes (generation++), drops the live
// connection, releases m.mu, and stops any freed port-forward OUTSIDE the lock
// (fwd.stop must never run under m.mu). The caller must hold m.mu; it is released
// on return. Centralizing the epilogue keeps every config mutator's lock ordering
// identical — the stop-outside-lock invariant is expressed once, not five times.
func (m *Manager) bumpAndReset() {
	m.generation++
	fwd := m.dropConnectionLocked()
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
}

// Reset drops connection state and cache but keeps the configured
// URL/token — after a kubeconfig context switch the next Probe rediscovers
// against the new cluster.
func (m *Manager) Reset() {
	m.mu.Lock()
	m.bumpAndReset()
}

func (m *Manager) dropConnectionLocked() *activeForward {
	m.baseURL = ""
	m.client = nil
	m.cache = nil
	m.revMetaCache = nil
	m.repoCache = nil
	// Clear the retry throttles too, so a reconnect or context switch (which is
	// what dropped the connection) probes and refetches immediately instead of
	// staying suppressed for up to a retry interval on the PREVIOUS failure.
	m.nextProbe = time.Time{}
	m.repoRetryAfter = time.Time{}
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
	// Serialize probes. Background reconnection and the synchronous probe from
	// ManagedResourcesCached would otherwise run discovery + port-forward setup
	// concurrently and race on m.forward / m.baseURL / m.client — the generation
	// check only discards STALE results, it doesn't stop two same-generation
	// probes from both committing (and leaking a port-forward). One probe at a
	// time; the second re-snapshots and either no-ops or supersedes cleanly.
	m.probeMu.Lock()
	defer m.probeMu.Unlock()

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
	// cluster exposes. A token bound to a different context must NOT be sent to
	// it — the in-cluster Service DNS is identical across clusters, so the URL
	// can't distinguish them. When the configured token isn't bound to the
	// captured context (or a switch raced the capture), refuse to connect at all
	// rather than connect unauthenticated: an unauthenticated "connected" state
	// would light the deep-diff capability while every managed-resource call
	// 403s. Stay disconnected until the user re-confirms the token for this
	// cluster. Explicit-URL tokens are governed by the origin guard instead.
	contextStable := ctxBefore == ctxAfter
	if snap.manualURL == "" && snap.token != "" && (!contextStable || snap.tokenContext != ctxAfter) {
		return errTokenContextMismatch
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
		// resolve may have installed a fresh port-forward to a reachable
		// argocd-server; a token failure means we won't use that endpoint, so
		// tear the tunnel down rather than leave a disconnected manager holding a
		// live forward. Skip when a config change already superseded this probe —
		// that SetConfig/Reset owns the cleanup.
		m.mu.Lock()
		var fwd *activeForward
		if !m.staleLocked(snap) {
			fwd = m.dropConnectionLocked()
		}
		m.mu.Unlock()
		if fwd != nil {
			fwd.stop()
		}
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
		m.revMetaCache = nil
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

// errTokenContextMismatch means the configured token is bound to a different
// kubeconfig context than the current one (auto-discovery mode), so the probe
// refused to connect rather than send the token to another cluster or connect
// unauthenticated. It wraps ErrTokenInvalid: to every downstream error mapper
// this is effectively "the token isn't valid for this cluster", so it surfaces
// as a re-authenticate prompt instead of a misleading 502/"unreachable".
var errTokenContextMismatch = fmt.Errorf("argocd: token not valid for the current cluster context: %w", ErrTokenInvalid)

// connectedClient returns a live client, probing on demand. Get() is called
// first (so its background-reprobe nudge still fires); on a miss it probes
// synchronously and re-fetches, surfacing "connection was reset" if a config
// change raced the probe.
func (m *Manager) connectedClient(ctx context.Context) (*argoapi.Client, error) {
	if client, ok := m.Get(); ok {
		return client, nil
	}
	if err := m.Probe(ctx); err != nil {
		return nil, err
	}
	client, ok := m.Get()
	if !ok {
		return nil, fmt.Errorf("%w: connection was reset", ErrUnreachable)
	}
	return client, nil
}

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

	client, err := m.connectedClient(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	gen := m.generation
	m.mu.Unlock()

	items, err := client.ManagedResources(ctx, q)
	if err != nil {
		return nil, err
	}

	if !filtered {
		m.mu.Lock()
		// A SetConfig/Reset (context switch, reconnect) during the fetch bumps the
		// generation; the result is for a superseded cluster/config, so it must not
		// be cached under the app key — a later read for the new target would serve
		// the old cluster's manifests. Mirrors the probe path's staleLocked guard.
		if m.generation == gen {
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
		}
		m.mu.Unlock()
	}
	return items, nil
}

// RevisionMetadataCached returns Git commit metadata for a revision, cached per
// (appNamespace, app, sourceIndex, revision) with a bounded TTL, and connecting
// on demand like ManagedResourcesCached. The cache is cleared on reconnect /
// context switch (dropConnectionLocked), so it never serves another cluster's
// data.
func (m *Manager) RevisionMetadataCached(ctx context.Context, q argoapi.RevisionMetadataQuery) (*argoapi.RevisionMetadata, error) {
	key := q.AppNamespace + "\x00" + q.AppName + "\x00" + q.SourceIndex + "\x00" + q.Revision

	m.mu.Lock()
	if e, ok := m.revMetaCache[key]; ok && time.Now().Before(e.expires) {
		meta := e.meta
		m.mu.Unlock()
		return meta, nil
	}
	m.mu.Unlock()

	client, err := m.connectedClient(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	gen := m.generation
	m.mu.Unlock()

	meta, err := client.RevisionMetadata(ctx, q)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	// Don't cache a result whose config was superseded mid-fetch (see the
	// generation guard in ManagedResourcesCached); still return it to this caller.
	if m.generation != gen {
		m.mu.Unlock()
		return meta, nil
	}
	if m.revMetaCache == nil {
		m.revMetaCache = make(map[string]revMetaEntry)
	}
	now := time.Now()
	for k, e := range m.revMetaCache {
		if now.After(e.expires) {
			delete(m.revMetaCache, k)
		}
	}
	// Hard cap: if still full after the expiry sweep, drop an arbitrary entry.
	// Metadata is cheap to refetch, so exact LRU isn't worth the bookkeeping.
	if len(m.revMetaCache) >= revisionMetadataCacheCap {
		for k := range m.revMetaCache {
			delete(m.revMetaCache, k)
			break
		}
	}
	m.revMetaCache[key] = revMetaEntry{meta: meta, expires: now.Add(revisionMetadataTTL)}
	m.mu.Unlock()
	return meta, nil
}

// RepositoriesCached returns Argo CD's repository list with cached connection
// state, and NEVER blocks the caller on network I/O — repo health rides the
// insights hot path (polled every 2s), so it must not add the Argo client's
// timeout to it. A fresh cache is returned immediately; a stale/cold cache
// returns the last value (or nil) while a single background refresh runs, so
// the data appears at most one poll late. Stale is always same-cluster (the
// cache is cleared on reconnect/context switch).
func (m *Manager) RepositoriesCached() []argoapi.Repository {
	m.mu.Lock()
	repos := m.repoCache
	fresh := repos != nil && time.Now().Before(m.repoCacheExpires)
	m.mu.Unlock()
	if !fresh {
		m.refreshRepositoriesAsync()
	}
	return repos
}

// refreshRepositoriesAsync kicks at most one background repository fetch
// (TryLock skips if one is already running). It uses a detached, timeout-bounded
// context — the triggering request may end immediately — and a generation guard
// so a context switch mid-fetch can't cache the previous cluster's repos.
func (m *Manager) refreshRepositoriesAsync() {
	if !m.repoFetchMu.TryLock() {
		return
	}
	m.mu.Lock()
	gen := m.generation
	// Honor the post-failure backoff: a token without `repositories, get` would
	// otherwise 403 on every 2s insights poll, flooding argocd-server and logs.
	if time.Now().Before(m.repoRetryAfter) {
		m.mu.Unlock()
		m.repoFetchMu.Unlock()
		return
	}
	m.mu.Unlock()
	go func() {
		defer m.repoFetchMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()

		client, err := m.connectedClient(ctx)
		if err != nil {
			return
		}
		repos, err := client.Repositories(ctx)
		if err != nil {
			m.mu.Lock()
			if m.generation == gen {
				m.repoRetryAfter = time.Now().Add(repoErrorBackoff)
			}
			m.mu.Unlock()
			log.Printf("[argocd] repository list failed (backing off %s): %v", repoErrorBackoff, m.redactSelfToken(err.Error()))
			return
		}
		m.mu.Lock()
		if m.generation == gen {
			m.repoCache = repos
			m.repoCacheExpires = time.Now().Add(repositoriesTTL)
			m.repoRetryAfter = time.Time{}
		}
		m.mu.Unlock()
	}()
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
	// Reachability only — send NO token. /api/version answers 200 (public) or 401
	// either way, so the bearer token is never needed to prove an endpoint is up.
	// During auto-discovery this runs against every labeled candidate Service; a
	// tokenless probe means the configured token is disclosed only to the single
	// endpoint that verifyAuth later authenticates against, never to a losing (or
	// attacker-planted) candidate. A 401 still counts as reachable.
	_, err := newClient(url, "", snap.insecureTLS).Version(probeCtx)
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
	m.tokenContext = c.ArgoCDTokenContext
}

// TokenContext returns the kubeconfig context the current token is bound to
// (empty for explicit-URL tokens). The server persists this so the binding
// survives restarts.
func (m *Manager) TokenContext() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSeededLocked()
	return m.tokenContext
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

// RedactToken masks the default manager's current bearer token if it appears in
// s. Exposed so the HTTP layer can give its Argo-error logs the same guarantee
// the manager's own logs have, instead of relying on the token never landing in
// an upstream error body.
func RedactToken(s string) string { return defaultManager.redactSelfToken(s) }

func (m *Manager) redactSelfToken(s string) string {
	m.mu.Lock()
	tok := m.token
	m.mu.Unlock()
	return redactToken(s, tok)
}
