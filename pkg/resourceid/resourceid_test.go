package resourceid

import "testing"

// TestResourceKey_GroupAware pins that two resources sharing
// kind+namespace+name but in different API groups produce distinct keys, so
// indexes keyed by ResourceKey can't conflate a Knative serving.knative.dev/
// Service with the core /Service of the same name.
func TestResourceKey_GroupAware(t *testing.T) {
	core := ResourceKey("", "Service", "prod", "api")
	knative := ResourceKey("serving.knative.dev", "Service", "prod", "api")
	if core == knative {
		t.Fatalf("ResourceKey collides across groups: %q == %q", core, knative)
	}
}

// TestGroupForBuiltinKind pins the (Kind→Group) table. Drift between this table
// and the actual API group a consumer scans would silently mis-key resources.
func TestGroupForBuiltinKind(t *testing.T) {
	cases := map[string]string{
		"Pod":                     "",
		"Service":                 "",
		"ConfigMap":               "",
		"Secret":                  "",
		"Deployment":              "apps",
		"StatefulSet":             "apps",
		"DaemonSet":               "apps",
		"ReplicaSet":              "apps",
		"Job":                     "batch",
		"CronJob":                 "batch",
		"HorizontalPodAutoscaler": "autoscaling",
		"Ingress":                 "networking.k8s.io",
		"NetworkPolicy":           "networking.k8s.io",
		"PodDisruptionBudget":     "policy",
		"UnknownCRD":              "",
	}
	for kind, want := range cases {
		if got := GroupForBuiltinKind(kind); got != want {
			t.Errorf("GroupForBuiltinKind(%q) = %q, want %q", kind, got, want)
		}
	}
}
