package relations

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
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
	v := NewView(cluster{objects: objects}, ownership.Unrestricted())
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
	c := allNamespaces{cluster{objects: []runtime.Object{auth, inShop, inTeam}}}
	subject := resourceid.Ref{Group: "keda.sh", Kind: "ClusterTriggerAuthentication", Name: "shared-creds"}
	if got := referrerRefs(NewView(c, ownership.Unrestricted()).Referrers(KEDAAuthentication, subject, nil)); len(got) != 2 {
		t.Errorf("referrers = %v; want the ScaledObjects in both namespaces", got)
	}
	shopOnly := NewView(c, namespaceAccess("shop")).Referrers(KEDAAuthentication, subject, nil)
	if got := referrerRefs(shopOnly); len(got) != 1 || got[0].Namespace != "shop" || !shopOnly.Coverage.Narrowed {
		t.Errorf("a caller reading only shop got %+v", shopOnly)
	}
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
// every object, as the cache source does.
type allNamespaces struct{ cluster }

func (c allNamespaces) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if index != "" {
		return c.cluster.Indexed(gk, index, keys, scope)
	}
	var r IndexedRead
	for _, obj := range c.objects {
		if ref := refOf(obj); ref.GroupKind() == gk {
			r.Objects = append(r.Objects, IndexedObject{Ref: ref, Object: obj})
		}
	}
	return r
}

// namespaceAccess may read only its namespaces, and anything cluster-scoped.
type namespaceAccess string

func (n namespaceAccess) CanRead(_ resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == "" || namespace == string(n) {
		return ownership.Allowed
	}
	return ownership.Denied
}
func (n namespaceAccess) Namespaces() ([]string, bool) { return []string{string(n)}, false }
