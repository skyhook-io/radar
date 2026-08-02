package k8s

import (
	"fmt"
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

	got := detectConfigProblems(cache, "prod", now, listPodsByNamespace(cache, "prod"))
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

func TestDetectStuckSidecarJobs(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name            string
		mutate          func(*sidecarJobFixture)
		allNamespaces   bool
		wantDetections  int
		wantActiveCount int
		wantAge         time.Duration
		wantDuration    time.Duration
	}{
		{
			name:            "accumulating jobs with completed and running containers",
			wantDetections:  1,
			wantActiveCount: 3,
		},
		{
			name:            "all namespace scan",
			allNamespaces:   true,
			wantDetections:  1,
			wantActiveCount: 3,
		},
		{
			name: "split work without accumulation",
			mutate: func(f *sidecarJobFixture) {
				f.jobs = f.jobs[:1]
				f.pods = f.pods[:1]
			},
		},
		{
			name: "overlapping single container jobs",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{sidecarTestRunningStatus("worker", now.Add(-10*time.Minute))}
				}
			},
		},
		{
			name: "recent CronJob success",
			mutate: func(f *sidecarJobFixture) {
				recent := metav1.NewTime(now.Add(-5 * time.Minute))
				f.cronJob.Status.LastSuccessfulTime = &recent
			},
		},
		{
			name: "recent succeeded owned Job",
			mutate: func(f *sidecarJobFixture) {
				f.jobs = append(f.jobs, sidecarTestSucceededJob(f.cronJob, "recent-success", now.Add(-5*time.Minute)))
			},
		},
		{
			name: "partial success without completion time suppresses conservatively",
			mutate: func(f *sidecarJobFixture) {
				job := sidecarTestSucceededJob(f.cronJob, "partial-success", now.Add(-24*time.Hour))
				job.Status.CompletionTime = nil
				f.jobs = append(f.jobs, job)
			},
		},
		{
			name: "old retained successes do not hide a new completion drought",
			mutate: func(f *sidecarJobFixture) {
				old := metav1.NewTime(now.Add(-24 * time.Hour))
				f.cronJob.Status.LastSuccessfulTime = &old
				for i := 0; i < 3; i++ {
					f.jobs = append(f.jobs, sidecarTestSucceededJob(f.cronJob, fmt.Sprintf("old-success-%d", i), old.Time))
				}
			},
			wantDetections:  1,
			wantActiveCount: 3,
		},
		{
			name: "crashed workload container",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
				}
			},
		},
		{
			name: "native sidecar is ignored",
			mutate: func(f *sidecarJobFixture) {
				restartAlways := corev1.ContainerRestartPolicyAlways
				for _, pod := range f.pods {
					pod.Spec.Containers = []corev1.Container{{Name: "archiver"}}
					pod.Spec.InitContainers = []corev1.Container{{Name: "fluent-bit-sidecar", RestartPolicy: &restartAlways}}
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{sidecarTestCompletedStatus("archiver", 0, now.Add(-10*time.Minute))}
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-20*time.Minute))}
				}
			},
		},
		{
			name: "current container pair is within grace",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-time.Minute))
				}
			},
		},
		{
			name: "container pair at grace boundary fires",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-2*time.Minute))
				}
			},
			wantDetections:  1,
			wantActiveCount: 3,
			wantDuration:    2 * time.Minute,
		},
		{
			name: "hourly schedule requires an hour in the stuck shape",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.Schedule = "0 * * * *"
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-30*time.Minute))
				}
			},
		},
		{
			name: "hourly schedule fires after schedule-aware grace",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.Schedule = "0 * * * *"
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-61*time.Minute))
					pod.Status.ContainerStatuses[1] = sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-2*time.Hour))
				}
			},
			wantDetections:  1,
			wantActiveCount: 3,
			wantDuration:    61 * time.Minute,
		},
		{
			name: "hourly schedule uses two intervals for recent success",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.Schedule = "0 * * * *"
				recent := metav1.NewTime(now.Add(-90 * time.Minute))
				f.cronJob.Status.LastSuccessfulTime = &recent
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-3*time.Hour))
					pod.Status.ContainerStatuses[1] = sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-4*time.Hour))
				}
			},
		},
		{
			name: "hourly schedule ignores success older than two intervals",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.Schedule = "0 * * * *"
				old := metav1.NewTime(now.Add(-121 * time.Minute))
				f.cronJob.Status.LastSuccessfulTime = &old
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-3*time.Hour))
					pod.Status.ContainerStatuses[1] = sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-4*time.Hour))
				}
			},
			wantDetections:  1,
			wantActiveCount: 3,
			wantDuration:    3 * time.Hour,
		},
		{
			name: "invalid schedule fails closed",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.Schedule = "not a schedule"
			},
		},
		{
			name: "invalid time zone fails closed",
			mutate: func(f *sidecarJobFixture) {
				invalid := "Mars/Olympus_Mons"
				f.cronJob.Spec.TimeZone = &invalid
			},
		},
		{
			name: "suspended CronJob is intentionally quiet",
			mutate: func(f *sidecarJobFixture) {
				suspended := true
				f.cronJob.Spec.Suspend = &suspended
			},
		},
		{
			name: "new CronJob without any success gets a fair startup window",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.CreationTimestamp = metav1.NewTime(now.Add(-29 * time.Minute))
				setSidecarTestJobStarts(f.jobs, now.Add(-29*time.Minute))
			},
		},
		{
			name: "new CronJob startup window boundary fires",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.CreationTimestamp = metav1.NewTime(now.Add(-30 * time.Minute))
				setSidecarTestJobStarts(f.jobs, now.Add(-30*time.Minute))
			},
			wantDetections:  1,
			wantActiveCount: 3,
			wantAge:         30 * time.Minute,
		},
		{
			name: "old CronJob resumed recently gets a fresh startup window",
			mutate: func(f *sidecarJobFixture) {
				setSidecarTestJobStarts(f.jobs, now.Add(-10*time.Minute))
			},
		},
		{
			name: "running sibling restarted within grace",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses[1] = sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-time.Minute))
				}
			},
		},
		{
			name: "active jobs have no pods",
			mutate: func(f *sidecarJobFixture) {
				f.pods = nil
			},
		},
		{
			name: "completed Job pod cannot supply evidence for active Jobs",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{sidecarTestRunningStatus("worker", now.Add(-10*time.Minute))}
				}
				completedJob := sidecarTestSucceededJob(f.cronJob, "old-completed", now.Add(-24*time.Hour))
				completedPod := f.pods[0].DeepCopy()
				completedPod.Name = "old-completed-pod"
				completedPod.OwnerReferences[0].Name = completedJob.Name
				completedPod.OwnerReferences[0].UID = completedJob.UID
				completedPod.Status.ContainerStatuses = []corev1.ContainerStatus{
					sidecarTestCompletedStatus("archiver", 0, now.Add(-10*time.Minute)),
					sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-20*time.Minute)),
				}
				f.jobs = append(f.jobs, completedJob)
				f.pods = append(f.pods, completedPod)
			},
		},
		{
			name: "succeeded-phase active Job pods cannot supply evidence",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.Status.Phase = corev1.PodSucceeded
				}
			},
		},
		{
			name: "same CronJob name with a different UID does not own Jobs",
			mutate: func(f *sidecarJobFixture) {
				for _, job := range f.jobs {
					job.OwnerReferences[0].UID = types.UID("old-cronjob-uid")
				}
			},
		},
		{
			name: "same Job name with a different UID does not own pods",
			mutate: func(f *sidecarJobFixture) {
				for _, pod := range f.pods {
					pod.OwnerReferences[0].UID = types.UID("old-job-uid")
				}
			},
		},
		{
			name: "Forbid policy single stuck Job stays below accumulation threshold",
			mutate: func(f *sidecarJobFixture) {
				f.cronJob.Spec.ConcurrencyPolicy = batchv1.ForbidConcurrent
				f.jobs = f.jobs[:1]
				f.pods = f.pods[:1]
			},
		},
		{
			name: "threshold boundary N minus one",
			mutate: func(f *sidecarJobFixture) {
				f.jobs = f.jobs[:sidecarJobAccumulationThreshold-1]
				f.pods = f.pods[:sidecarJobAccumulationThreshold-1]
			},
		},
		{
			name: "threshold boundary N",
			mutate: func(f *sidecarJobFixture) {
				f.jobs = f.jobs[:sidecarJobAccumulationThreshold]
				f.pods = f.pods[:sidecarJobAccumulationThreshold]
			},
			wantDetections:  1,
			wantActiveCount: sidecarJobAccumulationThreshold,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSidecarJobFixture(now, 3)
			if tt.mutate != nil {
				tt.mutate(fixture)
			}
			cache := sidecarJobTestCache(t, fixture)
			namespace := "prod"
			if tt.allNamespaces {
				namespace = ""
			}
			var got []Detection
			for _, detection := range detectConfigProblems(cache, namespace, now, listPodsByNamespace(cache, namespace)) {
				if detection.Reason == "SidecarBlocksJobCompletion" {
					got = append(got, detection)
				}
			}
			if len(got) != tt.wantDetections {
				t.Fatalf("got %d sidecar detections, want %d: %+v", len(got), tt.wantDetections, got)
			}
			if tt.wantDetections == 0 {
				return
			}

			detection := got[0]
			if detection.Kind != "CronJob" || detection.Group != "batch" || detection.Namespace != "prod" || detection.Name != "audit-log-archiver" {
				t.Fatalf("unexpected subject: %+v", detection)
			}
			if detection.Severity != "warning" || detection.Fingerprint != "job-sidecar-block:prod:audit-log-archiver" {
				t.Fatalf("unexpected severity/fingerprint: %+v", detection)
			}
			for _, want := range []string{fmt.Sprintf("%d active Jobs", tt.wantActiveCount), "completed successfully", "still running"} {
				if !strings.Contains(detection.Message, want) {
					t.Errorf("message %q does not contain %q", detection.Message, want)
				}
			}
			for _, hidden := range []string{fixture.pods[0].Name, "fluent-bit-sidecar"} {
				if strings.Contains(detection.Message, hidden) {
					t.Errorf("message exposes related-resource detail %q: %q", hidden, detection.Message)
				}
			}
			for _, want := range []string{"Kubernetes 1.29+", "initContainers", "restartPolicy: Always", "KEP-753", "stable in Kubernetes 1.33", "Otherwise"} {
				if !strings.Contains(detection.Action, want) {
					t.Errorf("action %q does not contain %q", detection.Action, want)
				}
			}
			wantAge := tt.wantAge
			if wantAge == 0 {
				wantAge = 6 * time.Hour
			}
			if detection.AgeSeconds != int64(wantAge.Seconds()) {
				t.Errorf("AgeSeconds = %d, want %d", detection.AgeSeconds, int64(wantAge.Seconds()))
			}
			wantDuration := tt.wantDuration
			if wantDuration == 0 {
				wantDuration = 10 * time.Minute
			}
			if detection.DurationSeconds != int64(wantDuration.Seconds()) {
				t.Errorf("DurationSeconds = %d, want %d", detection.DurationSeconds, int64(wantDuration.Seconds()))
			}
		})
	}
}

