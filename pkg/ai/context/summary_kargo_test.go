package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// kargoFixture reads real objects from a Kargo v1.12.7 project.
func kargoFixture(t *testing.T, file string) map[string]*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "kargo", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := yaml.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]*unstructured.Unstructured{}
	for _, item := range list.Items {
		u := &unstructured.Unstructured{Object: item}
		out[u.GetName()] = u
	}
	return out
}

func TestSummarizeKargoStages(t *testing.T) {
	stages := kargoFixture(t, "stages.yaml")

	prod := summarizeUnstructured(stages["prod"])
	if prod.Status != "Ready" || prod.Issue != "" || prod.Kargo == nil || !slices.Equal(prod.Kargo.Versions, []string{"nginx:1.31.9"}) ||
		!slices.Equal(prod.Kargo.Sources, []string{"uat"}) || !slices.Equal(prod.Kargo.ArgoCDApps, []string{"argocd/kargo-demo-prod"}) {
		t.Fatalf("prod = %+v %+v", prod, prod.Kargo)
	}
	test := summarizeUnstructured(stages["test"])
	if test.Status != "Failed" || !strings.HasPrefix(test.Issue, "last promotion errored: ") || !strings.Contains(test.Issue, "error pushing") ||
		test.Kargo.FailedStep != "promo-process::step-6" || !slices.Equal(test.Kargo.Sources, []string{"Warehouse/kargo-demo"}) {
		t.Fatalf("test = %+v %+v", test, test.Kargo)
	}
	uat := summarizeUnstructured(stages["uat"])
	if !strings.HasPrefix(uat.Issue, "verification failed: ") || !strings.Contains(uat.Issue, "(AnalysisRun uat.") {
		t.Fatalf("uat = %+v", uat)
	}
}

func TestSummarizeKargoFreightPromotionWarehouse(t *testing.T) {
	for _, u := range kargoFixture(t, "freight.yaml") {
		s := summarizeUnstructured(u)
		if s.Kargo == nil || s.Kargo.Alias == "" || s.Kargo.Origin != "Warehouse/kargo-demo" || len(s.Kargo.Versions) != 1 || !strings.HasPrefix(s.Kargo.Versions[0], "nginx:1.31.") {
			t.Fatalf("freight = %+v %+v", s, s.Kargo)
		}
	}
	failed, errored := 0, 0
	for _, u := range kargoFixture(t, "promotions.yaml") {
		s := summarizeUnstructured(u)
		switch s.Status {
		case "Failed":
			failed++
			if s.Kargo.FailedStep != "release-gate" || !strings.Contains(s.Issue, "change freeze") {
				t.Fatalf("failed promotion = %+v %+v", s, s.Kargo)
			}
		case "Errored":
			errored++
			if s.Kargo.FailedStep != "promo-process::step-6" || s.Kargo.Stage != "test" {
				t.Fatalf("errored promotion = %+v %+v", s, s.Kargo)
			}
		case "Succeeded":
			if s.Issue != "" || s.Kargo.FailedStep != "" {
				t.Fatalf("succeeded promotion = %+v", s)
			}
		}
	}
	if failed != 1 || errored != 1 {
		t.Fatalf("failed %d errored %d", failed, errored)
	}
	for _, u := range kargoFixture(t, "warehouses.yaml") {
		s := summarizeUnstructured(u)
		if s.Kind != "Warehouse" || s.Status != "Ready" || s.Issue != "" {
			t.Fatalf("warehouse = %+v", s)
		}
	}

	aborted := kargoFixture(t, "stages.yaml")["test"]
	_ = unstructured.SetNestedField(aborted.Object, "Aborted", "status", "lastPromotion", "status", "phase")
	if s := summarizeUnstructured(aborted); strings.Contains(s.Issue, "promotion") {
		t.Fatalf("an aborted promotion is not an issue: %q", s.Issue)
	}
	other := kargoFixture(t, "stages.yaml")["prod"]
	other.SetAPIVersion("example.com/v1")
	if s := summarizeUnstructured(other); s.Kargo != nil {
		t.Fatalf("another tool's Stage got a Kargo summary: %+v", s.Kargo)
	}
}

// Promotion step config is free-form, so an inline credential in it must
// not reach an agent through the detail view.
func TestKargoPromotionDetailRedactsInlineStepCredentials(t *testing.T) {
	const token = "Bearer abcdefghijklmnopqrstuvwxyz0123456789"
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Promotion",
		"metadata": map[string]any{"name": "uat.1", "namespace": "shop"},
		"spec": map[string]any{"stage": "uat", "steps": []any{map[string]any{
			"uses": "http", "as": "notify",
			"config": map[string]any{"url": "https://hooks.example/x", "headers": []any{map[string]any{"name": "Authorization", "value": token}}},
		}}},
		"status": map[string]any{"phase": "Succeeded", "state": map[string]any{"notify": map[string]any{"token": "s3cr3t-value"}}},
	}}
	out, err := json.Marshal(MinifyUnstructured(u, LevelDetail))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "abcdefghijklmnopqrstuvwxyz0123456789") || strings.Contains(string(out), "s3cr3t-value") {
		t.Fatalf("detail leaks a credential: %s", out)
	}
	if !strings.Contains(string(out), "https://hooks.example/x") {
		t.Fatalf("detail dropped diagnostic config: %s", out)
	}
}

// Discovery and waiting states are not issues an agent should chase.
func TestSummarizeKargoWaitingIsNoIssue(t *testing.T) {
	for _, reason := range []string{"DiscoveryInProgress", "AwaitingFreightCreation"} {
		w := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Warehouse", "metadata": map[string]any{"name": "w", "namespace": "p"},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": "Discovering artifacts"}}},
		}}
		if s := summarizeUnstructured(w); s.Issue != "" {
			t.Errorf("%s: issue %q", reason, s.Issue)
		}
	}
	stage := kargoFixture(t, "stages.yaml")["prod"]
	_ = unstructured.SetNestedSlice(stage.Object, []any{map[string]any{"type": "Ready", "status": "False", "reason": "WaitingForHealthCheck"}}, "status", "conditions")
	if s := summarizeUnstructured(stage); s.Status == "Failed" || s.Issue != "" {
		t.Errorf("waiting stage = %q %q", s.Status, s.Issue)
	}
}
