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

// While promoteFull is set the controller clears status.abort on every reconcile, so
// sending the patch would report an abort that never takes effect.
func TestAbortRefusedDuringFullPromotion(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1", "promoteFull": true}
	}))

	_, err := Abort(context.Background(), client, "prod", "web")
	if !errors.Is(err, ErrPromoteFullInProgress) {
		t.Fatalf("Abort err = %v, want ErrPromoteFullInProgress", err)
	}
	if !strings.Contains(err.Error(), "roll back to the stable revision") {
		t.Errorf("error %q does not say what stops the promotion", err)
	}
	if patches := recordedPatches(t, client); len(patches) != 0 {
		t.Errorf("Abort patched the Rollout during a full promotion: %v", patches)
	}
}

// The flag outlives the promotion only until the controller's next write; once the new
// revision is stable it no longer blocks anything.
func TestAbortAllowedWhenPromoteFullHasLanded(t *testing.T) {
	client := newFakeRollouts(rolloutForTest("prod", "web", func(o map[string]any) {
		o["status"] = map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v2", "promoteFull": true}
	}))

	if _, err := Abort(context.Background(), client, "prod", "web"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(statusPatchBodies(t, client)) != 1 {
		t.Errorf("want the abort patch, got %v", statusPatchBodies(t, client))
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

	res, err := PromoteFull(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 1 {
		t.Errorf("want only the initial patch once the controller lands it, got %d: %v", len(bodies), bodies)
	}
	if res.Outcome != OutcomeLanded {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeLanded)
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
	if res.Outcome != "" {
		t.Errorf("Outcome = %q, want none when nothing was sent", res.Outcome)
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
	if res.Outcome != OutcomeUnconfirmed {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeUnconfirmed)
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

// scriptedRollouts answers every get from reads, in order (the last one repeats), and
// answers each patch with the next object from writes. It stands in for a controller
// whose status writes the test decides.
func scriptedRollouts(t *testing.T, reads, writes []*unstructured.Unstructured, others ...runtime.Object) *fake.FakeDynamicClient {
	t.Helper()
	client := newFakeRollouts(append([]runtime.Object{reads[0]}, others...)...)
	var gets, patches int
	client.PrependReactor("get", "rollouts", func(clienttesting.Action) (bool, runtime.Object, error) {
		ro := reads[min(gets, len(reads)-1)]
		gets++
		return true, ro.DeepCopy(), nil
	})
	client.PrependReactor("patch", "rollouts", func(clienttesting.Action) (bool, runtime.Object, error) {
		if patches >= len(writes) {
			t.Fatalf("unexpected patch %d", patches+1)
		}
		ro := writes[patches].DeepCopy()
		patches++
		return true, ro, nil
	})
	return client
}

// The test canary has 3 steps; step 1 is its manual pause.
type canaryState struct {
	promoteFull bool
	// Held at the manual pause, as the controller leaves it until it acts.
	held       bool
	specPaused bool
	aborted    bool
	invalid    bool
	// When the controller last flipped the Paused condition to False; "" while held.
	resumedAt string
	// A Progressing condition update, which the controller makes on many reconciles.
	progressedAt string
	stepIndex    int64
	// Our unpause landed and the controller reconciled it (generation 4).
	unpaused bool
	// A spec change the controller has not reconciled yet.
	unobservedSpec bool
	// Someone else's spec change, which the controller has published as observed on a
	// condition write but not yet reconciled in full.
	respecced bool
	// Overrides status.observedGeneration ("3"); "absent" removes it.
	observed string
}

// A canary mid-revision: hash-v2 is rolling out over hash-v1.
func midCanary(st canaryState) *unstructured.Unstructured {
	return rolloutForTest("prod", "web", func(o map[string]any) {
		meta := o["metadata"].(map[string]any)
		generation, observed := int64(3), "3"
		if st.unpaused || st.respecced {
			generation, observed = generation+1, "4"
		}
		if st.unobservedSpec {
			generation++
		}
		meta["generation"] = generation
		if st.specPaused {
			o["spec"].(map[string]any)["paused"] = true
		}
		status := map[string]any{
			"currentPodHash":     "hash-v2",
			"stableRS":           "hash-v1",
			"phase":              "Progressing",
			"observedGeneration": observed,
			"currentStepIndex":   st.stepIndex,
		}
		if st.held {
			status["phase"] = "Paused"
			status["controllerPause"] = true
			status["pauseConditions"] = []any{map[string]any{"reason": "CanaryPauseStep"}}
		}
		if st.promoteFull {
			status["promoteFull"] = true
		}
		if st.aborted {
			status["abort"] = true
			status["phase"] = "Degraded"
		}
		switch st.observed {
		case "":
		case "absent":
			delete(status, "observedGeneration")
		default:
			status["observedGeneration"] = st.observed
		}
		conds := []any{pausedCondition(st.resumedAt)}
		if st.progressedAt != "" {
			conds = append(conds, map[string]any{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated", "lastUpdateTime": st.progressedAt})
		}
		if st.invalid {
			conds = append(conds, map[string]any{"type": "InvalidSpec", "status": "True", "reason": "InvalidSpec"})
		}
		status["conditions"] = conds
		o["status"] = status
	})
}

func pausedCondition(resumedAt string) map[string]any {
	if resumedAt == "" {
		return map[string]any{"type": "Paused", "status": "True", "reason": "RolloutPaused", "lastUpdateTime": "2026-10-13T10:00:00Z"}
	}
	return map[string]any{"type": "Paused", "status": "False", "reason": "RolloutPaused", "lastUpdateTime": resumedAt}
}

const resumed = "2026-10-13T10:05:00Z"

func promoteFullWithin(t *testing.T, client *fake.FakeDynamicClient, timeout time.Duration) (OperationResult, time.Duration) {
	t.Helper()
	prevTimeout, prevInterval := promoteFullSettleTimeout, promoteFullSettleInterval
	promoteFullSettleTimeout, promoteFullSettleInterval = timeout, 5*time.Millisecond
	t.Cleanup(func() { promoteFullSettleTimeout, promoteFullSettleInterval = prevTimeout, prevInterval })

	start := time.Now()
	res, err := PromoteFull(context.Background(), client, "prod", "web")
	if err != nil {
		t.Fatalf("PromoteFull: %v", err)
	}
	return res, time.Since(start)
}

func assertUnderWayFast(t *testing.T, res OperationResult, elapsed time.Duration) {
	t.Helper()
	if elapsed > time.Second {
		t.Errorf("PromoteFull took %v, want it to return once the controller took the flag", elapsed)
	}
	if !strings.Contains(res.Message, "under way") {
		t.Errorf("message = %q, want it to say the promotion is under way", res.Message)
	}
	if res.Outcome != OutcomeUnderway {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeUnderway)
	}
}

// New pods can take minutes to become ready. Once the controller has acted on the flag
// and kept it, the promotion is under way and the caller has nothing left to wait for.
func TestPromoteFullReturnsOnceTheControllerTakesIt(t *testing.T) {
	cases := map[string]struct{ before, after canaryState }{
		// The manual pause: the controller resumes it and jumps to the last step.
		"held at a pause": {
			before: canaryState{held: true, stepIndex: 1},
			after:  canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3},
		},
		// Mid-step with nothing paused, which is also where a rollback starts over.
		"mid-step, nothing paused": {
			before: canaryState{resumedAt: "2026-10-13T09:00:00Z", stepIndex: 0},
			after:  canaryState{promoteFull: true, resumedAt: "2026-10-13T09:00:00Z", stepIndex: 3},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			written := tc.before
			written.promoteFull = true
			client := scriptedRollouts(t,
				[]*unstructured.Unstructured{midCanary(tc.before), midCanary(tc.after)},
				[]*unstructured.Unstructured{midCanary(written)})

			res, elapsed := promoteFullWithin(t, client, 2*time.Second)

			assertUnderWayFast(t, res, elapsed)
			if bodies := statusPatchBodies(t, client); len(bodies) != 1 {
				t.Errorf("want only the initial patch, got %d: %v", len(bodies), bodies)
			}
		})
	}
}

// A dropped flag is re-applied, and only the controller taking the re-applied flag counts.
func TestPromoteFullWaitsForTheControllerToTakeTheReappliedFlag(t *testing.T) {
	client := scriptedRollouts(t,
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, stepIndex: 1}),
			midCanary(canaryState{held: true, stepIndex: 1}),                    // the controller dropped the flag
			midCanary(canaryState{held: true, stepIndex: 1, promoteFull: true}), // our re-apply
			midCanary(canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3}),
		},
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, stepIndex: 1, promoteFull: true}),
			midCanary(canaryState{held: true, stepIndex: 1, promoteFull: true}),
		})

	res, elapsed := promoteFullWithin(t, client, 2*time.Second)

	assertUnderWayFast(t, res, elapsed)
	if bodies := statusPatchBodies(t, client); len(bodies) != 2 {
		t.Errorf("want the initial patch and one re-apply, got %d: %v", len(bodies), bodies)
	}
}

