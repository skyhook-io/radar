package audit

import "testing"

// TestResourceKey_GroupAware pins that two resources sharing
// kind+namespace+name but in different API groups produce distinct
// keys. Pre-fix, ResourceKey was group-blind: a Knative
// serving.knative.dev/Service "api" in "prod" collided with the core
// "" /Service "api" in "prod", and IndexByResource would conflate
// their findings (whichever Finding came last would shadow the other
// in the dedup checkKey, and any lookup by ResourceKey returned the
// pooled set). The fix routes Group through the key.
func TestResourceKey_GroupAware(t *testing.T) {
	core := ResourceKey("", "Service", "prod", "api")
	knative := ResourceKey("serving.knative.dev", "Service", "prod", "api")
	if core == knative {
		t.Fatalf("ResourceKey collides across groups: %q == %q", core, knative)
	}
}

// TestIndexByResource_NoCrossGroupCollision exercises the same fix
// end-to-end: emit two Findings for kind/ns/name "Service/prod/api",
// one with Group="" (core) and one with Group="serving.knative.dev"
// (Knative), and verify each lookup returns ONLY its own finding —
// not the union.
func TestIndexByResource_NoCrossGroupCollision(t *testing.T) {
	findings := []Finding{
		{Kind: "Service", Group: "", Namespace: "prod", Name: "api", CheckID: "core-finding"},
		{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", CheckID: "knative-finding"},
	}
	idx := IndexByResource(findings)

	core := idx[ResourceKey("", "Service", "prod", "api")]
	if len(core) != 1 || core[0].CheckID != "core-finding" {
		t.Errorf("core lookup: got %+v, want 1 finding with CheckID=core-finding", core)
	}
	knative := idx[ResourceKey("serving.knative.dev", "Service", "prod", "api")]
	if len(knative) != 1 || knative[0].CheckID != "knative-finding" {
		t.Errorf("knative lookup: got %+v, want 1 finding with CheckID=knative-finding", knative)
	}
}

// TestGroupForBuiltinKind pins the (Kind→Group) table used by
// buildResults to populate Finding.Group for emission sites that leave
// it empty. Centralising the table here keeps per-check code terse;
// drift between this table and the actual API group a check scans
// would silently mis-key findings.
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
