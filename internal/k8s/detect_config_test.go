package k8s

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFindDuplicateEnvVarsForObject(t *testing.T) {
	t.Run("motivating duplicate reports positions values and final declaration", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "APP_HOST", Value: "api"},
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "APP_HOST", Value: "localhost"},
			},
		}}, nil)

		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 {
			t.Fatalf("got %d checks, want 1: %+v", len(got), got)
		}
		check := got[0]
		if check.Container != "app" || check.EnvName != "APP_HOST" || check.LastDeclaredValue != "localhost" {
			t.Fatalf("unexpected check: %+v", check)
		}
		if len(check.Occurrences) != 2 || check.Occurrences[0].Position != 1 || check.Occurrences[0].Value != "api" || check.Occurrences[1].Position != 3 || check.Occurrences[1].Value != "localhost" {
			t.Fatalf("unexpected occurrences: %+v", check.Occurrences)
		}
		for _, want := range []string{"app", "APP_HOST", "2 times", `1="api"`, `3="localhost"`, "typically takes effect"} {
			if !strings.Contains(check.Message, want) {
				t.Errorf("message %q does not contain %q", check.Message, want)
			}
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env:  []corev1.EnvVar{{Name: "APP_HOST", Value: "api"}, {Name: "LOG_LEVEL", Value: "info"}},
		}}, nil)
		if got := FindDuplicateEnvVarsForObject(deployment); len(got) != 0 {
			t.Fatalf("got duplicate checks for unique env vars: %+v", got)
		}
	})

	t.Run("init container", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment(nil, []corev1.Container{{
			Name: "migrate",
			Env:  []corev1.EnvVar{{Name: "DB_HOST", Value: "old"}, {Name: "DB_HOST", Value: "new"}},
		}})
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || got[0].Container != "migrate" || got[0].LastDeclaredValue != "new" {
			t.Fatalf("unexpected init-container checks: %+v", got)
		}
	})

	t.Run("three declarations emit once", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env:  []corev1.EnvVar{{Name: "MODE", Value: "one"}, {Name: "MODE", Value: "two"}, {Name: "MODE", Value: "three"}},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || len(got[0].Occurrences) != 3 || got[0].LastDeclaredValue != "three" {
			t.Fatalf("unexpected three-way duplicate: %+v", got)
		}
	})

	t.Run("containers are independent", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{
			{Name: "app", Env: []corev1.EnvVar{{Name: "PORT", Value: "8080"}, {Name: "PORT", Value: "8081"}}},
			{Name: "sidecar", Env: []corev1.EnvVar{{Name: "PORT", Value: "9090"}, {Name: "PORT", Value: "9091"}}},
			{Name: "metrics", Env: []corev1.EnvVar{{Name: "PORT", Value: "9100"}}},
		}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 2 || got[0].Container != "app" || got[1].Container != "sidecar" {
			t.Fatalf("unexpected per-container checks: %+v", got)
		}

		deployment = duplicateEnvTestDeployment([]corev1.Container{
			{Name: "app", Env: []corev1.EnvVar{{Name: "SHARED", Value: "one"}}},
			{Name: "sidecar", Env: []corev1.EnvVar{{Name: "SHARED", Value: "two"}}},
		}, nil)
		if got := FindDuplicateEnvVarsForObject(deployment); len(got) != 0 {
			t.Fatalf("same name in separate containers must not be flagged: %+v", got)
		}
	})

	t.Run("identical values still duplicate and names are case sensitive", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "MODE", Value: "prod"},
				{Name: "mode", Value: "lowercase"},
				{Name: "MODE", Value: "prod"},
			},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || got[0].EnvName != "MODE" || len(got[0].Occurrences) != 2 {
			t.Fatalf("unexpected exact-name duplicate result: %+v", got)
		}
	})

	t.Run("secret values are redacted in live message", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "API_TOKEN", Value: "first-super-secret-token"},
				{Name: "API_TOKEN", Value: "second-super-secret-token"},
			},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 {
			t.Fatalf("got %d checks, want 1: %+v", len(got), got)
		}
		check := got[0]
		if check.LastDeclaredValue != "[REDACTED]" || check.Occurrences[0].Value != "[REDACTED]" || check.Occurrences[1].Value != "[REDACTED]" {
			t.Fatalf("secret values were not redacted: %+v", check)
		}
		if strings.Contains(check.Message, "first-super-secret-token") || strings.Contains(check.Message, "second-super-secret-token") || !strings.Contains(check.Message, "[REDACTED]") {
			t.Fatalf("live detection message is not safely redacted: %q", check.Message)
		}
	})

	t.Run("pathological duplicates bound message detail", func(t *testing.T) {
		env := make([]corev1.EnvVar, 0, 7)
		for _, value := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
			env = append(env, corev1.EnvVar{Name: "MODE", Value: value})
		}
		deployment := duplicateEnvTestDeployment([]corev1.Container{{Name: "app", Env: env}}, nil)

		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || len(got[0].Occurrences) != 7 || got[0].LastDeclaredValue != "seven" {
			t.Fatalf("unexpected bounded duplicate check: %+v", got)
		}
		for _, want := range []string{"7 times", `5="five"`, "... and 2 more", `last definition "seven"`} {
			if !strings.Contains(got[0].Message, want) {
				t.Errorf("message %q does not contain %q", got[0].Message, want)
			}
		}
		if strings.Contains(got[0].Message, `6="six"`) || strings.Contains(got[0].Message, `7="seven"`) {
			t.Fatalf("message contains unbounded occurrence detail: %q", got[0].Message)
		}
	})
}

