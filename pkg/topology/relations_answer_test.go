package topology

import (
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type projectionSession struct {
	ownership.ReadSession
	objects map[resourceid.Ref]runtime.Object
	reads   []resourceid.Ref
	gaps    map[resourceid.GroupKind]ownership.Gap
}

func projectionRef(obj runtime.Object) resourceid.Ref {
	m, _ := apimeta.Accessor(obj)
	gvk := obj.GetObjectKind().GroupVersionKind()
	return resourceid.NewRef(gvk.Group, gvk.Kind, m.GetNamespace(), m.GetName())
}
func projectionObject(obj runtime.Object, group, kind string) runtime.Object {
	obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: "v1", Kind: kind})
	return obj
}

// recordMembers gives a GitOps manager the member list its status records,
// read as production reads it. The entries are placed by placeMembers once
// the fixture's catalog is known.
func recordMembers(o *ownershiptest.Object, u *unstructured.Unstructured) (gitops.MemberList, bool) {
	var list gitops.MemberList
	var ok, here, elsewhere bool
	switch o.Ref.GroupKind() {
	case ownership.ArgoApplication:
		list, ok = gitops.ArgoMembers(u)
		name, server, _ := gitops.ArgoComparedDestination(u)
		here, elsewhere = gitops.IsInClusterArgoDestination(name, server), gitops.ArgoAppliesElsewhere(u)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		list, ok = gitops.FluxMembers(u)
		here = gitops.FluxTargetsLocalCluster(u)
		elsewhere = !here
	default:
		return gitops.MemberList{}, false
	}
	if !ok {
		if elsewhere {
			o.Membership = ownership.MembersRemote
		}
		return gitops.MemberList{}, false
	}
	o.Membership = ownership.MembersRecorded
	if !here {
		// A remote manager's list names another cluster's objects.
		o.Membership = ownership.MembersRemote
	}
	o.DestinationNamespace = list.DestinationNamespace
	return list, true
}

// placeMembers names the object each entry means, as the cache source does:
// by its kind's scope where the catalog knows it, and as recorded otherwise.
func placeMembers(o *ownershiptest.Object, list gitops.MemberList, catalog *ownership.Catalog) {
	for _, m := range list.Entries {
		ref := resourceid.NewRef(m.Group, m.Kind, m.Namespace, m.Name)
		if namespaced, known := catalog.Scope(ref.GroupKind()); known {
			ref.Namespace = gitops.MemberNamespace(namespaced, m.Namespace, list.DestinationNamespace)
		}
		o.Members = append(o.Members, ref)
	}
}

func newProjectionSession(t testing.TB, objects ...runtime.Object) *projectionSession {
	t.Helper()
	return projectionSessionOf(t, false, objects)
}

// newRecordingProjectionSession is newProjectionSession with each GitOps
// manager's recorded member list, read and placed as the cache source does.
func newRecordingProjectionSession(t testing.TB, objects ...runtime.Object) *projectionSession {
	t.Helper()
	return projectionSessionOf(t, true, objects)
}

// projectionSessionOf serves each object's kind at the object's version,
// beside served, and with record gives GitOps managers their recorded
// members.
func projectionSessionOf(t testing.TB, record bool, objects []runtime.Object, served ...ownership.ServedResource) *projectionSession {
	t.Helper()
	w := ownershiptest.World{Resources: served}
	s := &projectionSession{objects: map[resourceid.Ref]runtime.Object{}}
	seen := map[resourceid.GroupKind]bool{}
	recorded := map[int]gitops.MemberList{}
	for _, obj := range objects {
		ref := projectionRef(obj)
		m, _ := apimeta.Accessor(obj)
		s.objects[ref] = obj
		o := ownershiptest.Object{Ref: ref, UID: m.GetUID(), APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(), ResourceVersion: m.GetResourceVersion(), OwnerReferences: m.GetOwnerReferences(), Labels: m.GetLabels(), Annotations: m.GetAnnotations()}
		if u, ok := obj.(*unstructured.Unstructured); ok && record {
			if list, ok := recordMembers(&o, u); ok {
				recorded[len(w.Objects)] = list
			}
		}
		w.Objects = append(w.Objects, o)
		if !seen[ref.GroupKind()] {
			version := obj.GetObjectKind().GroupVersionKind().Version
			if version == "" {
				version = "v1"
			}
			w.Resources = append(w.Resources, ownership.ServedResource{Group: ref.Group, Version: version, Kind: ref.Kind, Resource: ref.Kind + "s", Namespaced: ref.Namespace != "", Verbs: []string{"get", "list", "watch", "delete"}})
			seen[ref.GroupKind()] = true
		}
	}
	catalog := ownership.NewCatalog(w.Resources, true)
	for i, list := range recorded {
		placeMembers(&w.Objects[i], list, catalog)
	}
	var err error
	s.ReadSession, err = ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (s *projectionSession) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	s.reads = append(s.reads, ref)
	if gap, ok := s.gaps[ref.GroupKind()]; ok && (gap.Namespace == "" || gap.Namespace == ref.Namespace) {
		return nil, ownership.ObjectRead{State: ownership.Unavailable, Gap: gap}
	}
	if obj := s.objects[ref]; obj != nil {
		m, _ := apimeta.Accessor(obj)
		return obj, ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(ref, m)}
	}
	return nil, ownership.ObjectRead{State: ownership.Absent}
}
func (s *projectionSession) Object(ref resourceid.Ref) ownership.ObjectRead {
	_, read := s.Borrow(ref)
	return read
}
func (s *projectionSession) Indexed(gk resourceid.GroupKind, _ string, _ []string, scope ownership.SearchScope) relations.IndexedRead {
	var out relations.IndexedRead
	gap, incomplete := s.gaps[gk]
	if incomplete {
		out.Gaps = []ownership.Gap{gap}
	}
	for ref, obj := range s.objects {
		if incomplete && (gap.Namespace == "" || gap.Namespace == ref.Namespace) {
			continue
		}
		if ref.GroupKind() == gk && (ref.Namespace == "" && scope.IncludeClusterScoped || ref.Namespace != "" && (scope.AllNamespaces || slices.Contains(scope.Namespaces, ref.Namespace))) {
			out.Objects = append(out.Objects, relations.IndexedObject{Ref: ref, Object: obj})
		}
	}
	slices.SortFunc(out.Objects, func(a, b relations.IndexedObject) int {
		if a.Ref.Key() < b.Ref.Key() {
			return -1
		}
		return 1
	})
	return out
}

