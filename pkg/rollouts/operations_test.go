package rollouts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func rolloutForTest(namespace, name string, mutate func(map[string]any)) *unstructured.Unstructured {
	ro := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"uid":       "rollout-uid",
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"strategy": map[string]any{
				"canary": map[string]any{
					"steps": []any{
						map[string]any{"setWeight": int64(20)},
						map[string]any{"pause": map[string]any{}},
						map[string]any{"setWeight": int64(60)},
					},
				},
			},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "web", "image": "web:v2"}},
				},
			},
		},
		"status": map[string]any{},
	}}
	if mutate != nil {
		mutate(ro.Object)
	}
	return ro
}

func blueGreenRolloutForTest(mutate func(map[string]any)) *unstructured.Unstructured {
	return rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["strategy"] = map[string]any{
			"blueGreen": map[string]any{
				"activeService":  "web-active",
				"previewService": "web-preview",
			},
		}
		if mutate != nil {
			mutate(o)
		}
	})
}

func replicaSetForTest(namespace, name string, revision string, podHash string, image string, ownerUID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"namespace":   namespace,
			"name":        name,
			"annotations": map[string]any{RevisionAnnotation: revision},
			"labels":      map[string]any{PodTemplateHashLabel: podHash},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Rollout",
				"name":       "web",
				"uid":        ownerUID,
			}},
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{
					"app":                "web",
					PodTemplateHashLabel: podHash,
				}},
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "web", "image": image}},
				},
			},
		},
	}}
}

