package images

import (
	"container/list"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
)

// Image statuses. Anything but StatusOK means the image's runtimes are
// unknown, never that it has none.
const (
	StatusOK                 = "ok"
	StatusAuthRequired       = "auth_required"
	StatusNotFound           = "not_found"
	StatusRateLimited        = "rate_limited"
	StatusBlocked            = "blocked"
	StatusError              = "error"
	StatusNotChecked         = "not_checked"
	StatusNoDeclaration      = "no_declaration"
	java4ShellDockerHubLimit = "docker_hub_limit"
)

const (
	defaultInventoryLimit      = 50
	maxInventoryLimit          = 200
	maxWorkloadsPerImage       = 20
	maxLookupsPerCall          = 200
	java4ShellDockerHubLookups = 24
	maxAttemptsPerImage        = 4
	maxConfigBytes             = 1 << 20
	inventoryCallBudget        = 40 * time.Second
	java4ShellScanBudget       = 5 * time.Minute
	java4ShellAttemptRate      = 8
	java4ShellAttemptBurst     = 4
	java4ShellCLIFetchSlots    = 8
	configFetchTimeout         = 15 * time.Second
	registryFetchSlots         = 4
	configCacheEntries         = 4096
	tagResultTTL               = 10 * time.Minute
	credentialResultTTL        = time.Hour
	maxOutstandingLookups      = 16
	failureTTL                 = 10 * time.Minute
	transientFailureTTL        = 30 * time.Second
	rateLimitBackoff           = 5 * time.Minute
)

// InventoryOptions narrows an inventory.
type InventoryOptions struct {
	// Runtime keeps images that declare it (e.g. "php"), plus images whose
	// runtimes couldn't be read.
	Runtime string
	// FixedIn classifies each declared version of Runtime; see ClassifyFix.
	// Fixes are per runtime, so it needs Runtime.
	FixedIn []string
	Limit   int
	// RadarCredentials lets lookups fall back to Radar's own registry
	// credentials (Docker config, Google application default credentials).
	// Only for single-user local mode, where they are the user's own: with
	// auth on, they would read images the Pod's own credentials can't.
	RadarCredentials bool
}

// PodSource supplies what the inventory reads about a Pod's surroundings. The
// caller has already limited the Pods to what the requesting user may list.
type PodSource struct {
	Pods []*corev1.Pod
	// Workload names the workload a human would call the Pod's (a
	// Deployment rather than its ReplicaSet).
	Workload func(pod *corev1.Pod) (kind, name string)
	// Platform is the OS/architecture of a node, used to pick the image a
	// multi-arch index resolves to; ok=false falls back to linux/amd64 and
	// the image says so.
	Platform func(nodeName string) (v1.Platform, bool)
	// PullSecrets lists the image pull secrets the Pod itself would use:
	// those recorded in the admitted Pod's spec.
	PullSecrets func(pod *corev1.Pod) []string
}

// WorkloadUse is one workload running an image.
type WorkloadUse struct {
	Kind       string   `json:"kind"`
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Pods       int      `json:"pods"`
}

