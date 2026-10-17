package k8s

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func int32Ptr(v int32) *int32 { return &v }

func intOrStrPtr(s string) *intstr.IntOrString {
	v := intstr.Parse(s)
	return &v
}

func specTestDeployment(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-1", Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: intOrStrPtr("25%"),
					MaxSurge:       intOrStrPtr("25%"),
				},
			},
			ProgressDeadlineSeconds: int32Ptr(600),
			RevisionHistoryLimit:    int32Ptr(10),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: func() *int64 { v := int64(30); return &v }(),
					Containers: []corev1.Container{{
						Name:  "web",
						Image: "nginx:1.0",
						Env:   []corev1.EnvVar{{Name: "MODE", Value: "a"}},
					}},
				},
			},
		},
	}
}

func bumped(d *appsv1.Deployment) *appsv1.Deployment {
	out := d.DeepCopy()
	out.Generation++
	return out
}

func findChange(diff *DiffInfo, path string) *FieldChange {
	if diff == nil {
		return nil
	}
	for i := range diff.Fields {
		if diff.Fields[i].Path == path {
			return &diff.Fields[i]
		}
	}
	return nil
}

// One patch that adds a blocking init container and opens the strategy to
// total unavailability must report both edits, not just the visible one.
func TestComputeDiff_DeploymentInitContainerAndStrategyInOnePatch(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "hang-init", Image: "busybox:1.36", Command: []string{"sleep", "infinity"}}}
	newDep.Spec.Strategy.RollingUpdate.MaxUnavailable = intOrStrPtr("100%")
	newDep.Spec.Strategy.RollingUpdate.MaxSurge = intOrStrPtr("0%")

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil {
		t.Fatal("expected diff")
	}
	for _, want := range []string{
		"init container hang-init added (busybox:1.36)",
		"maxUnavailable: 25%→100%",
		"maxSurge: 25%→0%",
		"rollout can now take all 3 replicas unavailable at once",
	} {
		if !strings.Contains(diff.Summary, want) {
			t.Errorf("summary %q missing %q", diff.Summary, want)
		}
	}
	if strings.Contains(diff.Summary, "other spec changes") {
		t.Errorf("tracked fields leaked into the untracked backstop: %q", diff.Summary)
	}
	change := findChange(diff, "spec.strategy.rollingUpdate.maxUnavailable")
	if change == nil || change.OldValue != "25%" || change.NewValue != "100%" {
		t.Fatalf("maxUnavailable row = %+v", change)
	}
}

func TestComputeDiff_DeploymentRiskNotRepeatedWhenAlreadyRisky(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Strategy.RollingUpdate.MaxUnavailable = intOrStrPtr("100%")
	oldDep.Spec.Strategy.RollingUpdate.MaxSurge = intOrStrPtr("0%")
	newDep := bumped(oldDep)
	newDep.Spec.MinReadySeconds = 10

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || diff.Summary != "minReadySeconds: 0→10" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestComputeDiff_DeploymentRolloutFields(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*appsv1.Deployment)
		wantPath    string
		wantSummary string
	}{
		{
			name: "strategy type",
			mutate: func(d *appsv1.Deployment) {
				d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
			},
			wantPath:    "spec.strategy.rollingUpdate.maxUnavailable",
			wantSummary: "strategy: RollingUpdate→Recreate",
		},
		{
			name:        "progress deadline",
			mutate:      func(d *appsv1.Deployment) { d.Spec.ProgressDeadlineSeconds = int32Ptr(60) },
			wantPath:    "spec.progressDeadlineSeconds",
			wantSummary: "progressDeadlineSeconds: 600→60",
		},
		{
			name:        "revision history unset",
			mutate:      func(d *appsv1.Deployment) { d.Spec.RevisionHistoryLimit = nil },
			wantPath:    "spec.revisionHistoryLimit",
			wantSummary: "revisionHistoryLimit: 10→(default)",
		},
		{
			name: "rollout restart",
			mutate: func(d *appsv1.Deployment) {
				d.Spec.Template.Annotations = map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-10-16T10:00:00Z"}
			},
			wantPath:    "spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]",
			wantSummary: "pod template annotation kubectl.kubernetes.io/restartedAt added (new pod template revision)",
		},
		{
			name: "config checksum annotation",
			mutate: func(d *appsv1.Deployment) {
				d.Spec.Template.Annotations = map[string]string{"checksum/config": "abc"}
			},
			wantPath:    "spec.template.metadata.annotations[checksum/config]",
			wantSummary: "pod template annotation checksum/config added (new pod template revision)",
		},
		{
			name:        "template label",
			mutate:      func(d *appsv1.Deployment) { d.Spec.Template.Labels["version"] = "v2" },
			wantPath:    "spec.template.metadata.labels.version",
			wantSummary: "pod template label version added (new pod template revision)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldDep := specTestDeployment(3)
			newDep := bumped(oldDep)
			tt.mutate(newDep)
			diff := ComputeDiff("Deployment", oldDep, newDep)
			if findChange(diff, tt.wantPath) == nil {
				t.Fatalf("missing %s in %+v", tt.wantPath, diff)
			}
			if diff.Summary != tt.wantSummary {
				t.Fatalf("summary = %q, want %q", diff.Summary, tt.wantSummary)
			}
		})
	}
}

func TestComputeDiff_DeploymentUntrackedSpecFieldReported(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.Containers[0].Image = "nginx:2.0"
	grace := int64(5)
	newDep.Spec.Template.Spec.TerminationGracePeriodSeconds = &grace

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || !strings.Contains(diff.Summary, "other spec changes: terminationGracePeriodSeconds") {
		t.Fatalf("summary = %+v", diff)
	}
	change := findChange(diff, "spec.template.spec.terminationGracePeriodSeconds")
	if change == nil || change.OldValue != int64(30) || change.NewValue != int64(5) {
		t.Fatalf("grace period row = %+v", change)
	}
}

