package relations

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func object(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": "shop", "name": name}, "spec": spec,
	}}
}

func referrerRefs(a ReferrersAnswer) []resourceid.Ref {
	var out []resourceid.Ref
	for _, r := range a.Referrers {
		out = append(out, r.Ref)
	}
	return out
}

// checkFamily asks f both ways about source and target in a cluster of
// objects: what source declares must be target, present, and target's
// referrers must be source.
func checkFamily(t *testing.T, f *Family, objects []runtime.Object, source runtime.Object, target resourceid.Ref, path string) {
	t.Helper()
	checkFamilyIn(t, allNamespaces{cluster: cluster{objects: objects}}, f, source, target, path)
}

// checkFamilyIn is checkFamily on the cluster s.
func checkFamilyIn(t *testing.T, s Session, f *Family, source runtime.Object, target resourceid.Ref, path string) {
	t.Helper()
	v := NewView(s, ownership.Unrestricted())
	refs := v.References(f, refOf(source), source)
	if len(refs.Declared) != 1 || refs.Declared[0].Ref != target || refs.Declared[0].State != TargetPresent || !slices.Equal(refs.Declared[0].Paths, []string{path}) || !refs.Coverage.Complete() {
		t.Errorf("%s: %v declares %+v; want %v at %s, present", f.Name, refOf(source), refs, target, path)
	}
	if got := referrerRefs(v.Referrers(f, target, nil)); !slices.Equal(got, []resourceid.Ref{refOf(source)}) {
		t.Errorf("%s: referrers of %v = %v; want %v", f.Name, target, got, refOf(source))
	}
}

func TestAutoscalersNameTheWorkloadTheyScale(t *testing.T) {
	web := deployment("web", mounts())
	hpa := typed(&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("web"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}}}, "autoscaling", "v2", "HorizontalPodAutoscaler")
	checkFamily(t, Autoscalers, []runtime.Object{web, hpa}, hpa, ref("apps", "Deployment", "web"), "spec.scaleTargetRef")

	vpa := object("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", "web", map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}})
	checkFamily(t, Autoscalers, []runtime.Object{web, vpa}, vpa, ref("apps", "Deployment", "web"), "spec.targetRef")

	// KEDA defaults an omitted kind to an apps Deployment.
	so := object("keda.sh/v1alpha1", "ScaledObject", "web", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}})
	checkFamily(t, Autoscalers, []runtime.Object{web, so}, so, ref("apps", "Deployment", "web"), "spec.scaleTargetRef")

	argo := object("argoproj.io/v1alpha1", "Rollout", "checkout", nil)
	toRollout := object("keda.sh/v1alpha1", "ScaledObject", "checkout", map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "name": "checkout"}})
	checkFamily(t, Autoscalers, []runtime.Object{argo, toRollout}, toRollout, ref("argoproj.io", "Rollout", "checkout"), "spec.scaleTargetRef")
}

func TestKEDAAuthenticationNamesTheTriggerAuthentication(t *testing.T) {
	auth := object("keda.sh/v1alpha1", "TriggerAuthentication", "queue-creds", nil)
	so := object("keda.sh/v1alpha1", "ScaledObject", "worker", map[string]any{
		"scaleTargetRef": map[string]any{"name": "worker"},
		"triggers":       []any{map[string]any{"type": "rabbitmq", "authenticationRef": map[string]any{"name": "queue-creds"}}},
	})
	checkFamily(t, KEDAAuthentication, []runtime.Object{auth, so}, so, ref("keda.sh", "TriggerAuthentication", "queue-creds"), "spec.triggers[].authenticationRef")
}