type projectionAccess struct {
	denied     []string
	namespaces []string
}

func (a projectionAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if slices.Contains(a.denied, gk.Kind) || ns != "" && a.namespaces != nil && !slices.Contains(a.namespaces, ns) {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (a projectionAccess) Namespaces() ([]string, bool) { return a.namespaces, a.namespaces == nil }
func projectionAnswer(t testing.TB, s *projectionSession, obj runtime.Object, emit, traverse ownership.Access) *Relationships {
	t.Helper()
	captured, ok := relations.CaptureSubject(s, projectionRef(obj), obj)
	if !ok {
		t.Fatal("capture failed")
	}
	rel, _ := RelationsAnswer(captured, emit, traverse, ownership.NewView(captured, emit), projectionRef(obj), obj, nil, nil)
	return rel
}
func projectionCR(group, kind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: fields}
	u.SetAPIVersion(group + "/v1")
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestRelationsAnswerDirectAccess(t *testing.T) {
	claim := "claim"
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod"}, Spec: corev1.PodSpec{ServiceAccountName: "sa", NodeName: "node", ResourceClaims: []corev1.PodResourceClaim{{Name: "claim", ResourceClaimName: &claim}}}}, "", "Pod")
	s := newProjectionSession(t, pod)
	denied := projectionAccess{denied: []string{"ServiceAccount", "Node", "ResourceClaim"}}
	rel := projectionAnswer(t, s, pod, denied, ownership.Unrestricted())
	// The Pod's own spec names its ServiceAccount, its Node and its
	// ResourceClaim, so they are listed, marked.
	if rel.ServiceAccount == nil || *rel.ServiceAccount != (ResourceRef{Kind: "ServiceAccount", Namespace: "shop", Name: "sa", State: ResourceStateDenied}) || rel.Node == nil || *rel.Node != (ResourceRef{Kind: "Node", Name: "node", State: ResourceStateDenied}) ||
		!slices.Equal(rel.ResourceClaims, []ResourceRef{{Kind: "ResourceClaim", Group: "resource.k8s.io", Namespace: "shop", Name: "claim", State: ResourceStateDenied}}) {
		t.Fatalf("hidden spec references: %+v", rel)
	}
	for _, read := range s.reads {
		if slices.Contains(denied.denied, read.Kind) {
			t.Fatalf("read before access: %v", read)
		}
	}
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, "", "Node")
	visible := pod.(*corev1.Pod).DeepCopy()
	visible.Name = "visible"
	hidden := visible.DeepCopy()
	hidden.Namespace = "hidden"
	s = newProjectionSession(t, node, visible, hidden)
	rel = projectionAnswer(t, s, node, projectionAccess{namespaces: []string{"shop"}}, ownership.Unrestricted())
	if len(rel.Pods) != 1 || rel.Pods[0].Namespace != "shop" {
		t.Fatalf("node Pods: %+v", rel.Pods)
	}
	class := "fast"
	pvc := projectionObject(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "disk"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv", StorageClassName: &class}}, "", "PersistentVolumeClaim")
	s = newProjectionSession(t, pvc)
	rel = projectionAnswer(t, s, pvc, projectionAccess{denied: []string{"PersistentVolume", "StorageClass"}}, ownership.Unrestricted())
	if len(rel.StorageRefs)+len(rel.ConfigRefs) > 0 {
		t.Fatalf("hidden storage: %+v", rel)
	}
}

// podChecks counts the namespaces whose Pods access is asked about.
type podChecks struct {
	projectionAccess
	asked map[string]int
}

func (a *podChecks) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if gk == (resourceid.GroupKind{Kind: "Pod"}) {
		a.asked[ns]++
	}
	return a.projectionAccess.CanRead(gk, ns)
}

// A Node's Pods are authorized once they match the Node, so a Pod on another
// node costs no access check, which with exact checks is a review per
// namespace.
func TestRelationsAnswerChecksOnlyTheNodesPods(t *testing.T) {
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, "", "Node")
	here := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "here"}, Spec: corev1.PodSpec{NodeName: "node"}}, "", "Pod")
	elsewhere := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "far", Name: "elsewhere"}, Spec: corev1.PodSpec{NodeName: "other"}}, "", "Pod")
	access := &podChecks{projectionAccess: projectionAccess{namespaces: []string{"shop", "far"}}, asked: map[string]int{}}
	rel := projectionAnswer(t, newProjectionSession(t, node, here, elsewhere), node, access, ownership.Unrestricted())
	if len(rel.Pods) != 1 || rel.Pods[0].Name != "here" {
		t.Fatalf("node Pods: %+v", rel.Pods)
	}
	if access.asked["far"] > 0 {
		t.Errorf("checked access to Pods in far, which holds none on the node: %v", access.asked)
	}
}

func TestRelationsAnswerSpecialAccess(t *testing.T) {
	ksvc := projectionCR("serving.knative.dev", "Service", "shop", "web", map[string]any{"status": map[string]any{"traffic": []any{map[string]any{"revisionName": "web-1"}}}})
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-private"}}, "", "Service")
	s := newProjectionSession(t, ksvc, svc)
	// The subject remains readable; only the core Service is refused.
	emit := projectionCoreServiceDenied{Access: ownership.Unrestricted()}
	rel := projectionAnswer(t, s, ksvc, emit, ownership.Unrestricted())
	if len(rel.Backends) > 0 {
		t.Fatalf("hidden Knative service: %+v", rel)
	}
	for _, r := range s.reads {
		if r.Group == "" && r.Kind == "Service" {
			t.Fatalf("read hidden core Service: %v", r)
		}
	}
	composite := projectionCR("example.org", "Widget", "shop", "widget", map[string]any{"spec": map[string]any{"resourceRefs": []any{map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "name": "bucket"}}}})
	composite.SetLabels(map[string]string{"crossplane.io/composite": "widget"})
	bucket := projectionCR("example.org", "Bucket", "", "bucket", map[string]any{})
	s = newProjectionSession(t, composite, bucket)
	rel = projectionAnswer(t, s, composite, projectionAccess{denied: []string{"Bucket"}}, ownership.Unrestricted())
	if len(rel.Children)+len(rel.Manages) > 0 {
		t.Fatalf("hidden Crossplane target: %+v", rel)
	}
	for _, r := range s.reads {
		if r.Kind == "Bucket" {
			t.Fatalf("read hidden Bucket: %v", r)
		}
	}
}

