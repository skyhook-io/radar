package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDetectProblems_OOMLimitDiscrepancy(t *testing.T) {
	defer ResetTestState()
	now := time.Now()

	type testCase struct {
		name          string
		specLimit     string
		statusLimit   string
		templateLimit string
		wantDetection bool
		wantCause     bool
		wantReason    string
		wantContains  []string
		mutate        func(*corev1.Pod, *appsv1.ReplicaSet) []runtime.Object
	}

	cases := []testCase{
		{
			name: "benchmark-shape", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"currently reported enacted memory limit is 16Mi", "ReplicaSet \"rs-benchmark-shape\" template limit of 256Mi", "current Pod spec limit is 16Mi"},
		},
		{
			name: "status-equal-template", specLimit: "16Mi", statusLimit: "256Mi", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "status-higher-than-template", specLimit: "16Mi", statusLimit: "512Mi", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "equivalent-quantities", specLimit: "0.25Gi", statusLimit: "0.25Gi", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "status-absent-no-resize", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"current admitted Pod spec memory limit is 16Mi", "does not currently report an enacted runtime limit"},
		},
		{
			name: "status-absent-legacy-resize", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Resize = corev1.PodResizeStatusInProgress
				return nil
			},
		},
		{
			name: "status-absent-legacy-resize-deferred", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Resize = corev1.PodResizeStatusDeferred
				return nil
			},
		},
		{
			name: "status-absent-legacy-resize-infeasible", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Resize = corev1.PodResizeStatusInfeasible
				return nil
			},
		},
		{
			name: "status-absent-resize-condition", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{Type: corev1.PodResizePending, Status: corev1.ConditionFalse})
				return nil
			},
		},
		{
			name: "status-absent-resize-in-progress-condition", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{Type: corev1.PodResizeInProgress, Status: corev1.ConditionFalse})
				return nil
			},
		},
		{
			name: "first-start-oom", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true, wantReason: "OOMKilled",
			wantContains: []string{"current admitted Pod spec memory limit is 16Mi", "does not currently report an enacted runtime limit"},
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.ContainerStatuses[0].RestartCount = 0
				pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
				pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{}
				return nil
			},
		},
		{
			name: "runtime-below-desired-spec", specLimit: "256Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"currently reported enacted memory limit is 16Mi", "current Pod spec limit is 256Mi"},
		},
		{
			name: "runtime-status-authoritative-during-resize", specLimit: "512Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"currently reported enacted memory limit is 16Mi", "current Pod spec limit is 512Mi"},
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue})
				return nil
			},
		},
		{
			name: "status-present-without-limit", specLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.ContainerStatuses[0].Resources = &corev1.ResourceRequirements{}
				return nil
			},
		},
		{
			name: "status-present-with-zero-limit", specLimit: "16Mi", statusLimit: "0", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "template-limit-absent", specLimit: "16Mi", statusLimit: "16Mi",
			wantDetection: true,
		},
		{
			name: "template-limit-zero", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "0",
			wantDetection: true,
		},
		{
			name: "pod-limit-and-status-absent", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "pod-limit-zero-and-status-absent", specLimit: "0", templateLimit: "256Mi",
			wantDetection: true,
		},
		{
			name: "status-present-pod-limit-absent", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"currently reported enacted memory limit is 16Mi", "Pod spec has no explicit memory limit"},
		},
		{
			name: "oom-container-missing-from-template", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(_ *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				rs.Spec.Template.Spec.Containers[0].Name = "renamed"
				return nil
			},
		},
		{
			name: "owner-uid-mismatch", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.OwnerReferences[0].UID = types.UID("stale-owner")
				return nil
			},
		},
		{
			name: "owner-uid-missing", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.OwnerReferences[0].UID = ""
				return nil
			},
		},
		{
			name: "cached-owner-uid-missing", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(_ *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				rs.UID = ""
				return nil
			},
		},
		{
			name: "replicaset-not-found", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(_ *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				rs.Name = "different-replicaset"
				return nil
			},
		},
		{
			name: "non-controller-owner", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				controller := false
				pod.OwnerReferences[0].Controller = &controller
				return nil
			},
		},
		{
			name: "unsupported-owner", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.OwnerReferences[0].Kind = "StatefulSet"
				return nil
			},
		},
		{
			name: "different-container-mismatch", specLimit: "256Mi", statusLimit: "256Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Resources: memoryRequirements("16Mi")})
				pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: "sidecar", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, Resources: statusResources("16Mi")})
				rs.Spec.Template.Spec.Containers = append(rs.Spec.Template.Spec.Containers, corev1.Container{Name: "sidecar", Resources: memoryRequirements("256Mi")})
				return nil
			},
		},
		{
			name: "second-active-container-mismatch", specLimit: "256Mi", statusLimit: "256Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true,
			wantContains: []string{"Container \"sidecar\"", "currently reported enacted memory limit is 16Mi"},
			mutate: func(pod *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Resources: memoryRequirements("16Mi")})
				pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, activeOOMStatus("sidecar", "16Mi", now.Add(-10*time.Second)))
				rs.Spec.Template.Spec.Containers = append(rs.Spec.Template.Spec.Containers, corev1.Container{Name: "sidecar", Resources: memoryRequirements("256Mi")})
				return nil
			},
		},
		{
			name: "old-rollout-rs-match", specLimit: "256Mi", statusLimit: "256Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(_ *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				controller := true
				rs.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "current-deployment", UID: types.UID("deployment-current"), Controller: &controller}}
				one := int32(1)
				return []runtime.Object{&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "current-deployment", Namespace: rs.Namespace, UID: types.UID("deployment-current")},
					Spec:       appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: memoryRequirements("512Mi")}}}}},
					Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1, ReadyReplicas: 1},
				}}
			},
		},
		{
			name: "init-container-oom", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantReason: "OOMKilled",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-10 * time.Minute))}}}
				pod.Spec.InitContainers = []corev1.Container{{Name: "setup", Resources: memoryRequirements("16Mi")}}
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}, Resources: statusResources("16Mi")}}
				return nil
			},
		},
		{
			name: "recovered-stale-oom", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
				pod.Status.ContainerStatuses[0].Ready = true
				pod.Status.ContainerStatuses[0].RestartCount = 1
				pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Minute))}}
				pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-30 * time.Minute))}}
				return nil
			},
		},
		{
			name: "recent-running-oom", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true, wantReason: "OOMKilled",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.ContainerStatuses[0].Ready = true
				pod.Status.ContainerStatuses[0].RestartCount = 1
				pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-2 * time.Minute))}}
				pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = metav1.NewTime(now.Add(-2 * time.Minute))
				return nil
			},
		},
		{
			name: "long-running-recovered-oom", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.ContainerStatuses[0].Ready = true
				pod.Status.ContainerStatuses[0].RestartCount = 1
				pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Minute))}}
				pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = metav1.NewTime(now.Add(-30 * time.Minute))
				return nil
			},
		},
		{
			name: "benchmark-completed-reason", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantCause: true, wantReason: "Completed",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.InitContainers = []corev1.Container{{Name: "setup"}}
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}}}
				return nil
			},
		},
		{
			name: "readiness-precedence-collision", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantReason: "ReadinessProbeInvalid",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromString("health")}}}
				pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse})
				return nil
			},
		},
		{
			name: "liveness-precedence-collision", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantReason: "LivenessProbeInvalid",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Spec.Containers[0].LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("health")}}}
				return nil
			},
		},
		{
			name: "image-precedence-collision", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantReason: "ImagePullBackOff",
			mutate: func(pod *corev1.Pod, rs *appsv1.ReplicaSet) []runtime.Object {
				imageSpec := corev1.Container{Name: "pulling", Image: "invalid.example/image"}
				pod.Spec.Containers = append([]corev1.Container{imageSpec}, pod.Spec.Containers...)
				pod.Status.ContainerStatuses = append([]corev1.ContainerStatus{{Name: "pulling", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}, pod.Status.ContainerStatuses...)
				rs.Spec.Template.Spec.Containers = append([]corev1.Container{imageSpec}, rs.Spec.Template.Spec.Containers...)
				return nil
			},
		},
		{
			name: "init-precedence-collision", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true, wantReason: "InitContainerStalled",
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.Status.Phase = corev1.PodPending
				pod.Spec.InitContainers = []corev1.Container{{Name: "setup", Image: "setup:latest"}}
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-10 * time.Minute))}}}}
				return nil
			},
		},
		{
			name: "standalone-pod", specLimit: "16Mi", statusLimit: "16Mi", templateLimit: "256Mi",
			wantDetection: true,
			mutate: func(pod *corev1.Pod, _ *appsv1.ReplicaSet) []runtime.Object {
				pod.OwnerReferences = nil
				return nil
			},
		},
	}

	objects := make([]runtime.Object, 0, len(cases)*2)
	for _, tc := range cases {
		pod, rs := oomTestObjects(tc.name, tc.specLimit, tc.statusLimit, tc.templateLimit, now)
		var extra []runtime.Object
		if tc.mutate != nil {
			extra = tc.mutate(pod, rs)
		}
		objects = append(objects, pod, rs)
		objects = append(objects, extra...)
	}

	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	wantRows := 0
	for _, tc := range cases {
		if tc.wantDetection {
			wantRows++
		}
	}
	var detections []Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detections = DetectProblems(cache, "oom-test")
		rows := 0
		for _, detection := range detections {
			if detection.Kind == "Pod" && strings.HasPrefix(detection.Name, "pod-") {
				rows++
			}
		}
		if rows >= wantRows {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	byName := make(map[string]Detection)
	for _, detection := range detections {
		if detection.Kind == "Pod" {
			byName[detection.Name] = detection
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detection, found := byName["pod-"+tc.name]
			if found != tc.wantDetection {
				t.Fatalf("detection found = %v, want %v; detections: %+v", found, tc.wantDetection, detections)
			}
			if !found {
				return
			}
			if tc.wantReason != "" && detection.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", detection.Reason, tc.wantReason)
			}
			if tc.wantCause {
				if detection.Cause == "" || detection.Action == "" {
					t.Fatalf("expected cause and action, got %+v", detection)
				}
				for _, fragment := range tc.wantContains {
					if !strings.Contains(detection.Cause, fragment) {
						t.Errorf("cause %q does not contain %q", detection.Cause, fragment)
					}
				}
				if !strings.Contains(detection.Action, "ReplicaSet template") || !strings.Contains(detection.Action, "changed after Pod creation") || !strings.Contains(detection.Action, "VPA") || !strings.Contains(detection.Action, "admission") {
					t.Errorf("action does not identify bounded mutation sources: %q", detection.Action)
				}
			} else if detection.Cause != "" || detection.Action != "" {
				t.Fatalf("unexpected OOM discrepancy diagnosis: cause=%q action=%q", detection.Cause, detection.Action)
			}
		})
	}
}