func TestDetectConfigProblemsIncludesDuplicateEnvVars(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	deployment := duplicateEnvTestDeployment([]corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "APP_HOST", Value: "api"}, {Name: "APP_HOST", Value: "localhost"}},
	}}, nil)
	deployment.CreationTimestamp = metav1.NewTime(now.Add(-2 * time.Hour))

	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(deployment),
		ResourceTypes: map[string]bool{k8score.Deployments: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}

	got := detectConfigProblems(cache, "prod", now)
	if len(got) != 1 {
		t.Fatalf("got %d config problems, want 1: %+v", len(got), got)
	}
	detection := got[0]
	if detection.Kind != "Deployment" || detection.Group != "apps" || detection.Namespace != "prod" || detection.Name != "web" {
		t.Fatalf("unexpected subject: %+v", detection)
	}
	if detection.Severity != "warning" || detection.Reason != "DuplicateEnvVar" {
		t.Fatalf("unexpected severity/reason: %+v", detection)
	}
	if detection.Fingerprint != FormatDuplicateEnvVarFingerprint("prod", "web", "app", "APP_HOST") || detection.AgeSeconds != 7200 {
		t.Fatalf("unexpected identity/age: %+v", detection)
	}
}

func duplicateEnvTestDeployment(containers, initContainers []corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: metav1.Now()},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers, InitContainers: initContainers}},
		},
	}
}