func TestIngressBackendsNameTheirServices(t *testing.T) {
	svc := typed(&corev1.Service{ObjectMeta: meta("web")}, "", "v1", "Service")
	pathType := networkingv1.PathTypePrefix
	ing := typed(&networkingv1.Ingress{ObjectMeta: meta("front"), Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
		Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web"}}}}}}}}}}, "networking.k8s.io", "v1", "Ingress")
	checkFamily(t, IngressBackends, []runtime.Object{svc, ing}, ing, ref("", "Service", "web"), "spec.rules[].http.paths[].backend.service.name")
}

func TestRolloutServicesNameTheirTrafficServices(t *testing.T) {
	stable := typed(&corev1.Service{ObjectMeta: meta("checkout-stable")}, "", "v1", "Service")
	argo := object("argoproj.io/v1alpha1", "Rollout", "checkout", map[string]any{"strategy": map[string]any{"canary": map[string]any{"stableService": "checkout-stable"}}})
	checkFamily(t, RolloutServices, []runtime.Object{stable, argo}, argo, ref("", "Service", "checkout-stable"), "spec.strategy.canary.stableService")
}

func TestCertificateSecretsNameTheSecretTheyWrite(t *testing.T) {
	tls := secret("front-tls")
	cert := object("cert-manager.io/v1", "Certificate", "front", map[string]any{"secretName": "front-tls"})
	checkFamily(t, CertificateSecrets, []runtime.Object{tls, cert}, cert, ref("", "Secret", "front-tls"), "spec.secretName")
}

func TestAClusterTriggerAuthenticationsReferrersAreReadAcrossNamespaces(t *testing.T) {
	auth := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ClusterTriggerAuthentication", "metadata": map[string]any{"name": "shared-creds"}}}
	trigger := map[string]any{"triggers": []any{map[string]any{"type": "rabbitmq", "authenticationRef": map[string]any{"kind": "ClusterTriggerAuthentication", "name": "shared-creds"}}}}
	inShop := object("keda.sh/v1alpha1", "ScaledObject", "worker", trigger)
	inTeam := object("keda.sh/v1alpha1", "ScaledObject", "worker", trigger)
	inTeam.SetNamespace("team")
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{auth, inShop, inTeam}}}
	subject := resourceid.Ref{Group: "keda.sh", Kind: "ClusterTriggerAuthentication", Name: "shared-creds"}
	if got := referrerRefs(NewView(c, ownership.Unrestricted()).Referrers(KEDAAuthentication, subject, nil)); len(got) != 2 {
		t.Errorf("referrers = %v; want the ScaledObjects in both namespaces", got)
	}
	// The caller may read ScaledObjects in shop but can't be told about them
	// cluster-wide, as MCP can't, and team's are still loading.
	c.gaps = []ownership.Gap{{Kind: kindScaledObject, Namespace: "team", Reason: readreason.KindSyncPending}}
	shopOnly := NewView(c, namespaceAccess{"shop", []string{"ClusterTriggerAuthentication"}}).Referrers(KEDAAuthentication, subject, nil)
	if got := referrerRefs(shopOnly); len(got) != 1 || got[0].Namespace != "shop" || !shopOnly.Coverage.Narrowed {
		t.Errorf("a caller reading only shop got %+v", shopOnly)
	}
	if len(shopOnly.Coverage.NotChecked) != 0 {
		t.Errorf("a caller reading only shop was told of team's gaps: %+v", shopOnly.Coverage.NotChecked)
	}
	c.gaps = []ownership.Gap{{Kind: kindScaledObject, Namespace: "shop", Reason: readreason.KindSyncPending}}
	if got := NewView(c, namespaceAccess{"shop", []string{"ClusterTriggerAuthentication"}}).Referrers(KEDAAuthentication, subject, nil); got.Coverage.Complete() {
		t.Errorf("shop's ScaledObjects are loading, yet the answer is complete: %+v", got)
	}
}

func TestAScaledObjectMayScaleAReplicaSet(t *testing.T) {
	rs := replicaSet("worker", nil, mounts())
	so := object("keda.sh/v1alpha1", "ScaledObject", "worker", map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "worker"}})
	checkFamily(t, Autoscalers, []runtime.Object{rs, so}, so, ref("apps", "ReplicaSet", "worker"), "spec.scaleTargetRef")
}

func TestDeclarationsASourceCannotMakeAreLeftOut(t *testing.T) {
	ds := typed(&appsv1.DaemonSet{ObjectMeta: meta("agent")}, "apps", "v1", "DaemonSet")
	hpa := typed(&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("agent"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "DaemonSet", Name: "agent"}}}, "autoscaling", "v2", "HorizontalPodAutoscaler")
	v := NewView(cluster{objects: []runtime.Object{ds, hpa}}, ownership.Unrestricted())
	if got := v.References(Autoscalers, refOf(hpa), hpa); len(got.Declared) != 0 {
		t.Errorf("an HPA can't scale a DaemonSet, yet declares %+v", got.Declared)
	}
	if got := referrerRefs(v.Referrers(Autoscalers, refOf(ds), nil)); len(got) != 0 {
		t.Errorf("a DaemonSet's autoscalers = %v; HPAs can't scale it", got)
	}
}

