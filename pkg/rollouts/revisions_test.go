package rollouts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func setTemplateImage(rollout map[string]any, image string) {
	containers, _, _ := unstructured.NestedSlice(rollout, "spec", "template", "spec", "containers")
	containers[0].(map[string]any)["image"] = image
	_ = unstructured.SetNestedSlice(rollout, containers, "spec", "template", "spec", "containers")
}

func TestBuildRevisionsMarksCurrentAndStableSeparately(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{
			"currentPodHash": "hash-v3",
			"stableRS":       "hash-v2",
		}
	})
	rsList := []unstructured.Unstructured{
		*replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		*replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
		*replicaSetForTest("prod", "web-3", "3", "hash-v3", "web:v3", "rollout-uid"),
	}

	revisions := BuildRevisions(rsList, ro)
	if len(revisions) != 3 {
		t.Fatalf("got %d revisions, want 3", len(revisions))
	}
	if revisions[0].Number != 3 {
		t.Errorf("revisions not newest-first: %d", revisions[0].Number)
	}

	// During a canary the rolling-out revision and the traffic-serving revision
	// differ; conflating them would mislabel what an abort reverts to.
	byNumber := map[int64]Revision{}
	for _, r := range revisions {
		byNumber[r.Number] = r
	}
	if !byNumber[3].IsCurrent || byNumber[3].IsStable {
		t.Errorf("rev 3 = current:%v stable:%v, want current only", byNumber[3].IsCurrent, byNumber[3].IsStable)
	}
	if byNumber[2].IsCurrent || !byNumber[2].IsStable {
		t.Errorf("rev 2 = current:%v stable:%v, want stable only", byNumber[2].IsCurrent, byNumber[2].IsStable)
	}
	if byNumber[1].Image != "web:v1" {
		t.Errorf("rev 1 image = %q, want web:v1", byNumber[1].Image)
	}
}

func TestBuildRevisionsIgnoresForeignReplicaSets(t *testing.T) {
	ro := rolloutForTest("prod", "web", nil)
	rsList := []unstructured.Unstructured{
		*replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		*replicaSetForTest("prod", "other-1", "1", "hash-x", "other:v1", "different-uid"),
	}

	revisions := BuildRevisions(rsList, ro)
	if len(revisions) != 1 {
		t.Fatalf("got %d revisions, want 1 (foreign ReplicaSet leaked in)", len(revisions))
	}
}

// A stale hash label in the restored template makes the controller compute the
// wrong hash, so the rollback silently mismatches.
func TestUndoStripsPodTemplateHashLabel(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v2"}
	})
	client := newFakeRollouts(
		ro,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	res, err := Undo(context.Background(), client, "prod", "web", 1)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Revision != 1 {
		t.Errorf("Revision = %d, want 1", res.Revision)
	}

	patches := recordedPatches(t, client)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	raw := string(patches[0].raw)
	if strings.Contains(raw, PodTemplateHashLabel) {
		t.Fatalf("undo patch still carries %s: %s", PodTemplateHashLabel, raw)
	}
	if !strings.Contains(raw, "web:v1") {
		t.Errorf("undo patch does not restore the v1 image: %s", raw)
	}
	if !strings.Contains(raw, `"/spec/template"`) {
		t.Errorf("undo patch does not target /spec/template: %s", raw)
	}
}

func TestUndoWithoutRevisionPicksPreviousRevision(t *testing.T) {
	client := newFakeRollouts(
		rolloutForTest("prod", "web", func(o map[string]any) {
			o["status"] = map[string]any{"currentPodHash": "hash-v3"}
			setTemplateImage(o, "web:v3")
		}),
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
		replicaSetForTest("prod", "web-3", "3", "hash-v3", "web:v3", "rollout-uid"),
	)

	res, err := Undo(context.Background(), client, "prod", "web", 0)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Revision != 2 {
		t.Errorf("Revision = %d, want 2 (second-newest)", res.Revision)
	}
}

func TestUndoRejectsUnknownRevision(t *testing.T) {
	client := newFakeRollouts(
		rolloutForTest("prod", "web", nil),
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 99)
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("err = %v, want ErrRevisionNotFound", err)
	}
}

func TestUndoRejectsWhenOnlyOneRevisionExists(t *testing.T) {
	client := newFakeRollouts(
		rolloutForTest("prod", "web", nil),
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 0)
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("err = %v, want ErrRevisionNotFound", err)
	}
}

func TestUndoRejectsUnchangedTemplate(t *testing.T) {
	// Revision 2's template equals the live one once the hash label is stripped.
	client := newFakeRollouts(
		rolloutForTest("prod", "web", nil),
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 2)
	if !errors.Is(err, ErrTemplateUnchanged) {
		t.Fatalf("err = %v, want ErrTemplateUnchanged", err)
	}
	if len(recordedPatches(t, client)) != 0 {
		t.Errorf("no-op undo still issued a patch")
	}
}

