package auth

import (
	"context"
	"log"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// UserPermissions holds cached permission data for an authenticated user.
//
// AllowedNamespaces describes namespace-list scope for *namespaced* resources:
//
//   - nil:  user can list namespaced resources cluster-wide (no per-namespace
//     filter required at read time)
//   - []:   user has no namespace access — every read returns empty
//   - non-empty: user can read only the listed namespaces
//
// AllowedNamespaces does NOT imply anything about cluster-scoped resources.
// A user can have cluster-wide pod visibility (AllowedNamespaces == nil) and
// still lack permission to list Nodes, PVs, ClusterRoles, or any specific
// CRD. Cluster-scoped reads MUST be gated separately via per-kind SAR; use
// the canI map below.
type UserPermissions struct {
	AllowedNamespaces []string // nil = all namespaces; see doc above
	ExpiresAt         time.Time

	// ContextName stamps the K8s context this entry was computed under.
	// PermissionCache.Get refuses entries whose stamp doesn't match the
	// cache's current context — this closes the cross-cluster authorization
	// window between client-swap (PerformContextSwitch step 2) and
	// post-switch invalidation. Without the stamp, an in-flight request
	// mid-swap reads new-cluster data through old-cluster AllowedNamespaces.
	ContextName string

	// canI caches per-(verb, group, resource, namespace) SubjectAccessReview
	// results for this user. Used by the per-kind authorization gate that
	// covers cluster-scoped reads (and any other check beyond the namespace-
	// list discovery). Empty namespace = cluster-scoped check.
	canIMu sync.RWMutex
	canI   map[string]bool
}

// canIKey builds the cache key for SAR results.
func canIKey(verb, group, resource, namespace string) string {
	return verb + "\x00" + group + "\x00" + resource + "\x00" + namespace
}

// CanI returns the cached SAR result for this (verb, group, resource,
// namespace) tuple. The second return is true when the entry exists; false
// means the caller should run a fresh SAR and call SetCanI.
func (p *UserPermissions) CanI(verb, group, resource, namespace string) (bool, bool) {
	p.canIMu.RLock()
	defer p.canIMu.RUnlock()
	if p.canI == nil {
		return false, false
	}
	v, ok := p.canI[canIKey(verb, group, resource, namespace)]
	return v, ok
}

// SetCanI stores a SAR result. Bound by the same TTL as the parent
// UserPermissions entry — when PermissionCache evicts the user, the SAR
// cache evaporates with it.
func (p *UserPermissions) SetCanI(verb, group, resource, namespace string, allowed bool) {
	p.canIMu.Lock()
	defer p.canIMu.Unlock()
	if p.canI == nil {
		p.canI = make(map[string]bool)
	}
	p.canI[canIKey(verb, group, resource, namespace)] = allowed
}

// PermissionCache caches per-user permission lookups (thread-safe)
type PermissionCache struct {
	mu          sync.RWMutex
	cache       map[string]*UserPermissions // keyed by cacheKey(username, groups)
	ttl         time.Duration
	contextName func() string // current K8s context; entries with a different stamp are invisible

	// flights holds the discoveries Resolve is running, keyed by identity and
	// connection. Guarded by mu, so joining a run and finding its cached
	// answer can't interleave.
	flights map[string]*permissionFlight
}

// permissionFlight is one discovery shared by the callers that missed the
// cache for the same identity and connection while it ran.
type permissionFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	perms   *UserPermissions // nil when the discovery couldn't decide; written before done closes
}

// cacheKey builds the composite cache key for an identity. The verdicts stored
// in a UserPermissions entry are computed by SubjectAccessReviews that run with
// the username AND its groups, so an entry is only valid for that exact
// (username, groups) identity — keying by username alone lets a different group
// set inherit another identity's RBAC ceiling for the cache TTL. Groups are
// canonicalized (sorted + deduped) so order and duplicates don't fork the key;
// empty/nil groups is a valid, distinct identity (not a wildcard).
//
// Each name is length-prefixed rather than joined with a separator: names come
// from identity-provider claims without any restriction on their bytes, so no
// separator is guaranteed absent, and with one "alice" in group
// "team<sep>prod" would key the same as "alice<sep>team" in group "prod".
func cacheKey(username string, groups []string) string {
	canonical := canonicalGroups(groups)
	n := len(username) + 4
	for _, g := range canonical {
		n += len(g) + 4
	}
	b := make([]byte, 0, n)
	b = appendKeyPart(b, username)
	for _, g := range canonical {
		b = appendKeyPart(b, g)
	}
	return string(b)
}

