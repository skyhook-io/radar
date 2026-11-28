package kargo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// The testdata objects come from a live Kargo v1.12.7 project: prod holds
// nginx 1.31.9; test and uat hold 1.31.10, which failed verification in uat;
// test's last promotion Errored at git-push; an older prod promotion Failed
// and a later one Succeeded.
func fixture(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := yaml.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := make([]*unstructured.Unstructured, len(list.Items))
	for i, item := range list.Items {
		out[i] = &unstructured.Unstructured{Object: item}
	}
	return out
}

func fixtureStages(t *testing.T) map[string]Stage {
	t.Helper()
	out := map[string]Stage{}
	for _, u := range fixture(t, "stages.yaml") {
		if !Is(u, KindStage) {
			t.Fatalf("not a Kargo Stage: %v", u.GetAPIVersion())
		}
		out[u.GetName()] = ParseStage(u)
	}
	return out
}

func TestFixtureStages(t *testing.T) {
	stages := fixtureStages(t)
	tag := func(s Stage) string { return s.Current[0].Images[0].Tag }
	if tag(stages["prod"]) != "1.31.9" || tag(stages["uat"]) != "1.31.10" || tag(stages["test"]) != "1.31.10" {
		t.Fatalf("current freight: prod %s uat %s test %s", tag(stages["prod"]), tag(stages["uat"]), tag(stages["test"]))
	}
	uat := stages["uat"]
	if v := uat.Verification; v == nil || !v.Failed() || v.AnalysisRun == nil || !strings.HasPrefix(v.AnalysisRun.Name, "uat.") || !strings.Contains(v.Message, "error-rate") {
		t.Fatalf("uat verification = %+v", uat.Verification)
	}
	test := stages["test"]
	f := test.LastPromotion.Failure()
	if test.LastPromotion.Phase != PhaseErrored || !f.Failed || f.StepAlias != "promo-process::step-6" || !strings.Contains(f.Message, "error pushing") {
		t.Fatalf("test last promotion = %+v, failure %+v", test.LastPromotion, f)
	}
	if prod := stages["prod"]; prod.LastPromotion.Phase != PhaseSucceeded || prod.LastPromotion.Failure().Failed {
		t.Fatalf("prod's failed promotion is history: %+v", prod.LastPromotion)
	}
	if apps := stages["prod"].ArgoCDApps; !slices.Equal(apps, []ObjectRef{{Namespace: "argocd", Name: "kargo-demo-prod"}}) {
		t.Fatalf("prod apps = %v", apps)
	}
	if h := stages["prod"].Health; h == nil || len(h.Apps) != 1 || h.Apps[0].Name != "kargo-demo-prod" || h.Apps[0].Health != "Healthy" || h.Apps[0].Sync != "Synced" {
		t.Fatalf("prod health = %+v", h)
	}
	if Phase(fixture(t, "stages.yaml")[0]) != "Ready" {
		t.Fatal("prod is Ready")
	}

	var list []Stage
	for _, s := range stages {
		list = append(list, s)
	}
	var order []string
	for _, p := range Pipeline(list) {
		order = append(order, p.Name)
	}
	if !slices.Equal(order, []string{"test", "uat", "prod"}) {
		t.Fatalf("pipeline = %v", order)
	}
}

func TestFixtureWhyNotHere(t *testing.T) {
	stages := fixtureStages(t)
	var list []Stage
	for _, s := range stages {
		list = append(list, s)
	}
	freight := fixture(t, "freight.yaml")
	now := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)

	prod := WhyNotHere(stages["prod"], NewProject(list, freight, true), now)
	if len(prod) != 1 || prod[0].State != LagBehind || prod[0].Reason != ReasonVerificationFailed || !slices.Equal(prod[0].Stages, []string{"uat"}) || prod[0].Newest.Images[0].Tag != "1.31.10" {
		t.Fatalf("prod = %+v", prod)
	}
	if want := "Failed verification in uat (error-rate). It can't reach prod until it passes there."; prod[0].Message != want {
		t.Fatalf("prod message = %q, want %q", prod[0].Message, want)
	}
	if len(prod[0].AnalysisRuns) != 1 || !strings.HasPrefix(prod[0].AnalysisRuns[0].Name, "uat.") {
		t.Fatalf("prod analysis runs = %v", prod[0].AnalysisRuns)
	}
	if denied := WhyNotHere(stages["prod"], NewProject(list, nil, false), now); denied[0].State != LagUnknown || denied[0].Cause != CauseFreightUnreadable || denied[0].Newest == nil {
		t.Fatalf("prod without Freight access = %+v", denied)
	}
	// test takes Freight straight from the Warehouse, whose newest by
	// discovery is the hand-made 1.31.9, which Kargo would pick; its errored
	// last promotion was of 1.31.10, so it isn't the answer, and
	// auto-promotion is off for test.
	test := WhyNotHere(stages["test"], NewProject(list, freight, true), now)
	if test[0].Reason != ReasonAutoPromotionOff || test[0].Newest.Images[0].Tag != "1.31.9" {
		t.Fatalf("test = %+v", test)
	}
	// uat holds what test holds, but Kargo still offers it the hand-made
	// 1.31.9, verified in test and newer by discovery.
	if uat := WhyNotHere(stages["uat"], NewProject(list, freight, true), now); uat[0].Reason != ReasonAutoPromotionOff || uat[0].Newest.Images[0].Tag != "1.31.9" {
		t.Fatalf("uat = %+v", uat)
	}
	for _, u := range freight {
		if f := ParseFreight(u); f.Alias == "" || len(f.Labels()) != 1 || !strings.HasPrefix(f.Labels()[0], "nginx:1.31.") {
			t.Fatalf("freight = %+v", f)
		}
	}
	if since, ok := Since(freight[0], "uat"); !ok || since.IsZero() {
		t.Fatal("since comes from the Freight's currentlyIn")
	}
}

func TestFixturePromotions(t *testing.T) {
	var failed, errored int
	for _, u := range fixture(t, "promotions.yaml") {
		p := ParsePromotion(u)
		f := p.Failure()
		switch p.Phase {
		case PhaseFailed:
			failed++
			if f.StepAlias != "release-gate" || !strings.Contains(f.Message, "change freeze") || p.Steps[f.Step].Uses != "fail" {
				t.Fatalf("failed promotion %s: %+v", p.Name, f)
			}
		case PhaseErrored:
			errored++
			if p.Steps[f.Step].Uses != "git-push" || p.Stage != "test" {
				t.Fatalf("errored promotion %s: %+v", p.Name, f)
			}
		default:
			if f.Failed {
				t.Fatalf("%s %s has a failure", p.Name, p.Phase)
			}
		}
	}
	if failed != 1 || errored != 1 {
		t.Fatalf("failed %d, errored %d", failed, errored)
	}
}
