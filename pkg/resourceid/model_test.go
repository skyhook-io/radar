package resourceid

import "testing"

func TestSplitAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		in, group, version string
	}{
		{"", "", ""},
		{"v1", "", "v1"},
		{"apps/v1", "apps", "v1"},
		{"cluster.x-k8s.io/v1beta1", "cluster.x-k8s.io", "v1beta1"},
		// Kubernetes treats any slash-less apiVersion as a core-group version.
		{"v1beta1", "", "v1beta1"},
	} {
		group, version := SplitAPIVersion(tc.in)
		if group != tc.group || version != tc.version {
			t.Errorf("SplitAPIVersion(%q) = (%q, %q), want (%q, %q)", tc.in, group, version, tc.group, tc.version)
		}
		if got := GroupFromAPIVersion(tc.in); got != tc.group {
			t.Errorf("GroupFromAPIVersion(%q) = %q, want %q", tc.in, got, tc.group)
		}
	}
}

func TestAPIVersionRoundTrips(t *testing.T) {
	for _, av := range []string{"v1", "apps/v1", "batch.volcano.sh/v1alpha1"} {
		if got := APIVersion(SplitAPIVersion(av)); got != av {
			t.Errorf("APIVersion(SplitAPIVersion(%q)) = %q", av, got)
		}
	}
}

func TestNormalizeGroup(t *testing.T) {
	for in, want := range map[string]string{"": "", "core": "", "apps": "apps", "cert-manager.io": "cert-manager.io"} {
		if got := NormalizeGroup(in); got != want {
			t.Errorf("NormalizeGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRef(t *testing.T) {
	core := NewRef("core", "Job", "ml", "train")
	volcano := NewRef("batch.volcano.sh", "Job", "ml", "train")
	if core.Group != "" {
		t.Fatalf("NewRef kept the core spelling: %+v", core)
	}
	if core.Key() == volcano.Key() {
		t.Fatalf("refs in different groups share a key: %q", core.Key())
	}
	if core.Key() != ResourceKey("", "Job", "ml", "train") {
		t.Fatalf("Ref.Key diverged from ResourceKey: %q", core.Key())
	}
	if got := volcano.String(); got != "Job.batch.volcano.sh ml/train" {
		t.Errorf("String() = %q", got)
	}
	if got := NewRef("", "Node", "", "n1").String(); got != "Node n1" {
		t.Errorf("cluster-scoped String() = %q", got)
	}
}