func appendKeyPart(b []byte, s string) []byte {
	b = strconv.AppendInt(b, int64(len(s)), 10)
	b = append(b, ':')
	return append(b, s...)
}

// IdentityCacheKey is cacheKey for callers outside this package that cache
// per-user SubjectAccessReview results. The key is self-delimiting, so a
// caller may append further fields to it.
func IdentityCacheKey(username string, groups []string) string {
	return cacheKey(username, groups)
}

// canonicalGroups returns a group set sorted and deduped. Empty-string
// elements are dropped (an empty group is not a real principal), so nil, [],
// [""], and ["", ""] are all one identity. The proxy path already trims
// empties, but the OIDC path (internal/auth/oidc.go) does not; dropping them
// here makes the no-groups collision explicit rather than accidental.
func canonicalGroups(groups []string) []string {
	if len(groups) == 0 {
		return nil
	}
	uniq := make([]string, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if g == "" {
			continue // empty group is not a real principal
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		uniq = append(uniq, g)
	}
	sort.Strings(uniq)
	return uniq
}

// NewPermissionCache creates a new permission cache with a 2-minute TTL.
// Use WithContextName to wire cluster-identity stamping; without it, every
// entry stamps as "" and the stamp check is a no-op (test-friendly default).
func NewPermissionCache() *PermissionCache {
	return &PermissionCache{
		cache:       make(map[string]*UserPermissions),
		ttl:         2 * time.Minute,
		contextName: func() string { return "" },
	}
}

// WithContextName wires the cache to a context-name provider so entries
// can be stamped at write time and verified at read time. Production code
// passes k8s.GetContextName so a request straddling a context switch
// can't be authorized by the previous cluster's RBAC.
func (pc *PermissionCache) WithContextName(provider func() string) *PermissionCache {
	if provider == nil {
		provider = func() string { return "" }
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.contextName = provider
	return pc
}

// Get returns cached permissions for a user, or nil if not cached / expired
// / stamped with a different context than the current one.
func (pc *PermissionCache) Get(username string, groups []string) *UserPermissions {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.liveLocked(cacheKey(username, groups))
}

// liveLocked is Get for a computed key, with pc.mu held.
func (pc *PermissionCache) liveLocked(key string) *UserPermissions {
	perms, ok := pc.cache[key]
	if !ok || time.Now().After(perms.ExpiresAt) {
		return nil
	}
	if pc.contextName != nil && perms.ContextName != pc.contextName() {
		// Entry was computed against a different cluster — refuse it
		// rather than authorizing the current cluster's reads with
		// the previous cluster's RBAC.
		return nil
	}
	return perms
}

// Set stores permissions for a user. Stamps the entry with the cache's
// current context so cross-cluster requests can't see it. Opportunistically
// evicts expired entries so a long-running deploy with churning users
// doesn't accumulate.
func (pc *PermissionCache) Set(username string, groups []string, perms *UserPermissions) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.setLocked(cacheKey(username, groups), perms)
}

// setLocked is Set for a computed key, with pc.mu held.
func (pc *PermissionCache) setLocked(key string, perms *UserPermissions) {
	now := time.Now()
	perms.ExpiresAt = now.Add(pc.ttl)
	if pc.contextName != nil {
		perms.ContextName = pc.contextName()
	}
	pc.cache[key] = perms

	for k, p := range pc.cache {
		if k == key {
			continue
		}
		if now.After(p.ExpiresAt) {
			delete(pc.cache, k)
		}
	}
}

// Invalidate removes all cached permissions (e.g., on context switch). A
// caller after it never shares a discovery that began before it.
func (pc *PermissionCache) Invalidate() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache = make(map[string]*UserPermissions)
	pc.flights = nil
}

