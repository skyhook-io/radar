package issues

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	clusterDNSContextRecentWindow = 30 * time.Minute
	maxDNSNamespaceSymptomScans   = 50
)

type coreDNSAccess struct {
	configMaps  bool
	deployments bool
	replicaSets bool
}

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
		out = append(out, k8s.DetectMissingCRDRefs(p.cache, p.dynamic, p.discovery, "")...)
		return out
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		out := k8s.DetectMissingRefs(p.cache, ns)
		out = append(out, k8s.DetectMissingGatewayRefs(p.cache, p.dynamic, p.discovery, ns)...)
		out = append(out, k8s.DetectMissingCRDRefs(p.cache, p.dynamic, p.discovery, ns)...)
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
		return out
	}
	if len(namespaces) == 0 {
		out := detect("")
		out = append(out, k8s.DetectPostBindProblems(p.cache, "")...)
		return out
	}
	perNs := make([][]k8s.Detection, 0, len(namespaces))
	for _, ns := range namespaces {
		perNs = append(perNs, detect(ns))
	}
	out := flattenNamespacedProblems(perNs)
	out = append(out, k8s.DetectPostBindProblemsForNamespaces(p.cache, namespaces)...)
	return out
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

func (p *CacheProvider) SelectedPodsForService(namespace, name string) []Ref {
	if p == nil || p.cache == nil || p.cache.Services() == nil || p.cache.Pods() == nil {
		return nil
	}
	svc, err := p.cache.Services().Services(namespace).Get(name)
	if err != nil || svc == nil || len(svc.Spec.Selector) == 0 {
		return nil
	}
	pods, err := p.cache.Pods().Pods(namespace).List(labels.SelectorFromSet(labels.Set(svc.Spec.Selector)))
	if err != nil {
		return nil
	}
	refs := make([]Ref, 0, len(pods))
	for _, pod := range pods {
		refs = append(refs, Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name})
	}
	sortRefs(refs)
	return refs
}

// PodsOnNode returns every pod scheduled onto the named node (spec.nodeName).
// The caller intersects these against the request-scoped issue set, so a pod the
// user can't see contributes no link — RBAC/namespace scoping is preserved by the
// intersection, not by this lister.
func (p *CacheProvider) PodsOnNode(nodeName string) []Ref {
	if p == nil || p.cache == nil || p.cache.Pods() == nil || nodeName == "" {
		return nil
	}
	pods, err := p.cache.Pods().List(labels.Everything())
	if err != nil {
		return nil
	}
	refs := make([]Ref, 0)
	for _, pod := range pods {
		if pod.Spec.NodeName == nodeName {
			refs = append(refs, Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name})
		}
	}
	sortRefs(refs)
	return refs
}

// PodsMountingPVC returns the pods that mount the named PersistentVolumeClaim
// (spec.volumes[].persistentVolumeClaim.claimName) — the declared edge from a
// PVC to the workloads it blocks. As with PodsOnNode, the caller intersects
// against the request-scoped issue set, so visibility is enforced there.
func (p *CacheProvider) PodsMountingPVC(namespace, pvcName string) []Ref {
	if p == nil || p.cache == nil || p.cache.Pods() == nil || namespace == "" || pvcName == "" {
		return nil
	}
	pods, err := p.cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	refs := make([]Ref, 0)
	for _, pod := range pods {
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvcName {
				refs = append(refs, Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name})
				break
			}
		}
	}
	sortRefs(refs)
	return refs
}

func (p *CacheProvider) ChangeContextForIssue(i Issue) *issuesapi.ChangeContext {
	if p == nil || p.cache == nil {
		return nil
	}
	if i.Kind != "Deployment" || (i.Group != "" && i.Group != "apps") || i.Namespace == "" || i.Name == "" {
		return nil
	}
	return deploymentChangeContext(p.cache, i.Namespace, i.Name)
}

// ClusterContextForIssues surfaces cross-namespace cluster context (today: the
// CoreDNS DNS hint) alongside a namespace-scoped issue query. canReadCoreDNS
// gates kube-system disclosure by the concrete resource being read. A nil
// predicate means no auth gate (local/no-auth), matching s.canRead /
// canReadInNamespace passthrough semantics.
func (p *CacheProvider) ClusterContextForIssues(namespaces []string, canReadCoreDNS func(group, resource string) bool) *issuesapi.ClusterContext {
	access := coreDNSAccess{configMaps: true, deployments: true, replicaSets: true}
	if canReadCoreDNS != nil {
		access = coreDNSAccess{
			configMaps:  canReadCoreDNS("", "configmaps"),
			deployments: canReadCoreDNS("apps", "deployments"),
			replicaSets: canReadCoreDNS("apps", "replicasets"),
		}
	}
	if !access.configMaps {
		return nil
	}
	dns := p.clusterDNSContext(namespaces, access)
	if dns == nil {
		return nil
	}
	return &issuesapi.ClusterContext{DNS: dns}
}