// ImageRuntimes is one image, as run on one platform, and what it declares.
type ImageRuntimes struct {
	Image    string `json:"image"`
	Digest   string `json:"digest,omitempty"`
	Platform string `json:"platform"`
	// PlatformAssumed means the node's platform wasn't readable and
	// linux/amd64 was used; a multi-arch image may run a different variant.
	PlatformAssumed bool `json:"platformAssumed,omitempty"`
	// ResolvedFrom is "digest" when the kubelet reported the running digest,
	// "spec" when only the Pod spec's digest was known, or "tag" when only
	// its tag was, resolved now. Neither of the last two proves what runs,
	// so they get no fix verdict.
	ResolvedFrom   string            `json:"resolvedFrom"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
	Declared       []DeclaredRuntime `json:"declared,omitempty"`
	BaseImage      string            `json:"baseImage,omitempty"`
	Workloads      []WorkloadUse     `json:"workloads"`
	WorkloadsTotal int               `json:"workloadsTotal"`
}

// InventoryTotals count images before Limit.
type InventoryTotals struct {
	Images       int `json:"images"`
	Matched      int `json:"matched"`
	BelowFix     int `json:"belowFix,omitempty"`
	Unknown      int `json:"unknown"`
	NotChecked   int `json:"notChecked,omitempty"`
	AuthRequired int `json:"authRequired,omitempty"`
	RateLimited  int `json:"rateLimited,omitempty"`
	Returned     int `json:"returned"`
}

// Inventory is the result of one inventory call.
type Inventory struct {
	Images []ImageRuntimes `json:"images"`
	Totals InventoryTotals `json:"totals"`
}

// configLookup fetches an image config for a platform. Production uses the
// registry; tests substitute a fake.
type configLookup func(ctx context.Context, ref name.Reference, platform v1.Platform, keychain authn.Keychain) (cfg *v1.ConfigFile, digest string, err error)

// credential is one way to pull: a single pull secret, no credentials, or
// Radar's own.
type credential struct {
	namespace string
	secret    string
	radar     bool
	advisory  bool
	snapshot  *corev1.Secret
	// identity pins a pull secret to its exact contents (UID and
	// resourceVersion): a rotated or recreated Secret, or a same-named one in
	// another cluster, is a different credential.
	identity string
}

func (c credential) id() string {
	switch {
	case c.radar:
		return "radar"
	case c.secret == "":
		return "anonymous"
	}
	return c.namespace + "/" + c.secret + "@" + c.identity
}

// keychainSource builds the keychain for a credential and the identity of
// what it holds; ok=false when the credential has nothing for this registry
// (it would pull anonymously).
type keychainSource func(c credential, ref name.Reference) (kc authn.Keychain, identity string, ok bool)

// RuntimeInventory reads declared runtime versions of running images. It is
// process-wide: the cache, the registry concurrency limit and rate-limit
// backoff are shared by every caller, so concurrent requests can't multiply
// registry traffic.
type RuntimeInventory struct {
	lookup         configLookup
	keychain       keychainSource
	now            func() time.Time
	slots          chan struct{}
	dockerHubSlots chan struct{}
	fetchTimeout   time.Duration
	// outstanding counts lookups still running, including ones that timed
	// out: a credential helper can hang without honoring cancellation, and
	// timed-out lookups must not pile up behind it.
	outstanding      atomic.Int32
	java4ShellActive atomic.Bool

	mu      sync.Mutex
	cache   map[string]*list.Element
	order   *list.List
	backoff map[string]time.Time
}

type cachedConfig struct {
	key     string
	result  configResult
	expires time.Time // zero: never (digest-addressed content is immutable)
}

type configResult struct {
	status           string
	err              string
	digest           string
	declared         []DeclaredRuntime
	base             string
	javaHomeDeclared bool
}

var (
	defaultInventory     *RuntimeInventory
	defaultInventoryOnce sync.Once
)

// DefaultRuntimeInventory returns the process-wide inventory.
func DefaultRuntimeInventory(inCluster bool) *RuntimeInventory {
	defaultInventoryOnce.Do(func() {
		defaultInventory = newRuntimeInventory(registryConfigLookup(newGuardedTransport(inCluster)), clusterKeychain, registryFetchSlots)
	})
	return defaultInventory
}

// NewCLIJava4ShellInventory gives a standalone check its own registry slots.
func NewCLIJava4ShellInventory() *RuntimeInventory {
	r := newRuntimeInventory(registryConfigLookup(newGuardedTransport(false)), clusterKeychain, java4ShellCLIFetchSlots)
	r.dockerHubSlots = make(chan struct{}, registryFetchSlots)
	return r
}

func newRuntimeInventory(lookup configLookup, keychain keychainSource, slots int) *RuntimeInventory {
	return &RuntimeInventory{
		lookup:       lookup,
		keychain:     keychain,
		now:          time.Now,
		slots:        make(chan struct{}, slots),
		fetchTimeout: configFetchTimeout,
		cache:        make(map[string]*list.Element),
		order:        list.New(),
		backoff:      make(map[string]time.Time),
	}
}

// clusterKeychain resolves a credential against the cluster: one pull secret
// read from Radar's cache, or Radar's own keychains.
func clusterKeychain(c credential, ref name.Reference) (authn.Keychain, string, bool) {
	switch {
	case c.radar:
		keychain := GetAuthenticatedKeychain(ref.String(), "", nil)
		if c.advisory {
			ecr := runtimeECRKeychain()
			if ecr.registries[ref.Context().RegistryStr()] != nil {
				keychain = &ecrFallbackKeychain{ecr: ecr, fallback: keychain}
			}
		}
		return keychain, "", true
	case c.secret == "":
		return authn.NewMultiKeychain(), "", true
	}
	if c.snapshot != nil {
		kc := &pullSecretKeychain{auths: pullSecretAuths(c.snapshot)}
		auth, err := kc.Resolve(ref.Context())
		return kc, c.identity, err == nil && auth != authn.Anonymous
	}
	identity, ok := pullSecretIdentity(c.namespace, c.secret)
	if !ok {
		return nil, "", false
	}
	kc := getKeychainFromSecrets(c.namespace, []string{c.secret})
	if kc == nil {
		return nil, "", false
	}
	auth, err := kc.Resolve(ref.Context())
	if err != nil || auth == authn.Anonymous {
		return nil, "", false
	}
	return kc, identity, true
}

type strictRegistryLookupKey struct{}

// registryConfigLookup reads a manifest and its config blob, never layers.
// Retries are off: a rate-limited registry must not be asked again, and the
// caller's budget is better spent on other images.
func registryConfigLookup(guard *guardedTransport) configLookup {
	return func(ctx context.Context, ref name.Reference, platform v1.Platform, keychain authn.Keychain) (*v1.ConfigFile, string, error) {
		rt := guard.forLookup()
		defer rt.closeIdle()
		if ctx.Value(strictRegistryLookupKey{}) == true {
			rt.inCluster = true
		}
		auth, err := authn.Resolve(ctx, keychain, ref.Context())
		if err != nil {
			return nil, "", err
		}
		rt.requireTLS = auth != authn.Anonymous
		img, err := remote.Image(ref,
			remote.WithContext(withRegistryHost(ctx, ref.Context().RegistryStr())),
			remote.WithTransport(rt),
			remote.WithAuth(auth),
			remote.WithPlatform(platform),
			remote.WithRetryBackoff(remote.Backoff{Steps: 1}),
			remote.WithRetryStatusCodes(),
		)
		if err != nil {
			return nil, "", err
		}
		manifest, err := img.Manifest()
		if err != nil {
			return nil, "", err
		}
		if manifest.Config.Size > maxConfigBytes {
			return nil, "", fmt.Errorf("image config is %d bytes, over the %d byte limit", manifest.Config.Size, maxConfigBytes)
		}
		digest, err := img.Digest()
		if err != nil {
			return nil, "", err
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return nil, "", err
		}
		return cfg, digest.String(), nil
	}
}

// work is one image on one platform, with every workload running it and the
// pull secrets those Pods would use.
type work struct {
	key              string
	display          string
	ref              name.Reference
	resolvedFrom     string
	platform         v1.Platform
	platformAssumed  bool
	secrets          []credential
	radarCredentials bool
	advisory         bool
	workloads        map[string]*WorkloadUse
	pods             map[string]map[string]bool // workload key -> Pod names
}

// Inventory reads the declared runtimes of every image the given Pods run.
func (r *RuntimeInventory) Inventory(ctx context.Context, src PodSource, opts InventoryOptions) Inventory {
	items := collectWork(src)
	return shapeInventory(items, r.lookupConfigs(ctx, items, opts.RadarCredentials, nil), opts)
}

func (r *RuntimeInventory) lookupConfigs(ctx context.Context, items []*work, radarCredentials bool, progress func(done, total int)) []configResult {
	duration := inventoryCallBudget
	var budget *atomic.Int32
	var limiter *rate.Limiter
	if progress == nil {
		budget = new(atomic.Int32)
		budget.Store(maxLookupsPerCall)
	} else {
		duration = java4ShellScanBudget
		limiter = rate.NewLimiter(java4ShellAttemptRate, java4ShellAttemptBurst)
		if slices.ContainsFunc(items, func(w *work) bool { return w.advisory && (radarCredentials || w.radarCredentials) }) {
			runtimeECRKeychain()
		}
		progress(0, len(items))
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	results := make([]configResult, len(items))
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	complete := func(i int, result configResult) {
		results[i] = result
		if progress != nil {
			progressMu.Lock()
			completed++
			progress(completed, len(items))
			progressMu.Unlock()
		}
	}
	workers := make(chan struct{}, cap(r.slots))
	acquire := func(slots chan struct{}, i int) bool {
		select {
		case slots <- struct{}{}:
			return true
		case <-ctx.Done():
			complete(i, configResult{status: StatusNotChecked, err: "time budget for this call ran out"})
			return false
		}
	}
	advisoryLookups, dockerHubLookups := 0, 0
	for i, w := range items {
		onDockerHub := w.advisory && registryBudgetHost(w.ref) == name.DefaultRegistry
		dockerHubLimit := onDockerHub && dockerHubLookups >= java4ShellDockerHubLookups
		if dockerHubLimit || (progress == nil && w.advisory && advisoryLookups >= maxLookupsPerCall) {
			// Public results can still answer after the limit. Avoid resolving
			// private credentials for groups that cannot make a registry read.
			cached, hit, _ := r.fromCache(w, []credential{{}})
			if hit && cached.status == StatusOK {
				complete(i, cached)
			} else if dockerHubLimit {
				complete(i, configResult{status: java4ShellDockerHubLimit})
			} else {
				complete(i, configResult{status: StatusNotChecked, err: "per-call image lookup limit reached; narrow by namespace"})
			}
			continue
		}
		creds := r.credentials(w, radarCredentials || w.radarCredentials)
		res, hit, untried := r.fromCache(w, creds)
		if hit {
			complete(i, res)
			continue
		}
		if w.advisory {
			advisoryLookups++
			if onDockerHub {
				dockerHubLookups++
			}
		}
		limitDockerHub := onDockerHub && r.dockerHubSlots != nil
		if !limitDockerHub && !acquire(workers, i) {
			continue
		}
		wg.Add(1)
		go func(i int, w *work, untried []credential) {
			defer wg.Done()
			// At most 24 Docker Hub groups can queue here. Let them wait for
			// their own cap before occupying a worker other registries need.
			if limitDockerHub {
				if !acquire(r.dockerHubSlots, i) {
					return
				}
				defer func() { <-r.dockerHubSlots }()
				if !acquire(workers, i) {
					return
				}
			}
			defer func() { <-workers }()
			complete(i, r.fetch(ctx, w, untried, budget, limiter))
		}(i, w, untried)
	}
	wg.Wait()

	return results
}

// credentials lists what a lookup may try, in the kubelet's order: each pull
// secret that has an entry for the image's registry, then no credentials,
// then (local mode only) Radar's own.
func (r *RuntimeInventory) credentials(w *work, radar bool) []credential {
	secretSlots := maxAttemptsPerImage - 1
	if radar {
		secretSlots--
	}
	var out []credential
	useECR := w.advisory && radar && runtimeECRKeychain().registries[w.ref.Context().RegistryStr()] != nil
	if w.advisory && !useECR {
		out = append(out, credential{})
	}
	secretCount := 0
	for _, c := range w.secrets {
		if secretCount == secretSlots {
			break
		}
		if _, identity, ok := r.keychain(c, w.ref); ok {
			c.identity = identity
			out = append(out, c)
			secretCount++
		}
	}
	if !w.advisory {
		out = append(out, credential{})
	}
	if radar {
		out = append(out, credential{radar: true, advisory: w.advisory})
	}
	return out
}

// resultKey: an anonymous success describes a public image and answers every
// caller; anything read with credentials answers only callers whose Pods
// carry the same credentials.
func resultKey(w *work, c credential) string {
	if c.id() == "anonymous" {
		return w.key
	}
	return w.key + "|" + c.id()
}

// fromCache returns a cached success the caller's credentials may see, or,
// when every credential failed recently, the latest failure. Otherwise it
// returns the credentials still worth trying.
func (r *RuntimeInventory) fromCache(w *work, creds []credential) (res configResult, hit bool, untried []credential) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var failure configResult
	for _, c := range creds {
		if w.advisory && c.id() != "anonymous" {
			untried = append(untried, c)
			continue
		}
		if cached, found := r.getLocked(resultKey(w, c)); found {
			if cached.status == StatusOK {
				return cached, true, nil
			}
			failure = cached
			continue
		}
		untried = append(untried, c)
	}
	if len(untried) == 0 && failure.status != "" {
		return failure, true, nil
	}
	return configResult{}, false, untried
}

func (r *RuntimeInventory) getLocked(key string) (configResult, bool) {
	el, ok := r.cache[key]
	if !ok {
		return configResult{}, false
	}
	entry := el.Value.(*cachedConfig)
	if !entry.expires.IsZero() && r.now().After(entry.expires) {
		r.order.Remove(el)
		delete(r.cache, key)
		return configResult{}, false
	}
	r.order.MoveToFront(el)
	return entry.result, true
}

func (r *RuntimeInventory) store(key string, res configResult, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := &cachedConfig{key: key, result: res}
	if ttl > 0 {
		entry.expires = r.now().Add(ttl)
	}
	if el, ok := r.cache[key]; ok {
		el.Value = entry
		r.order.MoveToFront(el)
		return
	}
	r.cache[key] = r.order.PushFront(entry)
	for r.order.Len() > configCacheEntries {
		oldest := r.order.Back()
		r.order.Remove(oldest)
		delete(r.cache, oldest.Value.(*cachedConfig).key)
	}
}

func (r *RuntimeInventory) rateLimited(registry string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.backoff[registry]
	return ok && r.now().Before(until)
}

func (r *RuntimeInventory) backOff(registry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backoff[registry] = r.now().Add(rateLimitBackoff)
}

func registryBudgetHost(ref name.Reference) string {
	host := ref.Context().RegistryStr()
	if host == "registry-1.docker.io" || host == "registry.hub.docker.com" {
		return name.DefaultRegistry
	}
	return host
}

var (
	errLookupTimeout       = errors.New("registry did not answer in time")
	errRateLimitedRecently = errors.New("registry rate limited recently")
	errLookupsStuck        = errors.New("earlier registry lookups are still running")
)

// fetch tries the remaining credentials until one reads the config. Every
// attempt spends the call's lookup budget.
func (r *RuntimeInventory) fetch(ctx context.Context, w *work, creds []credential, budget *atomic.Int32, limiter *rate.Limiter) configResult {
	registry := registryBudgetHost(w.ref)
	res := configResult{status: StatusNotChecked, err: "per-call lookup limit reached; narrow by namespace"}
	for _, c := range creds {
		if budget != nil && budget.Add(-1) < 0 {
			return configResult{status: StatusNotChecked, err: "per-call lookup limit reached before all available credentials were tried"}
		}
		keychain, identity, ok := r.keychain(c, w.ref)
		if !ok {
			continue
		}
		c.identity = identity
		if limiter != nil && !r.rateLimited(registry) {
			if err := limiter.Wait(ctx); err != nil {
				return configResult{status: StatusNotChecked, err: "time budget for this call ran out"}
			}
		}
		cfg, digest, err := r.attempt(ctx, registry, w, keychain)
		if errors.Is(err, errRateLimitedRecently) {
			return configResult{status: StatusRateLimited, err: "registry rate limit hit recently; retry later"}
		}
		if err == nil {
			declared, base := DeclaredRuntimes(cfg)
			res = configResult{
				status: StatusOK, digest: digest, declared: declared, base: base,
				javaHomeDeclared: w.advisory && declaresJavaHome(cfg),
			}
			ttl := time.Duration(0)
			switch {
			case w.resolvedFrom == "tag":
				ttl = tagResultTTL
			case c.id() != "anonymous":
				// What a credential may read can change without its
				// identity changing (registry-side permissions).
				ttl = credentialResultTTL
			}
			// Image inspection revalidates private registry access on each
			// request. Advisory checks follow the same rule.
			if !w.advisory || c.id() == "anonymous" {
				r.store(resultKey(w, c), res, ttl)
			}
			return res
		}
		if ctx.Err() != nil {
			return configResult{status: StatusNotChecked, err: "time budget for this call ran out"}
		}
		res = classifyFetchError(err)
		if res.status == StatusRateLimited || res.status == StatusNotChecked {
			return res
		}
		ttl := failureTTL
		if res.status == StatusError {
			ttl = transientFailureTTL
		}
		if !w.advisory || c.id() == "anonymous" {
			r.store(resultKey(w, c), res, ttl)
		}
		// Registries that hide repositories from unauthorized callers answer
		// 404, so a not-found is worth trying with the next credential.
		if res.status != StatusAuthRequired && res.status != StatusNotFound {
			return res
		}
	}
	return res
}

// attempt runs one lookup under a process-wide slot. The backoff is checked
// after the slot is taken, so lookups queued behind a 429 don't go out. A
// lookup that outlives its timeout gives its slot back: credential helpers
// run as subprocesses that ignore cancellation.
func (r *RuntimeInventory) attempt(ctx context.Context, registry string, w *work, keychain authn.Keychain) (*v1.ConfigFile, string, error) {
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { <-r.slots }()
	if r.rateLimited(registry) {
		return nil, "", errRateLimitedRecently
	}

	if r.outstanding.Load() >= maxOutstandingLookups {
		return nil, "", errLookupsStuck
	}
	fctx, cancel := context.WithTimeout(ctx, r.fetchTimeout)
	defer cancel()
	type outcome struct {
		cfg    *v1.ConfigFile
		digest string
		err    error
	}
	done := make(chan outcome, 1)
	r.outstanding.Add(1)
	go func() {
		defer r.outstanding.Add(-1)
		cfg, digest, err := r.lookup(fctx, w.ref, w.platform, keychain)
		if err != nil && classifyFetchError(err).status == StatusRateLimited {
			r.backOff(registry)
		}
		done <- outcome{cfg, digest, err}
	}()
	select {
	case o := <-done:
		return o.cfg, o.digest, o.err
	case <-fctx.Done():
		return nil, "", errLookupTimeout
	}
}

func classifyFetchError(err error) configResult {
	if errors.Is(err, errECRCredentials) {
		return configResult{status: StatusAuthRequired, err: errECRCredentials.Error()}
	}
	if isBlocked(err) {
		return configResult{status: StatusBlocked, err: "registry address is not allowed from Radar"}
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return configResult{status: StatusAuthRequired, err: "registry requires credentials or rejected those available to Radar; node credentials were not used"}
		case http.StatusNotFound:
			return configResult{status: StatusNotFound, err: "image not found, or hidden from these credentials"}
		case http.StatusTooManyRequests:
			return configResult{status: StatusRateLimited, err: "registry rate limit"}
		}
		// The body can be anything the registry (or what it redirected to)
		// sent; the status code is enough.
		return configResult{status: StatusError, err: fmt.Sprintf("registry answered HTTP %d", terr.StatusCode)}
	}
	if errors.Is(err, errLookupsStuck) {
		return configResult{status: StatusNotChecked, err: "earlier registry lookups are still running; try again later"}
	}
	if errors.Is(err, errLookupTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return configResult{status: StatusError, err: "registry did not answer in time"}
	}
	// Other errors can carry text a registry or token endpoint sent (go-
	// containerregistry quotes token responses), so only their kind is
	// reported.
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dnsErr):
		return configResult{status: StatusError, err: "registry host did not resolve"}
	case errors.As(err, &certErr):
		return configResult{status: StatusError, err: "registry TLS certificate was not trusted"}
	case errors.As(err, &opErr):
		return configResult{status: StatusError, err: "could not connect to the registry"}
	}
	return configResult{status: StatusError, err: "registry lookup failed"}
}

// collectWork groups the Pods' containers by the image they run (the
// running digest when the kubelet reported one) and the node platform.
func collectWork(src PodSource) []*work {
	byKey := map[string]*work{}
	var order []string
	for _, pod := range src.Pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		platform, assumed := v1.Platform{OS: "linux", Architecture: "amd64"}, true
		if src.Platform != nil && pod.Spec.NodeName != "" {
			if p, ok := src.Platform(pod.Spec.NodeName); ok {
				platform, assumed = p, false
			}
		}
		running := map[string]string{}
		for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
			for _, s := range statuses {
				running[s.Name] = s.ImageID
			}
		}
		kind, wname := "Pod", pod.Name
		if src.Workload != nil {
			kind, wname = src.Workload(pod)
		}
		var secrets []string
		if src.PullSecrets != nil {
			secrets = src.PullSecrets(pod)
		}

		for _, c := range runningContainers(pod) {
			ref, resolvedFrom, ok := imageRef(c.Image, running[c.Name])
			if !ok {
				continue
			}
			// Name() is fully qualified, so php@sha256:x and
			// docker.io/library/php@sha256:x are one image.
			key := imageWorkKey(ref, platform)
			w := byKey[key]
			if w == nil {
				w = &work{key: key, display: c.Image, ref: ref, resolvedFrom: resolvedFrom, platform: platform, platformAssumed: assumed, workloads: map[string]*WorkloadUse{}, pods: map[string]map[string]bool{}}
				byKey[key] = w
				order = append(order, key)
			}
			// One kubelet-reported digest proves the image runs for the
			// whole group.
			if resolvedFrom == "digest" {
				w.resolvedFrom = "digest"
			}
			// Pods on readable and unreadable nodes can share a group; one
			// unknown platform makes the group's platform an assumption.
			w.platformAssumed = w.platformAssumed || assumed
			for _, s := range secrets {
				cred := credential{namespace: pod.Namespace, secret: s}
				if !slices.Contains(w.secrets, cred) {
					w.secrets = append(w.secrets, cred)
				}
			}
			wk := pod.Namespace + "/" + kind + "/" + wname
			use := w.workloads[wk]
			if use == nil {
				use = &WorkloadUse{Kind: kind, Namespace: pod.Namespace, Name: wname}
				w.workloads[wk] = use
			}
			if !slices.Contains(use.Containers, c.Name) {
				use.Containers = append(use.Containers, c.Name)
			}
			if w.pods[wk] == nil {
				w.pods[wk] = map[string]bool{}
			}
			w.pods[wk][pod.Name] = true
		}
	}
	out := make([]*work, 0, len(order))
	for _, k := range order {
		w := byKey[k]
		for wk, use := range w.workloads {
			sort.Strings(use.Containers)
			use.Pods = len(w.pods[wk])
		}
		out = append(out, w)
	}
	return out
}

func imageWorkKey(ref name.Reference, platform v1.Platform) string {
	return ref.Name() + "|" + platform.OS + "/" + platform.Architecture
}

// runningContainers are the containers that can run alongside the workload:
// regular containers, restartable (sidecar) init containers and running
// ephemeral debug containers. One-shot init containers have finished.
func runningContainers(pod *corev1.Pod) []corev1.Container {
	out := slices.Clone(pod.Spec.Containers)
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, c)
		}
	}
	// Ephemeral containers can't restart, so one that exited stays exited:
	// only a running one counts.
	running := map[string]bool{}
	for _, s := range pod.Status.EphemeralContainerStatuses {
		running[s.Name] = s.State.Running != nil
	}
	for _, c := range pod.Spec.EphemeralContainers {
		if running[c.Name] {
			out = append(out, corev1.Container{Name: c.Name, Image: c.Image, Command: c.Command, Args: c.Args, Env: c.Env})
		}
	}
	return out
}

// imageRef picks what to look up: the repo digest the kubelet reported when
// there is one ("digest"), otherwise the spec image ("spec" for a digest
// reference, "tag" for a tag). A bare "sha256:…" imageID is a local image ID,
// not a registry digest, so it can't be looked up. Only a kubelet-reported
// digest says what runs: a spec can change while the old container keeps
// running.
func imageRef(specImage, imageID string) (name.Reference, string, bool) {
	id := strings.TrimPrefix(imageID, "docker-pullable://")
	if repo, digest, ok := strings.Cut(id, "@"); ok && strings.HasPrefix(digest, "sha256:") {
		if ref, err := name.NewDigest(repo + "@" + digest); err == nil {
			return ref, "digest", true
		}
	}
	if ref, err := name.ParseReference(specImage); err == nil {
		if d, ok := ref.(name.Digest); ok {
			return d, "spec", true
		}
		return ref, "tag", true
	}
	return nil, "", false
}

func shapeInventory(items []*work, results []configResult, opts InventoryOptions) Inventory {
	runtime := strings.ToLower(strings.TrimSpace(opts.Runtime))
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultInventoryLimit
	}
	limit = min(limit, maxInventoryLimit)

	var inv Inventory
	type ranked struct {
		img  ImageRuntimes
		rank int
	}
	var rows []ranked
	for i, w := range items {
		res := results[i]
		img := ImageRuntimes{
			Image:           w.display,
			Digest:          res.digest,
			Platform:        w.platform.OS + "/" + w.platform.Architecture,
			PlatformAssumed: w.platformAssumed,
			ResolvedFrom:    w.resolvedFrom,
			Status:          res.status,
			Error:           res.err,
			BaseImage:       res.base,
		}
		if d, ok := w.ref.(name.Digest); ok && img.Digest == "" {
			img.Digest = d.DigestStr()
		}
		inv.Totals.Images++
		// rank: 0 below a fix, 1 read and classified (or no fixes asked),
		// 2 unknown or unclassifiable.
		rank := 2
		if res.status == StatusOK {
			matched := runtime == "" && len(res.declared) > 0
			rank = 1
			for _, d := range res.declared {
				if runtime != "" && d.Runtime != runtime {
					continue
				}
				matched = true
				// A tag resolved now may not be what runs: report what it
				// declares, but don't call the workload fixed or not.
				if runtime != "" && len(opts.FixedIn) > 0 && d.Runtime != "image" && w.resolvedFrom == "digest" && !w.platformAssumed {
					d.Fix, d.FixedIn = ClassifyFix(d.Runtime, d.Version, opts.FixedIn)
					if d.Fix == FixBelow {
						rank = 0
					}
				}
				img.Declared = append(img.Declared, d)
			}
			if !matched {
				img.Status = StatusNoDeclaration
				img.Error = "image config does not declare this runtime; its presence and version are unknown"
				inv.Totals.Unknown++
				rank = 2
			} else {
				inv.Totals.Matched++
			}
			if (w.resolvedFrom != "digest" || w.platformAssumed) && len(opts.FixedIn) > 0 {
				rank = 2
			}
			if rank == 0 {
				inv.Totals.BelowFix++
			}
		} else {
			inv.Totals.Unknown++
			switch res.status {
			case StatusNotChecked:
				inv.Totals.NotChecked++
			case StatusAuthRequired:
				inv.Totals.AuthRequired++
			case StatusRateLimited:
				inv.Totals.RateLimited++
			}
		}
		uses := make([]WorkloadUse, 0, len(w.workloads))
		for _, u := range w.workloads {
			uses = append(uses, *u)
		}
		sort.Slice(uses, func(a, b int) bool {
			if uses[a].Namespace != uses[b].Namespace {
				return uses[a].Namespace < uses[b].Namespace
			}
			return uses[a].Kind+"/"+uses[a].Name < uses[b].Kind+"/"+uses[b].Name
		})
		img.WorkloadsTotal = len(uses)
		img.Workloads = uses[:min(len(uses), maxWorkloadsPerImage)]
		rows = append(rows, ranked{img, rank})
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].rank != rows[b].rank {
			return rows[a].rank < rows[b].rank
		}
		if rows[a].img.WorkloadsTotal != rows[b].img.WorkloadsTotal {
			return rows[a].img.WorkloadsTotal > rows[b].img.WorkloadsTotal
		}
		return rows[a].img.Image < rows[b].img.Image
	})
	inv.Images = []ImageRuntimes{}
	for _, row := range rows[:min(len(rows), limit)] {
		inv.Images = append(inv.Images, row.img)
	}
	inv.Totals.Returned = len(inv.Images)
	return inv
}
