package issues

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// An Argo Rollouts Job metric that fails is the analysis's verdict. Its pod
// exits non-zero once and is never restarted, so the row it files under the
// AnalysisRun is a failed Job, not a crash loop.
func TestCompose_AnalysisRunJobFailureIsNotACrashLoop(t *testing.T) {
	defer k8s.ResetTestState()

	controller := true
	failedAt := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "4e86005b.error-rate.1", Namespace: "kargo-demo", UID: "job-uid",
			CreationTimestamp: metav1.NewTime(failedAt.Add(-time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "AnalysisRun", Name: "uat.01m89d5x.7c6082a", Controller: &controller}},
		},
		Status: batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: metav1.NewTime(failedAt)}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "4e86005b.error-rate.1-x7k2p", Namespace: "kargo-demo", CreationTimestamp: metav1.NewTime(failedAt.Add(-time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}},
		},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "smoke-fail", Image: "busybox:1.36"}}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: "smoke-fail", State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(failedAt)},
		}}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(job, pod)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(provider.DetectProblems(nil)) < 2 {
		time.Sleep(20 * time.Millisecond)
	}

	var analysisRows int
	for _, i := range Compose(provider, Filters{Grouped: true, Limit: NoLimit}) {
		if i.Category == issuesapi.CategoryCrashLoop {
			t.Errorf("crash loop row: %+v", i)
		}
		if i.Kind == "AnalysisRun" || i.Owner.Kind == "AnalysisRun" {
			analysisRows++
			if i.Category != issuesapi.CategoryJobFailed {
				t.Errorf("analysis row category = %s, want job_failed", i.Category)
			}
		}
	}
	if analysisRows != 1 {
		t.Fatalf("rows under the AnalysisRun = %d, want 1", analysisRows)
	}
}
