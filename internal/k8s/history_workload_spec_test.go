package k8s

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			wantSummary: "rollout restarted",
		},
		{
			name: "config checksum annotation",
			mutate: func(d *appsv1.Deployment) {
				d.Spec.Template.Annotations = map[string]string{"checksum/config": "abc"}
			},
			wantPath:    "spec.template.metadata.annotations[checksum/config]",
			wantSummary: "pod template annotation checksum/config added",
		},
		{
			name:        "template label",
			mutate:      func(d *appsv1.Deployment) { d.Spec.Template.Labels["version"] = "v2" },
			wantPath:    "spec.template.metadata.labels.version",
			wantSummary: "pod template label version added",
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
	if diff == nil || !strings.Contains(diff.Summary, "other spec changes: spec.template.spec.terminationGracePeriodSeconds") {
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
	if diff == nil || !strings.Contains(diff.Summary, "other spec changes: spec.template.spec.initContainers order") {
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
