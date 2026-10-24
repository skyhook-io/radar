package mcp

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var (
	deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	jobsGVR        = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	configMapsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

func fastWaitPolling(t *testing.T) {
	t.Helper()
	prev := waitPollInterval
	capBefore := waitMaxTimeout
	waitMaxTimeout = 80 * time.Millisecond
	waitPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { waitPollInterval = prev; waitMaxTimeout = capBefore })
}

// roomForPolls lifts the call cap for a test that expects several polls to
// reach met, so a loaded CI machine can't turn it into a timeout.
func roomForPolls(t *testing.T) {
	t.Helper()
	fastWaitPolling(t)
	waitMaxTimeout = 5 * time.Second
}

func waitDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		deploymentsGVR: "DeploymentList",
		jobsGVR:        "JobList",
		configMapsGVR:  "ConfigMapList",
		podsGVR:        "PodList",
		{Group: "apps", Version: "v1", Resource: "controllerrevisions"}: "ControllerRevisionList",
	}, objs...)
}

func deploymentObj(name string, generation, observed, desired, replicas, updated, available int64, conditions ...map[string]any) *unstructured.Unstructured {
	conds := make([]any, 0, len(conditions))
	for _, c := range conditions {
		conds = append(conds, c)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "shop", "generation": generation, "uid": "uid-" + name},
		"spec":     map[string]any{"replicas": desired},
		"status": map[string]any{
			"observedGeneration": observed, "replicas": replicas, "updatedReplicas": updated,
			"availableReplicas": available, "conditions": conds,
		},
	}}
}

func TestWaitForRolloutWaitsUntilDone(t *testing.T) {
	roomForPolls(t)
	dyn := waitDynamic()
	gets := 0
	dyn.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		gets++
		switch gets {
		case 1:
			return true, deploymentObj("web", 2, 1, 3, 3, 0, 3), nil
		case 2:
			return true, deploymentObj("web", 2, 2, 3, 4, 3, 2), nil
		case 3:
			return true, deploymentObj("web", 2, 2, 3, 3, 3, 2), nil
		}
		return true, deploymentObj("web", 2, 2, 3, 3, 3, 3), nil
	})
	res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "deployment", Namespace: "shop", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "met" || got["condition"] != "rollout" || gets != 4 {
		t.Fatalf("met after %d gets: %+v", gets, got)
	}
}

func TestRolloutStatusMirrorsKubectl(t *testing.T) {
	deadline := map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "ReplicaSet web-2 has timed out progressing."}
	for name, tc := range map[string]struct {
		obj         *unstructured.Unstructured
		met         bool
		wantMessage string
		wantFailure string
	}{
		"spec not observed":     {deploymentObj("web", 2, 1, 3, 3, 3, 3), false, "observe the new spec", ""},
		"replicas not updated":  {deploymentObj("web", 2, 2, 3, 3, 1, 3), false, "1 of 3 new replicas updated", ""},
		"old replicas linger":   {deploymentObj("web", 2, 2, 3, 4, 3, 3), false, "1 old replica(s) pending termination", ""},
		"updated not available": {deploymentObj("web", 2, 2, 3, 3, 3, 2), false, "2 of 3 updated replicas available", ""},
		"done":                  {deploymentObj("web", 2, 2, 3, 3, 3, 3), true, "rolled out", ""},
		"deadline exceeded":     {deploymentObj("web", 2, 2, 3, 3, 1, 2, deadline), false, "", "progress deadline exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			met, message, failure := rolloutStatus(tc.obj)
			if met != tc.met || !strings.Contains(message, tc.wantMessage) || !strings.Contains(failure, tc.wantFailure) || (tc.wantFailure == "") != (failure == "") {
				t.Fatalf("met=%v message=%q failure=%q", met, message, failure)
			}
		})
	}
}