// Every field below has an explicit row; the backstop must not repeat them.
func TestComputeDiff_DeploymentTrackedFieldsNotDuplicated(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := bumped(oldDep)
	c := &newDep.Spec.Template.Spec.Containers[0]
	c.Image = "nginx:2.0"
	c.Env = []corev1.EnvVar{{Name: "MODE", Value: "b"}, {Name: "EXTRA", Value: "1"}}
	c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}
	c.Args = []string{"--debug"}
	newDep.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}}
	newDep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "setup", Image: "busybox"}}
	newDep.Spec.Replicas = int32Ptr(5)
	newDep.Spec.Strategy.RollingUpdate.MaxSurge = intOrStrPtr("1")

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil {
		t.Fatal("expected diff")
	}
	if strings.Contains(diff.Summary, "other spec changes") {
		t.Fatalf("tracked field reported as untracked: %q", diff.Summary)
	}
}

func TestComputeDiff_DeploymentStatusOnlySkipsBackstop(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := oldDep.DeepCopy()
	newDep.Status.ReadyReplicas = 2
	// Same generation: a spec difference here can only be test noise, and the
	// backstop must not pay for a conversion on status updates.
	newDep.Spec.Template.Spec.TerminationGracePeriodSeconds = nil

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || diff.Summary != "ready: 0→2" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestComputeDiff_UntrackedSpecChangesCapped(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.Hostname = "h"
	newDep.Spec.Template.Spec.Subdomain = "s"
	newDep.Spec.Template.Spec.PriorityClassName = "p"
	newDep.Spec.Template.Spec.SchedulerName = "sched"
	newDep.Spec.Template.Spec.RuntimeClassName = func() *string { v := "gvisor"; return &v }()

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || !strings.HasSuffix(diff.Summary, "(+2 more)") {
		t.Fatalf("summary = %+v", diff)
	}
	if len(diff.Fields) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(diff.Fields), diff.Fields)
	}
}

func TestComputeDiff_StatefulSetRolloutFields(t *testing.T) {
	base := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1, UID: "sts"},
		Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: int32Ptr(0)},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "pg:16"}}}},
		},
	}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.UpdateStrategy.RollingUpdate.Partition = int32Ptr(3)
	updated.Spec.PodManagementPolicy = appsv1.ParallelPodManagement

	diff := ComputeDiff("StatefulSet", base, updated)
	if diff == nil || diff.Summary != "partition: 0→3, podManagementPolicy: (default)→Parallel" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestComputeDiff_DaemonSetRolloutFields(t *testing.T) {
	base := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1, UID: "ds"},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: intOrStrPtr("1"), MaxSurge: intOrStrPtr("0")},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "agent:1"}}}},
		},
	}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}

	diff := ComputeDiff("DaemonSet", base, updated)
	if diff == nil || diff.Summary != "updateStrategy: RollingUpdate→OnDelete" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestSplitSpecPath(t *testing.T) {
	got := splitSpecPath("spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt].x")
	want := []string{"spec", "template", "metadata", "annotations", "[kubectl.kubernetes.io/restartedAt]", "x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

// The env differ compares redacted values, so a rotation between two literal
// passwords looks unchanged to it and falls through to the backstop.
func TestComputeDiff_SecretEnvRotationNeverLeaksValues(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PASSWORD", Value: "hunter2"}}
	oldDep.Spec.Template.Annotations = map[string]string{"example.com/config": `{"password":"tok-old"}`}
	oldDep.Spec.Template.Labels["password"] = "lbl-old"
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PASSWORD", Value: "swordfish"}}
	newDep.Spec.Template.Annotations = map[string]string{"example.com/config": `{"password":"tok-new"}`}
	newDep.Spec.Template.Labels["password"] = "lbl-new"

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || findChange(diff, "spec.template.spec.containers[web].env[PASSWORD].value") == nil {
		t.Fatalf("expected the rotation to be reported, got %+v", diff)
	}
	rendered := fmt.Sprintf("%+v", diff)
	for _, secret := range []string{"hunter2", "swordfish", "tok-old", "tok-new", "lbl-old", "lbl-new"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("diff leaked %q: %s", secret, rendered)
		}
	}
}

func TestComputeDiff_InitContainerReorderWithOtherChange(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "m:1"}, {Name: "seed", Image: "s:1"}}
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "seed", Image: "s:1"}, {Name: "migrate", Image: "m:2"}}

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || !strings.Contains(diff.Summary, "other spec changes: initContainers order") {
		t.Fatalf("summary = %+v", diff)
	}
	if findChange(diff, "spec.template.spec.initContainers[migrate].image") == nil {
		t.Fatalf("image change lost: %+v", diff)
	}
}

func TestComputeDiff_EnvReorderAndResourceClaimsReported(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "A", Value: "foo"}, {Name: "B", Value: "$(A)"}}
	newDep := bumped(oldDep)
	c := &newDep.Spec.Template.Spec.Containers[0]
	c.Env = []corev1.EnvVar{{Name: "B", Value: "$(A)"}, {Name: "A", Value: "foo"}}
	c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}
	c.Resources.Claims = []corev1.ResourceClaim{{Name: "gpu"}}
	c.Image = "nginx:2.0"

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil {
		t.Fatal("expected diff")
	}
	order := findChange(diff, "spec.template.spec.containers[web].env")
	if order == nil || fmt.Sprint(order.NewValue) != "[B A]" {
		t.Fatalf("env order row = %+v in %+v", order, diff)
	}
	if findChange(diff, "spec.template.spec.containers[web].resources.claims") == nil {
		t.Fatalf("resource claims hidden behind the resources row: %+v", diff)
	}
}

