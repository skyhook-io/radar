package mcp

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// MCP read tools share the cluster-wide resource cache (populated by the pod
// SA), so per-user namespace filtering must happen at read time. This mirrors
// the REST handler chain (parseNamespacesForUser → getUserNamespaces) but
// lives in the MCP package because Server.permCache isn't reachable from here.

var (
	mcpPermCache     *pkgauth.PermissionCache
	mcpPermCacheOnce sync.Once
)

func getPermCache() *pkgauth.PermissionCache {
	mcpPermCacheOnce.Do(func() {
		// Stamp entries with the current K8s context — a request mid-context-
		// switch can't be authorized by the previous cluster's RBAC even if
		// the OnContextSwitch invalidation hasn't fired yet.
		mcpPermCache = pkgauth.NewPermissionCache().WithContextName(k8s.GetContextName)
		// New cluster ⇒ stale RBAC: drop everything we cached for the old one.
		k8s.OnContextSwitch(func(_ string) {
			mcpPermCache.Invalidate()
		})
	})
	return mcpPermCache
}

// resolveUserPerms returns the user's namespace permissions, discovering them
// via SubjectAccessReview on first call. Result is cached per-user with the
// PermissionCache TTL (2 min) so subsequent reads stay fast.
//
// Returns nil when no user is on context (auth disabled). Returns a perms
// struct with a non-nil but empty AllowedNamespaces on discovery failure
// (fail-closed: treat the user as having no access rather than leaking).
func resolveUserPerms(ctx context.Context) (*pkgauth.User, *pkgauth.UserPermissions) {
	user := pkgauth.UserFromContext(ctx)
	if user == nil {
		return nil, nil
	}
	cache := getPermCache()
	if perms := cache.Get(user.Username); perms != nil {
		return user, perms
	}

	client := k8s.GetClient()
	if client == nil {
		log.Printf("[mcp] K8s client unavailable for namespace discovery (user=%s) — denying access", user.Username)
		// Empty (not nil) AllowedNamespaces means "no access"; nil would mean cluster-admin.
		return user, &pkgauth.UserPermissions{AllowedNamespaces: []string{}}
	}

	allNamespaces := mcpAllNamespaceNames(ctx)
	allowed, err := pkgauth.DiscoverNamespaces(ctx, client, user.Username, user.Groups, allNamespaces)
	if err != nil {
		log.Printf("[mcp] DiscoverNamespaces failed for %s: %v — denying access (fail-closed)", user.Username, err)
		return user, &pkgauth.UserPermissions{AllowedNamespaces: []string{}}
	}

	perms := &pkgauth.UserPermissions{AllowedNamespaces: allowed}
	cache.Set(user.Username, perms)
	return user, perms
}

func mcpAllNamespaceNames(ctx context.Context) []string {
	var allNamespaces []string
	if rc := k8s.GetResourceCache(); rc != nil {
		if nsLister := rc.Namespaces(); nsLister != nil {
			nsList, _ := nsLister.List(labels.Everything())
			for _, ns := range nsList {
				allNamespaces = append(allNamespaces, ns.Name)
			}
		}
	}
	if len(allNamespaces) == 0 {
		if accessible, _ := k8s.GetAccessibleNamespaces(ctx); len(accessible) > 0 {
			allNamespaces = accessible
		}
	}
	return allNamespaces
}

// filterNamespacesForUser intersects requested namespaces with the user's
// allowed set. Mirrors auth.FilterNamespacesForUser semantics:
//
//   - nil return  → no filter (auth disabled or user is cluster-admin)
//   - empty slice → user has no access; caller should return empty results
//   - non-empty   → restrict reads to these namespaces
//
// requested may be nil ("all namespaces"). Pass a single-element slice to
// check a specific namespace.
func filterNamespacesForUser(ctx context.Context, requested []string) []string {
	user, perms := resolveUserPerms(ctx)
	if user == nil {
		return requested
	}
	return pkgauth.FilterNamespacesForUser(requested, user, perms)
}

// checkNamespaceAccess reports whether the user can read in this single
// namespace. Convenience for tools that target one namespaced resource
// (get_pod_logs, get_workload_logs).
//
// Only valid for namespaced reads. Empty namespace ("") routes to
// canReadClusterScopedKind — namespace-list discovery is not a sufficient
// signal for cluster-scoped authorization.
func checkNamespaceAccess(ctx context.Context, namespace string) bool {
	if namespace == "" {
		// Caller misuse — cluster-scoped reads must use canReadClusterScopedKind.
		return false
	}
	allowed := filterNamespacesForUser(ctx, []string{namespace})
	if allowed == nil {
		return true
	}
	return slices.Contains(allowed, namespace)
}

// subjectCanI is overridden in tests to bypass the live apiserver call.
var subjectCanI = pkgauth.SubjectCanI