func TestPodReasonClassifiesAsOOM(t *testing.T) {
	tests := []struct {
		name, reason, last string
		want               bool
	}{
		{name: "direct", reason: "OOMKilled", want: true},
		{name: "crashloop", reason: "CrashLoopBackOff", last: "OOMKilled", want: true},
		{name: "completed", reason: "Completed", last: "OOMKilled", want: true},
		{name: "failed", reason: "Failed", last: "OOMKilled", want: true},
		{name: "image", reason: "ImagePullBackOff", last: "OOMKilled"},
		{name: "readiness", reason: "ReadinessProbeInvalid", last: "OOMKilled"},
		{name: "liveness", reason: "LivenessProbeFailed", last: "OOMKilled"},
		{name: "init", reason: "InitContainerStalled", last: "OOMKilled"},
		{name: "waiting", reason: "ContainerCreating", last: "OOMKilled"},
		{name: "high restart", reason: "HighRestartCount", last: "OOMKilled"},
		{name: "no oom history", reason: "CrashLoopBackOff", last: "Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := podReasonClassifiesAsOOM(tt.reason, tt.last); got != tt.want {
				t.Fatalf("podReasonClassifiesAsOOM(%q, %q) = %v, want %v", tt.reason, tt.last, got, tt.want)
			}
		})
	}
}

func oomTestObjects(name, specLimit, statusLimit, templateLimit string, now time.Time) (*corev1.Pod, *appsv1.ReplicaSet) {
	controller := true
	rsName := "rs-" + name
	rsUID := types.UID("uid-" + name)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pod-" + name,
			Namespace:         "oom-test",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, UID: rsUID, Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: memoryRequirements(specLimit)}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{activeOOMStatus("app", statusLimit, now.Add(-20*time.Second))},
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: rsName, Namespace: "oom-test", UID: rsUID},
		Spec:       appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: memoryRequirements(templateLimit)}}}}},
		Status:     appsv1.ReplicaSetStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
	return pod, rs
}

func activeOOMStatus(name, limit string, finishedAt time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         name,
		RestartCount: 3,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(finishedAt),
		}},
		Resources: statusResources(limit),
	}
}

func memoryRequirements(limit string) corev1.ResourceRequirements {
	if limit == "" {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}}
}

func statusResources(limit string) *corev1.ResourceRequirements {
	if limit == "" {
		return nil
	}
	resources := memoryRequirements(limit)
	return &resources
}