func TestComputeDiff_RiskLineAndRestartValueEdgeCases(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	newDep := bumped(oldDep)
	newDep.Spec.Strategy = appsv1.DeploymentStrategy{
		Type:          appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: intOrStrPtr("100%"), MaxSurge: intOrStrPtr("0")},
	}
	newDep.Spec.Template.Annotations = map[string]string{"kubectl.kubernetes.io/restartedAt": "not-a-time-secret"}

	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil || strings.Contains(diff.Summary, "unavailable at once") {
		t.Fatalf("Recreate→RollingUpdate must not read as newly risky: %+v", diff)
	}
	restart := findChange(diff, "spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]")
	if restart == nil || restart.NewValue != hiddenSetMarker {
		t.Fatalf("non-timestamp restartedAt value recorded: %+v", restart)
	}
}

func specTestCronJob() *batchv1.CronJob {
	f := false
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "shop", UID: "cj", Generation: 1},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			Suspend:  &f,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				BackoffLimit: int32Ptr(6),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers:    []corev1.Container{{Name: "job", Image: "backup:1"}},
				}},
			}},
		},
	}
}

func TestComputeDiff_CronJob(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*batchv1.CronJob)
		wantPath    string
		wantSummary string
	}{
		{
			name:        "suspend",
			mutate:      func(c *batchv1.CronJob) { tr := true; c.Spec.Suspend = &tr },
			wantPath:    "spec.suspend",
			wantSummary: "suspended",
		},
		{
			name:        "schedule",
			mutate:      func(c *batchv1.CronJob) { c.Spec.Schedule = "*/5 * * * *" },
			wantPath:    "spec.schedule",
			wantSummary: "schedule: 0 * * * *→*/5 * * * *",
		},
		{
			name:        "job template image",
			mutate:      func(c *batchv1.CronJob) { c.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image = "backup:2" },
			wantPath:    "spec.jobTemplate.spec.template.spec.containers[job].image",
			wantSummary: "image(job): backup:1→backup:2",
		},
		{
			name:        "job template field without a row",
			mutate:      func(c *batchv1.CronJob) { c.Spec.JobTemplate.Spec.BackoffLimit = int32Ptr(0) },
			wantPath:    "spec.jobTemplate.spec.backoffLimit",
			wantSummary: "other spec changes: jobTemplate.backoffLimit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCJ := specTestCronJob()
			newCJ := oldCJ.DeepCopy()
			newCJ.Generation++
			tt.mutate(newCJ)
			diff := ComputeDiff("CronJob", oldCJ, newCJ)
			if findChange(diff, tt.wantPath) == nil {
				t.Fatalf("missing %s in %+v", tt.wantPath, diff)
			}
			if diff.Summary != tt.wantSummary {
				t.Fatalf("summary = %q, want %q", diff.Summary, tt.wantSummary)
			}
		})
	}
}

// Every scheduled run moves CronJob status; the run is recorded on its Job.
// Only schedule updates that change CronJob health are kept.
func TestComputeDiff_CronJobScheduleUpdates(t *testing.T) {
	at := func(h int) *metav1.Time {
		v := metav1.NewTime(time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC))
		return &v
	}
	tests := []struct {
		name        string
		old, new    *metav1.Time
		wantSummary string
	}{
		{name: "routine tick", old: at(1), new: at(2)},
		{name: "first run", old: nil, new: at(1), wantSummary: "first run scheduled"},
		{name: "after stale gap", old: at(0), new: at(30), wantSummary: "scheduled after 30h without a run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCJ := specTestCronJob()
			oldCJ.Status.LastScheduleTime = tt.old
			newCJ := oldCJ.DeepCopy()
			newCJ.Status.LastScheduleTime = tt.new
			newCJ.Status.Active = []corev1.ObjectReference{{Name: "backup-1"}}
			diff := ComputeDiff("CronJob", oldCJ, newCJ)
			if tt.wantSummary == "" {
				if diff != nil {
					t.Fatalf("expected routine tick to be dropped, got %+v", diff)
				}
				return
			}
			if diff == nil || diff.Summary != tt.wantSummary {
				t.Fatalf("summary = %+v, want %q", diff, tt.wantSummary)
			}
		})
	}
}

func TestComputeDiff_PDB(t *testing.T) {
	one := intstr.FromInt(1)
	base := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "pdb", Generation: 1},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &one,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 2, CurrentHealthy: 3},
	}

	spec := base.DeepCopy()
	spec.Generation = 2
	spec.Spec.MinAvailable = intOrStrPtr("100%")
	diff := ComputeDiff("PodDisruptionBudget", base, spec)
	if diff == nil || diff.Summary != "minAvailable: 1→100%" {
		t.Fatalf("summary = %+v", diff)
	}

	// disruptionsAllowed flips on every routine pod replacement.
	blocked := base.DeepCopy()
	blocked.Status.DisruptionsAllowed = 0
	blocked.Status.CurrentHealthy = 2
	if diff := ComputeDiff("PodDisruptionBudget", base, blocked); diff != nil {
		t.Fatalf("status churn must not record, got %+v", diff)
	}
}