func TestVersionlessAutoscalerTargetsResolveBuiltInGroups(t *testing.T) {
	web := deployment("web", mounts())
	hpa := typed(&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("web"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"}}}, "autoscaling", "v2", "HorizontalPodAutoscaler")
	checkFamily(t, Autoscalers, []runtime.Object{web, hpa}, hpa, ref("apps", "Deployment", "web"), "spec.scaleTargetRef")
}

func TestAnIngressDefaultBackendSaysSo(t *testing.T) {
	svc := typed(&corev1.Service{ObjectMeta: meta("fallback")}, "", "v1", "Service")
	ing := typed(&networkingv1.Ingress{ObjectMeta: meta("front"), Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "fallback"}}}}, "networking.k8s.io", "v1", "Ingress")
	checkFamily(t, IngressBackends, []runtime.Object{svc, ing}, ing, ref("", "Service", "fallback"), "spec.defaultBackend.service.name")
}

// allNamespaces is a cluster whose Indexed also answers an empty index with
// every object, as the cache source does, and reports gaps.
type allNamespaces struct {
	cluster
	gaps []ownership.Gap
}

func (c allNamespaces) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	// Another index only narrows what the caller evaluates, so every object
	// stands in for what it files under keys.
	if index == cache.NamespaceIndex {
		r := c.cluster.Indexed(gk, index, keys, scope)
		r.Gaps = append(r.Gaps, c.gaps...)
		return r
	}
	r := IndexedRead{Gaps: c.gaps}
	// A kind a complete discovery doesn't serve has nothing to read, so, as
	// the cache's session does, it costs no gap.
	if reason, ok := c.unread[gk]; ok && reason != readreason.KindUnknown {
		r.Gaps = append(slices.Clone(c.gaps), ownership.Gap{Kind: gk, Reason: reason})
		return r
	}
	for _, obj := range c.objects {
		if ref := refOf(obj); ref.GroupKind() == gk {
			r.Objects = append(r.Objects, IndexedObject{Ref: ref, Object: obj})
		}
	}
	return r
}

// namespaceAccess may read only its namespace and the cluster-scoped kinds
// it names. Asked about another kind across the cluster it can't decide, as
// MCP can't.
type namespaceAccess struct {
	namespace    string
	clusterKinds []string
}

func (n namespaceAccess) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	switch {
	case namespace == n.namespace, namespace == "" && slices.Contains(n.clusterKinds, gk.Kind):
		return ownership.Allowed
	case namespace == "":
		return ownership.Undecided
	}
	return ownership.Denied
}
func (n namespaceAccess) Namespaces() ([]string, bool) { return []string{n.namespace}, false }

func inNamespace(u *unstructured.Unstructured, namespace string) *unstructured.Unstructured {
	u.SetNamespace(namespace)
	return u
}

func TestGatewayRoutesNameGatewaysAndBackendsAcrossNamespaces(t *testing.T) {
	gw := inNamespace(object("gateway.networking.k8s.io/v1", "Gateway", "shared", map[string]any{"gatewayClassName": "envoy"}), "infra")
	svc := typed(&corev1.Service{ObjectMeta: meta("web")}, "", "v1", "Service")
	route := inNamespace(object("gateway.networking.k8s.io/v1", "HTTPRoute", "web", map[string]any{
		"parentRefs": []any{map[string]any{"name": "shared", "namespace": "infra", "sectionName": "https"}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "namespace": "shop", "port": int64(80)}}}},
	}), "frontend")
	objects := []runtime.Object{gw, svc, route}
	checkFamily(t, GatewayParents, objects, route, resourceid.Ref{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "infra", Name: "shared"}, "spec.parentRefs[]")
	checkFamily(t, GatewayBackends, objects, route, ref("", "Service", "web"), "spec.rules[].backendRefs[]")

	class := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass", "metadata": map[string]any{"name": "envoy"}}}
	checkFamily(t, GatewayClasses, []runtime.Object{class, gw}, gw, resourceid.Ref{Group: "gateway.networking.k8s.io", Kind: "GatewayClass", Name: "envoy"}, "spec.gatewayClassName")
}