func newFakeRollouts(objs ...runtime.Object) *fake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(GVR.GroupVersion().WithKind("Rollout"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(GVR.GroupVersion().WithKind("RolloutList"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(replicaSetGVR.GroupVersion().WithKind("ReplicaSet"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(replicaSetGVR.GroupVersion().WithKind("ReplicaSetList"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(analysisRunGVR.GroupVersion().WithKind("AnalysisRun"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(analysisRunGVR.GroupVersion().WithKind("AnalysisRunList"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}.GroupVersion().WithKind("Deployment"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}.GroupVersion().WithKind("DeploymentList"), &unstructured.UnstructuredList{})
	// Registered unstructured, not via corev1.AddToScheme: the dynamic fake is
	// handed unstructured objects and typed registration double-registers the kind.
	coreV1 := schema.GroupVersion{Version: "v1"}
	scheme.AddKnownTypeWithName(coreV1.WithKind("PodTemplate"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(coreV1.WithKind("PodTemplateList"), &unstructured.UnstructuredList{})
	return fake.NewSimpleDynamicClient(scheme, objs...)
}

type recordedPatch struct {
	subresource string
	body        map[string]any
	raw         []byte
}

func recordedPatches(t *testing.T, client *fake.FakeDynamicClient) []recordedPatch {
	t.Helper()
	var out []recordedPatch
	for _, action := range client.Actions() {
		pa, ok := action.(clienttesting.PatchAction)
		if !ok {
			continue
		}
		rp := recordedPatch{subresource: pa.GetSubresource(), raw: pa.GetPatch()}
		_ = json.Unmarshal(pa.GetPatch(), &rp.body)
		out = append(out, rp)
	}
	return out
}

func patchBodies(t *testing.T, client *fake.FakeDynamicClient, subresource string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, p := range recordedPatches(t, client) {
		if p.subresource == subresource {
			out = append(out, p.body)
		}
	}
	return out
}

func statusPatchBody(t *testing.T, client *fake.FakeDynamicClient) map[string]any {
	t.Helper()
	bodies := patchBodies(t, client, "status")
	if len(bodies) == 0 {
		t.Fatalf("no status-subresource patch recorded; actions=%v", client.Actions())
	}
	return bodies[0]
}

func statusPatchBodies(t *testing.T, client *fake.FakeDynamicClient) []map[string]any {
	t.Helper()
	return patchBodies(t, client, "status")
}

func mainPatchBodies(t *testing.T, client *fake.FakeDynamicClient) []map[string]any {
	t.Helper()
	return patchBodies(t, client, "")
}

func TestAbortPatchesStatusSubresource(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", nil))

	res, err := Abort(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if res.Operation != "abort" {
		t.Errorf("Operation = %q, want abort", res.Operation)
	}

	body := statusPatchBody(t, client)
	status, ok := body["status"].(map[string]any)
	if !ok {
		t.Fatalf("patch has no status object: %v", body)
	}
	if status["abort"] != true {
		t.Errorf("status.abort = %v, want true", status["abort"])
	}
	// Abort must not touch spec — that's what makes it instant and reversible.
	if len(mainPatchBodies(t, client)) != 0 {
		t.Errorf("Abort issued a main-resource patch: %v", mainPatchBodies(t, client))
	}
}

func TestRetryClearsAbort(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"abort": true}
	}))

	if _, err := Retry(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	status := statusPatchBody(t, client)["status"].(map[string]any)
	if status["abort"] != false {
		t.Errorf("status.abort = %v, want false", status["abort"])
	}
}

func TestPromoteFullSetsPromoteFullAndUnpauses(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["paused"] = true
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1"}
	}))

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}

	status := statusPatchBody(t, client)["status"].(map[string]any)
	if status["promoteFull"] != true {
		t.Errorf("status.promoteFull = %v, want true", status["promoteFull"])
	}

	mains := mainPatchBodies(t, client)
	if len(mains) != 1 {
		t.Fatalf("want 1 main-resource patch to unpause, got %d: %v", len(mains), mains)
	}
	spec := mains[0]["spec"].(map[string]any)
	if spec["paused"] != false {
		t.Errorf("spec.paused = %v, want false", spec["paused"])
	}
}

func TestPromoteFullSkipsUnpauseWhenNotPaused(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", nil))

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if mains := mainPatchBodies(t, client); len(mains) != 0 {
		t.Errorf("unpaused rollout got a spec patch: %v", mains)
	}
}

// A canary mid-analysis has no pauseConditions to clear, so kubectl-argo-rollouts
// advances the step instead; clearing conditions alone would be a no-op.
func TestPromoteAdvancesStepDuringRunningAnalysis(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{
			"currentStepIndex": int64(2),
			"canary": map[string]any{
				"currentStepAnalysisRunStatus": map[string]any{"status": "Running"},
			},
		}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.StepIndex == nil || *res.StepIndex != 3 {
		t.Fatalf("StepIndex = %v, want 3", res.StepIndex)
	}

	status := statusPatchBody(t, client)["status"].(map[string]any)
	if got := status["currentStepIndex"]; got != float64(3) {
		t.Errorf("status.currentStepIndex = %v, want 3", got)
	}
	if _, present := status["controllerPause"]; present {
		t.Errorf("running analysis must not touch controllerPause: %v", status)
	}
}

func TestPromoteLeavesBlueGreenStepIndexAlone(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["strategy"] = map[string]any{"blueGreen": map[string]any{}}
		o["status"] = map[string]any{}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.StepIndex != nil {
		t.Errorf("StepIndex = %v, want nil for blueGreen", *res.StepIndex)
	}
	if patches := statusPatchBodies(t, client); len(patches) != 0 {
		t.Errorf("blueGreen promote with nothing paused issued a status patch: %v", patches)
	}
}

func TestPromoteClearsPauseConditionsWithoutMovingStep(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{
			"currentStepIndex": int64(1),
			"pauseConditions":  []any{map[string]any{"reason": "CanaryPauseStep"}},
		}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.StepIndex != nil {
		t.Errorf("StepIndex = %v, want nil (controller advances the step)", *res.StepIndex)
	}

	status := statusPatchBody(t, client)["status"].(map[string]any)
	if _, present := status["currentStepIndex"]; present {
		t.Errorf("plain promote patched currentStepIndex: %v", status)
	}
	if v, present := status["pauseConditions"]; !present || v != nil {
		t.Errorf("status.pauseConditions = %v, want explicit null", v)
	}
}

// Inconclusive analysis is the one case where the step must move with
// controllerPause, or the controller re-pauses on the same verdict.
func TestPromoteAdvancesStepOnInconclusiveAnalysis(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{
			"currentStepIndex": int64(1),
			"controllerPause":  true,
			"pauseConditions":  []any{map[string]any{"reason": "InconclusiveAnalysisRun"}},
			"canary": map[string]any{
				"currentStepAnalysisRunStatus": map[string]any{"status": "Inconclusive"},
			},
		}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.StepIndex == nil || *res.StepIndex != 2 {
		t.Fatalf("StepIndex = %v, want 2", res.StepIndex)
	}

	status := statusPatchBody(t, client)["status"].(map[string]any)
	if status["controllerPause"] != false {
		t.Errorf("status.controllerPause = %v, want false", status["controllerPause"])
	}
	if got := status["currentStepIndex"]; got != float64(2) {
		t.Errorf("status.currentStepIndex = %v, want 2", got)
	}
}

// blueGreen's normal promote: the controller parks it with a BlueGreenPause
// condition and no step index, so promote must clear conditions and nothing else.
func TestPromoteBlueGreenClearsBlueGreenPause(t *testing.T) {
	client := newFakeRollouts(blueGreenRolloutForTest(func(o map[string]any) {
		o["status"] = map[string]any{
			"pauseConditions": []any{map[string]any{"reason": "BlueGreenPause"}},
		}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.StepIndex != nil {
		t.Errorf("StepIndex = %v, want nil for blueGreen", *res.StepIndex)
	}

	status := statusPatchBody(t, client)["status"].(map[string]any)
	if v, present := status["pauseConditions"]; !present || v != nil {
		t.Errorf("status.pauseConditions = %v, want explicit null", v)
	}
	if _, present := status["currentStepIndex"]; present {
		t.Errorf("blueGreen promote patched a step index: %v", status)
	}
}

func TestPromoteFullBlueGreenSkipsPostPromotionAnalysis(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(blueGreenRolloutForTest(func(o map[string]any) {
		o["status"] = map[string]any{
			"currentPodHash":  "hash-v2",
			"stableRS":        "hash-v1",
			"pauseConditions": []any{map[string]any{"reason": "BlueGreenPause"}},
			"blueGreen": map[string]any{
				"prePromotionAnalysisRunStatus": map[string]any{"name": "ar-pre", "status": "Running"},
			},
		}
	}))

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	status := statusPatchBody(t, client)["status"].(map[string]any)
	if status["promoteFull"] != true {
		t.Errorf("status.promoteFull = %v, want true", status["promoteFull"])
	}
}

func TestAbortBlueGreenTouchesOnlyStatusAbort(t *testing.T) {
	client := newFakeRollouts(blueGreenRolloutForTest(func(o map[string]any) {
		o["status"] = map[string]any{
			"blueGreen": map[string]any{"activeSelector": "abc123", "previewSelector": "def456"},
		}
	}))

	if _, err := Abort(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if status := statusPatchBody(t, client)["status"].(map[string]any); status["abort"] != true {
		t.Errorf("status.abort = %v, want true", status["abort"])
	}
	if mains := mainPatchBodies(t, client); len(mains) != 0 {
		t.Errorf("Abort issued a spec patch on blueGreen: %v", mains)
	}
}

// Steps removed from spec while a rollout sits past the end of the list: clamp to
// the step count rather than replaying an index that no longer exists.
func TestNextStepIndexClampsToStepCount(t *testing.T) {
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentStepIndex": int64(9)}
	})
	if got := nextStepIndex(ro); got != 3 {
		t.Errorf("nextStepIndex = %d, want 3 (the step count)", got)
	}
}

func TestSkipCurrentStepAdvancesOneStep(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentStepIndex": int64(0)}
	}))

	res, err := SkipCurrentStep(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("SkipCurrentStep: %v", err)
	}
	if res.StepIndex == nil || *res.StepIndex != 1 {
		t.Fatalf("StepIndex = %v, want 1", res.StepIndex)
	}
	status := statusPatchBody(t, client)["status"].(map[string]any)
	if got := status["currentStepIndex"]; got != float64(1) {
		t.Errorf("status.currentStepIndex = %v, want 1", got)
	}
}

func TestSkipCurrentStepRejectsBlueGreen(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["strategy"] = map[string]any{
			"blueGreen": map[string]any{"activeService": "web"},
		}
	}))

	_, err := SkipCurrentStep(context.Background(), client, "prod", "web")
	if !errors.Is(err, ErrNoSteps) {
		t.Fatalf("err = %v, want ErrNoSteps", err)
	}
}

func TestSkipCurrentStepRejectsWhenAlreadyPastLastStep(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentStepIndex": int64(3)}
	}))

	_, err := SkipCurrentStep(context.Background(), client, "prod", "web")
	if !errors.Is(err, ErrAlreadyAtLastStep) {
		t.Fatalf("err = %v, want ErrAlreadyAtLastStep", err)
	}
}