type projectionCoreServiceDenied struct{ ownership.Access }

func (a projectionCoreServiceDenied) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if gk == (resourceid.GroupKind{Kind: "Service"}) {
		return ownership.Denied
	}
	return a.Access.CanRead(gk, ns)
}

func TestRelationsAnswerNetworkingTraverse(t *testing.T) {
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	ingress := projectionObject(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web"}}}}, "networking.k8s.io", "Ingress")
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "web", map[string]any{"spec": map[string]any{"parentRefs": []any{map[string]any{"name": "front"}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web"}}}}}})
	gateway := projectionCR("gateway.networking.k8s.io", "Gateway", "shop", "front", map[string]any{})
	for _, bridge := range []bool{false, true} {
		emit := projectionAccess{denied: []string{"Service", "HTTPRoute"}}
		var traverse ownership.Access = emit
		if bridge {
			traverse = ownership.Unrestricted()
		}
		s := newProjectionSession(t, deployment, svc, ingress, route, gateway)
		rel := projectionAnswer(t, s, deployment, emit, traverse)
		if len(rel.Services)+len(rel.Routes) > 0 {
			t.Fatalf("hidden intermediates emitted: %+v", rel)
		}
		if bridge && (len(rel.Ingresses) != 1 || len(rel.Gateways) != 1) {
			t.Fatalf("lost readable entrypoints: %+v", rel)
		}
		if !bridge && len(rel.Ingresses)+len(rel.Gateways) > 0 {
			t.Fatalf("traversed denied hop: %+v", rel)
		}
	}
}

func TestRelationsAnswerDeviceAndHostAccess(t *testing.T) {
	claim := projectionCR("resource.k8s.io", "ResourceClaim", "shop", "claim", map[string]any{"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"exactly": map[string]any{"deviceClassName": "gpu"}}}}}, "status": map[string]any{"reservedFor": []any{map[string]any{"resource": "pods", "name": "hidden"}}}})
	s := newProjectionSession(t, claim)
	emit := projectionAccess{denied: []string{"DeviceClass", "Pod"}}
	rel := projectionAnswer(t, s, claim, emit, ownership.Unrestricted())
	if len(rel.ConfigRefs)+len(rel.Consumers) > 0 {
		t.Fatalf("hidden device references: %+v", rel)
	}
	endpoint := projectionCR("projectcalico.org", "HostEndpoint", "", "host", map[string]any{"spec": map[string]any{"node": "hidden"}})
	s = newProjectionSession(t, endpoint)
	rel = projectionAnswer(t, s, endpoint, projectionAccess{denied: []string{"Node"}}, ownership.Unrestricted())
	if rel.Node == nil || *rel.Node != (ResourceRef{Kind: "Node", Name: "hidden", State: ResourceStateDenied}) {
		t.Fatalf("hidden HostEndpoint Node: %+v", rel.Node)
	}
}

func TestRelationsAnswerOwnedAndPoolAccess(t *testing.T) {
	controller := true
	cluster := projectionCR("cluster.x-k8s.io", "Cluster", "shop", "cluster", map[string]any{})
	cluster.SetUID("cluster")
	check := projectionCR("cluster.x-k8s.io", "MachineHealthCheck", "shop", "check", map[string]any{})
	check.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1", Kind: "Cluster", Name: "cluster", UID: "cluster", Controller: &controller}})
	s := newProjectionSession(t, cluster, check)
	rel := projectionAnswer(t, s, cluster, projectionAccess{denied: []string{"MachineHealthCheck"}}, ownership.Unrestricted())
	if len(rel.HealthChecks)+len(rel.Children) > 0 {
		t.Fatalf("hidden health check: %+v", rel)
	}
	s = newProjectionSession(t, cluster, check)
	rel = projectionAnswer(t, s, check, projectionAccess{denied: []string{"Cluster"}}, ownership.Unrestricted())
	if len(rel.Dependencies) > 0 {
		t.Fatalf("hidden health check dependency: %+v", rel)
	}
	claim := projectionCR("karpenter.sh", "NodeClaim", "", "claim", map[string]any{})
	claim.SetLabels(map[string]string{"karpenter.sh/nodepool": "pool"})
	pool := projectionCR("karpenter.sh", "NodePool", "", "pool", map[string]any{})
	s = newProjectionSession(t, claim, pool)
	rel = projectionAnswer(t, s, claim, projectionAccess{denied: []string{"NodePool"}}, ownership.Unrestricted())
	if rel.Owner != nil || len(rel.ManagedBy)+len(rel.Managers) > 0 {
		t.Fatalf("hidden node pool: %+v", rel)
	}
	for _, ref := range s.reads {
		if ref.Kind == "NodePool" {
			t.Fatalf("read hidden pool: %v", ref)
		}
	}
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "web"}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "rs", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "web", Controller: &controller}}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs", Controller: &controller}}}}, "", "Pod")
	for _, denied := range []string{"Pod", "ReplicaSet"} {
		s = newProjectionSession(t, deployment, rs, pod)
		rel = projectionAnswer(t, s, deployment, projectionAccess{denied: []string{denied}}, ownership.Unrestricted())
		if len(rel.Pods) > 0 {
			t.Fatalf("Deployment Pod path crossed denied %s: %+v", denied, rel.Pods)
		}
	}
}

func TestRelationsAnswerScalars(t *testing.T) {
	source := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "source", ResourceVersion: "rv-opaque"}}, "", "ConfigMap")
	mirror := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "mirror", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "shop/source"}}}, "", "ConfigMap")
	s := newProjectionSession(t, source, mirror)
	rel := projectionAnswer(t, s, mirror, ownership.Unrestricted(), ownership.Unrestricted())
	if rel.Reflection == nil || rel.Reflection.SourceResourceVersion != "rv-opaque" {
		t.Fatalf("reflection scalar: %+v", rel.Reflection)
	}
}