func (p *CacheProvider) clusterDNSContext(namespaces []string, access coreDNSAccess) *issuesapi.ClusterDNSContext {
	if p == nil || p.cache == nil {
		return nil
	}
	findings := k8s.DetectSuspiciousCoreDNS(p.cache, time.Now())
	if len(findings) == 0 {
		return nil
	}
	signals := p.clusterDNSSignals(namespaces, access)
	if len(signals) == 0 {
		return nil
	}
	out := &issuesapi.ClusterDNSContext{
		Signals: signals,
		Hint:    "Cluster DNS context may be relevant: inspect kube-system/CoreDNS before attributing DNS or service-resolution symptoms only to application workloads.",
	}
	for _, f := range findings {
		out.Findings = append(out.Findings, issuesapi.ClusterDNSFinding{
			Kind:      f.Kind,
			Namespace: f.Namespace,
			Name:      f.Name,
			Severity:  f.Severity,
			Reason:    f.Reason,
			Message:   f.Message,
			Evidence:  strings.Join(signals, "; "),
		})
		if len(out.Findings) >= 3 {
			break
		}
	}
	if len(out.Findings) > 0 {
		first := out.Findings[0]
		out.Hint = fmt.Sprintf("%s %s/%s is suspicious (%s); %s", first.Kind, first.Namespace, first.Name, first.Reason, out.Hint)
	}
	return out
}

func (p *CacheProvider) clusterDNSSignals(namespaces []string, access coreDNSAccess) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] || len(out) >= 6 {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if access.deployments && access.replicaSets {
		add(p.coreDNSDeploymentRolloutSignal())
	}
	if access.configMaps {
		add(observedCoreDNSConfigMapChangeSignal())
	}
	for _, s := range p.namespaceDNSSymptomSignals(namespaces) {
		add(s)
	}
	return out
}

func (p *CacheProvider) coreDNSDeploymentRolloutSignal() string {
	if p.cache == nil || p.cache.Deployments() == nil || p.cache.ReplicaSets() == nil {
		return ""
	}
	deps, err := p.cache.Deployments().Deployments("kube-system").List(labels.Everything())
	if err != nil {
		return ""
	}
	for _, d := range deps {
		if d == nil || !isCoreDNSNameOrLabels(d.Name, d.Labels) {
			continue
		}
		if sig := deploymentRolloutSignal(p.cache, d, clusterDNSContextRecentWindow); sig != "" {
			return sig
		}
	}
	return ""
}

