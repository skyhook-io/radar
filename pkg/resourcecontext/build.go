package resourcecontext

import (
	"context"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/topology"
)

// Options carries everything Build needs to compute a ResourceContext.
//
// Per the v1 contract, this package depends only on pkg/topology — callers
// in internal/* pre-compute IssueSummary / AuditSummary / PolicyReports and
// pass them in, so we don't reach into internal/issues or internal/audit.
type Options struct {
	Tier      ContextTier
	MaxTokens int // reserved for future budgeting; not enforced in v1

	// AccessChecker gates every emitted ContextRef. nil = no gating (treat
	// as fully authorized — local-kubeconfig / tests).
	AccessChecker RefAccessChecker

	// Topology data sources. When Topology is nil, the topology-derived
	// fields (Exposes, SelectedBy, ScaledBy) are skipped.
	Topology    *topology.Topology
	Provider    topology.ResourceProvider
	DynamicProv topology.DynamicProvider

	// Pre-computed summaries — pass-through into the response.
	IssueSummary  *IssueSummary
	AuditSummary  *AuditSummary
	PolicyReports PolicyReportLookup // nil = Kyverno not installed / no findings

	// EmitHints controls whether SynthesizeHints runs over the structured
	// fields. AI-facing callers (MCP, /api/ai/*) set true; UI callers false.
	EmitHints bool
}

// PolicyReportLookup is the minimal interface Build needs from the
// PolicyReport index. The concrete index lives in pkg/policyreports.
//
// Build does not import pkg/policyreports directly because callers may
// adapt other policy engines into the same shape.
type PolicyReportLookup interface {
	FindingsFor(kind, namespace, name string) []KyvernoFinding
}

// RefAccessChecker abstracts the RBAC check so this package doesn't import
// any internal/* package. REST and MCP handlers each implement this with a
// request-scoped batch cache (see internal/server/rc_rbac.go).
//
// Implementations should treat (group, kind, namespace) as the cache key —
// per-name SAR has no upside since RBAC is namespace-granular.
type RefAccessChecker interface {
	CanRead(ctx context.Context, group, kind, namespace string) bool
}

// Build produces a ResourceContext for obj at the requested tier.
//
// Returns nil when obj is nil. Returns a zero-value (.Tier-only)
// ResourceContext when obj is recognized but no enrichment fields apply.
// Never panics on nil sub-fields of opts.
func Build(ctx context.Context, obj runtime.Object, opts Options) *ResourceContext {
	if obj == nil {
		return nil
	}

	ident, ok := identityOf(obj)
	if !ok {
		return &ResourceContext{Tier: opts.Tier}
	}

	rc := &ResourceContext{Tier: opts.Tier}
	omitted := newOmittedTracker()

	// 1. ManagedBy — owner chain + GitOps labels/annotations
	rc.ManagedBy = filterRefs(
		ctx, opts.AccessChecker,
		buildManagedBy(ident),
		"managedBy", omitted,
	)

	// 2. Topology-derived: Exposes, SelectedBy, ScaledBy
	var rel *topology.Relationships
	if opts.Topology != nil {
		rel = topology.GetRelationships(ident.Kind, ident.Namespace, ident.Name, opts.Topology, opts.Provider, opts.DynamicProv)
	}
	if rel != nil {
		exposes := make([]topology.ResourceRef, 0, len(rel.Services)+len(rel.Ingresses)+len(rel.Gateways)+len(rel.Routes))
		exposes = append(exposes, rel.Services...)
		exposes = append(exposes, rel.Ingresses...)
		exposes = append(exposes, rel.Gateways...)
		exposes = append(exposes, rel.Routes...)
		rc.Exposes = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(exposes, ReasonLabelSelector, SourceTopology),
			"exposes", omitted)

		selected := make([]topology.ResourceRef, 0, len(rel.PDBs)+len(rel.NetworkPolicies))
		selected = append(selected, rel.PDBs...)
		selected = append(selected, rel.NetworkPolicies...)
		rc.SelectedBy = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(selected, ReasonPodSelector, SourceTopology),
			"selectedBy", omitted)

		rc.ScaledBy = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(rel.Scalers, ReasonScaleTargetRef, SourceTopology),
			"scaledBy", omitted)
	}

	// 3. Pod-specific: Uses + RunsOn
	if pod, ok := obj.(*corev1.Pod); ok {
		rc.Uses = buildUsesFromPod(ctx, pod, opts.AccessChecker, omitted)

		if pod.Spec.NodeName != "" {
			candidate := &ContextRef{
				Kind:   "Node",
				Name:   pod.Spec.NodeName,
				Reason: ReasonNodeName,
				Source: SourceK8sSpec,
			}
			if checkRef(ctx, opts.AccessChecker, candidate) {
				rc.RunsOn = candidate
			} else {
				omitted.add("runsOn", OmittedRBACDenied)
			}
		}
	}

	// 4. Pre-computed summaries — pass-through.
	rc.IssueSummary = opts.IssueSummary
	rc.AuditSummary = opts.AuditSummary

	// 5. PolicyReports — Kyverno findings rolled up. Basic tier emits
	// counts only (fail/warn/pass); diagnostic tier adds the top[]
	// findings. Tier discrimination keeps the basic-tier wire size tight.
	if opts.PolicyReports != nil {
		findings := opts.PolicyReports.FindingsFor(ident.Kind, ident.Namespace, ident.Name)
		if len(findings) > 0 {
			rc.PolicySummary = buildPolicySummary(findings, opts.Tier)
		}
	}

	// 6. Hints — AI-only.
	if opts.EmitHints {
		rc.Hints = SynthesizeHints(rc, opts.Tier)
	}

	rc.Omitted = omitted.collect()
	return rc
}