// The answer reads Istio's service domain from istiod itself, so a
// VirtualService naming its Service by FQDN links to it with no domain from
// the caller.
func TestRelationsAnswerIstioDomainFromIstiod(t *testing.T) {
	istiod := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "istio-system", Name: "istiod", Labels: map[string]string{"app": "istiod"}}}
	istiod.Spec.Template.Spec.Containers = []corev1.Container{{Name: "discovery", Args: []string{"discovery", "--domain", "corp.internal"}}}
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}, "", "Service")
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "web", map[string]any{"spec": map[string]any{
		"http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "web.shop.svc.corp.internal"}}}}},
	}})
	s := newProjectionSession(t, vs, svc)
	captured, ok := relations.CaptureSubject(s, projectionRef(vs), vs)
	if !ok {
		t.Fatal("capture failed")
	}
	rel, _ := RelationsAnswer(captured, ownership.Unrestricted(), ownership.Unrestricted(), ownership.NewView(captured, ownership.Unrestricted()), projectionRef(vs), vs, &mockProvider{deployments: []*appsv1.Deployment{istiod}}, nil)
	if rel.IstioServiceDomain != "corp.internal" {
		t.Fatalf("Istio domain: %+v", rel)
	}
	requireProjectionRefs(t, rel.Backends, svc)
}

func TestRelationsAnswerManagedByControllerTop(t *testing.T) {
	controller := true
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "deployment"}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "rs", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deployment", Controller: &controller}}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs", Controller: &controller}}}}, "", "Pod").(*corev1.Pod)
	for _, tc := range []struct {
		name   string
		denied []string
		want   string
	}{{"top", nil, "web"}, {"stop at unreadable owner", []string{"Deployment"}, "rs"}, {"no readable owner", []string{"ReplicaSet"}, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProjectionSession(t, pod, rs, deployment)
			rel := projectionAnswer(t, s, pod, projectionAccess{denied: tc.denied}, ownership.Unrestricted())
			if tc.want == "" {
				if len(rel.ManagedBy) != 0 {
					t.Fatalf("ManagedBy: %+v", rel.ManagedBy)
				}
				return
			}
			if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != tc.want {
				t.Fatalf("ManagedBy: %+v", rel.ManagedBy)
			}
		})
	}
	pod.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "release", "helm.toolkit.fluxcd.io/namespace": "gitops"}
	s := newProjectionSession(t, pod, rs, deployment)
	rel := projectionAnswer(t, s, pod, ownership.Unrestricted(), ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != "release" {
		t.Fatalf("metadata precedence: %+v", rel.ManagedBy)
	}
	s = newProjectionSession(t, pod, rs, deployment)
	rel = projectionAnswer(t, s, pod, projectionAccess{denied: []string{"HelmRelease"}}, ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != "release" {
		t.Fatalf("a manager the subject's own labels name: %+v", rel.ManagedBy)
	}
}

func TestRelationsAnswerManagedByOwnManager(t *testing.T) {
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls", UID: "secret"}}, "", "Secret")
	cert := projectionCR("cert-manager.io", "Certificate", "shop", "web-cert", map[string]any{"spec": map[string]any{"secretName": "tls"}})
	s := newProjectionSession(t, secret, cert)
	rel := projectionAnswer(t, s, secret, ownership.Unrestricted(), ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Certificate" || rel.ManagedBy[0].Name != "web-cert" {
		t.Fatalf("a generated Secret's manager: %+v", rel.ManagedBy)
	}
	s = newProjectionSession(t, secret, cert)
	rel = projectionAnswer(t, s, secret, projectionAccess{denied: []string{"Certificate"}}, ownership.Unrestricted())
	if len(rel.ManagedBy) != 0 {
		t.Fatalf("hidden generator named: %+v", rel.ManagedBy)
	}
}

func TestRelationsAnswerManagedByClimbsNonControllerOwners(t *testing.T) {
	controller := true
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "deployment"}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "rs", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deployment", Controller: &controller}}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs", Controller: &controller}}}}, "", "Pod")
	// An object a Pod owns without being its controller, as a CiliumEndpoint is.
	endpoint := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod-endpoint", UID: "endpoint", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "pod", UID: "pod"}}}}, "", "ConfigMap")
	s := newProjectionSession(t, endpoint, pod, rs, deployment)
	rel := projectionAnswer(t, s, endpoint, ownership.Unrestricted(), ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Deployment" || rel.ManagedBy[0].Name != "web" {
		t.Fatalf("climb through a non-controller owner: %+v", rel.ManagedBy)
	}
	s = newProjectionSession(t, endpoint, pod, rs, deployment)
	rel = projectionAnswer(t, s, endpoint, projectionAccess{denied: []string{"ReplicaSet"}}, ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Pod" {
		t.Fatalf("stop below an unreadable owner: %+v", rel.ManagedBy)
	}
}

func TestRelationsAnswerManagedByWithheldControllerKeepsPrecedence(t *testing.T) {
	controller := true
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "deployment"}}, "apps", "Deployment")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod", UID: "pod"}}, "", "Pod")
	subject := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "state", UID: "state", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deployment", Controller: &controller},
		{APIVersion: "v1", Kind: "Pod", Name: "pod", UID: "pod"},
	}}}, "", "ConfigMap")
	s := newProjectionSession(t, subject, deployment, pod)
	rel := projectionAnswer(t, s, subject, ownership.Unrestricted(), ownership.Unrestricted())
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Deployment" {
		t.Fatalf("readable controller: %+v", rel.ManagedBy)
	}
	s = newProjectionSession(t, subject, deployment, pod)
	rel = projectionAnswer(t, s, subject, projectionAccess{denied: []string{"Deployment"}}, ownership.Unrestricted())
	if len(rel.ManagedBy) != 0 {
		t.Fatalf("a withheld controller gave way to a weaker owner: %+v", rel.ManagedBy)
	}
}

func deploymentWithPods(replicaSets, podsPerSet int) []runtime.Object {
	controller := true
	objects := []runtime.Object{projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "deployment"}}, "apps", "Deployment")}
	for i := range replicaSets {
		rsName := fmt.Sprintf("web-%d", i)
		objects = append(objects, projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: rsName, UID: types.UID(rsName), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deployment", Controller: &controller}}}}, "apps", "ReplicaSet"))
		for j := range podsPerSet {
			objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("%s-%d", rsName, j), UID: types.UID(fmt.Sprintf("%s-%d", rsName, j)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, UID: types.UID(rsName), Controller: &controller}}}}, "", "Pod"))
		}
	}
	return objects
}