// Blue-green parks the new revision in preview behind a pause; taking the flag clears it.
func TestPromoteFullReturnsOnceTheControllerTakesItOnBlueGreen(t *testing.T) {
	blueGreen := func(held, promoteFull bool) *unstructured.Unstructured {
		return blueGreenRolloutForTest(func(o map[string]any) {
			status := map[string]any{"currentPodHash": "hash-v2", "stableRS": "hash-v1", "phase": "Progressing", "observedGeneration": "1"}
			o["metadata"].(map[string]any)["generation"] = int64(1)
			resumedAt := resumed
			if held {
				status["phase"] = "Paused"
				status["controllerPause"] = true
				status["pauseConditions"] = []any{map[string]any{"reason": "BlueGreenPause"}}
				resumedAt = ""
			}
			if promoteFull {
				status["promoteFull"] = true
			}
			status["conditions"] = []any{pausedCondition(resumedAt)}
			o["status"] = status
		})
	}
	client := scriptedRollouts(t,
		[]*unstructured.Unstructured{blueGreen(true, false), blueGreen(false, true)},
		[]*unstructured.Unstructured{blueGreen(true, true)})

	res, elapsed := promoteFullWithin(t, client, 2*time.Second)

	assertUnderWayFast(t, res, elapsed)
}