// ---------------------------------------------------------------------------
// Identity extraction
// ---------------------------------------------------------------------------

// resourceIdentity is the projection of obj that Build needs without holding
// on to the full runtime.Object. Owner refs and labels feed ManagedBy; the
// (Kind, Namespace, Name) tuple keys topology + summary lookups.
type resourceIdentity struct {
	Kind        string
	Group       string
	Namespace   string
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Owners      []metav1.OwnerReference
}

// identityOf extracts identity from a typed K8s object or unstructured.
// Returns (_, false) for unknown shapes so callers can short-circuit.
func identityOf(obj runtime.Object) (resourceIdentity, bool) {
	if obj == nil {
		return resourceIdentity{}, false
	}
	switch v := obj.(type) {
	case *corev1.Pod:
		return identFromMeta("Pod", "", &v.ObjectMeta), true
	case *corev1.Service:
		return identFromMeta("Service", "", &v.ObjectMeta), true
	case *corev1.ConfigMap:
		return identFromMeta("ConfigMap", "", &v.ObjectMeta), true
	case *corev1.Secret:
		return identFromMeta("Secret", "", &v.ObjectMeta), true
	case *corev1.Node:
		return identFromMeta("Node", "", &v.ObjectMeta), true
	case *corev1.Namespace:
		return identFromMeta("Namespace", "", &v.ObjectMeta), true
	case *corev1.PersistentVolume:
		return identFromMeta("PersistentVolume", "", &v.ObjectMeta), true
	case *corev1.PersistentVolumeClaim:
		return identFromMeta("PersistentVolumeClaim", "", &v.ObjectMeta), true
	case *corev1.ServiceAccount:
		return identFromMeta("ServiceAccount", "", &v.ObjectMeta), true
	case *corev1.Event:
		return identFromMeta("Event", "", &v.ObjectMeta), true
	case *corev1.LimitRange:
		return identFromMeta("LimitRange", "", &v.ObjectMeta), true
	case *appsv1.Deployment:
		return identFromMeta("Deployment", "apps", &v.ObjectMeta), true
	case *appsv1.DaemonSet:
		return identFromMeta("DaemonSet", "apps", &v.ObjectMeta), true
	case *appsv1.StatefulSet:
		return identFromMeta("StatefulSet", "apps", &v.ObjectMeta), true
	case *appsv1.ReplicaSet:
		return identFromMeta("ReplicaSet", "apps", &v.ObjectMeta), true
	case *autoscalingv2.HorizontalPodAutoscaler:
		return identFromMeta("HorizontalPodAutoscaler", "autoscaling", &v.ObjectMeta), true
	case *batchv1.Job:
		return identFromMeta("Job", "batch", &v.ObjectMeta), true
	case *batchv1.CronJob:
		return identFromMeta("CronJob", "batch", &v.ObjectMeta), true
	case *networkingv1.Ingress:
		return identFromMeta("Ingress", "networking.k8s.io", &v.ObjectMeta), true
	case *networkingv1.NetworkPolicy:
		return identFromMeta("NetworkPolicy", "networking.k8s.io", &v.ObjectMeta), true
	case *policyv1.PodDisruptionBudget:
		return identFromMeta("PodDisruptionBudget", "policy", &v.ObjectMeta), true
	case *storagev1.StorageClass:
		return identFromMeta("StorageClass", "storage.k8s.io", &v.ObjectMeta), true
	case *rbacv1.Role:
		return identFromMeta("Role", "rbac.authorization.k8s.io", &v.ObjectMeta), true
	case *rbacv1.ClusterRole:
		return identFromMeta("ClusterRole", "rbac.authorization.k8s.io", &v.ObjectMeta), true
	case *rbacv1.RoleBinding:
		return identFromMeta("RoleBinding", "rbac.authorization.k8s.io", &v.ObjectMeta), true
	case *rbacv1.ClusterRoleBinding:
		return identFromMeta("ClusterRoleBinding", "rbac.authorization.k8s.io", &v.ObjectMeta), true
	case *unstructured.Unstructured:
		gvk := v.GroupVersionKind()
		return resourceIdentity{
			Kind:        gvk.Kind,
			Group:       gvk.Group,
			Namespace:   v.GetNamespace(),
			Name:        v.GetName(),
			Labels:      v.GetLabels(),
			Annotations: v.GetAnnotations(),
			Owners:      v.GetOwnerReferences(),
		}, true
	}
	return resourceIdentity{}, false
}

