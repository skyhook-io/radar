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
	"k8s.io/apimachinery/pkg/labels"
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

	// Optional kind-specific lookups. ServiceBackends is used only for
	// Service resources to attach realized pod-selection state. The raw
	// Service spec already carries selector/ports; this lookup answers
	// whether that selector currently resolves to ready Pods.
	ServiceBackends ServiceBackendLookup
}

// PolicyReportLookup is the minimal interface Build needs from the
// PolicyReport index. The concrete index lives in pkg/policyreports.
//
// Build does not import pkg/policyreports directly because callers may
// adapt other policy engines into the same shape.
type PolicyReportLookup interface {
	FindingsFor(group, kind, namespace, name string) []KyvernoFinding
}

type ServiceBackendLookup interface {
	PodsForServiceSelector(namespace string, selector labels.Selector) ([]*corev1.Pod, error)
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
	// ManagedBy stays delegated to topology to avoid duplicating its owner-chain
	// and GitOps logic. The direct Owner field below may still fall back to the
	// object's controller OwnerReference when topology is absent or cold.
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

	if rel != nil && rel.Owner != nil {
		candidate := &ContextRef{
			Kind:      rel.Owner.Kind,
			Group:     rel.Owner.Group,
			Namespace: rel.Owner.Namespace,
			Name:      rel.Owner.Name,
		}
		if checkRef(ctx, opts.AccessChecker, candidate) {
			rc.Owner = candidate
		} else {
			omitted.add("owner", OmittedRBACDenied)
		}
	} else if owner := ownerFromObject(obj, ident.Namespace); owner != nil {
		if checkRef(ctx, opts.AccessChecker, owner) {
			rc.Owner = owner
		} else {
			omitted.add("owner", OmittedRBACDenied)
		}
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
		rc.PodSummary = buildPodSummary(pod)

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

	if svc, ok := obj.(*corev1.Service); ok {
		rc.ServiceSummary = buildServiceSummary(ctx, svc, opts.ServiceBackends, opts.AccessChecker, omitted)
	}
	rc.ReferencedBy = buildReferencedBy(ctx, obj, opts.Provider, opts.AccessChecker, omitted)
	if uses := buildUsesFromWorkload(ctx, obj, opts.AccessChecker, omitted); uses != nil {
		rc.Uses = uses
	}
	rc.WorkloadSummary = buildWorkloadSummary(obj)
	rc.IngressSummary = buildIngressSummary(ctx, obj, opts.AccessChecker, omitted)
	rc.NodeSummary = buildNodeSummary(obj)
	rc.PVCSummary = buildPVCSummary(obj)
	rc.JobSummary = buildJobSummary(obj)
	rc.CronJobSummary = buildCronJobSummary(ctx, obj, opts.AccessChecker, omitted)
	rc.StatusSummary = buildStatusSummary(obj)

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

func ownerFromObject(obj runtime.Object, namespace string) *ContextRef {
	m, ok := obj.(metav1.Object)
	if !ok {
		return nil
	}
	owners := m.GetOwnerReferences()
	if len(owners) == 0 {
		return nil
	}
	chosen := owners[0]
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller {
			chosen = owner
			break
		}
	}
	return &ContextRef{
		Kind:      chosen.Kind,
		Group:     groupFromAPIVersion(chosen.APIVersion),
		Namespace: namespace,
		Name:      chosen.Name,
	}
}

func groupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || !strings.Contains(apiVersion, "/") {
		return ""
	}
	return strings.SplitN(apiVersion, "/", 2)[0]
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
	return buildUsesFromPodSpec(ctx, pod.Namespace, pod.Spec, ac, omitted)
}