func TestRestartUsesRestartAtNotTemplateAnnotation(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", nil))

	if _, err := Restart(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	mains := mainPatchBodies(t, client)
	if len(mains) != 1 {
		t.Fatalf("want 1 patch, got %d", len(mains))
	}
	spec := mains[0]["spec"].(map[string]any)
	if spec[RestartAtField] == nil {
		t.Errorf("spec.%s not set: %v", RestartAtField, spec)
	}
	if _, touched := spec["template"]; touched {
		t.Errorf("Restart patched spec.template, which would re-run every canary step: %v", spec)
	}
}

func TestOperationsRejectTerminatingRollout(t *testing.T) {
	now := metav1.Now()
	mutate := func(o map[string]any) {
		meta := o["metadata"].(map[string]any)
		meta["deletionTimestamp"] = now.UTC().Format("2006-01-02T15:04:05Z")
		meta["finalizers"] = []any{"argoproj.io/finalizer"}
	}

	ops := map[string]func(context.Context, *fake.FakeDynamicClient) error{
		"abort": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := Abort(ctx, c, "prod", "web")
			return err
		},
		"retry": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := Retry(ctx, c, "prod", "web")
			return err
		},
		"promote": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := Promote(ctx, c, "prod", "web")
			return err
		},
		"promote-full": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := PromoteFull(ctx, c, "prod", "web")
			return err
		},
		"skip-step": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := SkipCurrentStep(ctx, c, "prod", "web")
			return err
		},
		"restart": func(ctx context.Context, c *fake.FakeDynamicClient) error {
			_, err := Restart(ctx, c, "prod", "web")
			return err
		},
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			client := newFakeRollouts(rolloutForTest("prod", "web", mutate))
			err := op(context.Background(), client)
			if !errors.Is(err, ErrResourceTerminating) {
				t.Fatalf("err = %v, want ErrResourceTerminating", err)
			}
			if len(recordedPatches(t, client)) != 0 {
				t.Errorf("operation patched a terminating Rollout")
			}
		})
	}
}

