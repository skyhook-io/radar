package auth

import (
	"context"
	"log"
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

	// connection names the client the entry was discovered on. A reconnect,
	// even under the same context name, can reach a cluster where this user
	// has different access, so the cache hides entries from other connections
	// from the moment the new client is installed, before any invalidation.
	connection string

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
	mu                sync.RWMutex
	cache             map[string]*UserPermissions // keyed by cacheKey(username, groups)
	ttl               time.Duration
	currentConnection func() string
	discoveryTimeout  time.Duration

	// flights holds the discoveries Resolve is running, keyed by identity and
	// connection. Guarded by mu, so joining a run and finding its cached
	// answer can't interleave.
	flights map[string]*flight[*UserPermissions]
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

// NewPermissionCache creates a new permission cache with a 2-minute TTL and a
// 2-minute discovery budget. Without WithConnectionKey every entry belongs to
// the empty connection.
func NewPermissionCache() *PermissionCache {
	return &PermissionCache{
		cache:            make(map[string]*UserPermissions),
		ttl:              2 * time.Minute,
		discoveryTimeout: 2 * time.Minute,
	}
}

// WithConnectionKey binds permission entries to the client that discovered them.
// A reconnect under the same context name can have different permissions.
func (pc *PermissionCache) WithConnectionKey(provider func() string) *PermissionCache {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.currentConnection = provider
	return pc
}

func (pc *PermissionCache) connectionLocked() string {
	if pc.currentConnection == nil {
		return ""
	}
	return pc.currentConnection()
}

// Get returns the user's cached permissions, or nil when none are cached, they
// expired, or they were discovered on another connection.
func (pc *PermissionCache) Get(username string, groups []string) *UserPermissions {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.liveLocked(cacheKey(username, groups), pc.connectionLocked())
}

func (pc *PermissionCache) liveLocked(key, connection string) *UserPermissions {
	perms := pc.cache[key]
	if perms == nil || perms.connection != connection || time.Now().After(perms.ExpiresAt) {
		return nil
	}
	return perms
}

// Set stores permissions for a user on the current connection, and evicts
// expired entries so a long-running install with churning users doesn't
// accumulate them.
func (pc *PermissionCache) Set(username string, groups []string, perms *UserPermissions) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.setLocked(cacheKey(username, groups), pc.connectionLocked(), perms)
}

func (pc *PermissionCache) setLocked(key, connection string, perms *UserPermissions) {
	now := time.Now()
	perms.ExpiresAt = now.Add(pc.ttl)
	perms.connection = connection
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

// Resolve shares discovery for concurrent misses on one identity and connection.
// Discovery has its own budget so even a burst of short-lived requests can warm
// the cache. A nil answer, expired budget or superseded connection fails closed.
// Invalidate detaches a run from the cache, but its existing callers can still
// use its answer while the connection is current. discover must honor its context.
// Without a connection provider, use the empty connection key.
func (pc *PermissionCache) Resolve(ctx context.Context, username string, groups []string, connection string, discover func(context.Context) *UserPermissions) (*UserPermissions, bool) {
	key := cacheKey(username, groups)
	flightKey := string(appendKeyPart([]byte(key), connection))

	pc.mu.Lock()
	if ctx.Err() != nil || pc.connectionLocked() != connection {
		pc.mu.Unlock()
		return nil, false
	}
	if perms := pc.liveLocked(key, connection); perms != nil {
		pc.mu.Unlock()
		return perms, true
	}
	f := pc.flights[flightKey]
	if f == nil {
		f = startFlight(ctx, &pc.mu, "permission discovery", pc.discoveryTimeout, discover, func(f *flight[*UserPermissions], perms *UserPermissions) *UserPermissions {
			current := pc.flights[flightKey] == f
			if current {
				delete(pc.flights, flightKey)
			}
			if pc.connectionLocked() != connection {
				return nil
			}
			if current && perms != nil {
				if live := pc.liveLocked(key, connection); live != nil {
					return live
				}
				pc.setLocked(key, connection, perms)
			}
			return perms
		})
		if pc.flights == nil {
			pc.flights = make(map[string]*flight[*UserPermissions])
		}
		pc.flights[flightKey] = f
	} else {
		f.join()
	}
	pc.mu.Unlock()

	perms, ok := f.await(ctx, &pc.mu, nil)
	return perms, ok && perms != nil
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
