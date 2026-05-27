package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestTypedKindOwnsGroup pins the typed-vs-dynamic routing contract used by the
// resource GET/LIST handlers (REST + AI). The bug this guards: the SPA threads
// the real apiGroup for built-in workloads (deployments?group=apps), and the
// handlers' "explicit group ⇒ dynamic cache" dispatch sent those to the dynamic
// cache — which has no informer for built-ins — yielding a 400 "unknown
// resource kind: deployments (group: apps)" and an empty resource drawer.
//
// A built-in kind addressed by its OWN group must resolve via the typed cache
// (TypedKindOwnsGroup == true); a CRD whose plural shadows a core kind must NOT
// (so it still routes to the dynamic cache).
func TestTypedKindOwnsGroup(t *testing.T) {
	cases := []struct {
		kind  string
		group string
		want  bool
	}{
		// Built-in workloads addressed by their real group — the regression.
		{"deployments", "apps", true},
		{"deployment", "apps", true},
		{"statefulsets", "apps", true},
		{"daemonsets", "apps", true},
		{"replicasets", "apps", true},
		{"jobs", "batch", true},
		{"cronjobs", "batch", true},
		{"horizontalpodautoscalers", "autoscaling", true},
		{"hpa", "autoscaling", true},
		{"ingresses", "networking.k8s.io", true},
		{"networkpolicies", "networking.k8s.io", true},
		{"netpols", "networking.k8s.io", true},
		{"poddisruptionbudgets", "policy", true},
		{"storageclasses", "storage.k8s.io", true},
		{"clusterroles", "rbac.authorization.k8s.io", true},

		// Core kinds: own group is "" — typed, with or without an explicit "".
		{"pods", "", true},
		{"services", "", true},
		{"sa", "", true},
		{"secrets", "", true},

		// CRD whose plural shadows a core/built-in kind — must stay dynamic.
		{"services", "serving.knative.dev", false},
		{"deployments", "argoproj.io", false},

		// A built-in addressed with the WRONG group — not owned, routes dynamic
		// (and discovery will reject it).
		{"deployments", "batch", false},

		// Genuine CRD kinds — never typed.
		{"widgets", "example.com", false},
		{"applications", "argoproj.io", false},
	}
	for _, tc := range cases {
		if got := TypedKindOwnsGroup(tc.kind, tc.group); got != tc.want {
			t.Errorf("TypedKindOwnsGroup(%q, %q) = %v, want %v", tc.kind, tc.group, got, tc.want)
		}
	}
}

// TestBuiltinGVR pins the static GVR fallback used when API discovery can't
// resolve a built-in (partial discovery) — the safety net that keeps GitOps
// drift's live last-applied GET from silently returning nil for built-in
// managed resources. The GVR must carry the canonical plural + GA version even
// when addressed by a singular/abbreviation form, and must reject CRD-group
// collisions.
func TestBuiltinGVR(t *testing.T) {
	cases := []struct {
		kind, group string
		want        schema.GroupVersionResource
		ok          bool
	}{
		{"deployment", "apps", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, true},
		{"Deployment", "apps", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, true},
		{"pdb", "policy", schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, true},
		{"hpa", "autoscaling", schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, true},
		{"pods", "", schema.GroupVersionResource{Version: "v1", Resource: "pods"}, true},
		{"sa", "", schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
		{"netpols", "networking.k8s.io", schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, true},
		{"services", "serving.knative.dev", schema.GroupVersionResource{}, false}, // CRD collision
		{"widgets", "example.com", schema.GroupVersionResource{}, false},          // genuine CRD
	}
	for _, tc := range cases {
		got, ok := BuiltinGVR(tc.kind, tc.group)
		if ok != tc.ok || got != tc.want {
			t.Errorf("BuiltinGVR(%q, %q) = (%v, %v), want (%v, %v)", tc.kind, tc.group, got, ok, tc.want, tc.ok)
		}
	}
}