func TestComputeDiff_HPAScalingConfig(t *testing.T) {
	util := func(v int32) *int32 { return &v }
	base := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "hpa", Generation: 1},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			MinReplicas:    int32Ptr(2),
			MaxReplicas:    10,
			Metrics: []autoscalingv2.MetricSpec{{
				Type:     autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: util(80)}},
			}},
		},
	}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.Metrics[0].Resource.Target.AverageUtilization = util(50)
	// As stored by the API server: setting scaleDown alone defaults scaleUp.
	maxPolicy := autoscalingv2.MaxChangePolicySelect
	updated.Spec.Behavior = &autoscalingv2.HorizontalPodAutoscalerBehavior{
		ScaleDown: &autoscalingv2.HPAScalingRules{StabilizationWindowSeconds: util(0), SelectPolicy: &maxPolicy, Policies: []autoscalingv2.HPAScalingPolicy{{Type: autoscalingv2.PercentScalingPolicy, Value: 100, PeriodSeconds: 15}}},
		ScaleUp: &autoscalingv2.HPAScalingRules{StabilizationWindowSeconds: util(0), SelectPolicy: &maxPolicy, Policies: []autoscalingv2.HPAScalingPolicy{
			{Type: autoscalingv2.PodsScalingPolicy, Value: 4, PeriodSeconds: 15},
			{Type: autoscalingv2.PercentScalingPolicy, Value: 100, PeriodSeconds: 15},
		}},
	}

	diff := ComputeDiff("HorizontalPodAutoscaler", base, updated)
	if diff == nil || diff.Summary != "cpu target: 80%→50%, scaleDown behavior changed" {
		t.Fatalf("summary = %+v", diff)
	}
	if change := findChange(diff, "spec.behavior.scaleDown"); change == nil || change.OldValue != hpaDefaultScaleDown || change.NewValue != "stabilization=0s, select=Max, Percent 100/15s" {
		t.Fatalf("behavior row = %+v", change)
	}
}

func TestComputeDiff_ServicePolicyAndBackstop(t *testing.T) {
	base := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "svc"},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeLoadBalancer,
			Selector:              map[string]string{"app": "web"},
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
			SessionAffinity:       corev1.ServiceAffinityNone,
		},
	}
	updated := base.DeepCopy()
	updated.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
	updated.Spec.SessionAffinity = corev1.ServiceAffinityClientIP
	updated.Spec.SessionAffinityConfig = &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: int32Ptr(10800)}}
	updated.Spec.AllocateLoadBalancerNodePorts = func() *bool { f := false; return &f }()

	diff := ComputeDiff("Service", base, updated)
	if diff == nil || diff.Summary != "externalTrafficPolicy: Cluster→Local, sessionAffinity: None→ClientIP, other spec changes: allocateLoadBalancerNodePorts" {
		t.Fatalf("summary = %+v", diff)
	}

	// Services have no generation, so status-only updates reach the backstop
	// and must find nothing.
	lb := base.DeepCopy()
	lb.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}
	diff = ComputeDiff("Service", base, lb)
	if diff == nil || strings.Contains(diff.Summary, "other spec changes") {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestComputeDiff_IngressClass(t *testing.T) {
	class := func(s string) *string { return &s }
	base := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", UID: "ing", Generation: 1}, Spec: networkingv1.IngressSpec{IngressClassName: class("nginx")}}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.IngressClassName = class("alb")
	diff := ComputeDiff("Ingress", base, updated)
	if diff == nil || diff.Summary != "ingressClassName: nginx→alb" {
		t.Fatalf("summary = %+v", diff)
	}
}

// A traffic shift keeps the rule count, which is all diffGatewayRoute compares.
func TestComputeDiff_HTTPRouteBackendWeightShift(t *testing.T) {
	route := func(gen int64, stable, canary int64) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata":   map[string]any{"name": "web", "uid": "r", "generation": gen},
			"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{
				map[string]any{"name": "web-stable", "port": int64(80), "weight": stable},
				map[string]any{"name": "web-canary", "port": int64(80), "weight": canary},
			}}}},
		}}
	}
	diff := ComputeDiff("HTTPRoute", route(1, 90, 10), route(2, 10, 90))
	if diff == nil {
		t.Fatal("expected diff")
	}
	change := findChange(diff, "spec.rules[0].backendRefs[web-canary].weight")
	if change == nil || change.OldValue != int64(10) || change.NewValue != int64(90) {
		t.Fatalf("weight row = %+v in %+v", change, diff)
	}
}

func TestComputeDiff_RolloutStrategyAndTemplate(t *testing.T) {
	rollout := func(gen int64, maxUnavailable any, steps []any, image, env string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata":   map[string]any{"name": "web", "uid": "ro", "generation": gen},
			"spec": map[string]any{
				"strategy": map[string]any{"canary": map[string]any{"maxUnavailable": maxUnavailable, "steps": steps}},
				"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
					"name": "web", "image": image, "env": []any{map[string]any{"name": "MODE", "value": env}},
				}}}},
			},
		}}
	}
	oldRO := rollout(1, int64(1), canarySteps(4), "web:1", "a")
	newRO := rollout(2, "100%", canarySteps(2), "web:2", "b")

	diff := ComputeDiff("Rollout", oldRO, newRO)
	if diff == nil {
		t.Fatal("expected diff")
	}
	for _, want := range []string{"maxUnavailable: 1→100%", "canary steps changed (4→2 steps)", "image: web:2"} {
		if !strings.Contains(diff.Summary, want) {
			t.Errorf("summary %q missing %q", diff.Summary, want)
		}
	}
	if findChange(diff, "spec.template.spec.containers[web].env[MODE]") == nil {
		t.Errorf("env row missing: %+v", diff)
	}
	if strings.Contains(diff.Summary, "other spec changes") {
		t.Errorf("tracked fields leaked into the backstop: %q", diff.Summary)
	}
}