func buildUsesFromPodSpec(ctx context.Context, namespace string, spec corev1.PodSpec, ac RefAccessChecker, omitted *omittedTracker) *UsesBlock {
	cmSet := newRefSet()
	secretSet := newRefSet()
	pvcSet := newRefSet()

	scanVolumes(spec.Volumes, namespace, cmSet, secretSet, pvcSet)
	scanContainers(spec.InitContainers, namespace, cmSet, secretSet)
	scanContainers(spec.Containers, namespace, cmSet, secretSet)

	uses := &UsesBlock{
		ConfigMaps: filterRefs(ctx, ac, cmSet.refs("ConfigMap", ""), "uses.configMaps", omitted),
		Secrets:    filterRefs(ctx, ac, secretSet.refs("Secret", ""), "uses.secrets", omitted),
		PVCs:       filterRefs(ctx, ac, pvcSet.refs("PersistentVolumeClaim", ""), "uses.pvcs", omitted),
	}

	if sa := spec.ServiceAccountName; sa != "" {
		candidate := &ContextRef{
			Kind:      "ServiceAccount",
			Namespace: namespace,
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

func buildUsesFromWorkload(ctx context.Context, obj runtime.Object, ac RefAccessChecker, omitted *omittedTracker) *UsesBlock {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.Template.Spec, ac, omitted)
	case *appsv1.StatefulSet:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.Template.Spec, ac, omitted)
	case *appsv1.DaemonSet:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.Template.Spec, ac, omitted)
	case *appsv1.ReplicaSet:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.Template.Spec, ac, omitted)
	case *batchv1.Job:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.Template.Spec, ac, omitted)
	case *batchv1.CronJob:
		return buildUsesFromPodSpec(ctx, v.Namespace, v.Spec.JobTemplate.Spec.Template.Spec, ac, omitted)
	default:
		return nil
	}
}

const (
	maxReferencedByItems       = 20
	maxReferencedByPathsPerRef = 8
)

