package topology

import "testing"

// KindForGVK is the bridge between (obj.Kind, obj.Group) and the topology
// builder's pseudo-kind node-ID prefix. The builder emits pseudo-kinds for
// CRDs whose Kind collides with a core kind under a different group
// (Knative Service vs core Service, CAPI Cluster vs… nothing today but a
// future "Cluster" core kind, Istio Gateway vs Gateway API Gateway).
//
// A regression in this helper silently routes single-resource relationship
// lookups for those CRDs to the wrong topology node, so the table covers
// every group remapping plus the pass-through cases.
func TestKindForGVK(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		group string
		want  string
	}{
		// Knative Serving collisions.
		{"knative service", "Service", "serving.knative.dev", "knativeservice"},
		{"knative configuration", "Configuration", "serving.knative.dev", "knativeconfiguration"},
		{"knative revision", "Revision", "serving.knative.dev", "knativerevision"},
		{"knative route", "Route", "serving.knative.dev", "knativeroute"},
		// CAPI collision (Cluster, distinct from any future "Cluster" core kind).
		{"capi cluster", "Cluster", "cluster.x-k8s.io", "capicluster"},
		// Istio Gateway collision (vs Gateway API's gateway.networking.k8s.io/Gateway).
		{"istio gateway", "Gateway", "networking.istio.io", "istiogateway"},

		// Pass-through: core kinds (group == "").
		{"core service passthrough", "Service", "", "Service"},
		{"core pod passthrough", "Pod", "", "Pod"},
		// Pass-through: apps group.
		{"apps deployment passthrough", "Deployment", "apps", "Deployment"},
		{"batch job passthrough", "Job", "batch", "Job"},
		// Pass-through: Gateway API (uses the gateway.networking.k8s.io group,
		// distinct from networking.istio.io — must NOT be remapped to istiogateway).
		{"gateway api gateway passthrough", "Gateway", "gateway.networking.k8s.io", "Gateway"},
		// Pass-through: non-colliding CRDs.
		{"argo application passthrough", "Application", "argoproj.io", "Application"},
		{"cert-manager certificate passthrough", "Certificate", "cert-manager.io", "Certificate"},
		// Pass-through: a Kind that matches a Knative collision but under the
		// wrong group must NOT remap. Guards against accidental kind-only
		// matching that would mis-classify e.g. core Route or future CRDs.
		{"route under wrong group", "Route", "route.openshift.io", "Route"},
		{"service under wrong group", "Service", "argoproj.io", "Service"},
		// Empty kind: pass-through (caller's problem to validate).
		{"empty kind", "", "serving.knative.dev", ""},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := KindForGVK(tc.kind, tc.group)
			if got != tc.want {
				t.Errorf("KindForGVK(%q, %q) = %q, want %q", tc.kind, tc.group, got, tc.want)
			}
		})
	}
}