func TestComputeDiff_HTTPRouteHeaderValuesNeverShown(t *testing.T) {
	route := func(gen int64, auth string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata":   map[string]any{"name": "web", "uid": "r", "generation": gen},
			"spec": map[string]any{"rules": []any{map[string]any{
				"filters": []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{
					"set": []any{map[string]any{"name": "Authorization", "value": auth}},
				}}},
				"matches": []any{map[string]any{"headers": []any{map[string]any{"name": "X-Token", "value": auth + "-match"}}}},
			}}},
		}}
	}
	diff := ComputeDiff("HTTPRoute", route(1, "Basic dXNlcjpvbGQ="), route(2, "Basic dXNlcjpuZXc="))
	if diff == nil || findChange(diff, "spec.rules[0].filters[0].requestHeaderModifier.set[Authorization].value") == nil {
		t.Fatalf("expected the header change to be reported, got %+v", diff)
	}
	rendered := fmt.Sprintf("%+v", diff)
	for _, secret := range []string{"dXNlcjpvbGQ", "dXNlcjpuZXc"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("diff leaked %q: %s", secret, rendered)
		}
	}
}

func TestComputeDiff_HPAExternalMetricSelectorChange(t *testing.T) {
	qty := resource.MustParse("100")
	metric := func(queue string) []autoscalingv2.MetricSpec {
		return []autoscalingv2.MetricSpec{{
			Type: autoscalingv2.ExternalMetricSourceType,
			External: &autoscalingv2.ExternalMetricSource{
				Metric: autoscalingv2.MetricIdentifier{Name: "queue_depth", Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"queue": queue}}},
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.ValueMetricType, Value: &qty},
			},
		}}
	}
	base := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "hpa", Generation: 1},
		Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 5, Metrics: metric("orders")},
	}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.Metrics = metric("payments")

	diff := ComputeDiff("HorizontalPodAutoscaler", base, updated)
	if diff == nil || diff.Summary != "metric external/queue_depth{queue=orders} removed, metric external/queue_depth{queue=payments} added (target 100)" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestShortSpecPath(t *testing.T) {
	for path, want := range map[string]string{
		"spec.template.spec.terminationGracePeriodSeconds":                     "terminationGracePeriodSeconds",
		"spec.template.spec.containers[web].lifecycle.preStop":                 "containers[web].lifecycle.preStop",
		"spec.template.metadata.annotations[checksum/config]":                  "template.metadata.annotations[checksum/config]",
		"spec.jobTemplate.spec.backoffLimit":                                   "jobTemplate.backoffLimit",
		"spec.jobTemplate.spec.template.spec.containers[job].resources.limits": "containers[job].resources.limits",
		"spec.rules[0].backendRefs[web].weight":                                "rules[0].backendRefs[web].weight",
		"spec.sessionAffinityConfig":                                           "sessionAffinityConfig",
	} {
		if got := shortSpecPath(path); got != want {
			t.Errorf("shortSpecPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func namedRoute(gen int64, rules ...map[string]any) *unstructured.Unstructured {
	list := make([]any, 0, len(rules))
	for _, r := range rules {
		list = append(list, r)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": "web", "uid": "r", "generation": gen},
		"spec":       map[string]any{"rules": list},
	}}
}

func routeRule(name string, stable int64) map[string]any {
	rule := map[string]any{"backendRefs": []any{
		map[string]any{"name": "web", "port": int64(80), "weight": stable},
		map[string]any{"name": "web-canary", "port": int64(80), "weight": 100 - stable},
	}}
	if name != "" {
		rule["name"] = name
	}
	return rule
}

// With names, adding a rule no longer hides an edit to an existing one.
func TestComputeDiff_HTTPRouteNamedRulesAddAndEdit(t *testing.T) {
	admin := map[string]any{"name": "admin", "backendRefs": []any{map[string]any{"name": "admin", "port": int64(8080)}}}
	diff := ComputeDiff("HTTPRoute", namedRoute(1, routeRule("main", 90)), namedRoute(2, routeRule("main", 10), admin))
	if diff == nil {
		t.Fatal("expected diff")
	}
	want := "rule admin added, other spec changes: rules[main].backendRefs[web].weight, rules[main].backendRefs[web-canary].weight"
	if diff.Summary != want {
		t.Fatalf("summary = %q, want %q", diff.Summary, want)
	}
	added := findChange(diff, "spec.rules[admin]")
	if added == nil || fmt.Sprint(added.NewValue) != "[admin:8080]" {
		t.Fatalf("added rule row = %+v", added)
	}
	if findChange(diff, "spec.rules[main].backendRefs[web].weight") == nil {
		t.Fatalf("weight row missing: %+v", diff.Fields)
	}
}

// Unnamed rules are compared by position: the count row explains an appended
// rule, and an edit to an existing rule is still reported beside it.
func TestComputeDiff_HTTPRouteUnnamedRulesComparedByPosition(t *testing.T) {
	diff := ComputeDiff("HTTPRoute", namedRoute(1, routeRule("main", 90)), namedRoute(2, routeRule("main", 10), routeRule("", 50)))
	want := "rules: 1→2, other spec changes: rules[0].backendRefs[web].weight, rules[0].backendRefs[web-canary].weight"
	if diff == nil || diff.Summary != want {
		t.Fatalf("summary = %+v, want %q", diff, want)
	}
}

func TestComputeDiff_IngressRulesKeyedByHost(t *testing.T) {
	prefix := networkingv1.PathTypePrefix
	rule := func(host, svc string) networkingv1.IngressRule {
		return networkingv1.IngressRule{Host: host, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
			Path: "/", PathType: &prefix,
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: 80}}},
		}}}}}
	}
	base := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", UID: "ing", Generation: 1},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{rule("shop.example.com", "web")}}}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.Rules = []networkingv1.IngressRule{rule("shop.example.com", "web-v2"), rule("admin.example.com", "admin")}

	diff := ComputeDiff("Ingress", base, updated)
	if diff == nil {
		t.Fatal("expected diff")
	}
	if want := "rule admin.example.com added, other spec changes: rules[shop.example.com].http.paths[0].backend.service.name"; diff.Summary != want {
		t.Errorf("summary = %q, want %q", diff.Summary, want)
	}

	// Rules without hosts fall back to the count.
	noHost := base.DeepCopy()
	noHost.Spec.Rules[0].Host = ""
	noHostUpdated := noHost.DeepCopy()
	noHostUpdated.Generation = 2
	noHostUpdated.Spec.Rules = append(noHostUpdated.Spec.Rules, rule("admin.example.com", "admin"))
	diff = ComputeDiff("Ingress", noHost, noHostUpdated)
	if diff == nil || !strings.Contains(diff.Summary, "rules: 1→2") {
		t.Fatalf("summary = %+v", diff)
	}
}