// A Rollout paused through spec.paused is unpaused by our spec write after the status
// write; the controller then resumes it.
func TestPromoteFullReturnsOnceTheControllerResumesASpecPausedRollout(t *testing.T) {
	client := scriptedRollouts(t,
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, specPaused: true, stepIndex: 1}),
			midCanary(canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3, unpaused: true}),
		},
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, specPaused: true, promoteFull: true, stepIndex: 1}),
			midCanary(canaryState{held: true, promoteFull: true, stepIndex: 1}),
		})

	res, elapsed := promoteFullWithin(t, client, 2*time.Second)

	assertUnderWayFast(t, res, elapsed)
}

// Only the controller acting on the flag counts. Each case has the flag still set but
// lacks that evidence, or has it alongside something that undoes it.
func TestPromoteFullStaysUnconfirmedUntilTheControllerActs(t *testing.T) {
	held := canaryState{held: true, stepIndex: 1}
	taken := canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3}
	with := func(st canaryState, change func(*canaryState)) canaryState {
		change(&st)
		return st
	}
	cases := map[string]struct {
		before, after canaryState
		specPaused    bool
	}{
		"someone else retries it": {
			before: canaryState{aborted: true, resumedAt: "2026-10-13T09:00:00Z", stepIndex: 1},
			after:  canaryState{promoteFull: true, resumedAt: "2026-10-13T09:00:00Z", stepIndex: 1},
		},
		"the controller writes but still holds it": {
			before: held,
			after:  with(held, func(s *canaryState) { s.promoteFull, s.progressedAt = true, resumed }),
		},
		"the controller rejects the spec": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.invalid = true }),
		},
		"a spec change the controller has not seen yet": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.unobservedSpec = true }),
		},
		"the controller's generation counter is missing": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.observed = "absent" }),
		},
		"the controller's generation counter is a legacy hash": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.observed = "5d4c799df8" }),
		},
		"already on its last step, waiting for the new pods": {
			before: canaryState{resumedAt: "2026-10-13T09:00:00Z", stepIndex: 3},
			after:  canaryState{promoteFull: true, resumedAt: "2026-10-13T09:00:00Z", stepIndex: 3},
		},
		"someone else's Promote clears the pause, no controller write since": {
			before: held,
			after:  canaryState{promoteFull: true, stepIndex: 1},
		},
		"our unpause not reconciled yet": {
			before: canaryState{held: true, specPaused: true, stepIndex: 1},
			after:  with(taken, func(s *canaryState) { s.unpaused, s.observed = true, "3" }),
		},
		"a newer spec the controller has published but not reconciled": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.respecced = true }),
		},
		"someone else aborts it": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.aborted = true }),
		},
		"someone else pauses it after the controller resumed it": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.specPaused = true }),
		},
		"a pause comes back after the controller resumed it": {
			before: held,
			after:  with(taken, func(s *canaryState) { s.held = true }),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			written := tc.before
			written.promoteFull = true
			writes := []*unstructured.Unstructured{midCanary(written)}
			if tc.before.specPaused {
				writes = append(writes, midCanary(written)) // the unpause
			}
			client := scriptedRollouts(t,
				[]*unstructured.Unstructured{midCanary(tc.before), midCanary(tc.after)}, writes)

			res, _ := promoteFullWithin(t, client, 50*time.Millisecond)

			if !strings.Contains(res.Message, "not confirmed") {
				t.Errorf("message = %q, want it unconfirmed when the controller never acted", res.Message)
			}
		})
	}
}