// Argo's CLI omits the promoteFull patch when currentPodHash already equals
// stableRS; the spec unpause still applies.
func TestPromoteFullSkipsStatusPatchWhenAlreadyStable(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["paused"] = true
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v2"}
	}))

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}

	if bodies := statusPatchBodies(t, client); len(bodies) != 0 {
		t.Errorf("patched status on an already-stable Rollout: %v", bodies)
	}
	mains := mainPatchBodies(t, client)
	if len(mains) != 1 {
		t.Fatalf("want 1 main-resource patch to unpause, got %d: %v", len(mains), mains)
	}
	if mains[0]["spec"].(map[string]any)["paused"] != false {
		t.Errorf("spec.paused = %v, want false", mains[0]["spec"])
	}
}

// Keeps the settle loop from dominating unit-test runtime.
func shortenSettleWaits(t *testing.T) {
	t.Helper()
	timeout, interval := promoteFullSettleTimeout, promoteFullSettleInterval
	promoteFullSettleTimeout, promoteFullSettleInterval = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { promoteFullSettleTimeout, promoteFullSettleInterval = timeout, interval })
}

// A single patch loses to the controller's concurrent status writes, so a Rollout still
// mid-revision with the flag missing has to be patched again.
func TestPromoteFullRepatchesWhenTheFlagIsDropped(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v3", "stableRS": "hash-v2"}
	}))
	client.PrependReactor("get", "rollouts", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, rolloutForTest("prod", "web", func(o map[string]any) {
			o["status"] = map[string]any{"currentPodHash": "hash-v3", "stableRS": "hash-v2"}
		}), nil
	})

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) < 2 {
		t.Fatalf("want the promoteFull patch re-applied, got %d: %v", len(bodies), bodies)
	}
}