func specTestWebhookConfig(gen int64, failurePolicy, ca string, conditions ...string) *unstructured.Unstructured {
	hook := map[string]any{
		"name":                    "pods.policy.example.com",
		"failurePolicy":           failurePolicy,
		"sideEffects":             "None",
		"admissionReviewVersions": []any{"v1"},
		"clientConfig":            map[string]any{"caBundle": ca, "service": map[string]any{"namespace": "policy", "name": "policy-webhook"}},
		"rules":                   []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{""}, "apiVersions": []any{"v1"}, "resources": []any{"pods"}}},
	}
	if len(conditions) > 0 {
		list := make([]any, 0, len(conditions))
		for i, expr := range conditions {
			list = append(list, map[string]any{"name": fmt.Sprintf("c%d", i), "expression": expr})
		}
		hook["matchConditions"] = list
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       "ValidatingWebhookConfiguration",
		"metadata":   map[string]any{"name": "policy", "uid": "w", "generation": gen},
		"webhooks":   []any{hook},
	}}
}

func TestComputeDiff_WebhookBackstop(t *testing.T) {
	const notSystem = "!request.userInfo.username.startsWith('system:')"
	tests := []struct {
		name        string
		old, new    *unstructured.Unstructured
		wantSummary string
	}{
		{
			name:        "CA rotation only",
			old:         specTestWebhookConfig(1, "Fail", "Q0EtMQ=="),
			new:         specTestWebhookConfig(2, "Fail", "Q0EtMg=="),
			wantSummary: "webhook pods.policy.example.com caBundle rotated",
		},
		{
			name:        "matchConditions only",
			old:         specTestWebhookConfig(1, "Fail", "Q0EtMQ=="),
			new:         specTestWebhookConfig(2, "Fail", "Q0EtMQ==", notSystem),
			wantSummary: "other changes: webhooks[pods.policy.example.com].matchConditions",
		},
		{
			name:        "failurePolicy and matchConditions together",
			old:         specTestWebhookConfig(1, "Fail", "Q0EtMQ==", "true"),
			new:         specTestWebhookConfig(2, "Ignore", "Q0EtMQ==", notSystem),
			wantSummary: "webhook pods.policy.example.com changed, other changes: webhooks[pods.policy.example.com].matchConditions[c0].expression",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := ComputeDiff("ValidatingWebhookConfiguration", tt.old, tt.new)
			if diff == nil || diff.Summary != tt.wantSummary {
				t.Fatalf("summary = %+v, want %q", diff, tt.wantSummary)
			}
			if rendered := fmt.Sprintf("%+v", diff); strings.Contains(rendered, "Q0EtM") {
				t.Fatalf("caBundle bytes leaked: %s", rendered)
			}
		})
	}
}

func TestComputeDiff_ResourceQuotaBackstop(t *testing.T) {
	base := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "rq"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("4")}},
	}
	updated := base.DeepCopy()
	updated.Spec.Hard["requests.cpu"] = resource.MustParse("8")
	updated.Spec.Scopes = []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeNotBestEffort}
	diff := ComputeDiff("ResourceQuota", base, updated)
	if diff == nil || diff.Summary != "quota requests.cpu: 4→8, other spec changes: scopes" {
		t.Fatalf("summary = %+v", diff)
	}

	// No generation on quotas: usage updates must stay off the backstop.
	used := base.DeepCopy()
	used.Status.Used = corev1.ResourceList{"requests.cpu": resource.MustParse("3")}
	if diff := ComputeDiff("ResourceQuota", base, used); diff != nil {
		t.Fatalf("usage update recorded: %+v", diff)
	}
}

func TestComputeDiff_LimitRangeRatioTracked(t *testing.T) {
	base := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "limits", UID: "lr", Generation: 1},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type: corev1.LimitTypeContainer, Max: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
		}}},
	}
	updated := base.DeepCopy()
	updated.Generation = 2
	updated.Spec.Limits[0].MaxLimitRequestRatio = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}
	diff := ComputeDiff("LimitRange", base, updated)
	if diff == nil || diff.Summary != "limit ranges changed" {
		t.Fatalf("summary = %+v", diff)
	}
}

func TestComputeDiff_GatewayListenersKeyed(t *testing.T) {
	gw := func(gen int64, listeners ...any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata":   map[string]any{"name": "edge", "uid": "g", "generation": gen},
			"spec":       map[string]any{"gatewayClassName": "istio", "listeners": listeners},
		}}
	}
	listener := func(name string, port int64, host string) any {
		l := map[string]any{"name": name, "protocol": "HTTP", "port": port}
		if host != "" {
			l["hostname"] = host
		}
		return l
	}
	diff := ComputeDiff("Gateway",
		gw(1, listener("http", 80, "shop.example.com")),
		gw(2, listener("http", 80, "*.example.com"), listener("admin", 8080, "")))
	want := "listener admin added, other spec changes: listeners[http].hostname"
	if diff == nil || diff.Summary != want {
		t.Fatalf("summary = %+v, want %q", diff, want)
	}
	if added := findChange(diff, "spec.listeners[admin]"); added == nil || added.NewValue != "HTTP:8080" {
		t.Fatalf("listener row = %+v", added)
	}
}