func TestARestrictedCallerSeesOnlyTheRoutesItMayRead(t *testing.T) {
	gw := object("gateway.networking.k8s.io/v1", "Gateway", "shared", nil)
	parent := map[string]any{"parentRefs": []any{map[string]any{"name": "shared", "namespace": "shop"}}}
	own := object("gateway.networking.k8s.io/v1", "HTTPRoute", "own", parent)
	other := inNamespace(object("gateway.networking.k8s.io/v1", "GRPCRoute", "other", parent), "team")
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{gw, own, other}}}
	if got := referrerRefs(NewView(c, ownership.Unrestricted()).Referrers(GatewayParents, refOf(gw), nil)); len(got) != 2 {
		t.Errorf("the Gateway's routes = %v; want the routes in both namespaces", got)
	}
	a := NewView(c, namespaceAccess{namespace: "shop"}).Referrers(GatewayParents, refOf(gw), nil)
	if got := referrerRefs(a); len(got) != 1 || got[0].Name != "own" || !a.Coverage.Narrowed || len(a.Coverage.NotChecked) != 0 {
		t.Errorf("a caller reading only shop got %+v", a)
	}
}

func TestGatewayReferencesToOtherKindsAreLeftOut(t *testing.T) {
	route := object("gateway.networking.k8s.io/v1", "HTTPRoute", "mesh", map[string]any{
		"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "web"}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"group": "example.com", "kind": "Bucket", "name": "web"}}}},
	})
	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{route}}}, ownership.Unrestricted())
	for _, f := range []*Family{GatewayParents, GatewayBackends} {
		if got := v.References(f, refOf(route), route); len(got.Declared) != 0 {
			t.Errorf("%s: a mesh route declares %+v", f.Name, got.Declared)
		}
	}
}

func TestCertManagerIssuersNameIssuersAndClusterIssuers(t *testing.T) {
	issuer := object("cert-manager.io/v1", "Issuer", "letsencrypt", nil)
	cert := object("cert-manager.io/v1", "Certificate", "front", map[string]any{"issuerRef": map[string]any{"name": "letsencrypt"}})
	checkFamily(t, CertManagerIssuers, []runtime.Object{issuer, cert}, cert, ref("cert-manager.io", "Issuer", "letsencrypt"), "spec.issuerRef")

	cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": "shared"}}}
	order := inNamespace(object("acme.cert-manager.io/v1", "Order", "front-1", map[string]any{"issuerRef": map[string]any{"kind": "ClusterIssuer", "name": "shared"}}), "team")
	checkFamily(t, CertManagerIssuers, []runtime.Object{cluster, order}, order, resourceid.Ref{Group: "cert-manager.io", Kind: "ClusterIssuer", Name: "shared"}, "spec.issuerRef")

	external := object("cert-manager.io/v1", "Certificate", "vault", map[string]any{"issuerRef": map[string]any{"group": "vault.example.com", "kind": "VaultIssuer", "name": "v"}})
	if got := NewView(allNamespaces{}, ownership.Unrestricted()).References(CertManagerIssuers, refOf(external), external); len(got.Declared) != 0 {
		t.Errorf("an external issuer is declared: %+v", got.Declared)
	}
}

func TestFluxSourcesNameTheirArtifactsAcrossNamespaces(t *testing.T) {
	repo := inNamespace(object("source.toolkit.fluxcd.io/v1", "GitRepository", "platform", nil), "flux-system")
	ks := object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "apps", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "platform", "namespace": "flux-system"}})
	checkFamily(t, FluxSources, []runtime.Object{repo, ks}, ks, resourceid.Ref{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "flux-system", Name: "platform"}, "spec.sourceRef")

	charts := object("source.toolkit.fluxcd.io/v1", "HelmRepository", "charts", nil)
	hr := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "web", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}}})
	checkFamily(t, FluxSources, []runtime.Object{charts, hr}, hr, ref("source.toolkit.fluxcd.io", "HelmRepository", "charts"), "spec.chart.spec.sourceRef")
}