func TestWaitForStopsOnFailure(t *testing.T) {
	fastWaitPolling(t)
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": "migrate", "namespace": "shop"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Failed", "status": "True", "message": "BackoffLimitExceeded"}}},
	}}
	res, _, err := waitFor(t.Context(), waitDynamic(job), waitForInput{Kind: "job", Namespace: "shop", Name: "migrate", TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "failed" || !strings.Contains(got["objects"].([]any)[0].(map[string]any)["observed"].(string), "BackoffLimitExceeded") || got["condition"] != "complete" {
		t.Fatalf("result: %+v", got)
	}
}

func TestWaitForTimesOutWithLastState(t *testing.T) {
	fastWaitPolling(t)
	stuck := deploymentObj("web", 2, 2, 3, 3, 1, 2)
	start := time.Now()
	res, _, err := waitFor(t.Context(), waitDynamic(stuck), waitForInput{Kind: "deployment", Namespace: "shop", Name: "web", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}
	got := decodeToolResult(t, res)
	objects := got["objects"].([]any)
	if got["state"] != "waiting" || !strings.Contains(objects[0].(map[string]any)["observed"].(string), "1 of 3 new replicas updated") {
		t.Fatalf("result: %+v", got)
	}
}

func TestWaitForDeleted(t *testing.T) {
	fastWaitPolling(t)
	cm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "token", "namespace": "shop", "uid": "old", "deletionTimestamp": "2026-10-18T10:00:00Z", "finalizers": []any{"example.com/cleanup"}},
	}}
	res, _, err := waitFor(t.Context(), waitDynamic(cm), waitForInput{Kind: "configmap", Namespace: "shop", Name: "token", Condition: "deleted", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "waiting" || !strings.Contains(got["objects"].([]any)[0].(map[string]any)["observed"].(string), "example.com/cleanup") {
		t.Fatalf("finalizer-held object: %+v", got)
	}

	// A same-name replacement is a new object; the one waited on is gone.
	dyn := waitDynamic()
	gets := 0
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		gets++
		obj := cm.DeepCopy()
		if gets > 1 {
			obj.SetUID("new")
			obj.SetDeletionTimestamp(nil)
			obj.SetFinalizers(nil)
		}
		return true, obj, nil
	})
	res, _, err = waitFor(t.Context(), dyn, waitForInput{Kind: "configmap", Namespace: "shop", Name: "token", Condition: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); got["state"] != "met" {
		t.Fatalf("replacement kept the wait open: %+v", got)
	}

	res, _, err = waitFor(t.Context(), waitDynamic(), waitForInput{Kind: "pod", Namespace: "shop", LabelSelector: "app=gone", Condition: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); got["state"] != "met" {
		t.Fatalf("no matches must satisfy deleted: %+v", got)
	}
}

func TestWaitForSelectorNeedsEveryMatch(t *testing.T) {
	fastWaitPolling(t)
	pod := func(name, ready string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": name, "namespace": "shop", "labels": map[string]any{"app": "web"}},
			"status":   map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": ready}}},
		}}
	}
	res, _, err := waitFor(t.Context(), waitDynamic(pod("a", "True"), pod("b", "False")), waitForInput{Kind: "pod", Namespace: "shop", LabelSelector: "app=web", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "waiting" || len(got["objects"].([]any)) != 2 {
		t.Fatalf("one unready pod must keep the wait open: %+v", got)
	}
}

func TestWaitForIgnoresConditionsFromAnOlderGeneration(t *testing.T) {
	check := statusConditionCheck("Available")
	stale := deploymentObj("web", 2, 1, 3, 3, 3, 3, map[string]any{"type": "Available", "status": "True"})
	if met, message, _ := check(stale); met || !strings.Contains(message, "generation 1") {
		t.Fatalf("stale Available=True accepted: met=%v %q", met, message)
	}
	perCondition := deploymentObj("web", 2, 2, 3, 3, 3, 3, map[string]any{"type": "Available", "status": "True", "observedGeneration": int64(1)})
	if met, _, _ := check(perCondition); met {
		t.Fatal("condition-level observedGeneration ignored")
	}
	current := deploymentObj("web", 2, 2, 3, 3, 3, 3, map[string]any{"type": "Available", "status": "True"})
	if met, _, _ := check(current); !met {
		t.Fatal("current Available=True not accepted")
	}
}

func TestWaitForAllCoversEveryObjectOfTheKind(t *testing.T) {
	fastWaitPolling(t)
	job := func(name string, done bool) *unstructured.Unstructured {
		status := "False"
		if done {
			status = "True"
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": "shop"},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": status}}},
		}}
	}
	res, _, err := waitFor(t.Context(), waitDynamic(job("a", true), job("b", false)), waitForInput{Kind: "job", Namespace: "shop", All: true, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); got["state"] != "waiting" || got["all"] != true || len(got["objects"].([]any)) != 2 {
		t.Fatalf("result: %+v", got)
	}
	res, _, err = waitFor(t.Context(), waitDynamic(job("a", true), job("b", true)), waitForInput{Kind: "job", Namespace: "shop", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); got["state"] != "met" {
		t.Fatalf("result: %+v", got)
	}
}

func TestWaitForRefusesBadInput(t *testing.T) {
	fastWaitPolling(t)
	dyn := waitDynamic()
	for name, tc := range map[string]struct {
		input waitForInput
		want  string
	}{
		"nothing matches":     {waitForInput{Kind: "deployment", Namespace: "shop", Name: "ghost"}, ""},
		"timeout above limit": {waitForInput{Kind: "deployment", Namespace: "shop", Name: "web", TimeoutSeconds: 601}, ""},
		"name and selector":   {waitForInput{Kind: "pod", Namespace: "shop", Name: "a", LabelSelector: "app=a"}, "exactly one"},
		"name and all":        {waitForInput{Kind: "pod", Namespace: "shop", Name: "a", All: true}, "exactly one"},
		"neither":             {waitForInput{Kind: "pod", Namespace: "shop"}, "exactly one"},
		"no namespace":        {waitForInput{Kind: "deployment", Name: "web"}, "namespace is required"},
		"no default":          {waitForInput{Kind: "configmap", Namespace: "shop", Name: "x"}, "condition is required"},
		"rollout on a job":    {waitForInput{Kind: "job", Namespace: "shop", Name: "x", Condition: "rollout"}, "applies to Deployment"},
	} {
		t.Run(name, func(t *testing.T) {
			res, _, err := waitFor(t.Context(), dyn, tc.input)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				got := decodeToolResult(t, res)
				if got["state"] != "waiting" || got["observed"] != "nothing matches" {
					t.Fatal(got)
				}
				if tc.input.TimeoutSeconds > 45 && got["note"] != "one call waits at most 45s; call again to keep waiting" {
					t.Fatal(got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
