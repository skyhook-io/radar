package issues

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
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

func (p *CacheProvider) DetectProblems(namespaces []string) []k8s.Problem {
	if len(namespaces) == 0 {
		return k8s.DetectProblems(p.cache, "")
	}
	perNs := make([][]k8s.Problem, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectProblems(p.cache, ns))
	}
	return flattenNamespacedProblems(perNs)
}

// DetectMissingRefs returns dangling-reference problems for all enabled
// source kinds in DetectMissingRefs + DetectMissingWebhookRefs. Same
// flattenNamespacedProblems shape as DetectProblems: cluster-scoped
// rows (ClusterRoleBinding etc.) only come back when namespaces==nil.
func (p *CacheProvider) DetectMissingRefs(namespaces []string) []k8s.Problem {
	if len(namespaces) == 0 {
		out := k8s.DetectMissingRefs(p.cache, "")
		out = append(out, k8s.DetectMissingWebhookRefs(p.cache, p.dynamic, p.discovery, "")...)
		return out
	}
	perNs := make([][]k8s.Problem, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectMissingRefs(p.cache, ns))
	}
	// Webhook configs are cluster-scoped — namespace-bounded callers do
	// not see them, same convention DetectProblems uses for Node rows.
	return flattenNamespacedProblems(perNs)
}

func (p *CacheProvider) DetectCAPIProblems(namespaces []string) []k8s.Problem {
	if p.dynamic == nil || p.discovery == nil {
		return nil
	}
	if len(namespaces) == 0 {
		return k8s.DetectCAPIProblems(p.dynamic, p.discovery, "")
	}
	perNs := make([][]k8s.Problem, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, k8s.DetectCAPIProblems(p.dynamic, p.discovery, ns))
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
func flattenNamespacedProblems(perNs [][]k8s.Problem) []k8s.Problem {
	var out []k8s.Problem
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

func (p *CacheProvider) WarningEvents(namespaces []string, since time.Duration) []*corev1.Event {
	if p.cache.Events() == nil {
		return nil
	}
	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	collect := func(ns string) []*corev1.Event {
		var lst []*corev1.Event
		var err error
		if ns == "" {
			lst, err = p.cache.Events().List(labels.Everything())
		} else {
			lst, err = p.cache.Events().Events(ns).List(labels.Everything())
		}
		if err != nil {
			return nil
		}
		out := make([]*corev1.Event, 0, len(lst))
		for _, e := range lst {
			if e.Type != corev1.EventTypeWarning {
				continue
			}
			if !cutoff.IsZero() {
				last := e.LastTimestamp.Time
				if last.IsZero() {
					last = e.EventTime.Time
				}
				if last.Before(cutoff) {
					continue
				}
			}
			out = append(out, e)
		}
		return out
	}
	if len(namespaces) == 0 {
		return collect("")
	}
	var merged []*corev1.Event
	for _, ns := range namespaces {
		merged = append(merged, collect(ns)...)
	}
	return merged
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

func (p *CacheProvider) KyvernoFindings() []policyreports.SubjectFindings {
	idx := k8s.GetPolicyReportIndex()
	if idx == nil {
		return nil
	}
	return idx.All()
}

// KyvernoStatus is a thin string-typed wrapper around k8s.GetKyvernoStatus
// so the issues package doesn't need to depend on the k8s package for the
// enum. Values are the constants documented on k8s.KyvernoStatus.
func (p *CacheProvider) KyvernoStatus() string {
	return string(k8s.GetKyvernoStatus())
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