func TestNodePoolsNameTheirProvidersNodeClass(t *testing.T) {
	class := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.k8s.aws/v1", "kind": "EC2NodeClass", "metadata": map[string]any{"name": "default"}}}
	pool := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "general"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}}}}}}
	checkFamily(t, NodeClasses, []runtime.Object{class, pool}, pool, resourceid.Ref{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}, "spec.template.spec.nodeClassRef")
	if NodeClasses.Covers(resourceid.GroupKind{Kind: "ConfigMap"}) {
		t.Error("NodeClasses covers ConfigMaps")
	}
}

func TestClustersNameTheirClusterClass(t *testing.T) {
	class := inNamespace(object("cluster.x-k8s.io/v1beta2", "ClusterClass", "quick-start", nil), "classes")
	cluster := object("cluster.x-k8s.io/v1beta2", "Cluster", "prod", map[string]any{"topology": map[string]any{"classRef": map[string]any{"name": "quick-start", "namespace": "classes"}}})
	checkFamily(t, ClusterClasses, []runtime.Object{class, cluster}, cluster, resourceid.Ref{Group: "cluster.x-k8s.io", Kind: "ClusterClass", Namespace: "classes", Name: "quick-start"}, "spec.topology.classRef")
	legacy := object("cluster.x-k8s.io/v1beta1", "Cluster", "old", map[string]any{"topology": map[string]any{"class": "quick-start"}})
	if r, ok := configrefs.CAPIClusterClass(legacy); !ok || r.Ref.Namespace != "shop" || r.Path != "spec.topology.class" {
		t.Errorf("a v1beta1 Cluster names %+v", r)
	}
}

func TestKnativeFamiliesNameWhereEventsAndTrafficGo(t *testing.T) {
	ksvc := inNamespace(object("serving.knative.dev/v1", "Service", "handler", nil), "apps")
	trigger := object("eventing.knative.dev/v1", "Trigger", "orders", map[string]any{
		"broker":     "default",
		"subscriber": map[string]any{"ref": map[string]any{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "name": "handler", "namespace": "apps"}},
	})
	broker := object("eventing.knative.dev/v1", "Broker", "default", nil)
	checkFamily(t, KnativeEventDestinations, []runtime.Object{ksvc, trigger}, trigger, resourceid.Ref{Group: "serving.knative.dev", Kind: "Service", Namespace: "apps", Name: "handler"}, "spec.subscriber.ref")
	checkFamily(t, KnativeTriggerBrokers, []runtime.Object{broker, trigger}, trigger, ref("eventing.knative.dev", "Broker", "default"), "spec.broker")

	ping := object("sources.knative.dev/v1", "PingSource", "tick", map[string]any{"sink": map[string]any{"ref": map[string]any{"apiVersion": "eventing.knative.dev/v1", "kind": "Broker", "name": "default"}}})
	checkFamily(t, KnativeEventDestinations, []runtime.Object{broker, ping}, ping, ref("eventing.knative.dev", "Broker", "default"), "spec.sink.ref")

	rev := object("serving.knative.dev/v1", "Revision", "handler-00002", nil)
	route := object("serving.knative.dev/v1", "Route", "handler", nil)
	route.Object["status"] = map[string]any{"traffic": []any{map[string]any{"revisionName": "handler-00002", "percent": int64(100)}}}
	checkFamily(t, KnativeRouteRevisions, []runtime.Object{rev, route}, route, ref("serving.knative.dev", "Revision", "handler-00002"), "status.traffic[].revisionName")
}

func TestIstioFamiliesNameGatewaysAndServiceHosts(t *testing.T) {
	gw := inNamespace(object("networking.istio.io/v1", "Gateway", "ingress", nil), "istio-system")
	svc := typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "backend", Name: "api"}}, "", "v1", "Service")
	vs := object("networking.istio.io/v1", "VirtualService", "api", map[string]any{
		"gateways": []any{"istio-system/ingress", "mesh"},
		"http":     []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "api.backend.svc.cluster.local"}}}}},
	})
	checkFamily(t, IstioGateways, []runtime.Object{gw, vs}, vs, resourceid.Ref{Group: "networking.istio.io", Kind: "Gateway", Namespace: "istio-system", Name: "ingress"}, "spec.gateways[]")
	hosts := IstioServiceHosts("cluster.local")
	checkFamily(t, hosts, []runtime.Object{svc, vs}, vs, resourceid.Ref{Kind: "Service", Namespace: "backend", Name: "api"}, "spec.http[].route[].destination.host")

	dr := object("networking.istio.io/v1", "DestinationRule", "api", map[string]any{"host": "api.backend.svc.cluster.local"})
	checkFamily(t, hosts, []runtime.Object{svc, dr}, dr, resourceid.Ref{Kind: "Service", Namespace: "backend", Name: "api"}, "spec.host")
	if got := NewView(allNamespaces{}, ownership.Unrestricted()).References(IstioServiceHosts(""), refOf(dr), dr); len(got.Declared) != 0 {
		t.Errorf("with the mesh's domain unknown an FQDN names %+v", got.Declared)
	}
}