// Once the controller has taken it to stable there is nothing left to re-apply.
func TestPromoteFullStopsRepatchingOnceStable(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v3", "stableRS": "hash-v2"}
	}))
	promoted := false
	client.PrependReactor("get", "rollouts", func(clienttesting.Action) (bool, runtime.Object, error) {
		stable := "hash-v2"
		if promoted {
			stable = "hash-v3"
		}
		promoted = true
		return true, rolloutForTest("prod", "web", func(o map[string]any) {
			o["status"] = map[string]any{"currentPodHash": "hash-v3", "stableRS": stable}
		}), nil
	})

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 1 {
		t.Errorf("want only the initial patch once the controller lands it, got %d: %v", len(bodies), bodies)
	}
}

// A fresh Rollout has neither hash set; Argo's != comparison is false there too,
// so parity means no promoteFull patch rather than treating empty as a new revision.
func TestPromoteFullTreatsUnsetHashesAsStable(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", nil))

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 0 {
		t.Errorf("patched status with no revision to promote: %v", bodies)
	}
}

// A rollback changes the template while status still describes the previous revision.
// Promoting in that window is discarded by the controller, so PromoteFull has to wait
// for observedGeneration to catch up before it patches.
func TestPromoteFullWaitsForTheControllerBeforePromoting(t *testing.T) {
	shortenSettleWaits(t)
	stale := rolloutForTest("prod", "web", func(o map[string]any) {
		o["metadata"].(map[string]any)["generation"] = int64(4)
		o["status"] = map[string]any{
			"currentPodHash":     "hash-v2",
			"stableRS":           "hash-v2",
			"observedGeneration": "3",
		}
	})
	client := newFakeRollouts(stale)

	// The controller catches up on the second read, and the rollback's revision appears.
	var gets int
	client.PrependReactor("get", "rollouts", func(clienttesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 2 {
			return false, nil, nil
		}
		caught := stale.DeepCopy()
		_ = unstructured.SetNestedField(caught.Object, "4", "status", "observedGeneration")
		_ = unstructured.SetNestedField(caught.Object, "hash-v1", "status", "currentPodHash")
		return true, caught, nil
	})

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}

	bodies := statusPatchBodies(t, client)
	if len(bodies) == 0 {
		t.Fatal("promoteFull was never patched once the controller had observed the rollback")
	}
	status, _ := bodies[0]["status"].(map[string]any)
	if status["promoteFull"] != true {
		t.Errorf("first status patch = %v, want status.promoteFull true", bodies[0])
	}
}

// A promotion the controller would discard must not be reported as one that happened.
func TestPromoteFullFailsWhenTheControllerNeverCatchesUp(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["metadata"].(map[string]any)["generation"] = int64(4)
		o["status"] = map[string]any{
			"currentPodHash":     "hash-v2",
			"stableRS":           "hash-v2",
			"observedGeneration": "3",
		}
	}))

	_, err := PromoteFull(context.Background(), client, "prod", "web")
	if err == nil {
		t.Fatal("PromoteFull reported success while the controller was still behind the rollback")
	}
	if !errors.Is(err, ErrControllerNotCaughtUp) {
		t.Errorf("err = %v, want it to wrap ErrControllerNotCaughtUp", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 0 {
		t.Errorf("patched a status the controller was about to overwrite: %v", bodies)
	}
}

// workloadRefRollout builds a Rollout whose pod template lives on a Deployment, plus
// that Deployment at the given generation. An undo patches the Deployment, so the
// Rollout's own generation never moves and cannot report whether the controller has
// caught up — status.workloadObservedGeneration is the counter that can.
func workloadRefRollout(t *testing.T, workloadObserved string, deployGeneration int64) (*unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	ro := rolloutForTest("prod", "web", func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"name":       "web-deploy",
		}
		o["metadata"].(map[string]any)["generation"] = int64(1)
		o["status"] = map[string]any{
			"currentPodHash":             "hash-v2",
			"stableRS":                   "hash-v2",
			"observedGeneration":         "1",
			"workloadObservedGeneration": workloadObserved,
		}
	})
	deploy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace":  "prod",
			"name":       "web-deploy",
			"generation": deployGeneration,
		},
		"spec": map[string]any{"template": map[string]any{}},
	}}
	return ro, deploy
}

