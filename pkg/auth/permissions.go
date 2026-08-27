package auth

import (
	"context"
	"log"
	"slices"
	"sort"
	"strings"
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
}

// cacheKeySep separates the username from the groups fingerprint in a cache
// key. It is a control byte that cannot appear in a Kubernetes username or
// group name, so a username's entries remain enumerable by splitting on it.
const cacheKeySep = "\x00"

// cacheKey builds the composite cache key for an identity. The verdicts stored
// in a UserPermissions entry are computed by SubjectAccessReviews that run with
// the username AND its groups, so an entry is only valid for that exact
// (username, groups) identity — keying by username alone lets a different group
// set inherit another identity's RBAC ceiling for the cache TTL. Groups are
// canonicalized (sorted + deduped) so order and duplicates don't fork the key;
// empty/nil groups is a valid, distinct identity (not a wildcard).
func cacheKey(username string, groups []string) string {
	return username + cacheKeySep + groupsFingerprint(groups)
}

// groupsFingerprint returns a deterministic fingerprint of a group set:
// sorted, deduped, and joined. Empty/nil groups fingerprints to "".
func groupsFingerprint(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	uniq := make([]string, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		uniq = append(uniq, g)
	}
	sort.Strings(uniq)
	return strings.Join(uniq, cacheKeySep)
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

	perms, ok := pc.cache[cacheKey(username, groups)]
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

	now := time.Now()
	perms.ExpiresAt = now.Add(pc.ttl)
	if pc.contextName != nil {
		perms.ContextName = pc.contextName()
	}
	key := cacheKey(username, groups)
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

// Invalidate removes all cached permissions (e.g., on context switch)
func (pc *PermissionCache) Invalidate() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache = make(map[string]*UserPermissions)
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