// Resolve returns the identity's cached permissions, or runs discover to
// compute and cache them. Callers that miss at once for the same identity and
// connection share one discover run, so a cache expiry costs one discovery
// rather than one per request. The run keeps going while any of those callers
// still waits and is canceled when the last one stops. connection names the
// cluster connection discover reads; callers on another connection never
// share its answer.
//
// discover returns nil when it couldn't decide: every caller sharing that run
// then gets (nil, false) and must fail closed, and nothing is cached. A
// caller whose own context ends first also gets (nil, false).
func (pc *PermissionCache) Resolve(ctx context.Context, username string, groups []string, connection string, discover func(context.Context) *UserPermissions) (*UserPermissions, bool) {
	key := cacheKey(username, groups)
	flightKey := string(appendKeyPart([]byte(key), connection))

	pc.mu.Lock()
	if perms := pc.liveLocked(key); perms != nil {
		pc.mu.Unlock()
		return perms, true
	}
	f := pc.flights[flightKey]
	if f == nil {
		// The run outlives the caller that started it, so it doesn't take that
		// caller's cancellation; it ends when no caller waits for it.
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &permissionFlight{done: make(chan struct{}), cancel: cancel}
		if pc.flights == nil {
			pc.flights = make(map[string]*permissionFlight)
		}
		pc.flights[flightKey] = f
		go pc.runFlight(runCtx, key, flightKey, f, discover)
	}
	f.waiters++
	pc.mu.Unlock()

	select {
	case <-f.done:
		return f.perms, f.perms != nil
	case <-ctx.Done():
		pc.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			pc.detachLocked(flightKey, f)
		}
		pc.mu.Unlock()
		return nil, false
	}
}

func (pc *PermissionCache) runFlight(ctx context.Context, key, flightKey string, f *permissionFlight, discover func(context.Context) *UserPermissions) {
	defer f.cancel()
	perms := runDiscovery(ctx, discover)
	pc.mu.Lock()
	if perms != nil {
		pc.setLocked(key, perms)
	}
	pc.detachLocked(flightKey, f)
	f.perms = perms
	pc.mu.Unlock()
	close(f.done)
}

// runDiscovery runs discover on the flight's own goroutine, where a panic
// would end the process instead of failing the callers waiting on it.
func runDiscovery(ctx context.Context, discover func(context.Context) *UserPermissions) (perms *UserPermissions) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[auth] permission discovery panicked: %v\n%s", r, debug.Stack())
			perms = nil
		}
	}()
	return discover(ctx)
}

// detachLocked stops new callers from joining f, with pc.mu held.
func (pc *PermissionCache) detachLocked(flightKey string, f *permissionFlight) {
	if pc.flights[flightKey] == f {
		delete(pc.flights, flightKey)
	}
}

// DiscoverNamespaces checks which namespaces a user can access via SubjectAccessReview.
// Returns nil for "all namespaces" (cluster admin), empty slice for "no access".
func DiscoverNamespaces(ctx context.Context, client kubernetes.Interface, username string, groups []string, allNamespaces []string) ([]string, error) {
	// Step 1: Check cluster-wide "list pods" — if allowed, user is effectively cluster-admin
	allowed, err := SubjectCanI(ctx, client, username, groups, "", "", "pods", "list")
	if err != nil {
		log.Printf("[auth] DiscoverNamespaces: cluster-scope SAR failed for %s: %v", username, err)
		return nil, err
	}
	log.Printf("[auth] DiscoverNamespaces: user=%s cluster-scope list pods = %v", username, allowed)
	if allowed {
		return nil, nil // nil = all namespaces
	}

	// Step 2: Parallel check each namespace for "list pods" OR "list deployments".
	//
	// Per-namespace SAR errors are propagated. The caller must NOT cache an
	// empty/partial result on error — a transient apiserver hiccup would
	// otherwise lock the user out for the cache TTL window.
	type nsResult struct {
		namespace string
		allowed   bool
		err       error
	}

	results := make(chan nsResult, len(allNamespaces))
	sem := make(chan struct{}, 10) // limit to 10 concurrent checks

	var wg sync.WaitGroup
	for _, ns := range allNamespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ok, err := SubjectCanI(ctx, client, username, groups, namespace, "", "pods", "list")
			if err != nil {
				results <- nsResult{namespace: namespace, err: err}
				return
			}
			if ok {
				results <- nsResult{namespace: namespace, allowed: true}
				return
			}
			ok, err = SubjectCanI(ctx, client, username, groups, namespace, "apps", "deployments", "list")
			if err != nil {
				results <- nsResult{namespace: namespace, err: err}
				return
			}
			results <- nsResult{namespace: namespace, allowed: ok}
		}(ns)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	allowedNS := make([]string, 0) // empty (not nil) = no access; nil = all namespaces
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.allowed {
			allowedNS = append(allowedNS, r.namespace)
		}
	}
	if firstErr != nil {
		log.Printf("[auth] DiscoverNamespaces: per-namespace SAR errored for %s: %v (returning err to skip cache)", username, firstErr)
		return nil, firstErr
	}

	return allowedNS, nil
}