// sidecarDetectionCount runs the config detectors and counts SidecarBlocksJobCompletion
// rows — used to prove the neutral lead fires in windows where the confident detection
// (correctly) does not.
func sidecarDetectionCount(cache *ResourceCache, now time.Time) int {
	n := 0
	for _, d := range detectConfigProblems(cache, "prod", now, listPodsByNamespace(cache, "prod")) {
		if d.Reason == "SidecarBlocksJobCompletion" {
			n++
		}
	}
	return n
}

func TestStuckActiveJobReasonContract(t *testing.T) {
	reason := formatStuckActiveJobReason(2 * time.Hour)
	if reason != "Running for 2h with no completions" || !IsStuckActiveJobReason(reason) {
		t.Fatalf("stuck-active Job reason contract drifted: %q", reason)
	}
	if IsStuckActiveJobReason("BackoffLimitExceeded") {
		t.Fatal("failed Job reason matched the stuck-active predicate")
	}
}

func TestFindRunningPastCompletionForObject(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)

	t.Run("fires below the confident detector's accumulation threshold", func(t *testing.T) {
		fixture := newSidecarJobFixture(now, 1) // 1 active Job < sidecarJobAccumulationThreshold (2)
		cache := sidecarJobTestCache(t, fixture)
		if got := sidecarDetectionCount(cache, now); got != 0 {
			t.Fatalf("precondition: confident detector fired %d times, want 0 (below accumulation)", got)
		}
		shape := FindRunningPastCompletionForObject(cache, fixture.cronJob, now)
		if shape == nil {
			t.Fatal("lead is nil; want a neutral completion-blocked observation")
		}
		if shape.CompletedContainer != "archiver" || shape.RunningContainer != "fluent-bit-sidecar" {
			t.Errorf("container split = %q/%q, want archiver/fluent-bit-sidecar", shape.CompletedContainer, shape.RunningContainer)
		}
		if shape.ActiveJobs != 1 || shape.Pod == "" {
			t.Errorf("ActiveJobs=%d Pod=%q, want 1 and a pod name", shape.ActiveJobs, shape.Pod)
		}
	})

	t.Run("fires within the never-succeeded warmup window", func(t *testing.T) {
		// Fresh deploy: 2 accumulating Jobs, but the CronJob has never recorded a
		// success and its Jobs are only 5m old — under the confident detector's
		// 2×recentSuccessWindow (30m) warmup. The lead must still surface the shape.
		fixture := newSidecarJobFixture(now, 2)
		fixture.cronJob.CreationTimestamp = metav1.NewTime(now.Add(-5 * time.Minute))
		setSidecarTestJobStarts(fixture.jobs, now.Add(-5*time.Minute))
		for _, pod := range fixture.pods {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				sidecarTestCompletedStatus("archiver", 0, now.Add(-3*time.Minute)),
				sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-4*time.Minute)),
			}
		}
		cache := sidecarJobTestCache(t, fixture)
		if got := sidecarDetectionCount(cache, now); got != 0 {
			t.Fatalf("precondition: confident detector fired %d times, want 0 (warmup not met)", got)
		}
		if shape := FindRunningPastCompletionForObject(cache, fixture.cronJob, now); shape == nil {
			t.Fatal("lead is nil; want it to surface before the 30m warmup lets the detection fire")
		}
	})

	t.Run("respects the short grace (no transient startup split)", func(t *testing.T) {
		fixture := newSidecarJobFixture(now, 2)
		for _, pod := range fixture.pods {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				sidecarTestCompletedStatus("archiver", 0, now.Add(-30*time.Second)),
				sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-30*time.Second)),
			}
		}
		cache := sidecarJobTestCache(t, fixture)
		if shape := FindRunningPastCompletionForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("lead fired within grace; want nil, got %+v", shape)
		}
	})

	t.Run("suspended CronJob is quiet", func(t *testing.T) {
		fixture := newSidecarJobFixture(now, 2)
		suspended := true
		fixture.cronJob.Spec.Suspend = &suspended
		cache := sidecarJobTestCache(t, fixture)
		if shape := FindRunningPastCompletionForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("suspended CronJob produced a lead: %+v", shape)
		}
	})

	t.Run("no active Jobs is quiet", func(t *testing.T) {
		fixture := newSidecarJobFixture(now, 0)
		cache := sidecarJobTestCache(t, fixture)
		if shape := FindRunningPastCompletionForObject(cache, fixture.cronJob, now); shape != nil {
			t.Fatalf("no-active-Jobs CronJob produced a lead: %+v", shape)
		}
	})

	t.Run("non-CronJob object is quiet", func(t *testing.T) {
		fixture := newSidecarJobFixture(now, 2)
		cache := sidecarJobTestCache(t, fixture)
		if shape := FindRunningPastCompletionForObject(cache, fixture.pods[0], now); shape != nil {
			t.Fatalf("non-CronJob object produced a lead: %+v", shape)
		}
	})
}