func TestFindContainerCompletionSplitForObject(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)

	t.Run("CronJob and Job expose the same observation", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		cache := containerCompletionSplitTestCache(t, fixture)

		for _, subject := range []runtime.Object{fixture.cronJob, fixture.jobs[0]} {
			shape := FindContainerCompletionSplitForObject(cache, subject, now)
			if shape == nil {
				t.Fatalf("%T observation is nil", subject)
			}
			if shape.ExitedContainer != "archiver" || !slices.Equal(shape.RunningContainers, []string{"log-agent"}) ||
				shape.Job != fixture.jobs[0].Name || shape.Pod != fixture.pods[0].Name {
				t.Fatalf("%T observation = %+v", subject, shape)
			}
		}
	})

	t.Run("all running siblings are reported in stable order", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("worker", 0, now.Add(-10*time.Minute)),
			containerCompletionSplitRunningStatus("istio-proxy", now.Add(-time.Hour)),
			containerCompletionSplitRunningStatus("cloudsql-proxy", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now)
		if shape == nil || !slices.Equal(shape.RunningContainers, []string{"cloudsql-proxy", "istio-proxy"}) {
			t.Fatalf("running siblings were incomplete or unstable: %+v", shape)
		}
	})

	t.Run("ownership requires name and UID", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.jobs[0].OwnerReferences[0].UID = types.UID("old-cronjob")
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("CronJob accepted a Job from another UID: %+v", shape)
		}

		fixture = newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].OwnerReferences[0].UID = types.UID("old-job")
		cache = containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("Job accepted a Pod from another UID: %+v", shape)
		}
	})

	t.Run("suspended Job is quiet", func(t *testing.T) {
		suspended := true
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.jobs[0].Spec.Suspend = &suspended
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("suspended Job produced an observation: %+v", shape)
		}
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("CronJob exposed a suspended child Job observation: %+v", shape)
		}
	})

	t.Run("suspended CronJob still exposes an active child", func(t *testing.T) {
		suspended := true
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.cronJob.Spec.Suspend = &suspended
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape == nil {
			t.Fatal("suspended CronJob hid its active child observation")
		}
	})

	t.Run("terminating Job and Pod are quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		deletedAt := metav1.NewTime(now.Add(-time.Minute))
		fixture.jobs[0].DeletionTimestamp = &deletedAt
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("CronJob accepted a terminating Job: %+v", shape)
		}
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("terminating Job produced an observation: %+v", shape)
		}

		fixture = newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].DeletionTimestamp = &deletedAt
		cache = containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("CronJob accepted a terminating Pod: %+v", shape)
		}
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("Job accepted a terminating Pod: %+v", shape)
		}
	})

	t.Run("failed Pod is quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.Phase = corev1.PodFailed
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("CronJob accepted a failed Pod: %+v", shape)
		}
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("Job accepted a failed Pod: %+v", shape)
		}
	})

	t.Run("non-running Pod phases are quiet", func(t *testing.T) {
		for _, phase := range []corev1.PodPhase{corev1.PodPending, corev1.PodUnknown} {
			fixture := newContainerCompletionSplitFixture(now, 1)
			fixture.pods[0].Status.Phase = phase
			cache := containerCompletionSplitTestCache(t, fixture)
			if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
				t.Fatalf("%s Pod produced an observation: %+v", phase, shape)
			}
		}
	})

	t.Run("native restartable init sidecar is excluded", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-10*time.Minute)),
		}
		fixture.pods[0].Status.InitContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitRunningStatus("native-sidecar", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("native sidecar produced an observation: %+v", shape)
		}
	})

	t.Run("transient split within minimum grace is quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-time.Minute)),
			containerCompletionSplitRunningStatus("log-agent", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("transient split produced an observation: %+v", shape)
		}
	})

	t.Run("recent successful exit keeps an older split quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("fetch-config", 0, now.Add(-10*time.Minute)),
			containerCompletionSplitTerminatedStatus("worker", 0, now.Add(-time.Minute)),
			containerCompletionSplitRunningStatus("log-agent", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("older exit bypassed grace period for the latest split: %+v", shape)
		}
	})

	t.Run("running container restart does not reset the split", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-10*time.Minute)),
			containerCompletionSplitRunningStatus("log-agent", now.Add(-time.Minute)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now)
		if shape == nil || shape.SinceSeconds != int64((10*time.Minute).Seconds()) {
			t.Fatalf("running-container restart reset the completion clock: %+v", shape)
		}
	})

	t.Run("latest successful exit anchors the split", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("fetch-config", 0, now.Add(-6*time.Hour)),
			containerCompletionSplitTerminatedStatus("worker", 0, now.Add(-10*time.Minute)),
			containerCompletionSplitRunningStatus("log-agent", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now)
		if shape == nil || shape.ExitedContainer != "worker" || shape.SinceSeconds != int64((10*time.Minute).Seconds()) {
			t.Fatalf("split was not anchored to the latest successful exit: %+v", shape)
		}
	})

	t.Run("equal completion times use the container-name tie break", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		finishedAt := now.Add(-10 * time.Minute)
		fixture.pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{
			containerCompletionSplitTerminatedStatus("z-worker", 0, finishedAt),
			containerCompletionSplitTerminatedStatus("a-worker", 0, finishedAt),
			containerCompletionSplitRunningStatus("log-agent", now.Add(-time.Hour)),
		}
		cache := containerCompletionSplitTestCache(t, fixture)
		shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now)
		if shape == nil || shape.ExitedContainer != "a-worker" {
			t.Fatalf("equal completion times were not deterministic: %+v", shape)
		}
	})

	t.Run("failed container exit is quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.pods[0].Status.ContainerStatuses[0] = containerCompletionSplitTerminatedStatus("archiver", 1, now.Add(-10*time.Minute))
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape != nil {
			t.Fatalf("failed exit produced an observation: %+v", shape)
		}
	})

	t.Run("parallel Job with successful pods still reports active split", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		fixture.jobs[0].Status.Succeeded = 2
		cache := containerCompletionSplitTestCache(t, fixture)
		if shape := FindContainerCompletionSplitForObject(cache, fixture.jobs[0], now); shape == nil {
			t.Fatal("parallel Job observation is nil")
		}
		if shape := FindContainerCompletionSplitForObject(cache, fixture.cronJob, now); shape == nil {
			t.Fatal("parallel CronJob observation is nil")
		}
	})

	t.Run("unrelated kind is quiet", func(t *testing.T) {
		fixture := newContainerCompletionSplitFixture(now, 1)
		cache := containerCompletionSplitTestCache(t, fixture)
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "prod"}}
		if shape := FindContainerCompletionSplitForObject(cache, deployment, now); shape != nil {
			t.Fatalf("Deployment produced an observation: %+v", shape)
		}
	})
}

