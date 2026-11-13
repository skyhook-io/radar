package helm

import (
	"slices"
	"strings"

	"helm.sh/helm/v3/pkg/release"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// StatusAccess decides which of a release's objects the caller may see the
// live status of. Release health is computed from those objects only. A nil
// StatusAccess reads no live status at all, so health is "unknown".
type StatusAccess interface {
	// statusDecision says whether to look the object up. When it shouldn't be,
	// reason says why the caller can't see it, or is empty when the object is
	// left as it is without a lookup.
	statusDecision(kind, group, namespace string) (lookup bool, reason readreason.Code)
	// resolvedFor returns the access with the checks these objects need made
	// up front, so each kind is checked once across its namespaces rather than
	// once per object. Its decisions don't change.
	resolvedFor(resources []OwnedResource) StatusAccess
}

// statusDecision applies the caller's gates as Scan does. Under a
// --namespace-scope pin, an object outside the pinned namespace is left
// without a lookup and without a marker: Radar doesn't watch that namespace,
// so there is nothing to withhold.
func (s *ObjectScope) statusDecision(kind, group, namespace string) (bool, readreason.Code) {
	if outsidePin(namespace) {
		return false, ""
	}
	return s.Access.PermitsObject(kind, group, namespace)
}

func outsidePin(namespace string) bool {
	return k8s.ForceNamespaceScope && namespace != "" && namespace != k8s.GetNamespaceScopeTarget()
}

func (s *ObjectScope) resolvedFor(resources []OwnedResource) StatusAccess {
	var refs []search.ObjectRef
	for _, r := range resources {
		group, ok := builtInGroup(r)
		if !ok || outsidePin(r.Namespace) {
			continue
		}
		refs = append(refs, search.ObjectRef{Kind: r.Kind, Group: group, Namespace: r.Namespace})
	}
	resolved := *s
	resolved.Access = s.Access.Resolve(refs)
	return &resolved
}

// NamespaceStatusScope is a StatusAccess that knows only the namespaces the
// caller may see: Namespaces nil means every namespace, and Checked false
// means they couldn't be checked, so nothing is looked up. It looks up no
// cluster-scoped object and no kind that needs its own permission beyond
// namespace visibility, so it suits callers that use only release health,
// which counts namespaced workloads and Pods. The zero value reads nothing.
type NamespaceStatusScope struct {
	Namespaces []string
	Checked    bool
}

func (s NamespaceStatusScope) statusDecision(kind, group, namespace string) (bool, readreason.Code) {
	if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group); clusterScoped {
		return false, ""
	}
	if _, resource := k8s.ExactNamespaceAccessResource(kind, group); resource != "" {
		return false, ""
	}
	if !s.Checked {
		return false, readreason.UserPermissionCheckFailed
	}
	if s.Namespaces != nil && !slices.Contains(s.Namespaces, namespace) {
		return false, readreason.UserNamespaceNotVisible
	}
	return true, ""
}

func (s NamespaceStatusScope) resolvedFor([]OwnedResource) StatusAccess { return s }

// CacheKey identifies the scope, for results cached per scope.
func (s NamespaceStatusScope) CacheKey() string {
	switch {
	case !s.Checked:
		return "unchecked"
	case s.Namespaces == nil:
		return "all"
	}
	namespaces := slices.Clone(s.Namespaces)
	slices.Sort(namespaces)
	return "ns:" + strings.Join(slices.Compact(namespaces), ",")
}

// scannedObjects is the StatusAccess for objects a Scan already handed the
// caller under its gates, so every one may be looked up.
type scannedObjects struct{}

func (scannedObjects) statusDecision(string, string, string) (bool, readreason.Code) {
	return true, ""
}

func (s scannedObjects) resolvedFor([]OwnedResource) StatusAccess { return s }

func noStatusAccess(access StatusAccess) bool {
	if access == nil {
		return true
	}
	scope, ok := access.(*ObjectScope)
	return ok && scope == nil
}

// legacyBuiltinGroups are the API groups Kubernetes once served these
// built-in kinds under. A release installed then keeps the old group in its
// stored manifest while the live object is the same one.
var legacyBuiltinGroups = map[string]string{
	"Deployment":    "extensions",
	"DaemonSet":     "extensions",
	"ReplicaSet":    "extensions",
	"Ingress":       "extensions",
	"NetworkPolicy": "extensions",
}

// builtInGroup returns the API group of the built-in kind an object is, and
// false when it isn't one. The status lookup goes by kind alone, so a custom
// kind named like a built-in one would read, and count, an unrelated object.
// An empty APIVersion is left only on built-in objects read from the cache
// whose served version varies, so it counts as built-in.
func builtInGroup(r OwnedResource) (string, bool) {
	builtin, ok := resourceid.BuiltinForKind(r.Kind)
	if !ok {
		return "", false
	}
	if r.APIVersion == "" {
		return builtin.Group, true
	}
	group := resourceid.GroupFromAPIVersion(r.APIVersion)
	if group == builtin.Group || (group != "" && legacyBuiltinGroups[r.Kind] == group) {
		return builtin.Group, true
	}
	return "", false
}

// readReleaseStatus fills in the live status of the objects the caller may
// see and returns release health over them.
func readReleaseStatus(resources []OwnedResource, access StatusAccess) (health, issue, summary string) {
	if noStatusAccess(access) {
		return "unknown", "", ""
	}
	return readResolvedStatus(resources, access.resolvedFor(resources))
}

// readResolvedStatus is readReleaseStatus with access already resolved for
// resources.
func readResolvedStatus(resources []OwnedResource, access StatusAccess) (health, issue, summary string) {
	enrichResourcesWithStatus(resources, access)
	return computeResourceHealth(resources)
}

// addReleasesHealth resolves caller access once across all releases.
func addReleasesHealth(rows []HelmRelease, rels []*release.Release, access StatusAccess) {
	if noStatusAccess(access) {
		for i := range rows {
			rows[i].ResourceHealth = "unknown"
		}
		return
	}
	resources := make([][]OwnedResource, len(rels))
	var all []OwnedResource
	for i, rel := range rels {
		resources[i] = parseManifestResources(rel.Manifest, rel.Namespace)
		all = append(all, resources[i]...)
	}
	access = access.resolvedFor(all)
	for i := range rows {
		rows[i].ResourceHealth, rows[i].HealthIssue, rows[i].HealthSummary = readResolvedStatus(resources[i], access)
		rows[i].StatusUnreadCount = statusUnreadCount(resources[i])
	}
}

// statusUnreadCount counts withheld objects that contribute to release health.
func statusUnreadCount(resources []OwnedResource) int {
	count := 0
	for _, r := range resources {
		if r.StatusUnread == "" {
			continue
		}
		if _, builtin := builtInGroup(r); !builtin {
			continue
		}
		switch r.Kind {
		case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet", "Pod":
			count++
		}
	}
	return count
}
