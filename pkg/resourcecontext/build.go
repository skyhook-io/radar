package resourcecontext

import (
	"context"
	"sort"

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
	Tier ContextTier

	// AccessChecker gates every emitted ContextRef. nil = no gating (treat
	// as fully authorized — local-kubeconfig / tests).
	AccessChecker RefAccessChecker

	// Topology data sources. When Topology is nil, the topology-derived
	// fields (Exposes, SelectedBy, ScaledBy, ManagedBy, RunsOn,
	// Uses.ServiceAccount) are skipped.
	Topology    *topology.Topology
	Provider    topology.ResourceProvider
	DynamicProv topology.DynamicProvider

	// Relationships is the pre-computed per-resource projection. When non-nil,
	// Build consumes it directly instead of calling
	// topology.GetRelationshipsWithObject — single-resource handlers should
	// leave this nil and let Build compute; bulk/list callers that already
	// loop over relationships per row SHOULD pass it to avoid double work.
	//
	// Topology MUST still be set when Relationships is set — synthesis
	// helpers (e.g. ManagedBy owner walk) read Topology and RelIndex through
	// it.
	Relationships *topology.Relationships

	// RelIndex is the topology inverted-edge index. Pass a shared instance
	// (topology.IndexByResource(topo)) for high-fanout callers; nil is fine
	// for single-resource Build paths — the per-call inline scan is O(E) once.
	RelIndex *topology.RelationshipsIndex

	// Pre-computed summaries — pass-through into the response.
	IssueSummary  *IssueSummary
	AuditSummary  *AuditSummary
	PolicyReports PolicyReportLookup // nil = Kyverno not installed / no findings
}