func TestComputeDiff_GatewayClassAndReferenceGrantBackstop(t *testing.T) {
	gc := func(gen int64, params string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "GatewayClass",
			"metadata":   map[string]any{"name": "istio", "uid": "gc", "generation": gen},
			"spec": map[string]any{"controllerName": "istio.io/gateway-controller",
				"parametersRef": map[string]any{"group": "", "kind": "ConfigMap", "name": params, "namespace": "istio-system"}},
		}}
	}
	diff := ComputeDiff("GatewayClass", gc(1, "gw-options"), gc(2, "gw-options-v2"))
	if diff == nil || diff.Summary != "other spec changes: parametersRef.name" {
		t.Fatalf("GatewayClass summary = %+v", diff)
	}

	rg := func(gen int64, to string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1beta1",
			"kind":       "ReferenceGrant",
			"metadata":   map[string]any{"name": "allow", "uid": "rg", "generation": gen},
			"spec": map[string]any{
				"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": "apps"}},
				"to":   []any{map[string]any{"group": "", "kind": "Service", "name": to}},
			},
		}}
	}
	diff = ComputeDiff("ReferenceGrant", rg(1, "web"), rg(2, "web-v2"))
	if diff == nil || diff.Summary != "other spec changes: to[web] (removed), to[web-v2] (added)" {
		t.Fatalf("ReferenceGrant summary = %+v", diff)
	}
}

// An object without a generation must not let a condition change hide a spec
// change in the same update.
func TestDiffGenericUnstructured_SpecChangeBesideConditions(t *testing.T) {
	obj := func(replicas int64, ready string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w"},
			"spec":       map[string]any{"replicas": replicas},
			"status":     map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready}}},
		}}
	}
	_, summary := diffGenericUnstructured(obj(1, "True"), obj(2, "False"))
	if strings.Join(summary, ", ") != "status.conditions[Ready] changed, spec changed (fields not specifically tracked)" {
		t.Fatalf("summary = %q", summary)
	}
	_, summary = diffGenericUnstructured(obj(1, "True"), obj(1, "False"))
	if strings.Join(summary, ", ") != "status.conditions[Ready] changed" {
		t.Fatalf("condition-only summary = %q", summary)
	}
}

func TestHiddenValueMarkers(t *testing.T) {
	oldDep := specTestDeployment(3)
	oldDep.Spec.Template.Annotations = map[string]string{"checksum/config": "aaa", "checksum/secret": "bbb"}
	newDep := bumped(oldDep)
	newDep.Spec.Template.Annotations = map[string]string{"checksum/config": "ccc", "example.com/new": "x"}
	diff := ComputeDiff("Deployment", oldDep, newDep)
	for path, want := range map[string][2]any{
		"spec.template.metadata.annotations[checksum/config]": {hiddenValueMarker, hiddenValueMarker},
		"spec.template.metadata.annotations[checksum/secret]": {hiddenSetMarker, nil},
		"spec.template.metadata.annotations[example.com/new]": {nil, hiddenSetMarker},
	} {
		change := findChange(diff, path)
		if change == nil || change.OldValue != want[0] || change.NewValue != want[1] {
			t.Errorf("%s = %+v, want %v→%v", path, change, want[0], want[1])
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Minute:              "45m",
		30 * time.Hour:                "30h",
		90 * time.Minute:              "1h30m",
		76 * time.Hour:                "3d4h",
		72 * time.Hour:                "3d",
		30*time.Hour + 20*time.Second: "30h",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestComputeDiff_NestedAnnotationValuesHidden(t *testing.T) {
	oldCJ := specTestCronJob()
	oldCJ.Spec.JobTemplate.Annotations = map[string]string{"example.com/config": "correct-horse-battery"}
	newCJ := oldCJ.DeepCopy()
	newCJ.Generation++
	newCJ.Spec.JobTemplate.Annotations = map[string]string{"example.com/config": "staple-horse-battery"}
	diff := ComputeDiff("CronJob", oldCJ, newCJ)
	change := findChange(diff, "spec.jobTemplate.metadata.annotations[example.com/config]")
	if change == nil || change.OldValue != hiddenValueMarker || change.NewValue != hiddenValueMarker {
		t.Fatalf("annotation row = %+v in %+v", change, diff)
	}
	if rendered := fmt.Sprintf("%+v", diff); strings.Contains(rendered, "horse") {
		t.Fatalf("annotation value leaked: %s", rendered)
	}
}

func TestComputeDiff_BackstopCapKeepsEveryFieldName(t *testing.T) {
	oldDep := specTestDeployment(3)
	newDep := bumped(oldDep)
	oldLabels, newLabels := map[string]string{}, map[string]string{}
	for i := 0; i < 30; i++ {
		oldLabels[fmt.Sprintf("k%02d", i)] = "a"
		newLabels[fmt.Sprintf("k%02d", i)] = "b"
	}
	oldDep.Spec.Selector = &metav1.LabelSelector{MatchLabels: oldLabels}
	newDep.Spec.Selector = &metav1.LabelSelector{MatchLabels: newLabels}
	diff := ComputeDiff("Deployment", oldDep, newDep)
	if diff == nil {
		t.Fatal("expected diff")
	}
	more := findChange(diff, "spec (more fields)")
	if more == nil {
		t.Fatalf("no truncation row in %d fields", len(diff.Fields))
	}
	if omitted, _ := more.NewValue.([]string); len(omitted)+maxUntrackedSpecRows != 30 {
		t.Fatalf("truncation row lists %v", more.NewValue)
	}
}

func TestComputeDiff_RolloutStrategySwitchKeepsDetails(t *testing.T) {
	ro := func(gen int64, strategy map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
			"metadata": map[string]any{"name": "web", "uid": "ro", "generation": gen},
			"spec":     map[string]any{"strategy": strategy},
		}}
	}
	diff := ComputeDiff("Rollout",
		ro(1, map[string]any{"canary": map[string]any{"maxSurge": "25%"}}),
		ro(2, map[string]any{"blueGreen": map[string]any{"activeService": "web", "scaleDownDelaySeconds": int64(60)}}))
	if diff == nil || !strings.Contains(diff.Summary, "strategy: canary→blueGreen") || !strings.Contains(diff.Summary, "strategy.blueGreen.scaleDownDelaySeconds") {
		t.Fatalf("summary = %+v", diff)
	}
}

// An empty selector matches every pod; absent matches none. Neither direction
// has leaves to compare.
func TestComputeDiff_EmptyBlockAddedOrRemoved(t *testing.T) {
	withSpread := func(sel *metav1.LabelSelector) *appsv1.Deployment {
		d := specTestDeployment(3)
		d.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: sel,
		}}
		return d
	}
	absent, empty := withSpread(nil), withSpread(&metav1.LabelSelector{})
	added := empty.DeepCopy()
	added.Generation = 2
	removed := absent.DeepCopy()
	removed.Generation = 3
	for name, pair := range map[string][2]*appsv1.Deployment{"added": {absent, added}, "removed": {empty, removed}} {
		diff := ComputeDiff("Deployment", pair[0], pair[1])
		if findChange(diff, "spec.template.spec.topologySpreadConstraints[0].labelSelector") == nil {
			t.Errorf("%s: empty selector change lost: %+v", name, diff)
		}
	}
}

