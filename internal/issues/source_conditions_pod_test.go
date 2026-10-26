package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCompletedPodConditionsThroughCacheProvider(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	controller := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "successful-run", Namespace: "batch", UID: "pod-uid",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "successful", UID: "job-uid", Controller: &controller}},
		},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever},
		Status: corev1.PodStatus{
			Phase:             corev1.PodSucceeded,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodCompleted", LastTransitionTime: metav1.Now()}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "run", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "successful", Namespace: "batch", UID: "job-uid", CreationTimestamp: pod.CreationTimestamp, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "schedule", UID: "cronjob-uid", Controller: &controller}}},
		Status:     batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	cronjob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "schedule", Namespace: "batch", UID: "cronjob-uid", CreationTimestamp: job.CreationTimestamp},
		Spec:       batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
		Status:     batchv1.CronJobStatus{LastScheduleTime: &pod.CreationTimestamp, LastSuccessfulTime: &pod.Status.Conditions[0].LastTransitionTime},
	}
	failedPod := pod.DeepCopy()
	failedPod.Name, failedPod.UID = "failed-run", "failed-pod-uid"
	failedPod.OwnerReferences = nil
	failedPod.Status.Phase = corev1.PodFailed
	failedPod.Status.Conditions[0].Reason = "PodFailed"
	failedPod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
	failedPod.Status.ContainerStatuses[0].State.Terminated.Reason = "Error"
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "batch", UID: "failed-job-uid", CreationTimestamp: pod.CreationTimestamp},
		Status:     batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}},
	}
	gateBlocked := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "readiness-gate-blocked", Namespace: "batch", CreationTimestamp: pod.CreationTimestamp},
		Spec:       corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady",
		}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod, job, cronjob, failedPod, failedJob, gateBlocked)); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, pod, failedPod, gateBlocked)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	assertNoCompletedIssue := func() {
		t.Helper()
		failedPodReported, failedJobReported := false, false
		for _, issue := range Compose(provider, Filters{Namespaces: []string{"batch"}, Limit: NoLimit}) {
			switch issue.Name {
			case pod.Name, job.Name, cronjob.Name:
				t.Errorf("successful CronJob run emitted an issue: %+v", issue)
			case failedPod.Name:
				failedPodReported = failedPodReported || issue.Source == SourceProblem
			case failedJob.Name:
				failedJobReported = failedJobReported || issue.Category == issuesapi.CategoryJobFailed
			}
		}
		if !failedPodReported || !failedJobReported {
			t.Errorf("failed batch work must stay visible: Pod=%v, Job=%v", failedPodReported, failedJobReported)
		}
	}
	if k8s.GetDynamicResourceCache().HasCoveringInformer(gvr, "batch") {
		t.Fatal("Pod dynamic watch unexpectedly exists before the on-demand read")
	}
	assertNoCompletedIssue()
	if _, err := k8s.GetDynamicResourceCache().ListBlocking(gvr, "batch", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	assertNoCompletedIssue()
	gateReported := false
	for _, issue := range Compose(provider, Filters{Namespaces: []string{"batch"}, Limit: NoLimit}) {
		if issue.Name == gateBlocked.Name && issue.Source == SourceCondition {
			gateReported = true
		}
	}
	if !gateReported {
		t.Fatal("a Pod blocked by its readiness gate must retain its condition issue")
	}
}

func TestGenericConditionsRespectCorePodCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, group, kind, phase, reason string
		wantIssue                        bool
	}{
		{name: "completed", kind: "Pod", phase: "Succeeded", reason: "PodCompleted"},
		{name: "completion overrides stale readiness", kind: "Pod", phase: "Succeeded", reason: "ContainersNotReady"},
		{name: "failed", kind: "Pod", phase: "Failed", reason: "PodFailed", wantIssue: true},
		{name: "failed with completed readiness reason", kind: "Pod", phase: "Failed", reason: "PodCompleted", wantIssue: true},
		{name: "running not ready", kind: "Pod", phase: "Running", reason: "ContainersNotReady", wantIssue: true},
		{name: "unknown phase", kind: "Pod", reason: "ContainersNotReady", wantIssue: true},
		{name: "CRD named Pod", group: "example.com", kind: "Pod", phase: "Succeeded", reason: "ReconcileFailed", wantIssue: true},
		{name: "CRD successful phase with failed condition", group: "example.com", kind: "Widget", phase: "Succeeded", reason: "ReconcileFailed", wantIssue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: "pods"}
			object := &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"name": "run", "namespace": "batch"},
				"status": map[string]any{
					"phase": tc.phase,
					"conditions": []any{map[string]any{
						"type": "Ready", "status": "False", "reason": tc.reason,
					}},
				},
			}}
			p := &fakeProvider{
				dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {object}},
				kinds:      map[schema.GroupVersionResource]string{gvr: tc.kind},
				namespaced: map[schema.GroupVersionResource]bool{gvr: true},
			}
			for _, namespaces := range [][]string{nil, {"batch"}} {
				out := Compose(p, Filters{Namespaces: namespaces, Limit: NoLimit})
				if (len(out) > 0) != tc.wantIssue {
					t.Fatalf("namespaces=%v: issues=%+v, want issue=%v", namespaces, out, tc.wantIssue)
				}
				if tc.wantIssue && (len(out) != 1 || out[0].Category != issuesapi.CategoryOperatorConditionFail || out[0].Reason != "Ready: "+tc.reason) {
					t.Fatalf("unexpected condition issue: %+v", out)
				}
			}
		})
	}
}

func TestReadinessGateFailureDoesNotRequireADynamicWatch(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-7 * time.Minute)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gate-blocked", Namespace: "test", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady", Message: "gate controller has not reported ready", LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute))},
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(since)},
	}, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	found := false
	for _, i := range Compose(provider, Filters{Namespaces: []string{"test"}}) {
		if i.Source == SourceProblem && i.Name == pod.Name {
			found = true
			if i.Category != issuesapi.CategoryReadinessFailed || i.Severity != SeverityWarning || i.Message != "gate controller has not reported ready" || i.Cause != "Pod readiness is blocked by a declared gate." || i.Action == "" || !i.FirstSeen.Equal(since) {
				t.Fatalf("wrong gate finding %+v", i)
			}
		}
	}
	if !found {
		t.Fatal("native gate issue absent")
	}
}
