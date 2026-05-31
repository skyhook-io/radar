package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
)

// CacheProvider adapts radar's in-process caches to the Provider
// interface. Uses the package-level singletons (k8s.GetResourceCache,
// k8s.GetDynamicResourceCache, k8s.GetResourceDiscovery).
type CacheProvider struct {
	cache     *k8s.ResourceCache
	dynamic   *k8s.DynamicResourceCache
	discovery *k8s.ResourceDiscovery
}

// NewCacheProvider returns a Provider over the live radar caches, or
// nil if the typed cache isn't ready (cluster connection still pending).
func NewCacheProvider() *CacheProvider {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	return &CacheProvider{
		cache:     cache,
		dynamic:   k8s.GetDynamicResourceCache(),
		discovery: k8s.GetResourceDiscovery(),
	}
}

func (p *CacheProvider) DetectProblems(namespaces []string) []k8s.Detection {
	if len(namespaces) == 0 {
		return k8s.DetectProblems(p.cache, "")
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectProblems(p.cache, ns))
	}
	return flattenNamespacedProblems(perNs)
}

// DetectMissingRefs returns dangling-reference problems for all enabled
// source kinds in DetectMissingRefs plus dynamic webhook/Gateway checks. Same
// flattenNamespacedProblems shape as DetectProblems: cluster-scoped
// rows (ClusterRoleBinding etc.) only come back when namespaces==nil.
func (p *CacheProvider) DetectMissingRefs(namespaces []string) []k8s.Detection {
	if len(namespaces) == 0 {
		out := k8s.DetectMissingRefs(p.cache, "")
		out = append(out, k8s.DetectMissingWebhookRefs(p.cache, p.dynamic, p.discovery, "")...)
		out = append(out, k8s.DetectMissingGatewayRefs(p.cache, p.dynamic, p.discovery, "")...)
		return out
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		out := k8s.DetectMissingRefs(p.cache, ns)
		out = append(out, k8s.DetectMissingGatewayRefs(p.cache, p.dynamic, p.discovery, ns)...)
		perNs = append(perNs, out)
	}
	// Webhook configs are cluster-scoped — namespace-bounded callers do
	// not see them, same convention DetectProblems uses for Node rows.
	return flattenNamespacedProblems(perNs)
}

// DetectScheduling fans the three scheduling detectors (bind-time,
// admission, post-bind) across namespaces. All rows are namespaced, so the
// flattenNamespacedProblems convention applies unchanged.
func (p *CacheProvider) DetectScheduling(namespaces []string) []k8s.Detection {
	detect := func(ns string) []k8s.Detection {
		out := k8s.DetectSchedulingProblems(p.cache, ns)
		out = append(out, k8s.DetectAdmissionProblems(p.cache, ns)...)
		out = append(out, k8s.DetectPostBindProblems(p.cache, ns)...)
		return out
	}
	if len(namespaces) == 0 {
		return detect("")
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, detect(ns))
	}
	return flattenNamespacedProblems(perNs)
}

func (p *CacheProvider) DetectCAPIProblems(namespaces []string) []k8s.Detection {
	if p.dynamic == nil || p.discovery == nil {
		return nil
	}
	if len(namespaces) == 0 {
		return k8s.DetectCAPIProblems(p.dynamic, p.discovery, "")
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectCAPIProblems(p.dynamic, p.discovery, ns))
	}
	return flattenNamespacedProblems(perNs)
}

func (p *CacheProvider) DetectGitOpsProblems(namespaces []string) []k8s.Detection {
	if p.dynamic == nil || p.discovery == nil {
		return nil
	}
	if len(namespaces) == 0 {
		return k8s.DetectGitOpsProblems(p.dynamic, p.discovery, "")
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectGitOpsProblems(p.dynamic, p.discovery, ns))
	}
	return flattenNamespacedProblems(perNs)
}

// flattenNamespacedProblems concatenates per-namespace problem lists
// while dropping cluster-scoped entries (those with empty Namespace).
//
// k8s.DetectProblems appends cluster-scoped problems (Node, and any
// future kind with no Namespace) to its result regardless of the
// namespace argument — calling it per-namespace would therefore both
// LEAK those rows to a namespace-bounded caller (a Cloud viewer scoped
// to one ns has no RBAC to list cluster-scoped resources) and
// DUPLICATE them len(namespaces) times. Callers that want cluster-
// scoped issues pass namespaces == nil and skip this helper.
func flattenNamespacedProblems(perNs [][]k8s.Detection) []k8s.Detection {
	var out []k8s.Detection
	for _, lst := range perNs {
		for _, prob := range lst {
			if prob.Namespace == "" {
				continue
			}
			out = append(out, prob)
		}
	}
	return out
}

func (p *CacheProvider) WatchedDynamic() []schema.GroupVersionResource {
	if p.dynamic == nil {
		return nil
	}
	return p.dynamic.GetWatchedResources()
}

func (p *CacheProvider) ListDynamic(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, nil
	}
	return p.dynamic.List(gvr, namespace)
}

// ListDynamicAllNamespaces unions the GVR's cached objects across every watched
// scope. Safe only on the cluster-wide-intent path (the caller has already
// confirmed no namespace filter, which the handler only leaves empty for
// cluster-wide-authorized callers) — ListWatched does not itself apply per-user
// RBAC, so it must not back a namespace-scoped request.
func (p *CacheProvider) ListDynamicAllNamespaces(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, nil
	}
	return p.dynamic.ListWatched(gvr)
}

func (p *CacheProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	if p.discovery == nil {
		return ""
	}
	return p.discovery.GetKindForGVR(gvr)
}

func (p *CacheProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	if p.discovery == nil {
		return false, false
	}
	kind := p.discovery.GetKindForGVR(gvr)
	if kind == "" {
		return false, false
	}
	ar, ok := p.discovery.GetResourceWithGroup(kind, gvr.Group)
	if !ok {
		return false, false
	}
	return ar.Namespaced, true
}