// The volumes row compares source references only; a sizeLimit edit on one
// volume must survive a reference change on another in the same update.
func TestComputeDiff_VolumeDetailBesideReferenceChange(t *testing.T) {
	limit := func(s string) *resource.Quantity { q := resource.MustParse(s); return &q }
	oldDep := specTestDeployment(3)
	oldDep.Spec.Template.Spec.Volumes = []corev1.Volume{
		{Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg-v1"}}}},
		{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: limit("1Gi")}}},
	}
	newDep := bumped(oldDep)
	newDep.Spec.Template.Spec.Volumes[0].ConfigMap.Name = "cfg-v2"
	newDep.Spec.Template.Spec.Volumes[1].EmptyDir.SizeLimit = limit("10Gi")
	diff := ComputeDiff("Deployment", oldDep, newDep)
	if findChange(diff, "spec.template.spec.volumes") == nil || findChange(diff, "spec.template.spec.volumes[scratch].emptyDir.sizeLimit") == nil {
		t.Fatalf("sizeLimit edit hidden behind the volumes row: %+v", diff)
	}
}

func TestComputeDiff_CanaryStepEditWithSameCount(t *testing.T) {
	ro := func(gen, weight int64) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
			"metadata": map[string]any{"name": "web", "uid": "ro", "generation": gen},
			"spec": map[string]any{"strategy": map[string]any{"canary": map[string]any{"steps": []any{
				map[string]any{"setWeight": weight}, map[string]any{"pause": map[string]any{}},
			}}}},
		}}
	}
	diff := ComputeDiff("Rollout", ro(1, 10), ro(2, 100))
	change := findChange(diff, "spec.strategy.canary.steps[0].setWeight")
	if change == nil || change.OldValue != int64(10) || change.NewValue != int64(100) {
		t.Fatalf("setWeight row = %+v in %+v", change, diff)
	}
}

func TestComputeDiff_RouteQueryParamValuesHidden(t *testing.T) {
	route := func(gen int64, key string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
			"metadata": map[string]any{"name": "web", "uid": "r", "generation": gen},
			"spec": map[string]any{"rules": []any{map[string]any{"matches": []any{map[string]any{
				"queryParams": []any{map[string]any{"name": "key", "value": key}},
			}}}}},
		}}
	}
	diff := ComputeDiff("HTTPRoute", route(1, "hunter2"), route(2, "swordfish"))
	if diff == nil {
		t.Fatal("expected diff")
	}
	if rendered := fmt.Sprintf("%+v", diff); strings.Contains(rendered, "hunter2") || strings.Contains(rendered, "swordfish") {
		t.Fatalf("query parameter value leaked: %s", rendered)
	}
}

// Listener order carries no meaning; a reorder alone is not a change.
func TestComputeDiff_OrderOnlyOutsidePodTemplateIgnored(t *testing.T) {
	gw := func(gen int64, names ...string) *unstructured.Unstructured {
		listeners := make([]any, 0, len(names))
		for i, n := range names {
			listeners = append(listeners, map[string]any{"name": n, "protocol": "HTTP", "port": int64(80 + i)})
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
			"metadata": map[string]any{"name": "edge", "uid": "g", "generation": gen},
			"spec":     map[string]any{"listeners": listeners},
		}}
	}
	a := gw(1, "http", "admin")
	b := gw(2, "admin", "http")
	b.Object["spec"].(map[string]any)["listeners"] = []any{
		a.Object["spec"].(map[string]any)["listeners"].([]any)[1],
		a.Object["spec"].(map[string]any)["listeners"].([]any)[0],
	}
	if diff := ComputeDiff("Gateway", a, b); diff != nil && strings.Contains(diff.Summary, "order") {
		t.Fatalf("listener reorder reported: %+v", diff)
	}
}

// Two named rules with identical matches: the first one wins, so swapping
// them switches traffic.
func TestComputeDiff_NamedRouteRuleReorderReported(t *testing.T) {
	a, b := routeRule("blue", 100), routeRule("green", 100)
	b["backendRefs"] = []any{map[string]any{"name": "web-green", "port": int64(80)}}
	diff := ComputeDiff("HTTPRoute", namedRoute(1, a, b), namedRoute(2, b, a))
	if diff == nil || !strings.Contains(diff.Summary, "rules order") {
		t.Fatalf("rule reorder not reported: %+v", diff)
	}
}
