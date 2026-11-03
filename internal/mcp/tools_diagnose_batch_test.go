package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBatchDiagnoseRejectsGroupCollisions(t *testing.T) {
	for _, kind := range []string{"Job", "jobs", "JobSet", "jobsets", "RayJob", "rayjobs", "Workload", "workloads"} {
		_, _, err := handleDiagnose(context.Background(), nil, diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: kind, Group: "collision.io", Namespace: "alpha", Name: "x"}})
		if err == nil || !strings.Contains(err.Error(), "invalid group") {
			t.Fatalf("%s: collision accepted: %v", kind, err)
		}
	}
}

func TestBatchDiagnoseReturnsCanonicalAdmissionIssuesAndLimits(t *testing.T) {
	suspended, controller := true, true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "alpha", UID: "run-current"}, Spec: batchv1.JobSpec{Suspend: &suspended}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}}, job)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8s.ResetTestState(); getPermCache().Invalidate() })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload",
		"metadata": map[string]any{"name": "blocked", "namespace": "alpha", "uid": "workload-current", "generation": int64(1), "creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		"spec":     map[string]any{"queueName": "absent"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "QuotaReserved", "status": "False", "reason": "Inadmissible", "message": "LocalQueue absent doesn't exist", "observedGeneration": int64(1)}}},
	}}
	w.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}, w)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: gvr.Group, Version: gvr.Version, Kind: "Workload", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if _, err := cache.ListBlocking(gvr, "alpha", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	input := diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: "Workload", Namespace: "alpha", Name: "blocked"}}
	result, _, err := handleDiagnose(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	raw := renderContent(result.Content)
	var body struct {
		ResourceContext *resourcecontext.ResourceContext `json:"resourceContext"`
		RelatedIssues   []issues.Issue                   `json:"relatedIssues"`
		Coverage        string                           `json:"coverage"`
		Pods            *int                             `json:"pods"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.RelatedIssues) != 1 || body.RelatedIssues[0].Reason != issues.ReasonKueueLocalQueueMissing || body.ResourceContext == nil || body.ResourceContext.Scheduling == nil {
		t.Fatalf("missing canonical Issue/admission context: %s", raw)
	}
	if body.Pods != nil || !strings.Contains(body.Coverage, "runtime logs and metrics were not collected") {
		t.Fatalf("invented runtime coverage: %s", raw)
	}
	jobInput := diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: "Job", Namespace: "alpha", Name: "run"}}
	jobResult, _, err := handleDiagnose(context.Background(), nil, jobInput)
	if err != nil {
		t.Fatal(err)
	}
	var jobBody struct {
		RelatedIssues []issues.Issue `json:"relatedIssues"`
		Coverage      string         `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(renderContent(jobResult.Content)), &jobBody); err != nil {
		t.Fatal(err)
	}
	if len(jobBody.RelatedIssues) != 1 || jobBody.RelatedIssues[0].Reason != issues.ReasonKueueLocalQueueMissing || jobBody.Coverage == "" {
		t.Fatalf("Job did not find its Workload finding: %s", renderContent(jobResult.Content))
	}
	ctx := withTestUserPerms(t, "other-team", nil, []string{"other"})
	if _, _, err := handleDiagnose(ctx, nil, input); err == nil {
		t.Fatal("diagnose bypassed namespace access")
	}
	// Recovery must reach diagnosis through the same canonical composer.
	_ = unstructured.SetNestedSlice(w.Object, []any{map[string]any{"type": "Admitted", "status": "True", "reason": "Admitted", "observedGeneration": int64(1)}}, "status", "conditions")
	if _, err := client.Resource(gvr).Namespace("alpha").Update(context.Background(), w, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		result, _, err = handleDiagnose(context.Background(), nil, input)
		if err != nil {
			t.Fatal(err)
		}
		body.RelatedIssues = nil
		if err := json.Unmarshal([]byte(renderContent(result.Content)), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.RelatedIssues) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered Workload still diagnosed as blocked")
		}
	}
}