func TestFindContainerCompletionSplitEvidenceIsDeterministic(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	fixture := newContainerCompletionSplitFixture(now, 2)
	fixture.pods[0].Status.ContainerStatuses[0] = containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-10*time.Minute))
	fixture.pods[1].Status.ContainerStatuses[0] = containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-20*time.Minute))
	activeJobs := map[string]*batchv1.Job{
		fixture.jobs[0].Name: fixture.jobs[0],
		fixture.jobs[1].Name: fixture.jobs[1],
	}

	forward, ok := findContainerCompletionSplitEvidence(fixture.pods, activeJobs, now)
	if !ok {
		t.Fatal("forward lookup found no evidence")
	}
	reverse, ok := findContainerCompletionSplitEvidence([]*corev1.Pod{fixture.pods[1], fixture.pods[0]}, activeJobs, now)
	if !ok {
		t.Fatal("reverse lookup found no evidence")
	}
	if !reflect.DeepEqual(forward, reverse) || forward.podName != fixture.pods[1].Name {
		t.Fatalf("evidence changed with input order: forward=%+v reverse=%+v", forward, reverse)
	}

	for i := range fixture.pods {
		fixture.pods[i].Status.ContainerStatuses[0] = containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-10*time.Minute))
	}
	forward, ok = findContainerCompletionSplitEvidence(fixture.pods, activeJobs, now)
	if !ok {
		t.Fatal("equal-time forward lookup found no evidence")
	}
	reverse, ok = findContainerCompletionSplitEvidence([]*corev1.Pod{fixture.pods[1], fixture.pods[0]}, activeJobs, now)
	if !ok {
		t.Fatal("equal-time reverse lookup found no evidence")
	}
	if !reflect.DeepEqual(forward, reverse) || forward.podName != fixture.pods[0].Name {
		t.Fatalf("equal-time evidence changed with input order: forward=%+v reverse=%+v", forward, reverse)
	}
}

type containerCompletionSplitFixture struct {
	cronJob *batchv1.CronJob
	jobs    []*batchv1.Job
	pods    []*corev1.Pod
}

func newContainerCompletionSplitFixture(now time.Time, activeJobs int) *containerCompletionSplitFixture {
	controller := true
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "audit-log-archiver",
			Namespace: "prod",
			UID:       types.UID("cronjob-uid"),
		},
		Spec: batchv1.CronJobSpec{Schedule: "* * * * *"},
	}
	fixture := &containerCompletionSplitFixture{cronJob: cronJob}
	for i := 0; i < activeJobs; i++ {
		name := fmt.Sprintf("audit-log-archiver-%d", i)
		jobUID := types.UID("job-" + name)
		startedAt := metav1.NewTime(now.Add(time.Duration(i-2) * time.Minute))
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "prod",
				UID:       jobUID,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1",
					Kind:       "CronJob",
					Name:       cronJob.Name,
					UID:        cronJob.UID,
					Controller: &controller,
				}},
			},
			Status: batchv1.JobStatus{Active: 1, StartTime: &startedAt},
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name + "-pod",
				Namespace: "prod",
				UID:       types.UID("pod-" + name),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1",
					Kind:       "Job",
					Name:       job.Name,
					UID:        job.UID,
					Controller: &controller,
				}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					containerCompletionSplitTerminatedStatus("archiver", 0, now.Add(-2*time.Hour)),
					containerCompletionSplitRunningStatus("log-agent", now.Add(-3*time.Hour)),
				},
			},
		}
		fixture.jobs = append(fixture.jobs, job)
		fixture.pods = append(fixture.pods, pod)
	}
	return fixture
}

func containerCompletionSplitTerminatedStatus(name string, exitCode int32, finishedAt time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode:   exitCode,
			FinishedAt: metav1.NewTime(finishedAt),
		}},
	}
}

func containerCompletionSplitRunningStatus(name string, startedAt time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.NewTime(startedAt),
		}},
	}
}

func containerCompletionSplitTestCache(t *testing.T, fixtures ...*containerCompletionSplitFixture) *ResourceCache {
	t.Helper()
	var objects []runtime.Object
	for _, fixture := range fixtures {
		objects = append(objects, fixture.cronJob)
		for _, job := range fixture.jobs {
			objects = append(objects, job)
		}
		for _, pod := range fixture.pods {
			objects = append(objects, pod)
		}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(objects...),
		ResourceTypes: map[string]bool{
			k8score.CronJobs: true,
			k8score.Jobs:     true,
			k8score.Pods:     true,
		},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core}
}