func TestRelationsAnswerDeploymentPodsOncePerPod(t *testing.T) {
	objects := deploymentWithPods(3, 50)
	rel := projectionAnswer(t, newProjectionSession(t, objects...), objects[0], ownership.Unrestricted(), ownership.Unrestricted())
	seen := map[ResourceRef]bool{}
	for _, p := range rel.Pods {
		if seen[p] {
			t.Fatalf("Pod listed twice: %v", p)
		}
		seen[p] = true
	}
	if len(seen) != 150 {
		t.Fatalf("Pods: got %d, want 150", len(seen))
	}
}

func BenchmarkRelationsAnswerDeploymentPods(b *testing.B) {
	objects := deploymentWithPods(5, 1000)
	s := newProjectionSession(b, objects...)
	for b.Loop() {
		projectionAnswer(b, s, objects[0], ownership.Unrestricted(), ownership.Unrestricted())
	}
}

func TestRelationsAnswerKnativePrivateServiceNeedsNoRevision(t *testing.T) {
	for _, state := range []string{"denied", "absent", "uncollected"} {
		for _, latest := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/latest=%v", state, latest), func(t *testing.T) {
				status := map[string]any{"traffic": []any{map[string]any{"revisionName": "web-1"}}}
				if latest {
					status = map[string]any{"latestReadyRevisionName": "web-1"}
				}
				ksvc := projectionCR("serving.knative.dev", "Service", "shop", "web", map[string]any{"status": status})
				svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-private"}}, "", "Service")
				revision := projectionCR("serving.knative.dev", "Revision", "shop", "web-1", map[string]any{})
				revision.SetLabels(map[string]string{"serving.knative.dev/service": "web"})
				s := newProjectionSession(t, ksvc, svc, revision)
				var emit ownership.Access = ownership.Unrestricted()
				switch state {
				case "denied":
					emit = projectionAccess{denied: []string{"Revision"}}
				case "absent":
					delete(s.objects, projectionRef(revision))
				case "uncollected":
					delete(s.objects, projectionRef(revision))
					s.gaps = map[resourceid.GroupKind]ownership.Gap{projectionRef(revision).GroupKind(): {Kind: projectionRef(revision).GroupKind(), Namespace: "shop", Reason: readreason.RadarKindNotCollected}}
				}
				forward := projectionAnswer(t, s, ksvc, emit, emit)
				reverse := projectionAnswer(t, s, svc, emit, emit)
				if !slices.Equal(forward.Backends, []ResourceRef{refOf(projectionRef(svc))}) || !slices.Equal(reverse.RoutedFrom, []ResourceRef{refOf(projectionRef(ksvc))}) {
					t.Fatalf("forward %v, reverse %v", forward.Backends, reverse.RoutedFrom)
				}
				front := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", OwnerReferences: []metav1.OwnerReference{{APIVersion: "serving.knative.dev/v1", Kind: "Route", Name: "web"}}}}, "", "Service")
				s.objects[projectionRef(front)] = front
				forward = projectionAnswer(t, s, ksvc, emit, emit)
				reverse = projectionAnswer(t, s, front, emit, emit)
				if !slices.Contains(forward.Services, refOf(projectionRef(front))) || !slices.Contains(reverse.Backends, refOf(projectionRef(ksvc))) {
					t.Fatalf("front door forward %+v, reverse %+v", forward, reverse)
				}
			})
		}
	}
}

type hiddenNetworkAccess struct{ ownership.Access }

func (a hiddenNetworkAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if gk.Kind == "Service" || gk.Kind == "HTTPRoute" || gk.Kind == "Gateway" && ns == "shop" {
		return ownership.Denied
	}
	return a.Access.CanRead(gk, ns)
}

func TestRelationsAnswerNetworkingHiddenHopsHaveNoOutputLimit(t *testing.T) {
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	hiddenGateway := projectionCR("gateway.networking.k8s.io", "Gateway", "shop", "hidden", map[string]any{})
	gateway := projectionCR("gateway.networking.k8s.io", "Gateway", "public", "front", map[string]any{})
	for _, manyServices := range []bool{false, true} {
		t.Run(fmt.Sprintf("manyServices=%v", manyServices), func(t *testing.T) {
			objects := []runtime.Object{deployment, hiddenGateway, gateway}
			for i := 0; i <= relations.DefaultLimit; i++ {
				service := "web"
				if manyServices {
					service = fmt.Sprintf("web-%04d", i)
				}
				if manyServices || i == 0 {
					objects = append(objects, projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: service}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service"))
				}
				parent := map[string]any{"name": "hidden"}
				if i == relations.DefaultLimit {
					parent = map[string]any{"name": "front", "namespace": "public"}
				}
				objects = append(objects, projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", fmt.Sprintf("route-%04d", i), map[string]any{"spec": map[string]any{"parentRefs": []any{parent}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": service}}}}}}))
			}
			s := newProjectionSession(t, objects...)
			emit := hiddenNetworkAccess{ownership.Unrestricted()}
			captured, ok := relations.CaptureSubject(s, projectionRef(deployment), deployment)
			if !ok {
				t.Fatal("capture failed")
			}
			rel, coverage := RelationsAnswer(captured, emit, ownership.Unrestricted(), ownership.NewView(captured, emit), projectionRef(deployment), deployment, nil, nil)
			if !slices.Equal(rel.Gateways, []ResourceRef{refOf(projectionRef(gateway))}) || len(rel.Services)+len(rel.Routes) != 0 {
				t.Fatalf("gateways %v, services %v, routes %v", rel.Gateways, rel.Services, rel.Routes)
			}
			for name, c := range coverage {
				if c.Total != 0 || c.Truncated {
					t.Fatalf("%s leaked hidden-hop count or truncated traversal: %+v", name, c)
				}
			}
		})
	}
}

func TestRelationsAnswerNetworkingHiddenStoreGaps(t *testing.T) {
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "web", map[string]any{})
	for _, gk := range []resourceid.GroupKind{projectionRef(svc).GroupKind(), projectionRef(route).GroupKind()} {
		t.Run(gk.Kind, func(t *testing.T) {
			s := newProjectionSession(t, deployment, svc, route)
			gap := ownership.Gap{Kind: gk, Namespace: "shop", Reason: readreason.KindSyncPending}
			s.gaps = map[resourceid.GroupKind]ownership.Gap{gk: gap}
			emit := hiddenNetworkAccess{ownership.Unrestricted()}
			captured, ok := relations.CaptureSubject(s, projectionRef(deployment), deployment)
			if !ok {
				t.Fatal("capture failed")
			}
			rel, coverage := RelationsAnswer(captured, emit, ownership.Unrestricted(), ownership.NewView(captured, emit), projectionRef(deployment), deployment, nil, nil)
			key := relations.GatewayParents.Name
			if gk.Kind == "Service" {
				key = relations.ServiceSelectors.Name
			}
			c, found := coverage[key]
			if !found || c.Complete() || c.Total != 0 || !slices.Contains(c.NotChecked, gap) {
				t.Fatalf("missing count-free intermediate gap: %+v", coverage)
			}
			if len(rel.Gateways)+len(rel.Services)+len(rel.Routes) != 0 {
				t.Fatalf("unreadable hops emitted: %+v", rel)
			}
		})
	}
}