// PolicyReportLookup is the minimal interface Build needs from the
// PolicyReport index. The concrete index lives in pkg/policyreports.
//
// Build does not import pkg/policyreports directly because callers may
// adapt other policy engines into the same shape.
type PolicyReportLookup interface {
	FindingsFor(group, kind, namespace, name string) []KyvernoFinding
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

	// Topology-derived relationships drive ManagedBy / Exposes / SelectedBy /
	// ScaledBy / RunsOn / Uses.ServiceAccount. T23 made
	// topology.Relationships the canonical projection: server-side
	// SynthesizeManagedBy walks the owner chain + GitOps signals, and the Pod
	// hygiene fields (.ServiceAccount, .Node) are populated from pod.Spec.
	// We do NOT re-walk owner refs here — that would duplicate the topology
	// package's logic and risk drift.
	//
	// Single-resource callers (REST GET, MCP get_resource) leave
	// opts.Relationships nil and let us compute via GetRelationshipsWithObject
	// — passing obj keeps kind/group disambiguation correct for CRDs whose
	// plural collides with a core resource. Bulk callers that already loop
	// over relationships per row pass them in directly.
	rel := opts.Relationships
	if rel == nil && opts.Topology != nil {
		// Resolve the topology-pseudo-kind so cross-group CRDs (Knative
		// serving.knative.dev/Service, CAPI cluster.x-k8s.io/Cluster, …)
		// look up the right node. Using ident.Kind directly would lower-
		// case to "service" and resolve to the core Service node, leaking
		// the wrong resource's relationships into the CRD's resourceContext.
		// The handler-side pre-computation does this same KindForGVK
		// resolution; mirror it here so the fallback path doesn't undo it.
		rel = topology.GetRelationshipsWithObject(
			topology.KindForGVK(ident.Kind, ident.Group), ident.Namespace, ident.Name, obj,
			opts.Topology, opts.Provider, opts.DynamicProv, opts.RelIndex,
		)
	}

	// 1. ManagedBy — prefer Relationships.ManagedBy (server-synthesized when
	// a topology is available; covers GitOps signals + owner-chain walk).
	// Fall back to topology.SynthesizeManagedBy with the obj alone when no
	// topology is provided — that path still detects Argo/Flux/Helm signals
	// from labels and annotations without needing a graph.
	var managedBy []topology.ResourceRef
	if rel != nil && len(rel.ManagedBy) > 0 {
		managedBy = rel.ManagedBy
	} else if rel == nil {
		if m, ok := obj.(metav1.Object); ok {
			managedBy = topology.SynthesizeManagedBy(m, ident.Kind, ident.Namespace, ident.Name, nil, nil, nil)
		}
	}
	if len(managedBy) > 0 {
		rc.ManagedBy = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(managedBy),
			"managedBy", omitted)
	}

	// 2. Topology-derived: Exposes, SelectedBy, ScaledBy
	if rel != nil {
		exposes := make([]topology.ResourceRef, 0, len(rel.Services)+len(rel.Ingresses)+len(rel.Gateways)+len(rel.Routes))
		exposes = append(exposes, rel.Services...)
		exposes = append(exposes, rel.Ingresses...)
		exposes = append(exposes, rel.Gateways...)
		exposes = append(exposes, rel.Routes...)
		rc.Exposes = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(exposes),
			"exposes", omitted)

		selected := make([]topology.ResourceRef, 0, len(rel.PDBs)+len(rel.NetworkPolicies))
		selected = append(selected, rel.PDBs...)
		selected = append(selected, rel.NetworkPolicies...)
		rc.SelectedBy = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(selected),
			"selectedBy", omitted)

		rc.ScaledBy = filterRefs(ctx, opts.AccessChecker,
			toContextRefs(rel.Scalers),
			"scaledBy", omitted)
	}

	// 3. Pod-specific: RunsOn (Node) + Uses (ConfigMap/Secret/PVC/SA).
	//
	// RunsOn and Uses.ServiceAccount come from topology.Relationships when
	// available (T23 populates them from pod.Spec server-side). We still
	// scan pod.Spec.Volumes / .EnvFrom directly for the ConfigMap/Secret/PVC
	// inventory — topology doesn't model those use-edges at the granularity
	// Build needs.
	if pod, ok := obj.(*corev1.Pod); ok {
		rc.Uses = buildUsesFromPod(ctx, pod, opts.AccessChecker, omitted)

		// Prefer rel.ServiceAccount over re-reading pod.Spec — same source,
		// but consolidating through Relationships keeps Build aligned with
		// how MCP/agents consume the field.
		if rc.Uses != nil && rc.Uses.ServiceAccount == nil && rel != nil && rel.ServiceAccount != nil {
			candidate := &ContextRef{
				Kind:      rel.ServiceAccount.Kind,
				Group:     rel.ServiceAccount.Group,
				Namespace: rel.ServiceAccount.Namespace,
				Name:      rel.ServiceAccount.Name,
			}
			if checkRef(ctx, opts.AccessChecker, candidate) {
				rc.Uses.ServiceAccount = candidate
			} else {
				omitted.add("uses.serviceAccount", OmittedRBACDenied)
			}
		}

		// RunsOn: prefer the topology-supplied Node ref. Fall back to
		// pod.Spec.NodeName any time rel.Node is empty — the Node informer
		// may be cold, the node may not yet be in the topology graph, or
		// rel itself may be nil. The previous `else if rel == nil` guard
		// dropped the fallback when topology was built but rel.Node hadn't
		// been populated yet, leaving RunsOn empty even though the Pod
		// spec clearly named a node.
		var nodeName, nodeGroup string
		if rel != nil && rel.Node != nil {
			nodeName = rel.Node.Name
			nodeGroup = rel.Node.Group
		} else {
			nodeName = pod.Spec.NodeName
		}
		if nodeName != "" {
			candidate := &ContextRef{
				Kind:  "Node",
				Group: nodeGroup,
				Name:  nodeName,
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
		findings := opts.PolicyReports.FindingsFor(ident.Group, ident.Kind, ident.Namespace, ident.Name)
		if len(findings) > 0 {
			rc.PolicySummary = buildPolicySummary(findings, opts.Tier)
		}
	}

	rc.Omitted = omitted.collect()
	return rc
}

// ---------------------------------------------------------------------------
// Identity extraction
// ---------------------------------------------------------------------------

// resourceIdentity is the projection of obj that Build needs without holding
// on to the full runtime.Object. The (Kind, Namespace, Name) tuple keys
// topology relationship lookups and summary lookups; Group is retained for
// future use by callers inspecting the identity directly.
type resourceIdentity struct {
	Kind      string
	Group     string
	Namespace string
	Name      string
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
			Kind:      gvk.Kind,
			Group:     gvk.Group,
			Namespace: v.GetNamespace(),
			Name:      v.GetName(),
		}, true
	}
	return resourceIdentity{}, false
}

func identFromMeta(kind, group string, m *metav1.ObjectMeta) resourceIdentity {
	return resourceIdentity{
		Kind:      kind,
		Group:     group,
		Namespace: m.Namespace,
		Name:      m.Name,
	}
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
		ConfigMaps: filterRefs(ctx, ac, cmSet.refs("ConfigMap", ""), "uses.configMaps", omitted),
		Secrets:    filterRefs(ctx, ac, secretSet.refs("Secret", ""), "uses.secrets", omitted),
		PVCs:       filterRefs(ctx, ac, pvcSet.refs("PersistentVolumeClaim", ""), "uses.pvcs", omitted),
	}

	if sa := pod.Spec.ServiceAccountName; sa != "" {
		candidate := &ContextRef{
			Kind:      "ServiceAccount",
			Namespace: pod.Namespace,
			Name:      sa,
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
func (s *refSet) refs(kind, group string) []ContextRef {
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
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Topology ref → ContextRef
// ---------------------------------------------------------------------------

// toContextRefs translates a slice of topology.ResourceRef into ContextRefs.
// Sorted by (kind, namespace, name) for determinism — golden tests rely on
// this ordering.
func toContextRefs(refs []topology.ResourceRef) []ContextRef {
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
