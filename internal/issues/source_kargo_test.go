package issues

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/kargo"
)

var kargoStageGVR = schema.GroupVersionResource{Group: "kargo.akuity.io", Version: "v1alpha1", Resource: "stages"}

// kargoStages are real Stages from a Kargo v1.12.7 project: test's last
// promotion Errored at git-push, uat's verification Failed, prod is Ready
// after a failed promotion that a later one superseded.
func kargoStages(t *testing.T) map[string]*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "kargo", "testdata", "stages.yaml"))
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

func TestKargoStageIssues(t *testing.T) {
	stages := kargoStages(t)

	test := KargoStageIssues(kargoStageGVR, stages["test"])
	if len(test) != 1 {
		t.Fatalf("test issues = %+v", test)
	}
	p := test[0]
	if p.Reason != ReasonKargoPromotionFailed || p.Kind != "Promotion" || p.Group != "kargo.akuity.io" || !strings.HasPrefix(p.Name, "test.") || p.Severity != SeverityCritical || p.OnsetUnknown {
		t.Fatalf("promotion issue = %+v", p)
	}
	for _, want := range []string{"nginx:1.31.10", "stage test errored", "promo-process::step-6", "error pushing"} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("message %q lacks %q", p.Message, want)
		}
	}
	wantReads := []EvidenceRead{{Group: "kargo.akuity.io", Resource: "stages", Namespace: "kargo-demo", Verb: "list", Exact: true}}
	if !slices.Equal(p.RequiredReads, wantReads) {
		t.Errorf("promotion required reads = %+v", p.RequiredReads)
	}

	uat := KargoStageIssues(kargoStageGVR, stages["uat"])
	if len(uat) != 1 || uat[0].Reason != ReasonKargoVerificationFailed || uat[0].Kind != "Stage" || uat[0].Name != "uat" || uat[0].Severity != SeverityCritical {
		t.Fatalf("uat issues = %+v", uat)
	}
	if !slices.Equal(uat[0].RequiredReads, wantReads) {
		t.Errorf("verification required reads = %+v", uat[0].RequiredReads)
	}
	for _, want := range []string{"nginx:1.31.10", "in stage uat failed", "error-rate", "AnalysisRun uat."} {
		if !strings.Contains(uat[0].Message, want) {
			t.Errorf("message %q lacks %q", uat[0].Message, want)
		}
	}

	if prod := KargoStageIssues(kargoStageGVR, stages["prod"]); len(prod) != 0 {
		t.Fatalf("prod's failed promotion is history: %+v", prod)
	}
}

func TestKargoStageIssuesSkipCancellationsAndOtherGroups(t *testing.T) {
	stages := kargoStages(t)
	for _, tc := range []struct {
		name   string
		change func(u *unstructured.Unstructured)
	}{
		{"aborted promotion", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "Aborted", "status", "lastPromotion", "status", "phase")
		}},
		{"running promotion", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "Running", "status", "lastPromotion", "status", "phase")
		}},
		{"another tool's Stage", func(u *unstructured.Unstructured) { u.SetAPIVersion("example.com/v1") }},
		{"being deleted", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "2026-11-25T13:00:00Z", "metadata", "deletionTimestamp")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := stages["test"].DeepCopy()
			tc.change(u)
			if got := KargoStageIssues(kargoStageGVR, u); len(got) != 0 {
				t.Fatalf("issues = %+v", got)
			}
		})
	}
	for _, phase := range []string{"Aborted", "Inconclusive", "Running", "Successful"} {
		u := stages["uat"].DeepCopy()
		history, _, _ := unstructured.NestedSlice(u.Object, "status", "freightHistory")
		current := history[0].(map[string]any)
		current["verificationHistory"].([]any)[0].(map[string]any)["phase"] = phase
		_ = unstructured.SetNestedSlice(u.Object, history, "status", "freightHistory")
		if got := KargoStageIssues(kargoStageGVR, u); len(got) != 0 {
			t.Errorf("verification %s: %+v", phase, got)
		}
	}
	u := stages["uat"].DeepCopy()
	history, _, _ := unstructured.NestedSlice(u.Object, "status", "freightHistory")
	history[0].(map[string]any)["verificationHistory"].([]any)[0].(map[string]any)["phase"] = "Error"
	_ = unstructured.SetNestedSlice(u.Object, history, "status", "freightHistory")
	if got := KargoStageIssues(kargoStageGVR, u); len(got) != 1 || !strings.Contains(got[0].Message, "couldn't run") {
		t.Errorf("verification Error: %+v", got)
	}
}