func TestRelationsAnswerStorageClassReverseLists(t *testing.T) {
	class := "fast"
	sc := projectionObject(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: class}}, "storage.k8s.io", "StorageClass")
	pv := projectionObject(&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "volume"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class}}, "", "PersistentVolume")
	pvc := projectionObject(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "disk"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}}, "", "PersistentVolumeClaim")
	other := pvc.(*corev1.PersistentVolumeClaim).DeepCopy()
	other.Namespace = "other"
	for _, tc := range []struct {
		name    string
		emit    ownership.Access
		partial bool
		want    []ResourceRef
	}{
		{"allowed", ownership.Unrestricted(), false, []ResourceRef{refOf(projectionRef(pv)), refOf(projectionRef(pvc)), refOf(projectionRef(other))}},
		{"claims denied", projectionAccess{denied: []string{"PersistentVolumeClaim"}}, false, []ResourceRef{refOf(projectionRef(pv))}},
		{"volumes denied", projectionAccess{denied: []string{"PersistentVolume"}}, false, []ResourceRef{refOf(projectionRef(pvc)), refOf(projectionRef(other))}},
		{"namespace denied", projectionAccess{namespaces: []string{"shop"}}, false, []ResourceRef{refOf(projectionRef(pv)), refOf(projectionRef(pvc))}},
		{"claims partially collected", ownership.Unrestricted(), true, []ResourceRef{refOf(projectionRef(pv)), refOf(projectionRef(pvc))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProjectionSession(t, sc, pv, pvc, other)
			gap := ownership.Gap{Kind: projectionRef(pvc).GroupKind(), Namespace: "other", Reason: readreason.RadarNamespaceNotCollected}
			if tc.partial {
				s.gaps = map[resourceid.GroupKind]ownership.Gap{gap.Kind: gap}
			}
			captured, ok := relations.CaptureSubject(s, projectionRef(sc), sc)
			if !ok {
				t.Fatal("capture failed")
			}
			rel, coverage := RelationsAnswer(captured, tc.emit, ownership.Unrestricted(), ownership.NewView(captured, tc.emit), projectionRef(sc), sc, nil, nil)
			if len(rel.Consumers) != len(tc.want) {
				t.Fatalf("consumers %v, want %v", rel.Consumers, tc.want)
			}
			for _, ref := range tc.want {
				if !slices.Contains(rel.Consumers, ref) {
					t.Errorf("missing consumer %v in %v", ref, rel.Consumers)
				}
			}
			if tc.partial {
				c := coverage["direct"]
				if c.Complete() || !slices.Contains(c.NotChecked, gap) {
					t.Fatalf("partial collection lost: %+v", coverage)
				}
			}
		})
	}
}

// A manager that deploys to another cluster records that cluster's objects,
// even where its list names a local object's coordinates.
func TestProjectionSessionLocatesManagers(t *testing.T) {
	web := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "web"}}, "apps", "Deployment")
	for _, tc := range []struct {
		server string
		want   int
	}{{"https://kubernetes.default.svc", 1}, {"https://remote.example.com", 0}} {
		app := charUnstructured("argoproj.io/v1alpha1", "Application", "argocd", "shop", nil,
			map[string]any{"destination": map[string]any{"server": tc.server, "namespace": "shop"}},
			map[string]any{"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "web"}}})
		s := newRecordingProjectionSession(t, app, web)
		if got := ownership.NewView(s, ownership.Unrestricted()).RecordingManagers(projectionRef(web)).Managers; len(got) != tc.want {
			t.Errorf("destination %s: recording managers = %v, want %d", tc.server, got, tc.want)
		}
	}
}

// An entry for a cluster-scoped kind names the cluster-scoped object, with or
// without a recorded namespace, as the cache source reads it.
func TestProjectionSessionPlacesClusterScopedMembers(t *testing.T) {
	reader := projectionObject(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader", UID: "reader"}}, "rbac.authorization.k8s.io", "ClusterRole")
	for _, recorded := range []map[string]any{
		{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "reader"},
		{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "namespace": "shop", "name": "reader"},
	} {
		app := charUnstructured("argoproj.io/v1alpha1", "Application", "argocd", "shop", nil,
			map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}},
			map[string]any{"resources": []any{recorded}})
		s := newRecordingProjectionSession(t, app, reader)
		if got := ownership.NewView(s, ownership.Unrestricted()).RecordingManagers(projectionRef(reader)).Managers; len(got) != 1 {
			t.Errorf("entry %v: recording managers = %v, want the Application", recorded, got)
		}
	}
}

// Several owner references with no controller are garbage-collection
// dependencies with no primary, so none of them is climbed as a manager.
func TestRelationsAnswerManagedByIgnoresSeveralNonControllerOwners(t *testing.T) {
	a := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "a", UID: "a"}}, "", "Pod")
	z := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "z", UID: "z"}}, "", "Pod")
	state := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "state", UID: "state", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "v1", Kind: "Pod", Name: "z", UID: "z"},
		{APIVersion: "v1", Kind: "Pod", Name: "a", UID: "a"},
	}}}, "", "ConfigMap")
	rel := projectionAnswer(t, newProjectionSession(t, state, a, z), state, ownership.Unrestricted(), ownership.Unrestricted())
	if len(rel.ManagedBy) != 0 {
		t.Fatalf("picked a manager among several owners: %+v", rel.ManagedBy)
	}
}

// undecidedAccess can't decide the named kinds and counts what it is asked.
type undecidedAccess struct {
	projectionAccess
	undecided []string
	asked     map[string]int
}

func (a *undecidedAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	a.asked[gk.Kind+"/"+ns]++
	if slices.Contains(a.undecided, gk.Kind) {
		return ownership.Undecided
	}
	return a.projectionAccess.CanRead(gk, ns)
}

