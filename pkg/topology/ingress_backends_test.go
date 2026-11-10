package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIngressRoutesToDefaultBackendAndEachServiceOnce(t *testing.T) {
	backend := func(name string) networkingv1.IngressBackend {
		return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name, Port: networkingv1.ServiceBackendPort{Number: 80}}}
	}
	path := func(p, svc string) networkingv1.HTTPIngressPath {
		return networkingv1.HTTPIngressPath{Path: p, Backend: backend(svc)}
	}
	fallback := backend("fallback")
	provider := &mockProvider{
		ingresses: []*networkingv1.Ingress{
			{ObjectMeta: metav1.ObjectMeta{Name: "catch-all", Namespace: "team"}, Spec: networkingv1.IngressSpec{DefaultBackend: &fallback}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
				Host: "web.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
					path("/", "web"), path("/api", "web"), path("/static", "web"),
				}}},
			}}}},
		},
		services: []*corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "fallback", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "fallback"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}},
		},
	}
	for _, mode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(mode), func(t *testing.T) {
			opts := DefaultBuildOptions()
			opts.ViewMode = mode
			topo, err := NewBuilder(provider).Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			count := map[string]int{}
			for _, edge := range topo.Edges {
				count[edge.ID]++
			}
			for _, id := range []string{"ingress/team/catch-all-to-service/team/fallback", "ingress/team/web-to-service/team/web"} {
				if count[id] != 1 {
					t.Errorf("edge %s appears %d times, want once", id, count[id])
				}
			}
		})
	}
}