// The generic condition walk doesn't repeat what the Kargo findings say on
// Ready, and an aborted promotion files nothing, but a Stage's other
// conditions still reach Issues.
func TestComposeKargoStagesFileEachProblemOnce(t *testing.T) {
	stages := kargoStages(t)
	unhealthy := stages["prod"].DeepCopy()
	unhealthy.SetName("prod-east")
	_ = unstructured.SetNestedSlice(unhealthy.Object, []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "Unhealthy", "message": "Stage is not healthy"},
	}, "status", "conditions")
	aborted := stages["test"].DeepCopy()
	aborted.SetName("test-aborted")
	_ = unstructured.SetNestedField(aborted.Object, "Aborted", "status", "lastPromotion", "status", "phase")
	_ = unstructured.SetNestedSlice(aborted.Object, []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "LastPromotionAborted"},
	}, "status", "conditions")

	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{kargoStageGVR: {stages["test"], stages["uat"], stages["prod"], unhealthy, aborted}},
		kinds:      map[schema.GroupVersionResource]string{kargoStageGVR: "Stage"},
		namespaced: map[schema.GroupVersionResource]bool{kargoStageGVR: true},
	}
	var reasons []string
	for _, i := range Compose(p, Filters{Limit: NoLimit, AllowUnfilteredEvidence: true}) {
		reasons = append(reasons, i.Kind+"/"+i.Name+"/"+i.Reason)
	}
	want := map[string]bool{"Stage/uat/" + ReasonKargoVerificationFailed: true, "Stage/prod-east/Ready: Unhealthy": true}
	var promotions int
	for _, r := range reasons {
		switch {
		case strings.HasPrefix(r, "Promotion/test.") && strings.HasSuffix(r, ReasonKargoPromotionFailed):
			promotions++
		case want[r]:
			delete(want, r)
		default:
			t.Errorf("unexpected issue %s", r)
		}
	}
	if promotions != 1 || len(want) != 0 {
		t.Fatalf("issues = %v; missing %v", reasons, want)
	}
	for _, i := range Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(read EvidenceRead) bool { return read.Resource != "stages" }}) {
		if i.Reason == ReasonKargoPromotionFailed {
			t.Errorf("a caller who can't read the Stage got its promotion finding: %+v", i)
		}
	}
}

// A promotion finding is read off its Stage, so a Stage's related Issues and
// a kind=Stage or kind=Promotion list all include it.
func TestKargoPromotionFindingIsFoundThroughItsStage(t *testing.T) {
	stages := kargoStages(t)
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{kargoStageGVR: {stages["test"]}},
		kinds:      map[schema.GroupVersionResource]string{kargoStageGVR: "Stage"},
		namespaced: map[schema.GroupVersionResource]bool{kargoStageGVR: true},
	}
	has := func(issues []Issue) bool {
		for _, i := range issues {
			if i.Reason == ReasonKargoPromotionFailed {
				return true
			}
		}
		return false
	}
	for _, kind := range []string{"Stage", "Promotion"} {
		if !has(Compose(p, Filters{Kinds: []string{kind}, Limit: NoLimit, AllowUnfilteredEvidence: true})) {
			t.Errorf("kind=%s misses the promotion finding", kind)
		}
	}
	flat := Compose(p, Filters{Limit: NoLimit, AllowUnfilteredEvidence: true})
	related := RelatedIssuesFrom(flat, GroupIssues(flat), RelatedIssueOptions{AllowUnfilteredEvidence: true, CanReadRelated: func(Ref) bool { return true }}, "kargo.akuity.io", "Stage", "kargo-demo", "test")
	if !has(related) {
		t.Errorf("the Stage's related Issues miss its promotion finding: %v", reasonsOf(related))
	}
}