// For workloadRef Rollouts the rollback lands on the Deployment, so the Rollout's
// generation is untouched and its stable hashes still describe the revision the
// controller has not replaced. Promoting there is discarded in silence.
func TestPromoteFullWaitsForTheWorkloadRefRollback(t *testing.T) {
	shortenSettleWaits(t)
	ro, deploy := workloadRefRollout(t, "2", 3)
	client := newFakeRollouts(ro, deploy)

	_, err := PromoteFull(context.Background(), client, "prod", "web")
	if err == nil {
		t.Fatal("PromoteFull reported success while the controller was still behind the workloadRef rollback")
	}
	if !errors.Is(err, ErrControllerNotCaughtUp) {
		t.Errorf("err = %v, want it to wrap ErrControllerNotCaughtUp", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 0 {
		t.Errorf("patched a status the controller was about to overwrite: %v", bodies)
	}
}

// Once the controller has observed the referenced workload, the Rollout's own
// generation is still 1 — the workload counter is what says it is safe to promote.
func TestPromoteFullPromotesOnceWorkloadRefIsObserved(t *testing.T) {
	shortenSettleWaits(t)
	ro, deploy := workloadRefRollout(t, "3", 3)
	_ = unstructured.SetNestedField(ro.Object, "hash-v1", "status", "currentPodHash")
	client := newFakeRollouts(ro, deploy)

	if _, err := PromoteFull(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	bodies := statusPatchBodies(t, client)
	if len(bodies) == 0 {
		t.Fatal("promoteFull was never patched once the controller had observed the referenced workload")
	}
	status, _ := bodies[0]["status"].(map[string]any)
	if status["promoteFull"] != true {
		t.Errorf("first status patch = %v, want status.promoteFull true", bodies[0])
	}
}

// An unreadable template source means the controller's progress cannot be judged.
// Assuming it caught up is exactly the bug this check exists to prevent.
func TestPromoteFullFailsWhenTheReferencedWorkloadCannotBeRead(t *testing.T) {
	shortenSettleWaits(t)
	ro, deploy := workloadRefRollout(t, "2", 3)
	client := newFakeRollouts(ro, deploy)
	client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web-deploy", errors.New("nope"))
	})

	_, err := PromoteFull(context.Background(), client, "prod", "web")
	if err == nil {
		t.Fatal("PromoteFull succeeded without being able to read the template source")
	}
	if !strings.Contains(err.Error(), "cannot tell whether the controller has reconciled") {
		t.Errorf("err = %v, want it to name the unreadable template source", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 0 {
		t.Errorf("patched status without knowing the controller had caught up: %v", bodies)
	}
}

// The counters are strings today, but a numeric value must not silently disable the check.
func TestPromoteFullHonoursNumericObservedGeneration(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["metadata"].(map[string]any)["generation"] = int64(4)
		o["status"] = map[string]any{
			"currentPodHash":     "hash-v2",
			"stableRS":           "hash-v2",
			"observedGeneration": int64(3),
		}
	}))

	_, err := PromoteFull(context.Background(), client, "prod", "web")
	if !errors.Is(err, ErrControllerNotCaughtUp) {
		t.Fatalf("err = %v, want ErrControllerNotCaughtUp for a numeric observedGeneration", err)
	}
}