// ReviewSubjectAccess asks the apiserver authorizer whether a subject can
// perform the action described by attrs.
func ReviewSubjectAccess(ctx context.Context, client kubernetes.Interface, username string, groups []string, attrs authv1.ResourceAttributes) (authv1.SubjectAccessReviewStatus, error) {
	review := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:               username,
			Groups:             groups,
			ResourceAttributes: &attrs,
		},
	}

	result, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return authv1.SubjectAccessReviewStatus{}, err
	}
	return result.Status, nil
}

// SubjectCanI performs a SubjectAccessReview (not SelfSubject) to check if a specific
// user can perform an action. This uses the ServiceAccount's permissions to check
// on behalf of the user.
func SubjectCanI(ctx context.Context, client kubernetes.Interface, username string, groups []string, namespace, group, resource, verb string) (bool, error) {
	return SubjectCanISubresource(ctx, client, username, groups, namespace, group, resource, "", verb)
}

// SubjectCanISubresource performs the same impersonated authorization check
// for an exact resource/subresource tuple such as nodes/proxy.
func SubjectCanISubresource(ctx context.Context, client kubernetes.Interface, username string, groups []string, namespace, group, resource, subresource, verb string) (bool, error) {
	status, err := ReviewSubjectAccess(ctx, client, username, groups, authv1.ResourceAttributes{
		Namespace:   namespace,
		Group:       group,
		Resource:    resource,
		Subresource: subresource,
		Verb:        verb,
	})
	if err != nil {
		log.Printf("[auth] SubjectAccessReview failed for user=%s %s %s/%s: %v", username, verb, group, resource, err)
		return false, err
	}

	return status.Allowed, nil
}

// FilterNamespacesForUser intersects requested namespaces with user's allowed namespaces.
//
//   - user == nil: caller is unauthenticated/system; returns requested unchanged.
//   - user != nil, perms == nil: caller forgot to resolve permissions. Fail
//     closed (return empty) rather than passing through unfiltered — a missing
//     perms lookup must never widen access.
//   - perms.AllowedNamespaces == nil: user has cluster-wide access; returns
//     requested unchanged.
//   - requested empty: returns a copy of the user's allowed namespaces, so
//     callers can't mutate the cached slice.
func FilterNamespacesForUser(requested []string, user *User, perms *UserPermissions) []string {
	if user == nil {
		return requested
	}
	if perms == nil {
		return []string{}
	}
	if perms.AllowedNamespaces == nil {
		return requested
	}

	allowedSet := make(map[string]bool, len(perms.AllowedNamespaces))
	for _, ns := range perms.AllowedNamespaces {
		allowedSet[ns] = true
	}

	if len(requested) == 0 {
		return slices.Clone(perms.AllowedNamespaces)
	}

	result := make([]string, 0)
	for _, ns := range requested {
		if allowedSet[ns] {
			result = append(result, ns)
		}
	}
	return result
}