func buildReferencedBy(ctx context.Context, obj runtime.Object, provider topology.ResourceProvider, ac RefAccessChecker, omitted *omittedTracker) *ReferencedBy {
	if provider == nil {
		return nil
	}

	ident, ok := identityOf(obj)
	if !ok || ident.Namespace == "" {
		return nil
	}

	var target refTarget
	switch ident.Kind {
	case "ConfigMap":
		target = refTarget{kind: "ConfigMap", namespace: ident.Namespace, name: ident.Name}
	case "Secret":
		target = refTarget{kind: "Secret", namespace: ident.Namespace, name: ident.Name}
	default:
		return nil
	}

	var refs []ReferenceUse
	appendRef := func(ref ReferenceUse) {
		if len(ref.Paths) == 0 || ref.Namespace != target.namespace {
			return
		}
		refs = append(refs, ref)
	}

	if deployments, _ := provider.Deployments(); deployments != nil {
		for _, d := range deployments {
			if d == nil || d.Namespace != target.namespace {
				continue
			}
			appendRef(referenceUseForPodSpec("Deployment", "apps", d.Namespace, d.Name, d.Spec.Template.Spec, "spec.template.spec", target))
		}
	}
	if statefulSets, _ := provider.StatefulSets(); statefulSets != nil {
		for _, s := range statefulSets {
			if s == nil || s.Namespace != target.namespace {
				continue
			}
			appendRef(referenceUseForPodSpec("StatefulSet", "apps", s.Namespace, s.Name, s.Spec.Template.Spec, "spec.template.spec", target))
		}
	}
	if daemonSets, _ := provider.DaemonSets(); daemonSets != nil {
		for _, d := range daemonSets {
			if d == nil || d.Namespace != target.namespace {
				continue
			}
			appendRef(referenceUseForPodSpec("DaemonSet", "apps", d.Namespace, d.Name, d.Spec.Template.Spec, "spec.template.spec", target))
		}
	}
	if jobs, _ := provider.Jobs(); jobs != nil {
		for _, j := range jobs {
			if j == nil || j.Namespace != target.namespace {
				continue
			}
			appendRef(referenceUseForPodSpec("Job", "batch", j.Namespace, j.Name, j.Spec.Template.Spec, "spec.template.spec", target))
		}
	}
	if cronJobs, _ := provider.CronJobs(); cronJobs != nil {
		for _, c := range cronJobs {
			if c == nil || c.Namespace != target.namespace {
				continue
			}
			appendRef(referenceUseForPodSpec("CronJob", "batch", c.Namespace, c.Name, c.Spec.JobTemplate.Spec.Template.Spec, "spec.jobTemplate.spec.template.spec", target))
		}
	}
	if pods, _ := provider.Pods(); pods != nil {
		for _, p := range pods {
			if p == nil || p.Namespace != target.namespace || hasControllerOwner(p.OwnerReferences) {
				continue
			}
			appendRef(referenceUseForPodSpec("Pod", "", p.Namespace, p.Name, p.Spec, "spec", target))
		}
	}

	if len(refs) == 0 {
		return nil
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].Namespace != refs[j].Namespace {
			return refs[i].Namespace < refs[j].Namespace
		}
		return refs[i].Name < refs[j].Name
	})

	// Count visible refs only — leaking the pre-RBAC total would let a
	// caller infer the count of consuming resources they can't otherwise
	// see (e.g. ConfigMap visible to caller but consuming workloads in
	// hidden namespaces). The omitted=[referencedBy:rbac_denied] marker
	// emitted above signals that some refs were filtered without
	// disclosing how many.
	visible := 0
	filtered := make([]ReferenceUse, 0, minInt(len(refs), maxReferencedByItems))
	for i := range refs {
		ref := refs[i]
		if len(ref.Paths) > maxReferencedByPathsPerRef {
			ref.Paths = append([]string(nil), ref.Paths[:maxReferencedByPathsPerRef]...)
		}
		candidate := &ContextRef{Kind: ref.Kind, Group: ref.Group, Namespace: ref.Namespace, Name: ref.Name}
		if !checkRef(ctx, ac, candidate) {
			omitted.add("referencedBy", OmittedRBACDenied)
			continue
		}
		visible++
		if len(filtered) < maxReferencedByItems {
			filtered = append(filtered, ref)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &ReferencedBy{
		Total:     visible,
		Items:     filtered,
		Truncated: visible > len(filtered),
	}
}

type refTarget struct {
	kind      string
	namespace string
	name      string
}

func referenceUseForPodSpec(kind, group, namespace, name string, spec corev1.PodSpec, pathPrefix string, target refTarget) ReferenceUse {
	paths := podSpecReferencePaths(spec, pathPrefix, target)
	return ReferenceUse{
		Kind:      kind,
		Group:     group,
		Namespace: namespace,
		Name:      name,
		Paths:     paths,
	}
}

func podSpecReferencePaths(spec corev1.PodSpec, pathPrefix string, target refTarget) []string {
	pathSet := map[string]struct{}{}
	add := func(path string) {
		pathSet[path] = struct{}{}
	}

	for _, v := range spec.Volumes {
		if target.kind == "ConfigMap" && v.ConfigMap != nil && v.ConfigMap.Name == target.name {
			add(pathPrefix + ".volumes[].configMap.name")
		}
		if target.kind == "Secret" && v.Secret != nil && v.Secret.SecretName == target.name {
			add(pathPrefix + ".volumes[].secret.secretName")
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if target.kind == "ConfigMap" && src.ConfigMap != nil && src.ConfigMap.Name == target.name {
					add(pathPrefix + ".volumes[].projected.sources[].configMap.name")
				}
				if target.kind == "Secret" && src.Secret != nil && src.Secret.Name == target.name {
					add(pathPrefix + ".volumes[].projected.sources[].secret.name")
				}
			}
		}
	}
	if target.kind == "Secret" {
		for _, pullSecret := range spec.ImagePullSecrets {
			if pullSecret.Name == target.name {
				add(pathPrefix + ".imagePullSecrets[].name")
			}
		}
	}

	scanContainerReferencePaths(spec.InitContainers, pathPrefix, target, add)
	scanContainerReferencePaths(spec.Containers, pathPrefix, target, add)

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func scanContainerReferencePaths(containers []corev1.Container, pathPrefix string, target refTarget, add func(string)) {
	for _, c := range containers {
		for _, ef := range c.EnvFrom {
			if target.kind == "ConfigMap" && ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == target.name {
				add(pathPrefix + ".containers[].envFrom[].configMapRef.name")
			}
			if target.kind == "Secret" && ef.SecretRef != nil && ef.SecretRef.Name == target.name {
				add(pathPrefix + ".containers[].envFrom[].secretRef.name")
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if target.kind == "ConfigMap" && e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == target.name {
				add(pathPrefix + ".containers[].env[].valueFrom.configMapKeyRef.name")
			}
			if target.kind == "Secret" && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == target.name {
				add(pathPrefix + ".containers[].env[].valueFrom.secretKeyRef.name")
			}
		}
	}
}

func hasControllerOwner(refs []metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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

const maxServicePodRefs = 10

func buildServiceSummary(ctx context.Context, svc *corev1.Service, lookup ServiceBackendLookup, ac RefAccessChecker, omitted *omittedTracker) *ServiceSummary {
	if svc == nil {
		return nil
	}
	out := &ServiceSummary{}

	if len(svc.Spec.Selector) == 0 {
		if svc.Spec.Type != corev1.ServiceTypeExternalName {
			out.Warnings = append(out.Warnings, ServiceWarningNoSelector)
		}
		return out
	}
	if lookup == nil {
		return nil
	}

	selector := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
	pods, err := lookup.PodsForServiceSelector(svc.Namespace, selector)
	if err != nil {
		return nil
	}

	// Total counts visible refs only — leaking len(pods) would let a
	// caller with Service access infer the pod count even when RBAC
	// hides the individual Pods. Mirrors ReferencedBy.Total semantics.
	sel := &PodSelectionSummary{}
	for _, pod := range pods {
		ref := ContextRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
		if !checkRef(ctx, ac, &ref) {
			omitted.add("serviceSummary.selectedPods", OmittedRBACDenied)
			continue
		}
		sel.Total++
		if isPodReady(pod) {
			sel.Ready++
			appendBoundedPodRef(&sel.ReadyPods, ref, sel)
		} else {
			sel.NotReady++
			appendBoundedPodRef(&sel.NotReadyPods, ref, sel)
		}
	}

	if sel.Total == 0 {
		out.Warnings = append(out.Warnings, ServiceWarningNoSelectedPods)
	} else if sel.Ready == 0 {
		out.Warnings = append(out.Warnings, ServiceWarningNoReadyPods)
	}
	out.SelectedPods = sel
	return out
}

func appendBoundedPodRef(dst *[]ContextRef, ref ContextRef, sel *PodSelectionSummary) {
	if len(*dst) >= maxServicePodRefs {
		sel.Truncated = true
		return
	}
	*dst = append(*dst, ref)
}

func isPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

const maxSummaryItems = 12

func buildPodSummary(pod *corev1.Pod) *PodSummary {
	if pod == nil {
		return nil
	}
	out := &PodSummary{
		Phase: string(pod.Status.Phase),
		Ready: isPodReady(pod),
	}
	statuses := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	if len(statuses) > maxSummaryItems {
		statuses = statuses[:maxSummaryItems]
	}
	for _, st := range statuses {
		out.RestartCount += st.RestartCount
		out.Containers = append(out.Containers, containerStateSummary(st))
	}
	return out
}

func containerStateSummary(st corev1.ContainerStatus) ContainerStateSummary {
	out := ContainerStateSummary{
		Name:         st.Name,
		Ready:        st.Ready,
		RestartCount: st.RestartCount,
	}
	switch {
	case st.State.Waiting != nil:
		out.State = "waiting"
		out.Reason = st.State.Waiting.Reason
	case st.State.Running != nil:
		out.State = "running"
	case st.State.Terminated != nil:
		out.State = "terminated"
		out.Reason = st.State.Terminated.Reason
	}
	if st.LastTerminationState.Terminated != nil {
		out.LastTerminationReason = st.LastTerminationState.Terminated.Reason
	}
	return out
}

func buildWorkloadSummary(obj runtime.Object) *WorkloadSummary {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		return &WorkloadSummary{Replicas: &ReplicaSummary{
			Desired:     replicasOrZero(v.Spec.Replicas),
			Ready:       v.Status.ReadyReplicas,
			Available:   v.Status.AvailableReplicas,
			Updated:     v.Status.UpdatedReplicas,
			Unavailable: v.Status.UnavailableReplicas,
		}}
	case *appsv1.StatefulSet:
		return &WorkloadSummary{Replicas: &ReplicaSummary{
			Desired:   replicasOrZero(v.Spec.Replicas),
			Ready:     v.Status.ReadyReplicas,
			Available: v.Status.AvailableReplicas,
			Updated:   v.Status.UpdatedReplicas,
		}}
	case *appsv1.DaemonSet:
		return &WorkloadSummary{Replicas: &ReplicaSummary{
			Desired:     v.Status.DesiredNumberScheduled,
			Ready:       v.Status.NumberReady,
			Available:   v.Status.NumberAvailable,
			Updated:     v.Status.UpdatedNumberScheduled,
			Unavailable: v.Status.NumberUnavailable,
		}}
	case *appsv1.ReplicaSet:
		return &WorkloadSummary{Replicas: &ReplicaSummary{
			Desired:     replicasOrZero(v.Spec.Replicas),
			Ready:       v.Status.ReadyReplicas,
			Available:   v.Status.AvailableReplicas,
			Unavailable: maxInt32(0, v.Status.Replicas-v.Status.AvailableReplicas),
		}}
	default:
		return nil
	}
}

func buildIngressSummary(ctx context.Context, obj runtime.Object, ac RefAccessChecker, omitted *omittedTracker) *IngressSummary {
	ing, ok := obj.(*networkingv1.Ingress)
	if !ok || ing == nil {
		return nil
	}
	out := &IngressSummary{}
	if ing.Spec.IngressClassName != nil {
		out.Class = *ing.Spec.IngressClassName
	} else if ing.Annotations != nil {
		out.Class = ing.Annotations["kubernetes.io/ingress.class"]
	}
	for _, addr := range ing.Status.LoadBalancer.Ingress {
		if addr.IP != "" {
			out.Addresses = append(out.Addresses, addr.IP)
		} else if addr.Hostname != "" {
			out.Addresses = append(out.Addresses, addr.Hostname)
		}
	}
	if len(out.Addresses) == 0 {
		out.Warnings = append(out.Warnings, IngressWarningNoAddress)
	}
	if out.Class == "" {
		out.Warnings = append(out.Warnings, IngressWarningNoClass)
	}
	if len(ing.Spec.Rules) == 0 {
		out.Warnings = append(out.Warnings, IngressWarningNoRules)
	}

	svcSet := newRefSet()
	addIngressBackendService(svcSet, ing.Namespace, ing.Spec.DefaultBackend)
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			addIngressBackendService(svcSet, ing.Namespace, &path.Backend)
		}
	}
	out.BackendServices = filterRefs(ctx, ac, svcSet.refs("Service", ""), "ingressSummary.backendServices", omitted)

	secretSet := newRefSet()
	for _, tls := range ing.Spec.TLS {
		secretSet.add(tls.SecretName, ing.Namespace)
	}
	out.TLSSecrets = filterRefs(ctx, ac, secretSet.refs("Secret", ""), "ingressSummary.tlsSecrets", omitted)

	if out.Class == "" && len(out.Addresses) == 0 && len(out.BackendServices) == 0 && len(out.TLSSecrets) == 0 && len(out.Warnings) == 0 {
		return nil
	}
	return out
}

