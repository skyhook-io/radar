package health

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
	"time"
)

func TestPodReadinessGateFailure(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, gate, reason, phase                                     string
		readyAge, containersAge, creationAge                          time.Duration
		unknownTimes, noGates, notContainersReady, want, unknownOnset bool
	}{
		{name: "missing gate", want: true}, {name: "false gate", gate: "False", want: true}, {name: "passed gate", gate: "True"}, {name: "unknown gate", gate: "Unknown"},
		{name: "no declaration", noGates: true}, {name: "other ready failure", reason: "NodeNotReady"}, {name: "containers unready", notContainersReady: true},
		{name: "recent ready", readyAge: time.Minute}, {name: "long startup recently container ready", containersAge: time.Minute}, {name: "future containers ready", containersAge: -time.Minute, want: true, unknownOnset: true},
		{name: "future ready", readyAge: -time.Minute, want: true, unknownOnset: true},
		{name: "future vetoes fresh transition", readyAge: -time.Minute, containersAge: time.Minute, want: true, unknownOnset: true},
		{name: "unknown transitions future creation", unknownTimes: true, creationAge: -time.Minute, want: true, unknownOnset: true},
		{name: "unknown transitions old creation", unknownTimes: true, want: true, unknownOnset: true},
		{name: "unknown transitions young creation", unknownTimes: true, creationAge: time.Minute},
		{name: "succeeded", phase: "Succeeded"}, {name: "pending", phase: "Pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readyAge, containersAge, creationAge := tc.readyAge, tc.containersAge, tc.creationAge
			if readyAge == 0 {
				readyAge = 10 * time.Minute
			}
			if containersAge == 0 {
				containersAge = 7 * time.Minute
			}
			if creationAge == 0 {
				creationAge = time.Hour
			}
			reason := tc.reason
			if reason == "" {
				reason = "ReadinessGatesNotReady"
			}
			phase := tc.phase
			if phase == "" {
				phase = "Running"
			}
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-creationAge))}, Spec: corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}}, Status: corev1.PodStatus{Phase: corev1.PodPhase(phase), Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: reason, Message: "gate has not reported ready", LastTransitionTime: metav1.NewTime(now.Add(-readyAge))},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-containersAge))},
			}, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
			if tc.gate != "" {
				p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: "example.com/ready", Status: corev1.ConditionStatus(tc.gate)})
			}
			if tc.noGates {
				p.Spec.ReadinessGates = nil
			}
			if tc.notContainersReady {
				p.Status.Conditions[1].Status = corev1.ConditionFalse
			}
			if tc.unknownTimes {
				for i := range p.Status.Conditions {
					p.Status.Conditions[i].LastTransitionTime = metav1.Time{}
				}
			}
			since, got := PodReadinessGateFailure(p, now)
			if got != tc.want {
				t.Fatalf("failure=%v want%v", got, tc.want)
			}
			if tc.want {
				wantSince := now.Add(-7 * time.Minute)
				if tc.unknownOnset {
					wantSince = time.Time{}
				}
				if !since.Equal(wantSince) {
					t.Errorf("onset=%v want%v", since, wantSince)
				}
				v := Pod(p, now)
				if v.Level != LevelDegraded || v.Reason != "ReadinessGatesNotReady" || v.Message != "gate has not reported ready" {
					t.Errorf("verdict %+v", v)
				}

				if tc.name == "missing gate" {
					cs := &p.Status.ContainerStatuses[0]
					cs.RestartCount = 1
					cs.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}}
					cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-2 * time.Minute))}}
					if v := Pod(p, now); v.Level != LevelUnhealthy || v.Reason != "OOMKilled" {
						t.Errorf("gate masked active OOM: %+v", v)
					}
					cs.RestartCount = 2
					cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(-10 * time.Second))}}
					cs.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-5 * time.Second))}}
					if v := Pod(p, now); v.Reason != "CrashLoopBackOff" {
						t.Errorf("gate masked stable crash loop: %+v", v)
					}
					cs.LastTerminationState = corev1.ContainerState{}
				}
				p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}
				if v := Pod(p, now); v.Level != LevelUnhealthy || v.Reason != "ImagePullBackOff" {
					t.Errorf("masked fatal failure %+v", v)
				}
			}
		})
	}
}
