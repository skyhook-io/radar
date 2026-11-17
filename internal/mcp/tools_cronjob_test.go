package mcp

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func setupCronJobMCP(t *testing.T) (*dynamicfake.FakeDynamicClient, schema.GroupVersionResource) {
	t.Helper()
	cronJobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		cronJobs: "CronJobList", jobs: "JobList",
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"name": "report", "namespace": "prod", "uid": "cj-uid"},
		"spec":     map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{}}},
	}})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "batch", Version: "v1", Kind: "CronJob", Name: "cronjobs", Namespaced: true},
		{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return dyn, jobs
}

func TestManageCronJobTriggerPassesTheJobName(t *testing.T) {
	dyn, jobs := setupCronJobMCP(t)

	res, _, err := handleManageCronJob(context.Background(), nil, manageCronJobInput{
		Action: "trigger", Namespace: "prod", Name: "report", JobName: "report-rerun",
	})
	if err != nil {
		t.Fatalf("handleManageCronJob: %v", err)
	}
	if got := decodeToolResult(t, res)["jobName"]; got != "report-rerun" {
		t.Errorf("jobName = %v, want report-rerun", got)
	}
	var created string
	for _, a := range dyn.Actions() {
		if ca, ok := a.(clienttesting.CreateAction); ok && ca.GetResource() == jobs {
			created = ca.GetObject().(*unstructured.Unstructured).GetName()
		}
	}
	if created != "report-rerun" {
		t.Errorf("created Job name = %q, want report-rerun", created)
	}
}

func TestManageCronJobRejectsJobNameOutsideTrigger(t *testing.T) {
	for _, action := range []string{"suspend", "resume"} {
		t.Run(action, func(t *testing.T) {
			dyn, _ := setupCronJobMCP(t)
			_, _, err := handleManageCronJob(context.Background(), nil, manageCronJobInput{
				Action: action, Namespace: "prod", Name: "report", JobName: "report-rerun",
			})
			if err == nil || !strings.Contains(err.Error(), "job_name") {
				t.Fatalf("err = %v, want one naming job_name", err)
			}
			if n := len(dyn.Actions()); n != 0 {
				t.Errorf("issued %d API calls", n)
			}
		})
	}
}