// A special producer's failed check reads as a permission-check gap, a denied
// one as narrowed, and each is asked once however many objects match.
func TestRelationsAnswerSpecialChecksKeepTheirOutcome(t *testing.T) {
	claim := projectionCR("karpenter.sh", "NodeClaim", "", "claim", map[string]any{})
	claim.SetLabels(map[string]string{"karpenter.sh/nodepool": "pool"})
	pool := projectionCR("karpenter.sh", "NodePool", "", "pool", map[string]any{})
	answer := func(access ownership.Access, subject runtime.Object, objects ...runtime.Object) map[string]relations.Coverage {
		t.Helper()
		s := newProjectionSession(t, objects...)
		captured, ok := relations.CaptureSubject(s, projectionRef(subject), subject)
		if !ok {
			t.Fatal("capture failed")
		}
		_, coverage := RelationsAnswer(captured, access, access, ownership.NewView(captured, access), projectionRef(subject), subject, nil, nil)
		return coverage
	}
	coverage := answer(&undecidedAccess{undecided: []string{"NodePool"}, asked: map[string]int{}}, claim, claim, pool)
	if !slices.Contains(coverage["special"].NotChecked, ownership.Gap{Kind: resourceid.GroupKind{Group: "karpenter.sh", Kind: "NodePool"}, Reason: readreason.UserPermissionCheckFailed}) {
		t.Fatalf("failed NodePool check left no gap: %+v", coverage)
	}
	if coverage = answer(projectionAccess{denied: []string{"NodePool"}}, claim, claim, pool); !coverage["special"].Narrowed {
		t.Fatalf("denied NodePool didn't narrow: %+v", coverage)
	}

	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, "", "Node")
	objects := []runtime.Object{node}
	for i := range 3 {
		objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("pod-%d", i)}, Spec: corev1.PodSpec{NodeName: "node"}}, "", "Pod"))
	}
	access := &undecidedAccess{undecided: []string{"Pod"}, asked: map[string]int{}}
	coverage = answer(access, node, objects...)
	if access.asked["Pod/shop"] != 1 {
		t.Errorf("asked about Pods in shop %d times", access.asked["Pod/shop"])
	}
	if len(coverage["special"].NotChecked) != 1 {
		t.Errorf("want one permission-check gap for the node's Pods: %+v", coverage)
	}
}

// A workload's entrypoints read what routes to its Services, never what else
// those Services select.
func TestRelationsAnswerEntrypointsSkipServiceSiblings(t *testing.T) {
	labels := map[string]string{"app": "web"}
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}, "apps", "Deployment")
	sibling := projectionObject(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}, "apps", "StatefulSet")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: corev1.ServiceSpec{Selector: labels}}, "", "Service")
	ingress := projectionObject(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web"}}}}, "networking.k8s.io", "Ingress")
	access := &undecidedAccess{undecided: []string{"StatefulSet"}, asked: map[string]int{}}
	s := newProjectionSession(t, deployment, sibling, svc, ingress)
	captured, _ := relations.CaptureSubject(s, projectionRef(deployment), deployment)
	rel, coverage := RelationsAnswer(captured, access, access, ownership.NewView(captured, access), projectionRef(deployment), deployment, nil, nil)
	if len(rel.Services) != 1 || len(rel.Ingresses) != 1 {
		t.Fatalf("entrypoints: %+v", rel)
	}
	if access.asked["StatefulSet/shop"] > 0 {
		t.Errorf("read the Service's other workloads: %v, coverage %+v", access.asked, coverage)
	}
}

// A Crossplane reference that omits its namespace resolves by the kind's
// scope, so a namespaced composed resource is never probed cluster-wide,
// which a cache read refuses with a gap.
func TestRelationsAnswerCrossplaneReferenceUsesTheKindsScope(t *testing.T) {
	composite := projectionCR("example.org", "Widget", "shop", "widget", map[string]any{"spec": map[string]any{"crossplane": map[string]any{"resourceRefs": []any{map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "name": "bucket"}}}, "resourceRefs": []any{map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "name": "bucket"}}}})
	composite.SetLabels(map[string]string{"crossplane.io/composite": "widget"})
	bucket := projectionCR("example.org", "Bucket", "shop", "bucket", map[string]any{})
	s := newProjectionSession(t, composite, bucket)
	rel := projectionAnswer(t, s, composite, ownership.Unrestricted(), ownership.Unrestricted())
	if !slices.ContainsFunc(rel.Children, func(r ResourceRef) bool { return r.Kind == "Bucket" && r.Namespace == "shop" }) {
		t.Fatalf("namespaced composed resource: %+v", rel)
	}
	for _, r := range s.reads {
		if r.Kind == "Bucket" && r.Namespace == "" {
			t.Fatalf("probed a namespaced Bucket cluster-wide: %v", r)
		}
	}
}

// countingDeployments counts how often an answer lists every Deployment.
type countingDeployments struct {
	*mockProvider
	listed *int
}

func (p countingDeployments) Deployments() ([]*appsv1.Deployment, error) {
	*p.listed++
	return p.mockProvider.Deployments()
}

// Finding Istio's domain lists every Deployment, so an answer looks for it
// only when it reads a VirtualService or DestinationRule, and then once.
func TestRelationsAnswerFindsIstioDomainOnlyForIstioObjects(t *testing.T) {
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "settings"}}, "", "ConfigMap")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}, "", "Service")
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "web", map[string]any{"spec": map[string]any{
		"http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "web"}}}}},
	}})
	rule := projectionCR("networking.istio.io", "DestinationRule", "shop", "web", map[string]any{"spec": map[string]any{"host": "web"}})
	access := ownership.Unrestricted()
	for _, tc := range []struct {
		subject runtime.Object
		objects []runtime.Object
		want    int
	}{
		{cm, []runtime.Object{cm}, 0},
		{svc, []runtime.Object{svc}, 0},
		{svc, []runtime.Object{svc, vs, rule}, 1},
		{vs, []runtime.Object{svc, vs}, 1},
	} {
		listed := 0
		provider := countingDeployments{&mockProvider{}, &listed}
		s := newProjectionSession(t, tc.objects...)
		captured, ok := relations.CaptureSubject(s, projectionRef(tc.subject), tc.subject)
		if !ok {
			t.Fatal("capture failed")
		}
		RelationsAnswer(captured, access, access, ownership.NewView(captured, access), projectionRef(tc.subject), tc.subject, provider, nil)
		if listed != tc.want {
			t.Errorf("%s with %d objects listed Deployments %d times, want %d", projectionRef(tc.subject).Kind, len(tc.objects), listed, tc.want)
		}
	}
}