func TestSidecarJobTiming(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 30, 0, time.UTC)
	utcTimeZone := "UTC"
	fixedTimeZone := "Etc/GMT-2"
	invalidTimeZone := "Mars/Olympus_Mons"
	tests := []struct {
		name       string
		schedule   string
		timeZone   *string
		wantGrace  time.Duration
		wantWindow time.Duration
		wantOK     bool
	}{
		{name: "per-minute floors", schedule: "* * * * *", wantGrace: 2 * time.Minute, wantWindow: 15 * time.Minute, wantOK: true},
		{name: "five-minute grace with window floor", schedule: "*/5 * * * *", wantGrace: 5 * time.Minute, wantWindow: 15 * time.Minute, wantOK: true},
		{name: "hourly cadence", schedule: "0 * * * *", wantGrace: time.Hour, wantWindow: 2 * time.Hour, wantOK: true},
		{name: "non-uniform cadence uses sampled maximum", schedule: "0 9,17 * * *", timeZone: &utcTimeZone, wantGrace: 16 * time.Hour, wantWindow: 32 * time.Hour, wantOK: true},
		{name: "omitted timezone gets DST margin", schedule: "0 2 * * *", wantGrace: 25 * time.Hour, wantWindow: 50 * time.Hour, wantOK: true},
		{name: "daily cadence with timezone", schedule: "0 2 * * *", timeZone: &fixedTimeZone, wantGrace: 24 * time.Hour, wantWindow: 48 * time.Hour, wantOK: true},
		{name: "invalid schedule fails closed", schedule: "not a schedule"},
		{name: "invalid timezone fails closed", schedule: "0 2 * * *", timeZone: &invalidTimeZone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cj := &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-24 * time.Hour))},
				Spec:       batchv1.CronJobSpec{Schedule: tt.schedule, TimeZone: tt.timeZone},
			}
			grace, window, ok := sidecarJobTiming(cj)
			if ok != tt.wantOK || grace != tt.wantGrace || window != tt.wantWindow {
				t.Fatalf("sidecarJobTiming() = (%s, %s, %t), want (%s, %s, %t)", grace, window, ok, tt.wantGrace, tt.wantWindow, tt.wantOK)
			}
		})
	}
}