func (p *CacheProvider) namespaceDNSSymptomSignals(namespaces []string) []string {
	if p == nil || p.cache == nil || p.cache.Events() == nil {
		return nil
	}
	if len(namespaces) == 0 {
		namespaces = p.namespacesForDNSSymptomScan()
	}
	var out []string
	checkNamespace := func(ns string) {
		events, err := p.cache.Events().Events(ns).List(labels.Everything())
		if err != nil {
			return
		}
		for _, e := range events {
			if e == nil || e.Type != corev1.EventTypeWarning {
				continue
			}
			if textContainsDNSSymptom(e.Reason + " " + e.Message) {
				out = append(out, fmt.Sprintf("Warning events in namespace %s contain DNS resolution errors", ns))
				return
			}
		}
	}
	for _, ns := range namespaces {
		if ns != "" {
			checkNamespace(ns)
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func (p *CacheProvider) namespacesForDNSSymptomScan() []string {
	if p == nil || p.cache == nil || p.cache.Namespaces() == nil {
		return nil
	}
	items, err := p.cache.Namespaces().List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(items))
	for _, ns := range items {
		if ns == nil || !dnsSymptomNamespace(ns.Name) {
			continue
		}
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	if len(names) > maxDNSNamespaceSymptomScans {
		names = names[:maxDNSNamespaceSymptomScans]
	}
	return names
}

func dnsSymptomNamespace(namespace string) bool {
	switch namespace {
	case "", "kube-system", "kube-public", "kube-node-lease":
		return false
	default:
		return true
	}
}

func observedCoreDNSConfigMapChangeSignal() string {
	store := timeline.GetStore()
	if store == nil {
		return ""
	}
	events, err := store.Query(context.Background(), timeline.QueryOptions{
		Namespaces:     []string{"kube-system"},
		Kinds:          []string{"ConfigMap"},
		Since:          time.Now().Add(-clusterDNSContextRecentWindow),
		Limit:          20,
		ClusterContext: k8s.ActiveClusterContext(),
	})
	if err != nil {
		return ""
	}
	for _, e := range events {
		if e.EventType != timeline.EventTypeUpdate || !strings.Contains(strings.ToLower(e.Name), "coredns") {
			continue
		}
		return fmt.Sprintf("Radar observed ConfigMap kube-system/%s update %s ago", e.Name, k8s.FormatAge(time.Since(e.Timestamp)))
	}
	return ""
}

func deploymentChangeContext(cache *k8s.ResourceCache, namespace, name string) *issuesapi.ChangeContext {
	if cache == nil || cache.Deployments() == nil || cache.ReplicaSets() == nil {
		return nil
	}
	d, err := cache.Deployments().Deployments(namespace).Get(name)
	if err != nil || d == nil || d.Generation <= 1 {
		return nil
	}
	rss, err := cache.ReplicaSets().ReplicaSets(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	owned := ownedReplicaSets(d, rss)
	if len(owned) < 2 {
		return nil
	}
	sort.SliceStable(owned, func(i, j int) bool {
		return owned[i].CreationTimestamp.Time.After(owned[j].CreationTimestamp.Time)
	})
	newest := owned[0]
	parts := []string{
		fmt.Sprintf("generation=%d", d.Generation),
		fmt.Sprintf("observedGeneration=%d", d.Status.ObservedGeneration),
		fmt.Sprintf("%d owned ReplicaSets", len(owned)),
	}
	if !newest.CreationTimestamp.IsZero() {
		parts = append(parts, fmt.Sprintf("newest ReplicaSet %s created %s ago", newest.Name, k8s.FormatAge(time.Since(newest.CreationTimestamp.Time))))
	}
	ctx := &issuesapi.ChangeContext{
		Changed:  true,
		What:     "pod_template",
		Evidence: strings.Join(parts, ", "),
	}
	if !newest.CreationTimestamp.IsZero() {
		ctx.When = k8s.FormatAge(time.Since(newest.CreationTimestamp.Time))
	}
	return ctx
}

func deploymentRolloutSignal(cache *k8s.ResourceCache, d *appsv1.Deployment, window time.Duration) string {
	if cache == nil || d == nil || d.Generation <= 1 || cache.ReplicaSets() == nil {
		return ""
	}
	rss, err := cache.ReplicaSets().ReplicaSets(d.Namespace).List(labels.Everything())
	if err != nil {
		return ""
	}
	owned := ownedReplicaSets(d, rss)
	if len(owned) < 2 {
		return ""
	}
	sort.SliceStable(owned, func(i, j int) bool {
		return owned[i].CreationTimestamp.Time.After(owned[j].CreationTimestamp.Time)
	})
	newest := owned[0]
	if newest.CreationTimestamp.IsZero() || time.Since(newest.CreationTimestamp.Time) > window {
		return ""
	}
	return fmt.Sprintf("CoreDNS Deployment kube-system/%s rolled %s ago (generation=%d, %d owned ReplicaSets, newest=%s)", d.Name, k8s.FormatAge(time.Since(newest.CreationTimestamp.Time)), d.Generation, len(owned), newest.Name)
}

func ownedReplicaSets(d *appsv1.Deployment, rss []*appsv1.ReplicaSet) []*appsv1.ReplicaSet {
	if d == nil || d.UID == "" {
		return nil
	}
	var out []*appsv1.ReplicaSet
	for _, rs := range rss {
		if rs == nil {
			continue
		}
		for _, owner := range rs.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.Kind == "Deployment" && owner.UID == d.UID {
				out = append(out, rs)
				break
			}
		}
	}
	return out
}

func isCoreDNSNameOrLabels(name string, labels map[string]string) bool {
	low := strings.ToLower(name)
	if strings.Contains(low, "coredns") || strings.Contains(low, "kube-dns") {
		return true
	}
	for _, key := range []string{"k8s-app", "app", "app.kubernetes.io/name"} {
		if strings.Contains(strings.ToLower(labels[key]), "coredns") || strings.Contains(strings.ToLower(labels[key]), "kube-dns") {
			return true
		}
	}
	return false
}

func textContainsDNSSymptom(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "no such host") ||
		strings.Contains(low, "nxdomain") ||
		strings.Contains(low, "temporary failure in name resolution") ||
		strings.Contains(low, "name or service not known") ||
		strings.Contains(low, "server misbehaving") ||
		strings.Contains(low, "lookup ") && strings.Contains(low, ".svc")
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