func addIngressBackendService(dst *refSet, namespace string, backend *networkingv1.IngressBackend) {
	if backend == nil || backend.Service == nil {
		return
	}
	dst.add(backend.Service.Name, namespace)
}

func buildNodeSummary(obj runtime.Object) *NodeSummary {
	node, ok := obj.(*corev1.Node)
	if !ok || node == nil {
		return nil
	}
	out := &NodeSummary{
		Unschedulable: node.Spec.Unschedulable,
		Capacity:      compactResourceList(node.Status.Capacity),
		Allocatable:   compactResourceList(node.Status.Allocatable),
	}
	if out.Unschedulable {
		out.Warnings = append(out.Warnings, NodeWarningUnschedulable)
	}
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeReady:
			out.ReadyStatus = string(cond.Status)
			if cond.Status != corev1.ConditionTrue {
				out.Warnings = append(out.Warnings, NodeWarningNotReady)
			}
		case corev1.NodeDiskPressure:
			if cond.Status == corev1.ConditionTrue {
				out.Warnings = append(out.Warnings, NodeWarningDiskPressure)
			}
		case corev1.NodeMemoryPressure:
			if cond.Status == corev1.ConditionTrue {
				out.Warnings = append(out.Warnings, NodeWarningMemoryPressure)
			}
		case corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				out.Warnings = append(out.Warnings, NodeWarningPIDPressure)
			}
		case corev1.NodeNetworkUnavailable:
			if cond.Status == corev1.ConditionTrue {
				out.Warnings = append(out.Warnings, NodeWarningNetworkUnavailable)
			}
		}
	}
	for _, taint := range node.Spec.Taints {
		out.Taints = append(out.Taints, TaintSummary{
			Key:    taint.Key,
			Value:  taint.Value,
			Effect: string(taint.Effect),
		})
	}
	return out
}