func TestSidecarTimingCacheKeyDistinguishesOmittedFromUTC(t *testing.T) {
	localZone := time.FixedZone("UTC-5", -5*60*60)
	createdAt := metav1.NewTime(time.Date(2025, time.December, 31, 21, 0, 0, 0, localZone))
	utcTimeZone := "UTC"
	omitted := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: createdAt},
		Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *"},
	}
	explicitUTC := omitted.DeepCopy()
	explicitUTC.Spec.TimeZone = &utcTimeZone

	omittedKey := sidecarTimingCacheKey(omitted)
	explicitUTCKey := sidecarTimingCacheKey(explicitUTC)
	if omittedKey == explicitUTCKey {
		t.Fatalf("omitted and explicit UTC time zones share cache key %+v", omittedKey)
	}
	if omittedKey.timeZone != "" || explicitUTCKey.timeZone != "UTC" {
		t.Fatalf("unexpected cache keys: omitted=%+v explicitUTC=%+v", omittedKey, explicitUTCKey)
	}
	if omittedKey.anchorYear != 2026 || explicitUTCKey.anchorYear != 2026 {
		t.Fatalf("cache keys use a different sampling year: omitted=%+v explicitUTC=%+v", omittedKey, explicitUTCKey)
	}
}

func TestDetectStuckSidecarJobsKeepsOmittedAndExplicitUTCTimingSeparate(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	utcTimeZone := "UTC"
	oldSuccess := metav1.NewTime(now.Add(-7 * 24 * time.Hour))
	configure := func(fixture *sidecarJobFixture, name string, timeZone *string) {
		fixture.cronJob.Name = name
		fixture.cronJob.UID = types.UID("cronjob-" + name)
		fixture.cronJob.CreationTimestamp = metav1.NewTime(now.Add(-14 * 24 * time.Hour))
		fixture.cronJob.Spec.Schedule = "0 2 * * *"
		fixture.cronJob.Spec.TimeZone = timeZone
		fixture.cronJob.Status.LastSuccessfulTime = &oldSuccess
		for i, job := range fixture.jobs {
			job.Name = fmt.Sprintf("%s-%d", name, i)
			job.UID = types.UID("job-" + job.Name)
			job.OwnerReferences[0].Name = fixture.cronJob.Name
			job.OwnerReferences[0].UID = fixture.cronJob.UID
			pod := fixture.pods[i]
			pod.Name = job.Name + "-pod"
			pod.UID = types.UID("pod-" + pod.Name)
			pod.OwnerReferences[0].Name = job.Name
			pod.OwnerReferences[0].UID = job.UID
			pod.Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-24*time.Hour-30*time.Minute))
			pod.Status.ContainerStatuses[1] = sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-48*time.Hour))
		}
	}

	explicitUTC := newSidecarJobFixture(now, sidecarJobAccumulationThreshold)
	configure(explicitUTC, "a-explicit-utc", &utcTimeZone)
	omitted := newSidecarJobFixture(now, sidecarJobAccumulationThreshold)
	configure(omitted, "b-omitted-zone", nil)
	cache := sidecarJobTestCache(t, explicitUTC, omitted)

	var detected []string
	for _, detection := range detectConfigProblems(cache, "prod", now, listPodsByNamespace(cache, "prod")) {
		if detection.Reason == "SidecarBlocksJobCompletion" {
			detected = append(detected, detection.Name)
		}
	}
	if len(detected) != 1 || detected[0] != explicitUTC.cronJob.Name {
		t.Fatalf("sidecar detections = %v, want only %q", detected, explicitUTC.cronJob.Name)
	}
}

