package topology

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type projectionSession struct {
	ownership.ReadSession
	objects map[resourceid.Ref]runtime.Object
	reads   []resourceid.Ref
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
func newProjectionSession(t *testing.T, objects ...runtime.Object) *projectionSession {
	t.Helper()
	w := ownershiptest.World{}
	s := &projectionSession{objects: map[resourceid.Ref]runtime.Object{}}
	seen := map[resourceid.GroupKind]bool{}
	for _, obj := range objects {
		ref := projectionRef(obj)
		m, _ := apimeta.Accessor(obj)
		s.objects[ref] = obj
		w.Objects = append(w.Objects, ownershiptest.Object{Ref: ref, UID: m.GetUID(), APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(), ResourceVersion: m.GetResourceVersion(), OwnerReferences: m.GetOwnerReferences(), Labels: m.GetLabels(), Annotations: m.GetAnnotations()})
		if !seen[ref.GroupKind()] {
			w.Resources = append(w.Resources, ownership.ServedResource{Group: ref.Group, Version: "v1", Kind: ref.Kind, Resource: ref.Kind + "s", Namespaced: ref.Namespace != "", Verbs: []string{"get", "list", "watch", "delete"}})
			seen[ref.GroupKind()] = true
		}
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
	for ref, obj := range s.objects {
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
func projectionAnswer(t *testing.T, s *projectionSession, obj runtime.Object, emit, traverse ownership.Access) *Relationships {
	t.Helper()
	captured, ok := relations.CaptureSubject(s, projectionRef(obj), obj)
	if !ok {
		t.Fatal("capture failed")
	}
	rel, _ := RelationsAnswer(captured, emit, traverse, ownership.NewView(captured, emit), projectionRef(obj), obj, "", nil, nil)
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
	if rel.ServiceAccount != nil || rel.Node != nil || len(rel.ResourceClaims) > 0 {
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
	if rel.Node != nil {
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
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "web", map[string]any{})
	s = newProjectionSession(t, vs)
	rel, _ = RelationsAnswer(s, ownership.Unrestricted(), ownership.Unrestricted(), ownership.NewView(s, ownership.Unrestricted()), projectionRef(vs), vs, "corp.internal", nil, nil)
	if rel.IstioServiceDomain != "corp.internal" {
		t.Fatalf("Istio domain: %+v", rel)
	}
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
	if len(rel.ManagedBy) != 0 {
		t.Fatalf("hidden metadata manager: %+v", rel.ManagedBy)
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