func identFromMeta(kind, group string, m *metav1.ObjectMeta) resourceIdentity {
	return resourceIdentity{
		Kind:        kind,
		Group:       group,
		Namespace:   m.Namespace,
		Name:        m.Name,
		Labels:      m.Labels,
		Annotations: m.Annotations,
		Owners:      m.OwnerReferences,
	}
}

// ---------------------------------------------------------------------------
// ManagedBy detection
// ---------------------------------------------------------------------------

// GitOps label/annotation keys — kept in sync with packages/k8s-ui/src/utils/gitops-owner.ts.
const (
	argoTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel        = "argocd.argoproj.io/instance"
	fluxKustomizeNameLabel   = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNSLabel     = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmNameLabel        = "helm.toolkit.fluxcd.io/name"
	fluxHelmNSLabel          = "helm.toolkit.fluxcd.io/namespace"
)

// buildManagedBy returns the ContextRefs describing what manages this
// resource. Precedence (most-specific wins):
//  1. Flux HelmRelease labels
//  2. Flux Kustomization labels
//  3. Argo tracking-id annotation
//  4. Argo instance label
//  5. First owner reference (controller=true preferred)
//
// Only one path emits today — the field is a slice so future taxonomies
// (e.g. dual ArgoCD + Flux) can list multiple managers without a wire change.
func buildManagedBy(ident resourceIdentity) []ContextRef {
	if name, ns, ok := readPair(ident.Labels, fluxHelmNameLabel, fluxHelmNSLabel); ok {
		return []ContextRef{{
			Kind:      "HelmRelease",
			Group:     "helm.toolkit.fluxcd.io",
			Namespace: ns,
			Name:      name,
			Reason:    ReasonOwnerReference,
			Source:    SourceOwnerChain,
		}}
	}
	if name, ns, ok := readPair(ident.Labels, fluxKustomizeNameLabel, fluxKustomizeNSLabel); ok {
		return []ContextRef{{
			Kind:      "Kustomization",
			Group:     "kustomize.toolkit.fluxcd.io",
			Namespace: ns,
			Name:      name,
			Reason:    ReasonOwnerReference,
			Source:    SourceOwnerChain,
		}}
	}
	if id := ident.Annotations[argoTrackingIDAnnotation]; id != "" {
		if ns, name, ok := parseArgoTrackingID(id); ok && name != "" {
			return []ContextRef{{
				Kind:      "Application",
				Group:     "argoproj.io",
				Namespace: ns,
				Name:      name,
				Reason:    ReasonOwnerReference,
				Source:    SourceOwnerChain,
			}}
		}
	}
	if inst := ident.Labels[argoInstanceLabel]; inst != "" {
		// App namespace unknown without tracking-id — emit with empty ns
		// like the UI does; the consumer decides whether to navigate.
		return []ContextRef{{
			Kind:   "Application",
			Group:  "argoproj.io",
			Name:   inst,
			Reason: ReasonOwnerReference,
			Source: SourceOwnerChain,
		}}
	}

	if owner := pickControllerOwner(ident.Owners); owner != nil {
		group := groupFromAPIVersion(owner.APIVersion)
		return []ContextRef{{
			Kind:      owner.Kind,
			Group:     group,
			Namespace: ident.Namespace,
			Name:      owner.Name,
			Reason:    ReasonOwnerReference,
			Source:    SourceOwnerChain,
		}}
	}
	return nil
}