func TestStuckSidecarEvidenceIsDeterministic(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	fixture := newSidecarJobFixture(now, 2)
	fixture.pods[0].Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-10*time.Minute))
	fixture.pods[1].Status.ContainerStatuses[0] = sidecarTestCompletedStatus("archiver", 0, now.Add(-20*time.Minute))
	activeJobs := map[string]*batchv1.Job{
		fixture.jobs[0].Name: fixture.jobs[0],
		fixture.jobs[1].Name: fixture.jobs[1],
	}

	forward, ok := stuckSidecarEvidence(fixture.pods, activeJobs, now, 2*time.Minute)
	if !ok {
		t.Fatal("stuckSidecarEvidence() found no evidence")
	}
	reverse, ok := stuckSidecarEvidence([]*corev1.Pod{fixture.pods[1], fixture.pods[0]}, activeJobs, now, 2*time.Minute)
	if !ok {
		t.Fatal("stuckSidecarEvidence() found no evidence for reversed input")
	}
	if forward != reverse || forward.podName != fixture.pods[1].Name {
		t.Fatalf("evidence changed with input order: forward=%+v reverse=%+v", forward, reverse)
	}
}

type sidecarJobFixture struct {
	cronJob *batchv1.CronJob
	jobs    []*batchv1.Job
	pods    []*corev1.Pod
}

