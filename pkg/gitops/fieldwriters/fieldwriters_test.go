package fieldwriters

import (
	"reflect"
	"testing"
)

func TestLeafPathsFlattensFieldsV1(t *testing.T) {
	raw := []byte(`{
		"f:metadata": {"f:annotations": {"f:kubectl.kubernetes.io/restartedAt": {}}},
		"f:spec": {
			"f:replicas": {},
			"f:template": {"f:spec": {"f:containers": {
				"k:{\"name\":\"engine\"}": {
					".": {}, "f:image": {},
					"f:ports": {"k:{\"containerPort\":80,\"protocol\":\"TCP\"}": {".": {}}}
				}
			}}}
		}
	}`)
	got, err := LeafPaths(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`metadata.annotations["kubectl.kubernetes.io/restartedAt"]`,
		"spec.replicas",
		"spec.template.spec.containers[name=engine].image",
		"spec.template.spec.containers[name=engine].ports[containerPort=80,protocol=TCP]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q\nwant %q", got, want)
	}
	if _, err := LeafPaths([]byte(`{"f:spec":`)); err == nil {
		t.Fatal("malformed fieldsV1 accepted")
	}
}

func TestLeafPathsSkipsValueAndIndexMembers(t *testing.T) {
	got, err := LeafPaths([]byte(`{"f:spec": {"f:finalizers": {"v:\"x\"": {}}, "f:args": {"i:0": {}}, "f:paused": {}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"spec.args", "spec.finalizers", "spec.paused"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestClassifyManagers(t *testing.T) {
	owners := map[string]bool{"helm-controller": true}
	radar := map[string]bool{"radar": true}
	for manager, want := range map[string]Class{
		"helm-controller":         ClassOwner,
		"kubectl-patch":           ClassHand,
		"kubectl":                 ClassHand,
		"node-fetch":              ClassHand,
		"k9s":                     ClassHand,
		"radar":                   ClassHand,
		"Radar":                   ClassHand,
		"helm":                    ClassOther,
		"kube-controller-manager": ClassOther,
		"Runner":                  ClassOther,
		"fleetagent":              ClassOther,
	} {
		if got := Classify(manager, owners, radar); got != want {
			t.Errorf("Classify(%q) = %q, want %q", manager, got, want)
		}
	}
}

func TestToolNames(t *testing.T) {
	radar := map[string]bool{"radar": true}
	for manager, want := range map[string]string{
		"kubectl-patch":             "kubectl patch",
		"kubectl-client-side-apply": "kubectl apply",
		"kubectl-rollout":           "kubectl rollout",
		"kubectl":                   "kubectl",
		"node-fetch":                "Lens",
		"radar":                     "Radar",
		"Radar":                     "Radar",
		"k9s":                       "k9s",
	} {
		if got := ToolName(manager, radar); got != want {
			t.Errorf("ToolName(%q) = %q, want %q", manager, got, want)
		}
	}
}

func TestSplitHarmless(t *testing.T) {
	restart := `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`
	reconcile := `metadata.annotations["reconcile.fluxcd.io/requestedAt"]`
	for _, tc := range []struct {
		in     []string
		kept   []string
		labels []string
	}{
		{[]string{restart}, nil, []string{"kubectl rollout restart"}},
		{[]string{restart, "spec.replicas"}, []string{"spec.replicas"}, []string{"kubectl rollout restart"}},
		{[]string{reconcile}, nil, []string{"Flux reconcile request"}},
		{[]string{"metadata.labels.k8slens-edit-resource-version"}, nil, []string{"Lens edit marker"}},
		{[]string{restart, reconcile, `metadata.annotations["reconcile.fluxcd.io/forceAt"]`}, nil, []string{"Flux reconcile request", "kubectl rollout restart"}},
		{[]string{"spec.replicas"}, []string{"spec.replicas"}, nil},
	} {
		kept, labels := SplitHarmless(tc.in)
		if !reflect.DeepEqual(kept, tc.kept) || !reflect.DeepEqual(labels, tc.labels) {
			t.Errorf("SplitHarmless(%q) = %q, %q; want %q, %q", tc.in, kept, labels, tc.kept, tc.labels)
		}
	}
}

func TestLeafPathsQuotesKeysTheParserReads(t *testing.T) {
	got, err := LeafPaths([]byte(`{"f:metadata": {"f:annotations": {"f:a\"b": {}, "f:c\\d": {}, "f:e'f\"g": {}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`metadata.annotations["c\d"]`, `metadata.annotations['a"b']`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q (a key with both quote kinds can't be written and is left out)", got, want)
	}
}