func readPair(m map[string]string, k1, k2 string) (string, string, bool) {
	a := m[k1]
	b := m[k2]
	if a == "" || b == "" {
		return "", "", false
	}
	return a, b, true
}

// parseArgoTrackingID mirrors gitops-owner.ts. Two forms:
//
//	"<appName>:..."                  (legacy, single name)
//	"<appNamespace>_<appName>:..."   (namespaced install)
//
// Returns (ns, name, ok).
func parseArgoTrackingID(value string) (string, string, bool) {
	colon := strings.IndexByte(value, ':')
	if colon < 0 {
		return "", "", false
	}
	head := value[:colon]
	if head == "" {
		return "", "", false
	}
	if sep := strings.IndexByte(head, '_'); sep >= 0 {
		ns := head[:sep]
		name := head[sep+1:]
		if name == "" {
			return "", "", false
		}
		return ns, name, true
	}
	return "", head, true
}

// pickControllerOwner returns the first owner with Controller=true; falls
// back to the first owner if none are marked controller. Returns nil when
// the slice is empty.
func pickControllerOwner(owners []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range owners {
		if owners[i].Controller != nil && *owners[i].Controller {
			return &owners[i]
		}
	}
	if len(owners) > 0 {
		return &owners[0]
	}
	return nil
}

