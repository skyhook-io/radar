package k8score

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var (
	cronJobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobsGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

func batchDiscovery() *ResourceDiscovery {
	rd := &ResourceDiscovery{resourceMap: map[string]APIResource{}, gvrMap: map[string]schema.GroupVersionResource{}}
	for kind, gvr := range map[string]schema.GroupVersionResource{"CronJob": cronJobsGVR, "Job": jobsGVR} {
		res := APIResource{Kind: kind, Name: gvr.Resource, Group: gvr.Group, Version: gvr.Version, Namespaced: true}
		rd.resourceMap[strings.ToLower(kind)] = res
		rd.resourceMap[gvr.Resource] = res
		rd.gvrMap[strings.ToLower(kind)] = gvr
		rd.gvrMap[gvr.Resource] = gvr
	}
	return rd
}

func newBatchClient(cronJobName string) *dynamicfake.FakeDynamicClient {
	return newBatchClientWithJobSpec(cronJobName, nil)
}

func newBatchClientWithJobSpec(cronJobName string, jobSpec map[string]any) *dynamicfake.FakeDynamicClient {
	cronJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": cronJobName, "namespace": "prod", "uid": "cj-uid"},
		"spec": map[string]any{
			"schedule": "@daily",
			"jobTemplate": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "report"}},
				"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
					"containers": []any{map[string]any{"name": "app", "image": "report:v1"}},
				}}},
			},
		},
	}}
	for k, v := range jobSpec {
		cronJob.Object["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)[k] = v
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		cronJobsGVR: "CronJobList", jobsGVR: "JobList",
	}, []runtime.Object{cronJob}...)
}

func createdJob(t *testing.T, client *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	t.Helper()
	for _, a := range client.Actions() {
		if ca, ok := a.(clienttesting.CreateAction); ok && ca.GetResource() == jobsGVR {
			return ca.GetObject().(*unstructured.Unstructured)
		}
	}
	t.Fatal("no Job was created")
	return nil
}

// The apiserver appends 5 characters to generateName and truncates the base to
// 58, so a base that keeps "-manual-" whole always yields a name of at most 63:
// the limit of the job-name label the Job's Pods carry.
func TestTriggerCronJobDefaultNameFitsTheJobNameLabel(t *testing.T) {
	for _, cronJobName := range []string{
		"report",
		strings.Repeat("a", 52),
		strings.Repeat("a", 49) + "-bc",
		strings.Repeat("a", 49) + ".bc",
	} {
		t.Run(cronJobName, func(t *testing.T) {
			client := newBatchClient(cronJobName)
			if _, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", cronJobName, ""); err != nil {
				t.Fatalf("TriggerCronJobNamed: %v", err)
			}
			job := createdJob(t, client)
			if job.GetName() != "" {
				t.Errorf("name = %q, want empty so the apiserver generates it", job.GetName())
			}
			base := job.GetGenerateName()
			if len(base) > 58 || !strings.HasSuffix(base, "-manual-") {
				t.Errorf("generateName = %q (%d chars), want ≤58 ending in -manual-", base, len(base))
			}
			if errs := validation.IsDNS1123Subdomain(base + "abcde"); len(errs) > 0 {
				t.Errorf("generated name %q is invalid: %v", base+"abcde", errs)
			}
			if !strings.HasPrefix(cronJobName, strings.TrimSuffix(base, "-manual-")) {
				t.Errorf("generateName %q does not start with the CronJob name", base)
			}
			if got := job.GetAnnotations()["cronjob.kubernetes.io/instantiate"]; got != "manual" {
				t.Errorf("instantiate annotation = %q", got)
			}
			if refs := job.GetOwnerReferences(); len(refs) != 1 || refs[0].UID != "cj-uid" || refs[0].Name != cronJobName {
				t.Errorf("ownerReferences = %v", refs)
			}
			if job.GetLabels()["app"] != "report" {
				t.Errorf("labels = %v, want the jobTemplate's", job.GetLabels())
			}
		})
	}
}

