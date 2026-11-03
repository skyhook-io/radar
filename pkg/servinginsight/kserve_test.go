package servinginsight

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func kserveFixture(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "kserve", name))
	if err != nil {
		t.Fatal(err)
	}
	if data, err = yaml.YAMLToJSON(data); err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	return obj
}

func newInferenceService(status map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1beta1", "kind": "InferenceService",
		"metadata": map[string]any{"name": "fraud", "namespace": "ml", "generation": int64(3)},
	}}
	if status != nil {
		obj.Object["status"] = status
	}
	return obj
}

func servingSelectionWire(t *testing.T, obj *unstructured.Unstructured) string {
	t.Helper()
	projection := ForResource(obj)
	projection.KServeInferenceService.FieldNotes = nil
	projection.KServeInferenceService.ReferenceWriterRelease = ""
	data, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Expected projections of real KServe v0.20.0 controller snapshots (Standard
// mode) and one writer-derived Knative snapshot; see testdata/kserve/README.md.
func TestKServeControllerSnapshots(t *testing.T) {
	cases := []struct{ file, want string }{
		{"loaded.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded"}}}`},
		// Recreate strategy removed the old Pod: nothing serves, yet activeModelState stays Loaded.
		{"failed-after-loaded.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"BlockedByFailedLoad","targetModelState":"FailedToLoad","lastFailure":{"reason":"ModelLoadFailed","exitCode":1}}}}`},
		// Recovery to Loaded keeps the earlier failure.
		{"recovered-stale-failure.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded","lastFailure":{"reason":"ModelLoadFailed","exitCode":1}}}}`},
		// The current blocker is an image pull; the recorded failure is from an earlier storage error.
		{"imagepull-after-older-failure.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"InProgress","targetModelState":"Pending","lastFailure":{"reason":"ModelLoadFailed","exitCode":1}}}}`},
		// The new spec's Pod is unschedulable; the controller still reports UpToDate from Deployment availability.
		{"uptodate-while-new-pod-unscheduled.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded"}}}`},
		// Canary modelStatus is always {"transitionStatus":""} in v0.20.0 and is not projected.
		{"canary-standard.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded"},"canaries":[{"name":"v2-good","ready":false,"trafficPercent":10},{"name":"v3-bad","ready":false,"trafficPercent":20}]}}`},
		// Stop clears model and failure history; the runtime selection stays.
		{"stopped.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":""}}}`},
		// No exitCode on the wire: zero or unreported. activeModelState is a reported empty string.
		{"invalid-spec.json", `{"kserveInferenceService":{"deploymentMode":"Standard","model":{"transitionStatus":"InvalidSpec","targetModelState":"FailedToLoad","lastFailure":{"reason":"NoSupportingRuntime"}}}}`},
		// Standard mode writes no revision facts for either component slot.
		{"transformer-not-ready.json", `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded"}}}`},
		{"namespaced-runtime.json", `{"kserveInferenceService":{"deploymentMode":"Standard","servingRuntimeName":"team-sklearn","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded"}}}`},
		{"knative-canary-failed-revision.synthetic.yaml", `{"kserveInferenceService":{"deploymentMode":"Serverless","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"BlockedByFailedLoad","targetModelState":"FailedToLoad","lastFailure":{"reason":"ModelLoadFailed","exitCode":1}},"components":{"predictor":{"latestReadyRevision":"fraud-model-predictor-00001","latestCreatedRevision":"fraud-model-predictor-00002","latestRolledoutRevision":"fraud-model-predictor-00001","traffic":[{"revisionName":"fraud-model-predictor-00001","latestRevision":true,"percent":10,"tag":"latest"},{"revisionName":"fraud-model-predictor-00001","latestRevision":false,"percent":90,"tag":"prev"}]}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			obj := kserveFixture(t, tc.file)
			before := obj.DeepCopy()
			projection := ForResource(obj)
			block := projection.KServeInferenceService
			notes := block.FieldNotes
			checkKServeSnapshotNotes(t, tc.file, notes, block.ReferenceWriterRelease)
			block.FieldNotes, block.ReferenceWriterRelease = nil, ""
			data, err := json.Marshal(projection)
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			if got != tc.want {
				t.Fatalf("projection:\n got %s\nwant %s", got, tc.want)
			}
			if !reflect.DeepEqual(obj, before) {
				t.Fatal("projection mutated input")
			}
			for _, excluded := range []string{"http", "gs://", "url", "Url", "address", "observedGeneration", "copies", "totalCopies", "message", "storage", "modelStatus"} {
				if strings.Contains(got, excluded) {
					t.Fatalf("copied excluded field %q: %s", excluded, got)
				}
			}
		})
	}
}

func TestKServeExactGVK(t *testing.T) {
	status := map[string]any{"deploymentMode": "Standard", "modelStatus": map[string]any{"transitionStatus": "UpToDate"}}
	for _, gvk := range []struct{ apiVersion, kind string }{
		{"serving.kserve.io/v1alpha1", "InferenceService"},
		{"other.io/v1beta1", "InferenceService"},
		{"serving.kserve.io/v1beta1", "LLMInferenceService"},
		{"serving.kserve.io/v1alpha2", "LLMInferenceService"},
		{"serving.kserve.io/v1alpha1", "InferenceGraph"},
		{"serving.kserve.io/v1alpha1", "TrainedModel"},
		{"serving.knative.dev/v1", "Service"},
	} {
		obj := newInferenceService(status)
		obj.SetAPIVersion(gvk.apiVersion)
		obj.SetKind(gvk.kind)
		if got := ForResource(obj); got != nil {
			t.Fatalf("unsupported %v projected: %+v", gvk, got)
		}
	}
	for _, obj := range []runtime.Object{nil, &corev1.Pod{}} {
		if ForResource(obj) != nil {
			t.Fatal("typed object projected")
		}
	}
}

func TestKServeEmptyAndUnreportedStatus(t *testing.T) {
	for name, status := range map[string]map[string]any{
		"no status":           nil,
		"empty status":        {},
		"empty components":    {"components": map[string]any{"predictor": map[string]any{"url": "http://x"}, "transformer": map[string]any{}}},
		"status not a map":    nil,
		"wrongly typed lists": {"canaryStatuses": "nope", "components": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			obj := newInferenceService(status)
			if name == "status not a map" {
				obj.Object["status"] = "garbage"
			}
			if got := servingSelectionWire(t, obj); got != `{"kserveInferenceService":{}}` {
				t.Fatalf("invented facts: %s", got)
			}
		})
	}
}

func TestKServePresenceIsNotZero(t *testing.T) {
	obj := newInferenceService(map[string]any{
		"modelStatus": map[string]any{
			"transitionStatus": "",
			"states":           map[string]any{"activeModelState": ""},
			"lastFailureInfo":  map[string]any{"reason": "ModelLoadFailed", "exitCode": int64(0)},
		},
		"components": map[string]any{"predictor": map[string]any{"traffic": []any{
			map[string]any{"revisionName": "r1", "percent": int64(0), "latestRevision": false},
			map[string]any{"revisionName": "r2"},
		}}},
		"canaryStatuses": []any{
			map[string]any{"name": "zero", "ready": false, "trafficPercent": int64(0)},
			map[string]any{"name": "absent"},
		},
	})
	want := `{"kserveInferenceService":{"model":{"transitionStatus":"","lastFailure":{"reason":"ModelLoadFailed","exitCode":0}},"components":{"predictor":{"traffic":[{"revisionName":"r1","latestRevision":false,"percent":0},{"revisionName":"r2"}]}},"canaries":[{"name":"zero","ready":false,"trafficPercent":0},{"name":"absent"}]}}`
	if got := servingSelectionWire(t, obj); got != want {
		t.Fatalf("presence:\n got %s\nwant %s", got, want)
	}
	// Wrong types are dropped, never coerced.
	obj = newInferenceService(map[string]any{
		"deploymentMode": int64(1),
		"modelStatus":    map[string]any{"transitionStatus": int64(2), "lastFailureInfo": map[string]any{"reason": "X", "exitCode": "1"}},
		"canaryStatuses": []any{"not-an-object", map[string]any{"name": "c", "ready": "true", "trafficPercent": "10"}},
	})
	want = `{"kserveInferenceService":{"model":{"lastFailure":{"reason":"X"}},"canaries":[{"name":"c"}]}}`
	if got := servingSelectionWire(t, obj); got != want {
		t.Fatalf("coerced wrong types:\n got %s\nwant %s", got, want)
	}
}

func TestKServeModelMeshDoesNotProjectModelState(t *testing.T) {
	obj := newInferenceService(map[string]any{
		"deploymentMode":     "ModelMesh",
		"observedGeneration": int64(2),
		"components":         map[string]any{"predictor": map[string]any{"latestReadyRevision": "r1"}},
		"canaryStatuses":     []any{map[string]any{"ready": true}},
		"modelStatus":        map[string]any{"transitionStatus": "UpToDate", "states": map[string]any{"activeModelState": "Standby"}},
	})
	wire, err := json.Marshal(ForResource(obj))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(wire); got != `{"kserveInferenceService":{"deploymentMode":"ModelMesh"}}` {
		t.Fatalf("ModelMesh model state projected: %s", got)
	}
}

func TestKServeCapsAndSourceOrder(t *testing.T) {
	// The v0.20.0 reconciler writes at most two traffic targets; these lists are
	// adversarial inputs that pin the bound, not controller states.
	traffic := []any{}
	canaries := []any{}
	for i := 9; i >= 0; i-- {
		traffic = append(traffic, map[string]any{"revisionName": fmt.Sprintf("rev-%d", i), "percent": int64(10), "url": strings.Repeat("http://u", 1000)})
		canaries = append(canaries, map[string]any{"name": fmt.Sprintf("canary-%d", i), "ready": true, "trafficPercent": int64(1), "modelStatus": map[string]any{"transitionStatus": ""}})
	}
	obj := newInferenceService(map[string]any{
		"components":     map[string]any{"predictor": map[string]any{"traffic": traffic}, "explainer": map[string]any{"traffic": traffic[:8]}},
		"canaryStatuses": canaries,
	})
	got := ForResource(obj).KServeInferenceService
	predictor := got.Components.Predictor
	if len(predictor.Traffic) != 8 || !predictor.TrafficTruncated || predictor.Traffic[0].RevisionName != "rev-9" || predictor.Traffic[7].RevisionName != "rev-2" {
		t.Fatalf("traffic cap/order: %+v", predictor)
	}
	if explainer := got.Components.Explainer; len(explainer.Traffic) != 8 || explainer.TrafficTruncated {
		t.Fatalf("exactly eight targets marked truncated: %+v", explainer)
	}
	if got.Components.Transformer != nil {
		t.Fatal("absent slot invented")
	}
	if len(got.Canaries) != 8 || !got.CanariesTruncated || got.Canaries[0].Name != "canary-9" || got.Canaries[7].Name != "canary-2" {
		t.Fatalf("canary cap/order: %+v", got.Canaries)
	}
	wire := servingSelectionWire(t, obj)
	if strings.Contains(wire, "http") || strings.Contains(wire, "modelStatus") {
		t.Fatalf("copied excluded detail: %s", wire)
	}
	full, err := json.Marshal(ForResource(obj))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) > 4500 {
		t.Fatalf("fixture projection exceeded budget: %d bytes", len(full))
	}
	if again := servingSelectionWire(t, obj); again != wire {
		t.Fatal("nondeterministic output")
	}
}

func TestKServeBuildKeepsConditionsSeparate(t *testing.T) {
	obj := kserveFixture(t, "transformer-not-ready.json")
	projected := ForResource(obj)
	got := resourcecontext.Build(context.Background(), obj, resourcecontext.Options{Tier: resourcecontext.TierBasic, Serving: projected})
	if got.Serving != projected {
		t.Fatal("context wiring")
	}
	byType := map[string]string{}
	for _, c := range got.StatusSummary.Conditions {
		byType[c.Type] = c.Status
	}
	if byType["Ready"] != "True" || byType["TransformerReady"] != "False" {
		t.Fatalf("root conditions lost or merged: %+v", got.StatusSummary.Conditions)
	}
}

func checkKServeSnapshotNotes(t *testing.T, file string, notes []resourcecontext.ServingFieldNote, release string) {
	t.Helper()
	fields := []string{"status.observedGeneration", "status.modelStatus.transitionStatus", "status.modelStatus.states.activeModelState", "status.modelStatus.copies"}
	switch file {
	case "failed-after-loaded.json", "recovered-stale-failure.json", "imagepull-after-older-failure.json", "knative-canary-failed-revision.synthetic.yaml":
		fields = []string{"status.observedGeneration", "status.modelStatus.transitionStatus", "status.modelStatus.states.activeModelState", "status.modelStatus.lastFailureInfo", "status.modelStatus.copies"}
	case "canary-standard.json":
		fields = append(fields, "status.canaryStatuses[].ready", "status.canaryStatuses[].trafficPercent", "status.canaryStatuses[].modelStatus")
	case "invalid-spec.json":
		fields = []string{"status.modelStatus.states.activeModelState", "status.modelStatus.lastFailureInfo"}
	case "stopped.json":
		fields = nil
	}
	var got []string
	for _, n := range notes {
		got = append(got, n.Field)
		if n.Note == "" {
			t.Fatalf("empty note: %+v", n)
		}
	}
	if !reflect.DeepEqual(got, fields) {
		t.Fatalf("notes: got %v want %v", got, fields)
	}
	wantRelease := "KServe v0.20.0"
	if len(fields) == 0 {
		wantRelease = ""
	}
	if release != wantRelease {
		t.Fatalf("writer reference: %q", release)
	}
}

func TestKServeFieldNoteMeaningAndApplicability(t *testing.T) {
	status := map[string]any{
		"observedGeneration": int64(1),
		"deploymentMode":     "Standard",
		"modelStatus": map[string]any{
			"transitionStatus": "InProgress",
			"states":           map[string]any{"activeModelState": "Loaded"},
			"lastFailureInfo":  map[string]any{"reason": "ModelLoadFailed"},
			"copies":           map[string]any{"failedCopies": int64(0)},
		},
		"canaryStatuses": []any{map[string]any{"ready": false, "trafficPercent": int64(0), "modelStatus": map[string]any{"transitionStatus": ""}}},
	}
	block := ForResource(newInferenceService(status)).KServeInferenceService
	want := []resourcecontext.ServingFieldNote{
		{Field: "status.observedGeneration", Note: "Copied from a child Deployment or Knative Service (whichever component was reconciled last), not this InferenceService's own; do not compare it with metadata.generation."},
		{Field: "status.modelStatus.transitionStatus", Note: "Derived from the newest predictor Pod, falling back to the predictor Deployment's availability; UpToDate and Loaded can be reported while a new rollout's Pod has not started."},
		{Field: "status.modelStatus.states.activeModelState", Note: "Set to Loaded when the controller marks a revision loaded and kept through later transitions (only a stop clears status); it does not show that any model is serving now."},
		{Field: "status.modelStatus.lastFailureInfo", Note: "Kept after recovery and through later transitions (only a stop clears status); it may describe an earlier attempt rather than the current one."},
		{Field: "status.modelStatus.copies", Note: "failedCopies is never written (always 0); totalCopies counts ready predictor Pods, not model copies."},
		{Field: "status.canaryStatuses[].ready", Note: "True when the canary's Deployment has at least one available replica."},
		{Field: "status.canaryStatuses[].trafficPercent", Note: "The declared spec value, not measured traffic."},
		{Field: "status.canaryStatuses[].modelStatus", Note: "Never written for canaries; it does not describe the canary's model."},
	}
	if !reflect.DeepEqual(block.FieldNotes, want) {
		t.Fatalf("writer semantics: %+v", block.FieldNotes)
	}
	for _, mode := range []string{"Standard", "RawDeployment", "Knative", "Serverless", "Unknown", ""} {
		for _, transition := range []string{"", "InvalidSpec", "InProgress"} {
			status["deploymentMode"] = mode
			status["modelStatus"].(map[string]any)["transitionStatus"] = transition
			b := ForResource(newInferenceService(status)).KServeInferenceService
			found := false
			for _, note := range b.FieldNotes {
				if note.Field == "status.modelStatus.transitionStatus" {
					found = true
					if (mode == "Knative" || mode == "Serverless") && !strings.Contains(note.Note, "latest created revision") {
						t.Fatal("Standard semantics on Knative")
					}
				}
			}
			expected := transition == "InProgress" && mode != "Unknown" && mode != ""
			if found != expected {
				t.Fatalf("transition note applicability mode=%q transition=%q: %v", mode, transition, found)
			}
		}
	}
}