func TestArgoWorkflowsNameTheirTemplates(t *testing.T) {
	tmpl := object("argoproj.io/v1alpha1", "WorkflowTemplate", "build", nil)
	wf := object("argoproj.io/v1alpha1", "Workflow", "build-x7", map[string]any{"workflowTemplateRef": map[string]any{"name": "build"}})
	checkFamily(t, ArgoWorkflowTemplates, []runtime.Object{tmpl, wf}, wf, ref("argoproj.io", "WorkflowTemplate", "build"), "spec.workflowTemplateRef")

	shared := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "ClusterWorkflowTemplate", "metadata": map[string]any{"name": "notify"}}}
	cron := inNamespace(object("argoproj.io/v1alpha1", "CronWorkflow", "nightly", map[string]any{"workflowSpec": map[string]any{"templates": []any{
		map[string]any{"name": "main", "steps": []any{[]any{map[string]any{"name": "tell", "templateRef": map[string]any{"name": "notify", "clusterScope": true}}}}},
	}}}), "ops")
	checkFamily(t, ArgoWorkflowTemplates, []runtime.Object{shared, cron}, cron, resourceid.Ref{Group: "argoproj.io", Kind: "ClusterWorkflowTemplate", Name: "notify"}, "spec.workflowSpec.templates[].steps[][].templateRef")
}

func TestOpenEndedTargetsFollowTheReferenceExactly(t *testing.T) {
	eks := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "eks.amazonaws.com/v1", "kind": "NodeClass", "metadata": map[string]any{"name": "default"}}}
	pool := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "auto"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": "eks.amazonaws.com", "kind": "NodeClass", "name": "default"}}}}}}
	checkFamily(t, NodeClasses, []runtime.Object{eks, pool}, pool, resourceid.Ref{Group: "eks.amazonaws.com", Kind: "NodeClass", Name: "default"}, "spec.template.spec.nodeClassRef")

	sink := object("sinks.knative.dev/v1alpha1", "JobSink", "jobs", nil)
	ping := object("sources.knative.dev/v1", "PingSource", "tick", map[string]any{"sink": map[string]any{"ref": map[string]any{"apiVersion": "sinks.knative.dev/v1alpha1", "kind": "JobSink", "name": "jobs"}}})
	checkFamily(t, KnativeEventDestinations, []runtime.Object{sink, ping}, ping, ref("sinks.knative.dev", "JobSink", "jobs"), "spec.sink.ref")
	if KnativeEventDestinations.Covers(resourceid.GroupKind{Kind: "ConfigMap"}) {
		t.Error("Knative destinations cover ConfigMaps")
	}
}

func TestAWorkflowTemplateUsedTwiceReportsBothPaths(t *testing.T) {
	use := map[string]any{"name": "lib", "templateRef": map[string]any{"name": "library"}}
	wf := object("argoproj.io/v1alpha1", "Workflow", "run", map[string]any{"templates": []any{
		map[string]any{"name": "a", "dag": map[string]any{"tasks": []any{use}}},
		map[string]any{"name": "b", "steps": []any{[]any{use}}},
	}})
	lib := object("argoproj.io/v1alpha1", "WorkflowTemplate", "library", nil)
	got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{wf, lib}}}, ownership.Unrestricted()).References(ArgoWorkflowTemplates, refOf(wf), wf)
	want := []string{"spec.templates[].dag.tasks[].templateRef", "spec.templates[].steps[][].templateRef"}
	if len(got.Declared) != 1 || !slices.Equal(got.Declared[0].Paths, want) {
		t.Errorf("declared %+v; want one target at %v", got.Declared, want)
	}
}