// A workloadRef Rollout keeps its steps on the Rollout, so a steps edit moves the
// Rollout's generation even though the referenced workload's is current.
func TestPromoteFullChecksBothGenerationsOnAWorkloadRefRollout(t *testing.T) {
	workloadRef := func(st canaryState) *unstructured.Unstructured {
		ro := midCanary(st)
		spec := ro.Object["spec"].(map[string]any)
		delete(spec, "template")
		spec["workloadRef"] = map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web-deploy"}
		_ = unstructured.SetNestedField(ro.Object, "2", "status", "workloadObservedGeneration")
		return ro
	}
	deploy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"namespace": "prod", "name": "web-deploy", "generation": int64(2)},
		"spec":       map[string]any{"template": map[string]any{}},
	}}
	held := canaryState{held: true, stepIndex: 1}
	written := canaryState{held: true, stepIndex: 1, promoteFull: true}
	taken := canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3}
	stepsEdited := taken
	stepsEdited.unobservedSpec = true

	t.Run("both observed", func(t *testing.T) {
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{workloadRef(held), workloadRef(taken)},
			[]*unstructured.Unstructured{workloadRef(written)}, deploy)

		res, elapsed := promoteFullWithin(t, client, 2*time.Second)

		assertUnderWayFast(t, res, elapsed)
	})
	t.Run("template edited on the workload since the promotion", func(t *testing.T) {
		edited := deploy.DeepCopy()
		edited.SetGeneration(3)
		after := workloadRef(taken)
		_ = unstructured.SetNestedField(after.Object, "3", "status", "workloadObservedGeneration")
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{workloadRef(held), after},
			[]*unstructured.Unstructured{workloadRef(written)}, edited)

		res, _ := promoteFullWithin(t, client, 50*time.Millisecond)

		if !strings.Contains(res.Message, "not confirmed") {
			t.Errorf("message = %q, want it unconfirmed after the workload's template changed", res.Message)
		}
	})
	t.Run("workload counter missing when we wrote", func(t *testing.T) {
		before := workloadRef(written)
		unstructured.RemoveNestedField(before.Object, "status", "workloadObservedGeneration")
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{workloadRef(held), workloadRef(taken)},
			[]*unstructured.Unstructured{before}, deploy)

		res, _ := promoteFullWithin(t, client, 50*time.Millisecond)

		if !strings.Contains(res.Message, "not confirmed") {
			t.Errorf("message = %q, want it unconfirmed without the workload counter to compare against", res.Message)
		}
	})
	t.Run("steps edit not observed yet", func(t *testing.T) {
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{workloadRef(held), workloadRef(stepsEdited)},
			[]*unstructured.Unstructured{workloadRef(written)}, deploy)

		res, _ := promoteFullWithin(t, client, 50*time.Millisecond)

		if !strings.Contains(res.Message, "not confirmed") {
			t.Errorf("message = %q, want it unconfirmed while the controller has not seen the steps edit", res.Message)
		}
	})
}