// canReadClusterScopedKind authorizes a single (verb, kind) cluster-scoped
// read for the calling user via SubjectAccessReview. Mirrors Server.canRead
// for the MCP package. Result is cached on UserPermissions per the same TTL
// as the namespace-discovery cache.
//
// group disambiguates colliding kinds (e.g. Knative Service vs core Service).
// Pass "" when the caller doesn't know the group; the static catalogue still
// wins on core kinds.
//
// Returns true (passthrough) when the kind is namespaced/unknown — the
// caller's namespace check is the gate in that case.
func canReadClusterScopedKind(ctx context.Context, kind, group, verb string) bool {
	user, perms := resolveUserPerms(ctx)
	if user == nil {
		return true
	}

	clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
	if !clusterScoped {
		return true // namespaced or unknown — gate via namespace check elsewhere
	}

	if perms != nil {
		if v, ok := perms.CanI(verb, gvrGroup, gvrResource, ""); ok {
			return v
		}
	}
	client := k8s.GetClient()
	if client == nil {
		// Fail-closed: no apiserver to ask, refuse the read rather than
		// quietly serving the user something they may not be entitled to.
		log.Printf("[mcp] canReadClusterScopedKind: no K8s client, denying %s on %s/%s for %s", verb, gvrGroup, gvrResource, user.Username)
		return false
	}
	allowed, err := subjectCanI(ctx, client, user.Username, user.Groups, "", gvrGroup, gvrResource, verb)
	if err != nil {
		log.Printf("[mcp] canReadClusterScopedKind SAR failed for %s on %s/%s: %v", user.Username, gvrGroup, gvrResource, err)
		return false
	}
	if perms != nil {
		perms.SetCanI(verb, gvrGroup, gvrResource, "", allowed)
	}
	return allowed
}

// canReadInNamespace authorizes a single (verb, group, resource, namespace)
// read via SubjectAccessReview, memoizing on UserPermissions.canI. Use when
// per-kind RBAC inside a namespace differs from namespace-list discovery
// (e.g. user can list pods but not secrets in `team-a`).
//
// namespace="" issues an any-namespace SAR — for a *namespaced* kind that
// asks "can the user list this kind cluster-wide?" (useful as the
// cluster-wide-scan branch in search RBAC). For *cluster-scoped* kinds
// keep using canReadClusterScopedKind; it routes via ClassifyKindScope.
//
// Returns true (passthrough) when no user is on context — auth-mode=none
// applies the SA's RBAC at the cache layer.
func canReadInNamespace(ctx context.Context, group, resource, namespace, verb string) bool {
	user, perms := resolveUserPerms(ctx)
	if user == nil {
		return true
	}
	if perms != nil {
		if v, ok := perms.CanI(verb, group, resource, namespace); ok {
			return v
		}
	}
	client := k8s.GetClient()
	if client == nil {
		log.Printf("[mcp] canReadInNamespace: no K8s client, denying %s on %s/%s in %q for %s", k8s.SanitizeForLog(verb), k8s.SanitizeForLog(group), k8s.SanitizeForLog(resource), k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(user.Username))
		return false
	}
	allowed, err := subjectCanI(ctx, client, user.Username, user.Groups, namespace, group, resource, verb)
	if err != nil {
		log.Printf("[mcp] canReadInNamespace SAR failed for %s on %s/%s in %q: %v", k8s.SanitizeForLog(user.Username), k8s.SanitizeForLog(group), k8s.SanitizeForLog(resource), k8s.SanitizeForLog(namespace), err)
		return false
	}
	if perms != nil {
		perms.SetCanI(verb, group, resource, namespace, allowed)
	}
	return allowed
}

// filterNamespacesByCanRead returns the subset of `namespaces` where the
// calling user passes a per-namespace SAR for (group, resource, verb). The
// MCP-side mirror of Server.filterNamespacesByCanRead.
//
// nil or empty input is returned unchanged.
func filterNamespacesByCanRead(ctx context.Context, group, resource, verb string, namespaces []string) []string {
	if len(namespaces) == 0 {
		return namespaces
	}
	out := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		if canReadInNamespace(ctx, group, resource, ns, verb) {
			out = append(out, ns)
		}
	}
	return out
}

// retainAllowedObjects post-filters cache results for namespace-restricted users.
// FetchResourceList accepts an `allowed` namespace slice for namespaced kinds
// (it iterates per-namespace), but cluster-scoped lookups (e.g. cache.Nodes)
// ignore it — and the "namespaces" lister always returns all namespaces.
// This helper applies the per-user filter to the result objects.
//
// Behavior:
//   - "namespaces" kind: keep only items whose .Name is in allowed
//   - other items with no metadata.namespace (cluster-scoped): drop them
//     (non-admins reach this only for kinds we forgot to flag clusterOnly)
//   - namespaced items: keep only those whose metadata.namespace is in allowed
func retainAllowedObjects(objs []runtime.Object, allowed []string, kind string) []runtime.Object {
	if allowed == nil {
		return objs
	}
	set := make(map[string]bool, len(allowed))
	for _, ns := range allowed {
		set[ns] = true
	}
	out := make([]runtime.Object, 0, len(objs))
	isNamespacesKind := strings.ToLower(kind) == "namespaces" || strings.ToLower(kind) == "namespace"
	for _, obj := range objs {
		m, err := meta.Accessor(obj)
		if err != nil {
			continue
		}
		if isNamespacesKind {
			if set[m.GetName()] {
				out = append(out, obj)
			}
			continue
		}
		ns := m.GetNamespace()
		if ns == "" {
			// Cluster-scoped resource leaked through (e.g. caller didn't gate
			// via isClusterOnlyKind). Drop it for non-admins.
			continue
		}
		if set[ns] {
			out = append(out, obj)
		}
	}
	return out
}
