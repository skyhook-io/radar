package topology

import (
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"slices"
	"testing"
)

func projectionOwnedAnswer(t *testing.T, s *projectionSession, obj runtime.Object, access ownership.Access) *Relationships {
	t.Helper()
	captured, ok := relations.CaptureSubject(s, projectionRef(obj), obj)
	if !ok {
		t.Fatal("capture failed")
	}
	ov := ownership.NewView(captured, access)
	rel, _ := RelationsAnswer(captured, access, access, ov, projectionRef(obj), obj, "", nil, nil)
	m, _ := apimeta.Accessor(obj)
	ApplyOwnership(rel, ov, ownership.Observe(projectionRef(obj), m))
	return rel
}
func requireProjectionRefs(t *testing.T, got []ResourceRef, want ...runtime.Object) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("refs = %+v; want %d", got, len(want))
	}
	for _, o := range want {
		if !slices.Contains(got, refOf(projectionRef(o))) {
			t.Fatalf("refs = %+v; missing %v", got, projectionRef(o))
		}
	}
}
func TestRelationsAnswerCertificateRoutes(t *testing.T) {
	cert := projectionCR("cert-manager.io", "Certificate", "shop", "cert", map[string]any{"spec": map[string]any{"secretName": "tls"}})
	contour := projectionCR("projectcontour.io", "HTTPProxy", "edge", "front", map[string]any{"spec": map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "shop/tls"}}}})
	for _, group := range []string{"traefik.io", "traefik.containo.us"} {
		t.Run(group, func(t *testing.T) {
			route := projectionCR(group, "IngressRoute", "shop", "front", map[string]any{"spec": map[string]any{"tls": map[string]any{"secretName": "tls"}}})
			transport := projectionCR(group, "ServersTransport", "shop", "transport", map[string]any{"spec": map[string]any{"certificatesSecrets": []any{"tls"}}})
			s := newProjectionSession(t, cert, contour, route, transport)
			access := projectionAccess{denied: []string{"Secret"}}
			requireProjectionRefs(t, projectionAnswer(t, s, cert, access, access).Consumers, route, contour)
			requireProjectionRefs(t, projectionAnswer(t, s, route, access, access).ConfigRefs, cert)
			requireProjectionRefs(t, projectionAnswer(t, s, contour, access, access).ConfigRefs, cert)
			requireProjectionRefs(t, projectionAnswer(t, s, transport, access, access).ConfigRefs)
			requireProjectionRefs(t, projectionAnswer(t, s, cert, projectionAccess{namespaces: []string{"shop"}, denied: []string{"Secret"}}, access).Consumers, route)
			requireProjectionRefs(t, projectionAnswer(t, s, route, projectionAccess{denied: []string{"Certificate", "Secret"}}, access).ConfigRefs)
			for _, r := range s.reads {
				if r.Kind == "Secret" {
					t.Fatalf("borrowed logical Secret: %v", r)
				}
			}
		})
	}
}
func TestRelationsAnswerMappedDirections(t *testing.T) {
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "stable"}}, "", "Service")
	rollout := projectionCR("argoproj.io", "Rollout", "shop", "web", map[string]any{"spec": map[string]any{"strategy": map[string]any{"canary": map[string]any{"stableService": "stable"}}}})
	class := projectionCR("gateway.networking.k8s.io", "GatewayClass", "", "implementation", nil)
	gw := projectionCR("gateway.networking.k8s.io", "Gateway", "shop", "gateway", map[string]any{"spec": map[string]any{"gatewayClassName": "implementation"}})
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "route", map[string]any{"spec": map[string]any{"parentRefs": []any{map[string]any{"name": "gateway"}}}})
	istio := projectionCR("networking.istio.io", "Gateway", "shop", "istio", nil)
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "vs", map[string]any{"spec": map[string]any{"gateways": []any{"istio"}}})
	broker := projectionCR("eventing.knative.dev", "Broker", "shop", "broker", nil)
	trigger := projectionCR("eventing.knative.dev", "Trigger", "shop", "trigger", map[string]any{"spec": map[string]any{"broker": "broker"}})
	cluster := projectionCR("cluster.x-k8s.io", "Cluster", "shop", "cluster", nil)
	cluster.SetUID("cluster-uid")
	mhc := projectionCR("cluster.x-k8s.io", "MachineHealthCheck", "shop", "check", nil)
	mhc.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1", Kind: "Cluster", Name: cluster.GetName(), UID: cluster.GetUID()}})
	s := newProjectionSession(t, svc, rollout, class, gw, route, istio, vs, broker, trigger, cluster, mhc)
	a := ownership.Unrestricted()
	requireProjectionRefs(t, projectionAnswer(t, s, rollout, a, a).Services, svc)
	requireProjectionRefs(t, projectionAnswer(t, s, svc, a, a).Backends, rollout)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, class, a).Manages, gw)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, gw, a).Managers, class)
	requireProjectionRefs(t, projectionAnswer(t, s, gw, a, a).Backends, route)
	requireProjectionRefs(t, projectionAnswer(t, s, route, a, a).Gateways, gw)
	requireProjectionRefs(t, projectionAnswer(t, s, istio, a, a).Backends, vs)
	requireProjectionRefs(t, projectionAnswer(t, s, vs, a, a).RoutedFrom, istio)
	requireProjectionRefs(t, projectionAnswer(t, s, broker, a, a).Backends, trigger)
	requireProjectionRefs(t, projectionAnswer(t, s, trigger, a, a).RoutedFrom, broker)
	requireProjectionRefs(t, projectionAnswer(t, s, mhc, a, a).Dependencies, cluster)
	requireProjectionRefs(t, projectionAnswer(t, s, cluster, a, a).HealthChecks, mhc)
}
func TestRelationsAnswerManagementFallbacks(t *testing.T) {
	hr := projectionCR("helm.toolkit.fluxcd.io", "HelmRelease", "flux", "release", nil)
	d := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app", Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "release", "helm.toolkit.fluxcd.io/namespace": "flux"}}}, "apps", "Deployment")
	cron := projectionCR("argoproj.io", "CronWorkflow", "shop", "cron", nil)
	wf := projectionCR("argoproj.io", "Workflow", "shop", "run", nil)
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "cron"})
	a := ownership.Unrestricted()
	s := newProjectionSession(t, hr, d, cron, wf)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, a).Manages, d)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, a).Managers, hr)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, cron, a).Manages, wf)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, wf, a).Managers, cron)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, projectionAccess{namespaces: []string{"flux"}}).Manages)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, projectionAccess{denied: []string{"HelmRelease"}}).Managers)
	for _, fields := range []map[string]any{
		{"status": map[string]any{"inventory": map[string]any{"entries": []any{}}}},
		{"spec": map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "remote"}}}},
	} {
		release := projectionCR(hr.GroupVersionKind().Group, "HelmRelease", "flux", "release", fields)
		s = newProjectionSession(t, release, d)
		requireProjectionRefs(t, projectionOwnedAnswer(t, s, release, a).Manages)
		requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, a).Managers)
	}
	hook := d.(*appsv1.Deployment).DeepCopy()
	hook.Annotations = map[string]string{"helm.sh/hook": "pre-install"}
	s = newProjectionSession(t, hr, hook)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, a).Manages)
}