func TestTriggerCronJobUsesTheGivenName(t *testing.T) {
	client := newBatchClient("report")
	got, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", "report", "report-rerun-1")
	if err != nil {
		t.Fatalf("TriggerCronJobNamed: %v", err)
	}
	if got.GetName() != "report-rerun-1" {
		t.Errorf("returned name = %q", got.GetName())
	}
	if job := createdJob(t, client); job.GetName() != "report-rerun-1" || job.GetGenerateName() != "" {
		t.Errorf("created name=%q generateName=%q", job.GetName(), job.GetGenerateName())
	}
}

func TestTriggerCronJobRejectsAnInvalidName(t *testing.T) {
	for _, jobName := range []string{"Report", "report_1", "-report", strings.Repeat("a", 64)} {
		t.Run(jobName, func(t *testing.T) {
			client := newBatchClient("report")
			_, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", "report", jobName)
			if !errors.Is(err, ErrInvalidJobName) {
				t.Fatalf("err = %v, want ErrInvalidJobName", err)
			}
			if len(client.Actions()) != 0 {
				t.Errorf("issued %d API calls before validating the name", len(client.Actions()))
			}
		})
	}
}

// An Indexed Job's Pods get the hostname <job>-<index>, a DNS label of at most
// 63 characters, so the generated name leaves room for the widest index and
// drops dots.
func TestTriggerIndexedCronJobDefaultNameLeavesRoomForTheIndex(t *testing.T) {
	for _, tc := range []struct {
		cronJobName string
		completions int64
	}{
		{strings.Repeat("a", 52), 1},
		{strings.Repeat("a", 52), 10},
		{strings.Repeat("a", 52), 1000},
		{"report.v2", 3},
	} {
		t.Run(tc.cronJobName, func(t *testing.T) {
			client := newBatchClientWithJobSpec(tc.cronJobName, map[string]any{"completionMode": "Indexed", "completions": tc.completions})
			if _, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", tc.cronJobName, ""); err != nil {
				t.Fatalf("TriggerCronJobNamed: %v", err)
			}
			base := createdJob(t, client).GetGenerateName()
			hostname := fmt.Sprintf("%sabcde-%d", base, tc.completions-1)
			if errs := validation.IsDNS1123Label(hostname); len(errs) > 0 {
				t.Errorf("widest Pod hostname %q is invalid: %v", hostname, errs)
			}
			if !strings.HasSuffix(base, "-manual-") {
				t.Errorf("generateName = %q, want it to end in -manual-", base)
			}
		})
	}
}

func TestTriggerIndexedCronJobRejectsADottedName(t *testing.T) {
	client := newBatchClientWithJobSpec("report", map[string]any{"completionMode": "Indexed", "completions": int64(2)})
	_, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", "report", "report.rerun")
	if !errors.Is(err, ErrInvalidJobName) {
		t.Fatalf("err = %v, want ErrInvalidJobName", err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("created a Job with a dotted name for an Indexed template")
		}
	}
}

func TestTriggerCronJobTrimsTheGivenName(t *testing.T) {
	client := newBatchClient("report")
	got, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", "report", "  report-rerun \n")
	if err != nil {
		t.Fatalf("TriggerCronJobNamed: %v", err)
	}
	if got.GetName() != "report-rerun" {
		t.Errorf("name = %q, want report-rerun", got.GetName())
	}
}

func TestTriggerCronJobBlankNameGeneratesOne(t *testing.T) {
	client := newBatchClient("report")
	if _, err := NewWorkloadManager(client, batchDiscovery()).TriggerCronJobNamed(context.Background(), "prod", "report", "   "); err != nil {
		t.Fatalf("TriggerCronJobNamed: %v", err)
	}
	if got := createdJob(t, client).GetGenerateName(); got != "report-manual-" {
		t.Errorf("generateName = %q, want report-manual-", got)
	}
}