// A stalled controller is a lagging dependency, not a malformed request.
func TestPromoteFullTimeoutCarriesTheControllerSentinel(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["metadata"].(map[string]any)["generation"] = int64(4)
		o["status"] = map[string]any{
			"currentPodHash":     "hash-v2",
			"stableRS":           "hash-v2",
			"observedGeneration": "3",
		}
	}))

	_, err := PromoteFull(context.Background(), client, "prod", "web")
	if !errors.Is(err, ErrControllerNotCaughtUp) {
		t.Fatalf("err = %v, want it to wrap ErrControllerNotCaughtUp", err)
	}
}

// Reporting a promotion that never happened is the failure this package exists to avoid,
// so a Rollout with nothing paused has to say so rather than claim it was promoted.
func TestPromoteReportsNoChangeWhenNothingIsPaused(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["strategy"] = map[string]any{"blueGreen": map[string]any{}}
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1"}
	}))

	res, err := Promote(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !res.NoChange {
		t.Errorf("NoChange = false, want true when no patch was issued")
	}
	if strings.Contains(res.Message, "promoted") {
		t.Errorf("message = %q, want it not to claim a promotion", res.Message)
	}
	if patches := recordedPatches(t, client); len(patches) != 0 {
		t.Errorf("issued %d patches for a no-op promote", len(patches))
	}
}

// The already-at-final case must report itself rather than reuse the promoted wording.
func TestPromoteFullReportsNoChangeWhenAlreadyAtFinalRevision(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v2"}
	}))

	res, err := PromoteFull(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if !res.NoChange {
		t.Errorf("NoChange = false, want true when nothing was promoted")
	}
	if strings.Contains(res.Message, "promoted to full") {
		t.Errorf("message = %q, want it not to claim a promotion", res.Message)
	}
}

// Claiming the steps were skipped without seeing the Rollout reach its final revision is
// the same guess this package exists to stop making.
func TestPromoteFullDoesNotClaimSuccessTheControllerNeverConfirmed(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1"}
	}))

	res, err := PromoteFull(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if strings.Contains(res.Message, "remaining steps, pauses, and analysis skipped") {
		t.Errorf("message = %q, want it not to claim a promotion the controller never confirmed", res.Message)
	}
	if !strings.Contains(res.Message, "not confirmed") {
		t.Errorf("message = %q, want it to say the promotion is unconfirmed", res.Message)
	}
}

// A paused Rollout still gets spec.paused cleared, which is a real mutation.
func TestPromoteFullDoesNotReportNoChangeWhenItUnpauses(t *testing.T) {
	shortenSettleWaits(t)
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["spec"].(map[string]any)["paused"] = true
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v2"}
	}))

	res, err := PromoteFull(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if res.NoChange {
		t.Errorf("NoChange = true after clearing spec.paused")
	}
}

// None of these verbs reads the Rollout back, so none of them can claim the cluster
// reached a state — with the controller down the patch lands and nothing else happens.
func TestRequestOnlyVerbsDoNotClaimAnOutcome(t *testing.T) {
	claims := []string{"aborted", "retried", " promoted", "advanced to step"}

	ops := map[string]func(context.Context, *fake.FakeDynamicClient) (OperationResult, error){
		"abort": func(ctx context.Context, c *fake.FakeDynamicClient) (OperationResult, error) {
			return Abort(ctx, c, "prod", "web")
		},
		"retry": func(ctx context.Context, c *fake.FakeDynamicClient) (OperationResult, error) {
			return Retry(ctx, c, "prod", "web")
		},
		"promote": func(ctx context.Context, c *fake.FakeDynamicClient) (OperationResult, error) {
			return Promote(ctx, c, "prod", "web")
		},
		"skip-step": func(ctx context.Context, c *fake.FakeDynamicClient) (OperationResult, error) {
			return SkipCurrentStep(ctx, c, "prod", "web")
		},
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
				o["status"] = map[string]any{
					"currentPodHash": "hash-v2",
					"stableRS":       "hash-v1",
					"pauseConditions": []any{
						map[string]any{"reason": "CanaryPauseStep"},
					},
				}
			}))

			res, err := op(context.Background(), client)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, claim := range claims {
				if strings.Contains(res.Message, claim) {
					t.Errorf("message %q claims %q, which nothing read back", res.Message, claim)
				}
			}
		})
	}
}