// groupFromAPIVersion extracts the group from "group/version" or "version"
// (core/v1 form). Mirrors schema.ParseGroupVersion without the import.
func groupFromAPIVersion(apiVersion string) string {
	if i := strings.IndexByte(apiVersion, '/'); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Uses (Pod-specific)
// ---------------------------------------------------------------------------

// buildUsesFromPod extracts ConfigMap/Secret/PVC/ServiceAccount references
// from pod.Spec. Returns nil when the pod uses no configuration.
//
// Sources scanned:
//   - Volumes: ConfigMap / Secret / PVC / Projected (configMap + secret entries)
//   - Containers (init + regular): EnvFrom configMapRef/secretRef, Env valueFrom.{configMap,secret}KeyRef
//   - Spec.ServiceAccountName
func buildUsesFromPod(ctx context.Context, pod *corev1.Pod, ac RefAccessChecker, omitted *omittedTracker) *UsesBlock {
	if pod == nil {
		return nil
	}

	cmSet := newRefSet()
	secretSet := newRefSet()
	pvcSet := newRefSet()

	scanVolumes(pod.Spec.Volumes, pod.Namespace, cmSet, secretSet, pvcSet)
	scanContainers(pod.Spec.InitContainers, pod.Namespace, cmSet, secretSet)
	scanContainers(pod.Spec.Containers, pod.Namespace, cmSet, secretSet)

	uses := &UsesBlock{
		ConfigMaps: filterRefs(ctx, ac, cmSet.refs("ConfigMap", "", ReasonEnvVarRef, SourceK8sSpec), "uses.configMaps", omitted),
		Secrets:    filterRefs(ctx, ac, secretSet.refs("Secret", "", ReasonVolumeMount, SourceK8sSpec), "uses.secrets", omitted),
		PVCs:       filterRefs(ctx, ac, pvcSet.refs("PersistentVolumeClaim", "", ReasonClaimRef, SourceK8sSpec), "uses.pvcs", omitted),
	}

	if sa := pod.Spec.ServiceAccountName; sa != "" {
		candidate := &ContextRef{
			Kind:      "ServiceAccount",
			Namespace: pod.Namespace,
			Name:      sa,
			Reason:    ReasonSAName,
			Source:    SourceK8sSpec,
		}
		if checkRef(ctx, ac, candidate) {
			uses.ServiceAccount = candidate
		} else {
			omitted.add("uses.serviceAccount", OmittedRBACDenied)
		}
	}

	if len(uses.ConfigMaps) == 0 && len(uses.Secrets) == 0 && len(uses.PVCs) == 0 && uses.ServiceAccount == nil {
		return nil
	}
	return uses
}

func scanVolumes(vols []corev1.Volume, ns string, cm, secret, pvc *refSet) {
	for _, v := range vols {
		if v.ConfigMap != nil {
			cm.add(v.ConfigMap.Name, ns)
		}
		if v.Secret != nil {
			secret.add(v.Secret.SecretName, ns)
		}
		if v.PersistentVolumeClaim != nil {
			pvc.add(v.PersistentVolumeClaim.ClaimName, ns)
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil {
					cm.add(src.ConfigMap.Name, ns)
				}
				if src.Secret != nil {
					secret.add(src.Secret.Name, ns)
				}
			}
		}
	}
}

func scanContainers(containers []corev1.Container, ns string, cm, secret *refSet) {
	for _, c := range containers {
		for _, ef := range c.EnvFrom {
			if ef.ConfigMapRef != nil {
				cm.add(ef.ConfigMapRef.Name, ns)
			}
			if ef.SecretRef != nil {
				secret.add(ef.SecretRef.Name, ns)
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				cm.add(e.ValueFrom.ConfigMapKeyRef.Name, ns)
			}
			if e.ValueFrom.SecretKeyRef != nil {
				secret.add(e.ValueFrom.SecretKeyRef.Name, ns)
			}
		}
	}
}

// refSet collects (name, namespace) pairs with insertion-order preservation
// for deterministic output. Names with empty namespaces are tolerated (the
// PVC ClaimName can be cluster-scoped only in odd configurations, but we
// pass through whatever the pod spec says).
type refSet struct {
	seen  map[string]bool
	order []nsName
}

type nsName struct {
	Namespace string
	Name      string
}

func newRefSet() *refSet {
	return &refSet{seen: make(map[string]bool)}
}

func (s *refSet) add(name, ns string) {
	if name == "" {
		return
	}
	key := ns + "/" + name
	if s.seen[key] {
		return
	}
	s.seen[key] = true
	s.order = append(s.order, nsName{Namespace: ns, Name: name})
}