// A Pod's Node is named by its spec, so a caller who may open the Pod but
// not read Nodes still sees which Node, marked, in the drawer and the
// context alike; one whose check failed sees it unverified.
func TestRelationsAnswerNamesAPodsUnreadableNode(t *testing.T) {
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod"}, Spec: corev1.PodSpec{NodeName: "worker"}}, "", "Pod")
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}, "", "Node")
	for _, tc := range []struct {
		access ownership.Access
		want   string
	}{
		{projectionAccess{}, ""},
		{projectionAccess{denied: []string{"Node"}}, ResourceStateDenied},
		{undecidedKinds{"Node"}, ResourceStateUnverified},
	} {
		for name, answer := range map[string]func(relations.Session, ownership.Access, ownership.Access, *ownership.View, resourceid.Ref, runtime.Object, ResourceProvider, DynamicProvider) (*Relationships, *RelationshipExtras){"drawer": RelationsAnswerWithExtras, "context": RelationsContextAnswer} {
			s := newProjectionSession(t, pod, node)
			rel, extras := answer(s, tc.access, tc.access, ownership.NewView(s, tc.access), projectionRef(pod), pod, nil, nil)
			if rel.Node == nil || *rel.Node != (ResourceRef{Kind: "Node", Name: "worker", State: tc.want}) {
				t.Errorf("%s %T: node = %+v, want worker %q", name, tc.access, rel.Node, tc.want)
			}
			if c, ok := extras.Fields["Node"]; ok {
				t.Errorf("%s %T: the Node a spec names marked its field: %+v", name, tc.access, c)
			}
			if tc.want != "" {
				for _, read := range s.reads {
					if read.Kind == "Node" {
						t.Errorf("%s %T: read the Node the caller may not read", name, tc.access)
					}
				}
			}
		}
	}
}

// What a spec names is listed, marked, also when the caller's check on it
// couldn't be made: a HostEndpoint's Node and a Pod's declared ResourceClaim.
// A claim a template generates is named only in the Pod's status, so it
// stays hidden from a caller who may not read ResourceClaims.
func TestRelationsAnswerNamesSpecDeclaredHostAndClaimRefs(t *testing.T) {
	endpoint := projectionCR("crd.projectcalico.org", "HostEndpoint", "", "host", map[string]any{"spec": map[string]any{"node": "worker"}})
	s := newProjectionSession(t, endpoint)
	if rel := projectionAnswer(t, s, endpoint, undecidedKinds{"Node"}, ownership.Unrestricted()); rel.Node == nil || *rel.Node != (ResourceRef{Kind: "Node", Name: "worker", State: ResourceStateUnverified}) {
		t.Fatalf("unchecked HostEndpoint Node: %+v", rel.Node)
	}
	declared, generated := "declared", "generated-0"
	pod := projectionObject(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pod"},
		Spec:       corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "a", ResourceClaimName: &declared}, {Name: "b", ResourceClaimTemplateName: &generated}}},
		Status:     corev1.PodStatus{ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "b", ResourceClaimName: &generated}}},
	}, "", "Pod")
	s = newProjectionSession(t, pod)
	for _, tc := range []struct {
		access ownership.Access
		state  string
	}{{projectionAccess{denied: []string{"ResourceClaim"}}, ResourceStateDenied}, {undecidedKinds{"ResourceClaim"}, ResourceStateUnverified}} {
		rel := projectionAnswer(t, s, pod, tc.access, ownership.Unrestricted())
		if want := []ResourceRef{{Kind: "ResourceClaim", Group: "resource.k8s.io", Namespace: "shop", Name: "declared", State: tc.state}}; !slices.Equal(rel.ResourceClaims, want) {
			t.Errorf("%T: resource claims %+v, want %+v", tc.access, rel.ResourceClaims, want)
		}
		for _, read := range s.reads {
			if read.Kind == "ResourceClaim" {
				t.Errorf("%T: read a ResourceClaim the caller may not read", tc.access)
			}
		}
	}
}

// Managed By climbs the controller chain to the first GitOps manager above
// the subject and names it, as Applications groups a workload under its
// Application: past one lies a parent, such as an ApplicationSet, shared by
// apps that are separate. A GitOps manager's own Managed By is still its
// parent, and a chain without one climbs to its top.
func TestRelationsAnswerManagedByStopsAtTheFirstGitOpsManager(t *testing.T) {
	controller := true
	ref := func(apiVersion, kind, name, uid string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid), Controller: &controller}}
	}
	set := projectionCR("argoproj.io", "ApplicationSet", "argocd", "apps", map[string]any{})
	set.SetUID("set")
	app := projectionCR("argoproj.io", "Application", "argocd", "web", map[string]any{})
	app.SetUID("app")
	app.SetOwnerReferences(ref("argoproj.io/v1", "ApplicationSet", "apps", "set"))
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "web", UID: "deployment", OwnerReferences: ref("argoproj.io/v1", "Application", "web", "app")}}, "apps", "Deployment")
	for _, tc := range []struct {
		subject runtime.Object
		want    ResourceRef
	}{
		{deployment, ResourceRef{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "web"}},
		{app, ResourceRef{Kind: "ApplicationSet", Group: "argoproj.io", Namespace: "argocd", Name: "apps"}},
	} {
		s := newProjectionSession(t, set, app, deployment)
		rel := projectionAnswer(t, s, tc.subject, ownership.Unrestricted(), ownership.Unrestricted())
		if len(rel.ManagedBy) != 1 || refIdentity(rel.ManagedBy[0]) != refIdentity(tc.want) {
			t.Errorf("%s: ManagedBy %+v, want %+v", projectionRef(tc.subject).Kind, rel.ManagedBy, tc.want)
		}
	}
	plain := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "plain", UID: "plain", OwnerReferences: ref("argoproj.io/v1", "ApplicationSet", "apps", "set")}}, "apps", "Deployment")
	s := newProjectionSession(t, set, plain)
	if rel := projectionAnswer(t, s, plain, ownership.Unrestricted(), ownership.Unrestricted()); len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != "apps" {
		t.Errorf("a chain without a GitOps manager: ManagedBy %+v", rel.ManagedBy)
	}
}
