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
		"IngressClass":            "networking.k8s.io",
		"PodDisruptionBudget":     "policy",
		"StorageClass":            "storage.k8s.io",
		"Role":                    "rbac.authorization.k8s.io",
		"ClusterRoleBinding":      "rbac.authorization.k8s.io",
		"LimitRange":              "",
		"ResourceQuota":           "",
		"Event":                   "",
		"UnknownCRD":              "",
	}
	for kind, want := range cases {
		if got := GroupForBuiltinKind(kind); got != want {
			t.Errorf("GroupForBuiltinKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestBuiltinAPIVersion(t *testing.T) {
	for _, test := range []struct {
		kind string
		want string
		ok   bool
	}{
		{kind: "Service", want: "v1", ok: true},
		{kind: "Deployment", want: "apps/v1", ok: true},
		{kind: "Job", want: "batch/v1", ok: true},
		{kind: "HorizontalPodAutoscaler", want: "autoscaling/v2", ok: true},
		{kind: "PodDisruptionBudget", want: "policy/v1", ok: true},
		{kind: "IngressClass", want: "networking.k8s.io/v1", ok: true},
		{kind: "StorageClass", want: "storage.k8s.io/v1", ok: true},
		{kind: "Role", want: "rbac.authorization.k8s.io/v1", ok: true},
		{kind: "LimitRange", want: "v1", ok: true},
		{kind: "ResourceQuota", want: "v1", ok: true},
		{kind: "Widget"},
	} {
		got, ok := BuiltinAPIVersion(test.kind)
		if got != test.want || ok != test.ok {
			t.Errorf("BuiltinAPIVersion(%q) = (%q, %v), want (%q, %v)", test.kind, got, ok, test.want, test.ok)
		}
	}
}

func TestBuiltinGroupDistinguishesCoreFromCustom(t *testing.T) {
	if group, ok := BuiltinGroup("Service"); !ok || group != "" {
		t.Fatalf("BuiltinGroup(Service) = %q, %v; want core group and recognized", group, ok)
	}
	if group, ok := BuiltinGroup("Widget"); ok || group != "" {
		t.Fatalf("BuiltinGroup(Widget) = %q, %v; want unrecognized", group, ok)
	}
}