func newSidecarJobFixture(now time.Time, activeJobs int) *sidecarJobFixture {
	controller := true
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "audit-log-archiver",
			Namespace:         "prod",
			UID:               types.UID("cronjob-uid"),
			CreationTimestamp: metav1.NewTime(now.Add(-6 * time.Hour)),
		},
		Spec: batchv1.CronJobSpec{Schedule: "* * * * *"},
	}
	fixture := &sidecarJobFixture{cronJob: cronJob}
	for i := 0; i < activeJobs; i++ {
		name := fmt.Sprintf("audit-log-archiver-%d", i)
		uid := types.UID("job-" + name)
		startedAt := metav1.NewTime(now.Add(-6 * time.Hour))
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "prod",
				UID:               uid,
				CreationTimestamp: metav1.NewTime(now.Add(-6 * time.Hour)),
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
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "archiver"}, {Name: "fluent-bit-sidecar"}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					sidecarTestCompletedStatus("archiver", 0, now.Add(-10*time.Minute)),
					sidecarTestRunningStatus("fluent-bit-sidecar", now.Add(-20*time.Minute)),
				},
			},
		}
		fixture.jobs = append(fixture.jobs, job)
		fixture.pods = append(fixture.pods, pod)
	}
	return fixture
}

func setSidecarTestJobStarts(jobs []*batchv1.Job, startedAt time.Time) {
	for _, job := range jobs {
		job.CreationTimestamp = metav1.NewTime(startedAt)
		start := metav1.NewTime(startedAt)
		job.Status.StartTime = &start
	}
}

func sidecarTestSucceededJob(cj *batchv1.CronJob, name string, completedAt time.Time) *batchv1.Job {
	controller := true
	completion := metav1.NewTime(completedAt)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cj.Namespace,
			UID:       types.UID("job-" + name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cj.Name,
				UID:        cj.UID,
				Controller: &controller,
			}},
		},
		Status: batchv1.JobStatus{Succeeded: 1, CompletionTime: &completion},
	}
}

func sidecarTestCompletedStatus(name string, exitCode int32, finishedAt time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode:   exitCode,
			FinishedAt: metav1.NewTime(finishedAt),
		}},
	}
}

func sidecarTestRunningStatus(name string, startedAt time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.NewTime(startedAt),
		}},
	}
}

func sidecarJobTestCache(t *testing.T, fixtures ...*sidecarJobFixture) *ResourceCache {
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

func duplicateEnvTestDeployment(containers, initContainers []corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: metav1.Now()},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers, InitContainers: initContainers}},
		},
	}
}