// Kargo writes these Ready reasons while work is under way (Stage
// summarizeConditions, Warehouse discovery); none is a failure.
func TestKargoWaitingConditionsAreNotIssues(t *testing.T) {
	stages := kargoStages(t)
	warehouseGVR := schema.GroupVersionResource{Group: "kargo.akuity.io", Version: "v1alpha1", Resource: "warehouses"}
	var stageObjs []*unstructured.Unstructured
	for _, reason := range []string{"NoFreight", "ActivePromotion", "WaitingForHealthCheck", "WaitingForVerification", "PendingVerification", "VerificationPending", "VerificationRunning", "Progressing", "TargetAwareStage"} {
		u := stages["prod"].DeepCopy()
		u.SetName("waiting-" + strings.ToLower(reason))
		_ = unstructured.SetNestedSlice(u.Object, []any{
			map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": "waiting"},
			map[string]any{"type": "Healthy", "status": "False", "reason": reason, "message": "waiting"},
		}, "status", "conditions")
		stageObjs = append(stageObjs, u)
	}
	var warehouses []*unstructured.Unstructured
	for _, reason := range []string{"DiscoveryInProgress", "AwaitingFreightCreation"} {
		warehouses = append(warehouses, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Warehouse",
			"metadata": map[string]any{"name": "w-" + strings.ToLower(reason), "namespace": "kargo-demo"},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": "Discovering artifacts"},
				map[string]any{"type": "Healthy", "status": "Unknown", "reason": "Pending"},
			}},
		}})
	}
	// After a discovery that found nothing new, or whose creation criteria
	// held Freight back, Kargo leaves these False; they report what the
	// discovery did, not health.
	warehouses = append(warehouses, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Warehouse",
		"metadata": map[string]any{"name": "w-nothing-new", "namespace": "kargo-demo"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "ArtifactsDiscovered"},
			map[string]any{"type": "Healthy", "status": "True", "reason": "ReconciliationSucceeded"},
			map[string]any{"type": "FreightCreated", "status": "False", "reason": "AlreadyExists", "message": "Freight composed of the newest artifacts already exists"},
			map[string]any{"type": "FreightCreationCriteriaSatisfied", "status": "False", "reason": "CriteriaNotSatisfied", "message": "Freight creation criteria were not satisfied"},
		}},
	}})
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{kargoStageGVR: stageObjs, warehouseGVR: warehouses},
		kinds:      map[schema.GroupVersionResource]string{kargoStageGVR: "Stage", warehouseGVR: "Warehouse"},
		namespaced: map[schema.GroupVersionResource]bool{kargoStageGVR: true, warehouseGVR: true},
	}
	for _, i := range Compose(p, Filters{Limit: NoLimit, AllowUnfilteredEvidence: true}) {
		t.Errorf("waiting state filed an issue: %s/%s %s", i.Kind, i.Name, i.Reason)
	}
}

// A generic condition finding on a namespaced Kargo object needs the same
// exact read of its kind as the Kargo findings: Stage and Warehouse
// conditions carry diagnostic messages.
func TestKargoGenericConditionFindingsNeedTheirKind(t *testing.T) {
	stages := kargoStages(t)
	unhealthy := stages["prod"].DeepCopy()
	_ = unstructured.SetNestedSlice(unhealthy.Object, []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "Unhealthy", "message": "Deployment has 0 ready"},
	}, "status", "conditions")
	warehouseGVR := schema.GroupVersionResource{Group: "kargo.akuity.io", Version: "v1alpha1", Resource: "warehouses"}
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Warehouse", "metadata": map[string]any{"name": "w", "namespace": "kargo-demo"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "DiscoveryFailure", "message": "repo not found"}}},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{kargoStageGVR: {unhealthy}, warehouseGVR: {broken}},
		kinds:      map[schema.GroupVersionResource]string{kargoStageGVR: "Stage", warehouseGVR: "Warehouse"},
		namespaced: map[schema.GroupVersionResource]bool{kargoStageGVR: true, warehouseGVR: true},
	}
	all := Compose(p, Filters{Limit: NoLimit, AllowUnfilteredEvidence: true})
	if len(all) != 2 {
		t.Fatalf("issues = %v", reasonsOf(all))
	}
	for _, i := range all {
		want := []EvidenceRead{{Group: "kargo.akuity.io", Resource: strings.ToLower(i.Kind) + "s", Namespace: "kargo-demo", Verb: "list", Exact: true}}
		if !slices.Equal(i.RequiredReads, want) {
			t.Errorf("%s required reads = %+v", i.Kind, i.RequiredReads)
		}
	}
	for _, resource := range []string{"stages", "warehouses"} {
		for _, i := range Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(read EvidenceRead) bool { return read.Resource != resource }}) {
			if strings.ToLower(i.Kind)+"s" == resource {
				t.Errorf("a caller who can't list %s got %s", resource, i.Reason)
			}
		}
	}
}

var argoCDApplicationTestGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// namespacedProvider lists by namespace, as the caches do, and resolves the
// Jobs an AnalysisRun created.
type namespacedProvider struct {
	*fakeProvider
	jobControllers map[string]Ref
}

func (p namespacedProvider) ListDynamic(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, u := range p.dynamic[gvr] {
		if namespace == "" || u.GetNamespace() == namespace {
			out = append(out, u)
		}
	}
	return out, nil
}

func (p namespacedProvider) JobController(namespace, name string) (Ref, bool) {
	ref, ok := p.jobControllers[namespace+"/"+name]
	return ref, ok
}

func argoCDApplication(name, destination string) *unstructured.Unstructured {
	return argoCDApplicationOn(name, "https://kubernetes.default.svc", destination)
}

func argoCDApplicationOn(name, server, destination string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": "argocd"},
		"spec":     map[string]any{"destination": map[string]any{"server": server, "namespace": destination}},
	}}
}

func kargoDemoProvider(t *testing.T) namespacedProvider {
	stages := kargoStages(t)
	return namespacedProvider{fakeProvider: &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			kargoStageGVR: {stages["test"], stages["uat"], stages["prod"]},
			argoCDApplicationTestGVR: {
				argoCDApplication("kargo-demo-test", "kargo-demo-test"),
				argoCDApplication("kargo-demo-uat", "kargo-demo-uat"),
				argoCDApplication("kargo-demo-prod", "kargo-demo-prod"),
			},
		},
		kinds:      map[schema.GroupVersionResource]string{kargoStageGVR: "Stage", argoCDApplicationTestGVR: "Application"},
		namespaced: map[schema.GroupVersionResource]bool{kargoStageGVR: true, argoCDApplicationTestGVR: true},
	}}
}

// A view of an app namespace shows the Kargo findings about the Stage whose
// Argo CD Application deploys there, once, under the Stage's own namespace.
func TestKargoFindingsShowUnderTheirAppNamespace(t *testing.T) {
	p := kargoDemoProvider(t)
	for _, namespaces := range [][]string{{"kargo-demo-uat"}, {"kargo-demo", "kargo-demo-uat"}, {"kargo-demo-uat", "kargo-demo-test"}} {
		var got []string
		for _, i := range Compose(p, Filters{Namespaces: namespaces, Grouped: true, Limit: NoLimit, AllowUnfilteredEvidence: true}) {
			got = append(got, i.Namespace+"/"+i.Kind+"/"+i.Name+"/"+i.Reason)
		}
		verifications := 0
		for _, g := range got {
			if g == "kargo-demo/Stage/uat/"+ReasonKargoVerificationFailed {
				verifications++
			}
		}
		if verifications != 1 {
			t.Errorf("namespaces %v: issues = %v, want the uat verification once", namespaces, got)
		}
		wantPromotion := slices.Contains(namespaces, "kargo-demo-test") || slices.Contains(namespaces, "kargo-demo")
		if hasPromotion := slices.ContainsFunc(got, func(g string) bool { return strings.HasSuffix(g, ReasonKargoPromotionFailed) }); hasPromotion != wantPromotion {
			t.Errorf("namespaces %v: issues = %v, promotion finding present = %v", namespaces, got, hasPromotion)
		}
	}
	if got := Compose(p, Filters{Namespaces: []string{"kargo-demo-prod"}, Limit: NoLimit, AllowUnfilteredEvidence: true}); len(got) != 0 {
		t.Errorf("prod has no current finding: %v", reasonsOf(got))
	}
}