// workloadRef Rollouts have no spec.template; patching the Rollout would shadow
// the ref rather than roll back.
func TestUndoWithWorkloadRefPatchesReferencedDeployment(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"name":       "web-deploy",
		}
		o["status"] = map[string]any{"currentPodHash": "hash-v2"}
	})
	deploy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"namespace": "prod", "name": "web-deploy"},
		"spec":       map[string]any{"template": map[string]any{}},
	}}
	client := newFakeRollouts(
		ro,
		deploy,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	if _, err := Undo(context.Background(), client, "prod", "web", 1); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	var patched []clienttesting.PatchAction
	for _, action := range client.Actions() {
		if pa, ok := action.(clienttesting.PatchAction); ok {
			patched = append(patched, pa)
		}
	}
	if len(patched) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patched))
	}
	if got := patched[0].GetResource(); got != deploymentGVR {
		t.Errorf("patched %v, want the referenced Deployment %v", got, deploymentGVR)
	}
	if got := patched[0].GetName(); got != "web-deploy" {
		t.Errorf("patched name = %q, want web-deploy", got)
	}
}

// The unchanged guard has to read the referenced workload; the Rollout itself has
// no spec.template to compare against.
func TestUndoRejectsUnchangedWorkloadRefTemplate(t *testing.T) {
	// Revision 1's template once its pod-template-hash label is stripped.
	template := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "web", "image": "web:v1"}}},
	}
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"name":       "web-deploy",
		}
	})
	deploy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"namespace": "prod", "name": "web-deploy"},
		"spec":       map[string]any{"template": template},
	}}
	client := newFakeRollouts(
		ro,
		deploy,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 1)
	if !errors.Is(err, ErrTemplateUnchanged) {
		t.Fatalf("err = %v, want ErrTemplateUnchanged", err)
	}
	if len(recordedPatches(t, client)) != 0 {
		t.Errorf("no-op undo still patched the referenced Deployment")
	}
}

// Argo keeps PodTemplate's pod template at the root, not under spec.
func TestUndoPatchesPodTemplateAtItsRoot(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{
			"apiVersion": "v1",
			"kind":       "PodTemplate",
			"name":       "web-tmpl",
		}
	})
	podTemplate := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PodTemplate",
		"metadata":   map[string]any{"namespace": "prod", "name": "web-tmpl"},
		"template":   map[string]any{},
	}}
	client := newFakeRollouts(
		ro,
		podTemplate,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	if _, err := Undo(context.Background(), client, "prod", "web", 1); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	patches := recordedPatches(t, client)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	var ops []map[string]any
	if err := json.Unmarshal(patches[0].raw, &ops); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	if got := ops[0]["path"]; got != "/template" {
		t.Errorf("patch path = %v, want /template", got)
	}
}

func TestUndoRejectsUnsupportedWorkloadRefKind(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "StatefulSet",
			"name":       "web-sts",
		}
	})
	client := newFakeRollouts(
		ro,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 1)
	if !errors.Is(err, ErrWorkloadRefUnsupported) {
		t.Fatalf("err = %v, want ErrWorkloadRefUnsupported", err)
	}
}

func TestResolveTemplateTargetRejectsWrongAPIGroup(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		spec["workloadRef"] = map[string]any{
			"apiVersion": "example.io/v1",
			"kind":       "Deployment",
			"name":       "web-deploy",
		}
	})

	_, err := ResolveTemplateTarget(ro)
	if !errors.Is(err, ErrWorkloadRefUnsupported) {
		t.Fatalf("err = %v, want ErrWorkloadRefUnsupported", err)
	}
}

func TestUndoRejectsTerminatingRollout(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		meta := o["metadata"].(map[string]any)
		meta["deletionTimestamp"] = "2026-08-06T00:00:00Z"
	})
	client := newFakeRollouts(
		ro,
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	_, err := Undo(context.Background(), client, "prod", "web", 1)
	if !errors.Is(err, ErrResourceTerminating) {
		t.Fatalf("err = %v, want ErrResourceTerminating", err)
	}
}

func TestListRevisionsReturnsNewestFirst(t *testing.T) {
	client := newFakeRollouts(
		rolloutForTest("prod", "web", func(o map[string]any) {
			o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1"}
		}),
		replicaSetForTest("prod", "web-1", "1", "hash-v1", "web:v1", "rollout-uid"),
		replicaSetForTest("prod", "web-2", "2", "hash-v2", "web:v2", "rollout-uid"),
	)

	revisions, err := ListRevisions(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("got %d revisions, want 2", len(revisions))
	}
	if revisions[0].Number != 2 || revisions[1].Number != 1 {
		t.Errorf("order = %d,%d want 2,1", revisions[0].Number, revisions[1].Number)
	}
	if revisions[0].Template == "" {
		t.Error("revision template not populated")
	}
}

func TestStripPodTemplateHashRemovesEmptyLabelMap(t *testing.T) {
	template := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{PodTemplateHashLabel: "abc"}},
	}
	stripPodTemplateHash(template)

	if _, found, _ := unstructured.NestedMap(template, "metadata", "labels"); found {
		t.Error("labels map should be removed once the only key is stripped")
	}
}