// refs returns the accumulated set as ContextRefs sorted by (namespace, name)
// for deterministic golden output.
func (s *refSet) refs(kind, group string, reason RefReason, source RefSource) []ContextRef {
	if len(s.order) == 0 {
		return nil
	}
	out := make([]ContextRef, len(s.order))
	sorted := append([]nsName(nil), s.order...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Namespace != sorted[j].Namespace {
			return sorted[i].Namespace < sorted[j].Namespace
		}
		return sorted[i].Name < sorted[j].Name
	})
	for i, e := range sorted {
		out[i] = ContextRef{
			Kind:      kind,
			Group:     group,
			Namespace: e.Namespace,
			Name:      e.Name,
			Reason:    reason,
			Source:    source,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Topology ref → ContextRef
// ---------------------------------------------------------------------------

// toContextRefs translates a slice of topology.ResourceRef into ContextRefs
// with the given reason+source. Sorted by (kind, namespace, name) for
// determinism — golden tests rely on this ordering.
func toContextRefs(refs []topology.ResourceRef, reason RefReason, source RefSource) []ContextRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]ContextRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, ContextRef{
			Kind:      r.Kind,
			Group:     r.Group,
			Namespace: r.Namespace,
			Name:      r.Name,
			Reason:    reason,
			Source:    source,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ---------------------------------------------------------------------------
// RBAC gating
// ---------------------------------------------------------------------------

// filterRefs applies the access check to each ref. Denied refs are dropped
// and one omitted entry is recorded per field (deduped by the tracker).
// When ac is nil (local-kubeconfig / no auth), every ref passes.
func filterRefs(ctx context.Context, ac RefAccessChecker, refs []ContextRef, fieldPath string, omitted *omittedTracker) []ContextRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]ContextRef, 0, len(refs))
	deniedAny := false
	for _, r := range refs {
		if !checkRef(ctx, ac, &r) {
			deniedAny = true
			continue
		}
		out = append(out, r)
	}
	if deniedAny {
		omitted.add(fieldPath, OmittedRBACDenied)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// checkRef returns true when ac permits a read of (group, kind, namespace).
// Nil ac = permit everything.
func checkRef(ctx context.Context, ac RefAccessChecker, r *ContextRef) bool {
	if ac == nil || r == nil {
		return true
	}
	return ac.CanRead(ctx, r.Group, r.Kind, r.Namespace)
}

// ---------------------------------------------------------------------------
// Policy summary
// ---------------------------------------------------------------------------

// buildPolicySummary rolls up Kyverno findings into the summary block.
// Top findings are picked first by fail > warn > error > pass, then by
// stable input order — capped at policySummaryTopMax.
//
// Tier discrimination: basic emits counts only (Fail/Warn/Pass) for a
// minimal wire footprint; diagnostic adds the Top[] findings. Locked
// in the plan's v1 contract.
const policySummaryTopMax = 3

func buildPolicySummary(findings []KyvernoFinding, tier ContextTier) *PolicySummary {
	var fail, warn, pass int
	for _, f := range findings {
		switch f.Result {
		case "fail":
			fail++
		case "warn":
			warn++
		case "pass":
			pass++
		}
	}

	ks := &KyvernoSummary{
		Fail: fail,
		Warn: warn,
		Pass: pass,
	}

	// Top[] only on diagnostic tier. Basic stays counts-only.
	if tier == TierDiagnostic {
		ordered := append([]KyvernoFinding(nil), findings...)
		sort.SliceStable(ordered, func(i, j int) bool {
			return resultRank(ordered[i].Result) < resultRank(ordered[j].Result)
		})
		if len(ordered) > policySummaryTopMax {
			ordered = ordered[:policySummaryTopMax]
		}
		ks.Top = ordered
	}

	return &PolicySummary{Kyverno: ks}
}

func resultRank(r string) int {
	switch r {
	case "fail":
		return 0
	case "warn":
		return 1
	case "error":
		return 2
	case "pass":
		return 3
	default:
		return 4
	}
}

// ---------------------------------------------------------------------------
// Omitted tracker
// ---------------------------------------------------------------------------

// omittedTracker deduplicates (field, reason) entries so callers don't emit
// "managedBy" / OmittedRBACDenied twice when multiple refs in the same field
// fail. Insertion order is preserved for stable JSON output.
type omittedTracker struct {
	seen  map[string]bool
	items []OmittedField
}

func newOmittedTracker() *omittedTracker {
	return &omittedTracker{seen: make(map[string]bool)}
}

func (t *omittedTracker) add(field string, reason OmittedReason) {
	key := field + "|" + string(reason)
	if t.seen[key] {
		return
	}
	t.seen[key] = true
	t.items = append(t.items, OmittedField{Field: field, Reason: reason})
}

func (t *omittedTracker) collect() []OmittedField {
	if len(t.items) == 0 {
		return nil
	}
	return t.items
}