func compactResourceList(resources corev1.ResourceList) map[string]string {
	if len(resources) == 0 {
		return nil
	}
	out := make(map[string]string, 4)
	for _, name := range []corev1.ResourceName{
		corev1.ResourceCPU,
		corev1.ResourceMemory,
		corev1.ResourcePods,
		corev1.ResourceEphemeralStorage,
	} {
		if qty, ok := resources[name]; ok {
			out[string(name)] = qty.String()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildPVCSummary(obj runtime.Object) *PVCSummary {
	pvc, ok := obj.(*corev1.PersistentVolumeClaim)
	if !ok || pvc == nil {
		return nil
	}
	out := &PVCSummary{
		Phase:            string(pvc.Status.Phase),
		StorageClassName: valueOrEmpty(pvc.Spec.StorageClassName),
		VolumeName:       pvc.Spec.VolumeName,
		VolumeMode:       string(valueOrZero(pvc.Spec.VolumeMode)),
	}
	if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		out.RequestedStorage = req.String()
	}
	if cap, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		out.CapacityStorage = cap.String()
	}
	for _, mode := range pvc.Spec.AccessModes {
		out.AccessModes = append(out.AccessModes, string(mode))
	}
	if pvc.Annotations != nil {
		out.Provisioner = pvc.Annotations["volume.kubernetes.io/storage-provisioner"]
		out.SelectedNode = pvc.Annotations["volume.kubernetes.io/selected-node"]
		out.BindCompleted = pvc.Annotations["pv.kubernetes.io/bind-completed"]
	}
	switch pvc.Status.Phase {
	case corev1.ClaimPending:
		out.Warnings = append(out.Warnings, PVCWarningPending)
	case corev1.ClaimLost:
		out.Warnings = append(out.Warnings, PVCWarningLost)
	}
	return out
}

func buildJobSummary(obj runtime.Object) *JobSummary {
	job, ok := obj.(*batchv1.Job)
	if !ok || job == nil {
		return nil
	}
	out := &JobSummary{
		Active:       job.Status.Active,
		Succeeded:    job.Status.Succeeded,
		Failed:       job.Status.Failed,
		Completions:  int32OrDefault(job.Spec.Completions, 1),
		Parallelism:  int32OrDefault(job.Spec.Parallelism, 1),
		BackoffLimit: int32OrDefault(job.Spec.BackoffLimit, 6),
		Suspended:    boolOrFalse(job.Spec.Suspend),
	}
	return out
}

func buildCronJobSummary(ctx context.Context, obj runtime.Object, ac RefAccessChecker, omitted *omittedTracker) *CronJobSummary {
	cj, ok := obj.(*batchv1.CronJob)
	if !ok || cj == nil {
		return nil
	}
	out := &CronJobSummary{
		Schedule:  cj.Spec.Schedule,
		Suspended: boolOrFalse(cj.Spec.Suspend),
	}
	if cj.Status.LastScheduleTime != nil {
		out.LastScheduleTime = cj.Status.LastScheduleTime.Format("2006-01-02T15:04:05Z07:00")
	}
	if cj.Status.LastSuccessfulTime != nil {
		out.LastSuccessfulTime = cj.Status.LastSuccessfulTime.Format("2006-01-02T15:04:05Z07:00")
	}
	active := make([]ContextRef, 0, len(cj.Status.Active))
	for _, ref := range cj.Status.Active {
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		active = append(active, ContextRef{
			Kind:      ref.Kind,
			Group:     groupFromAPIVersion(ref.APIVersion),
			Namespace: cj.Namespace,
			Name:      ref.Name,
		})
	}
	out.ActiveJobs = filterRefs(ctx, ac, active, "cronJobSummary.activeJobs", omitted)
	return out
}

func replicasOrZero(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func int32OrDefault(p *int32, fallback int32) int32 {
	if p == nil {
		return fallback
	}
	return *p
}

func boolOrFalse(p *bool) bool {
	return p != nil && *p
}

func valueOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func valueOrZero[T ~string](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func buildStatusSummary(obj runtime.Object) *StatusSummary {
	if obj == nil {
		return nil
	}
	u, ok := objectToMap(obj)
	if !ok {
		return nil
	}
	status, ok, _ := unstructured.NestedMap(u, "status")
	if !ok {
		return nil
	}
	out := &StatusSummary{}
	if phase, ok, _ := unstructured.NestedString(status, "phase"); ok {
		out.Phase = phase
	}
	if conditions, ok, _ := unstructured.NestedSlice(status, "conditions"); ok {
		if len(conditions) > maxSummaryItems {
			conditions = conditions[:maxSummaryItems]
		}
		for _, item := range conditions {
			cond, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			summary := ConditionSummary{
				Type:               stringField(cond, "type"),
				Status:             stringField(cond, "status"),
				Reason:             stringField(cond, "reason"),
				Message:            truncateRunes(stringField(cond, "message"), 300),
				LastTransitionTime: stringField(cond, "lastTransitionTime"),
			}
			if summary.Type == "" && summary.Status == "" {
				continue
			}
			out.Conditions = append(out.Conditions, summary)
		}
	}
	if out.Phase == "" && len(out.Conditions) == 0 {
		return nil
	}
	return out
}

func objectToMap(obj runtime.Object) (map[string]interface{}, bool) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.Object, true
	}
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 || len(s) == 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
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