// Paused only through spec.paused there are no pause conditions. The controller flips
// the Paused condition as soon as our unpause lands, before it checks the template and
// steps, so that alone is not evidence; moving the canary to its last step is.
func TestPromoteFullOnASpecOnlyPauseWaitsForTheStrategySync(t *testing.T) {
	specPaused := canaryState{specPaused: true, stepIndex: 1}
	flipped := canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 1, unpaused: true}
	t.Run("Paused condition flipped, steps untouched", func(t *testing.T) {
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{midCanary(specPaused), midCanary(flipped)},
			[]*unstructured.Unstructured{
				midCanary(canaryState{specPaused: true, stepIndex: 1, promoteFull: true}),
				midCanary(canaryState{stepIndex: 1, promoteFull: true}),
			})

		res, _ := promoteFullWithin(t, client, 50*time.Millisecond)

		if !strings.Contains(res.Message, "not confirmed") {
			t.Errorf("message = %q, want the Paused flip alone not to count on a spec-only pause", res.Message)
		}
	})
	t.Run("sent to the last step", func(t *testing.T) {
		client := scriptedRollouts(t,
			[]*unstructured.Unstructured{
				midCanary(specPaused),
				midCanary(canaryState{promoteFull: true, resumedAt: resumed, stepIndex: 3, unpaused: true}),
			},
			[]*unstructured.Unstructured{
				midCanary(canaryState{specPaused: true, stepIndex: 1, promoteFull: true}),
				midCanary(canaryState{stepIndex: 1, promoteFull: true}),
			})

		res, elapsed := promoteFullWithin(t, client, 2*time.Second)

		assertUnderWayFast(t, res, elapsed)
	})
}

// After a re-apply the evidence is judged against the re-apply's own write. Here the
// controller dropped the flag and restarted the steps, so the Rollout was already
// resumed before the re-apply; that is not the controller taking the new flag.
func TestPromoteFullJudgesTheReappliedFlagFromItsOwnWrite(t *testing.T) {
	restarted := canaryState{resumedAt: resumed, stepIndex: 0}
	reapplied := restarted
	reapplied.promoteFull = true
	client := scriptedRollouts(t,
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, stepIndex: 1}),
			midCanary(restarted),
			midCanary(reapplied),
		},
		[]*unstructured.Unstructured{
			midCanary(canaryState{held: true, stepIndex: 1, promoteFull: true}),
			midCanary(reapplied),
		})

	res, _ := promoteFullWithin(t, client, 100*time.Millisecond)

	if !strings.Contains(res.Message, "not confirmed") {
		t.Errorf("message = %q, want it unconfirmed until the controller acts on the re-applied flag", res.Message)
	}
	if bodies := statusPatchBodies(t, client); len(bodies) != 2 {
		t.Errorf("want the initial patch and one re-apply, got %d: %v", len(bodies), bodies)
	}
}