// An Application that deploys to another cluster says nothing about this
// cluster's namespace of the same name.
func TestKargoFindingsIgnoreRemoteAppDestinations(t *testing.T) {
	p := kargoDemoProvider(t)
	p.dynamic[argoCDApplicationTestGVR] = []*unstructured.Unstructured{
		argoCDApplicationOn("kargo-demo-uat", "https://prod-cluster.example.com", "kargo-demo-uat"),
	}
	if got := Compose(p, Filters{Namespaces: []string{"kargo-demo-uat"}, Grouped: true, Limit: NoLimit, AllowUnfilteredEvidence: true}); len(got) != 0 {
		t.Errorf("remote destination: got %v", reasonsOf(got))
	}
}

// Seeing the app namespace isn't enough: the finding still needs the list of
// Stages in the project namespace, and the Stage itself.
func TestKargoFindingsUnderAppNamespaceKeepTheStageRead(t *testing.T) {
	p := kargoDemoProvider(t)
	cases := map[string]Filters{
		"can't list Stages": {CanReadEvidence: func(read EvidenceRead) bool {
			return !(read.Group == kargo.Group && read.Resource == "stages" && read.Namespace == "kargo-demo")
		}},
		"can't read the project namespace": {
			CanReadEvidence: func(EvidenceRead) bool { return true },
			CanReadSubject:  func(ref Ref) bool { return ref.Namespace != "kargo-demo" },
		},
	}
	for name, f := range cases {
		f.Namespaces, f.Grouped, f.Limit = []string{"kargo-demo-uat"}, true, NoLimit
		if got := Compose(p, f); len(got) != 0 {
			t.Errorf("%s: got %v", name, reasonsOf(got))
		}
	}
}

// The rows a Kargo verification's AnalysisRun files (its Pods, rolled up to
// the run, and its Job) point at the Stage whose check it is.
func TestKargoVerificationRunRowsPointAtTheirStage(t *testing.T) {
	p := kargoDemoProvider(t)
	run := Ref{Group: "argoproj.io", Kind: "AnalysisRun", Namespace: "kargo-demo", Name: "uat.01m89d5xvxsp57zytxjpy9c6ws.7c6082a"}
	other := Ref{Group: "argoproj.io", Kind: "AnalysisRun", Namespace: "kargo-demo", Name: "web-6c4f-2"}
	p.jobControllers = map[string]Ref{"kargo-demo/0ee8f3a3.error-rate.1": run, "kargo-demo/web.error-rate.1": other}
	p.problems = []k8s.Detection{
		{Kind: "Job", Group: "batch", Namespace: "kargo-demo", Name: "0ee8f3a3.error-rate.1", Severity: "critical", Reason: "BackoffLimitExceeded"},
		{Kind: "Pod", Namespace: "kargo-demo", Name: "0ee8f3a3.error-rate.1-x7k2p", Severity: "critical", Reason: "Error", OwnerGroup: run.Group, OwnerKind: run.Kind, OwnerName: run.Name},
		{Kind: "Job", Group: "batch", Namespace: "kargo-demo", Name: "web.error-rate.1", Severity: "critical", Reason: "BackoffLimitExceeded"},
	}
	byName := map[string]Issue{}
	var stageID string
	for _, i := range Compose(p, Filters{Grouped: true, Limit: NoLimit, AllowUnfilteredEvidence: true}) {
		byName[i.Kind+"/"+i.Name] = i
		if i.Reason == ReasonKargoVerificationFailed {
			stageID = i.ID
		}
	}
	for _, name := range []string{"Job/0ee8f3a3.error-rate.1", "AnalysisRun/" + run.Name} {
		parent := byName[name].IncidentParent
		if parent == nil || parent.ID != stageID || parent.FactType != factKargoVerification ||
			parent.Ref != (Ref{Group: kargo.Group, Kind: "Stage", Namespace: "kargo-demo", Name: "uat"}) {
			t.Errorf("%s parent = %s", name, fmt.Sprint(parent))
		}
	}
	if parent := byName["Job/web.error-rate.1"].IncidentParent; parent != nil {
		t.Errorf("another run's Job points at %+v", parent)
	}
}
