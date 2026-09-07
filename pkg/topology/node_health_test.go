package topology

import (
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A green node in the graph is also a "no problems" answer from MCP
// (internal/mcp/tools.go builds its problem list from degraded/unhealthy nodes),
// so these pin the line between declared and confirmed.

func routeWithParents(generation int64, parents ...map[string]any) *unstructured.Unstructured {
	list := make([]any, 0, len(parents))
	for _, p := range parents {
		list = append(list, p)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"generation": generation},
		"status":   map[string]any{"parents": list},
	}}
}

func parent(conds ...map[string]any) map[string]any {
	list := make([]any, 0, len(conds))
	for _, c := range conds {
		list = append(list, c)
	}
	return map[string]any{"conditions": list}
}

func cond(condType, status string, generation int64) map[string]any {
	return map[string]any{"type": condType, "status": status, "observedGeneration": generation}
}

func TestGetRouteHealth(t *testing.T) {
	tests := []struct {
		name  string
		route *unstructured.Unstructured
		want  HealthStatus
	}{
		{
			// The bug: a gateway accepting the route says nothing about whether
			// the backends it forwards to exist.
			name:  "accepted but backend refs unresolved",
			route: routeWithParents(1, parent(cond("Accepted", "True", 1), cond("ResolvedRefs", "False", 1))),
			want:  StatusDegraded,
		},
		{
			name:  "accepted and refs resolved",
			route: routeWithParents(1, parent(cond("Accepted", "True", 1), cond("ResolvedRefs", "True", 1))),
			want:  StatusHealthy,
		},
		{
			name:  "every parent rejected",
			route: routeWithParents(1, parent(cond("Accepted", "False", 1))),
			want:  StatusUnhealthy,
		},
		{
			name: "one parent rejected among several",
			route: routeWithParents(1,
				parent(cond("Accepted", "True", 1), cond("ResolvedRefs", "True", 1)),
				parent(cond("Accepted", "False", 1))),
			want: StatusDegraded,
		},
		{
			// One parent healthy and one unassessed is not a healthy route: the
			// parent that reported must not speak for the one that did not.
			name: "one parent current and healthy, another stale",
			route: routeWithParents(7,
				parent(cond("Accepted", "True", 7), cond("ResolvedRefs", "True", 7)),
				parent(cond("Accepted", "False", 3))),
			want: StatusDegraded,
		},
		{
			name: "one parent current and healthy, another reporting nothing",
			route: routeWithParents(1,
				parent(cond("Accepted", "True", 1), cond("ResolvedRefs", "True", 1)),
				parent()),
			want: StatusDegraded,
		},
		{
			name:  "no parent reports at all",
			route: routeWithParents(1),
			want:  StatusUnknown,
		},
		{
			// Conditions describing an older spec are not evidence about this one.
			name:  "conditions observed against an older generation",
			route: routeWithParents(7, parent(cond("Accepted", "True", 3), cond("ResolvedRefs", "True", 3))),
			want:  StatusUnknown,
		},
		{
			// Accepted alone is not confirmation the backends resolve.
			name:  "accepted with no ResolvedRefs reported yet",
			route: routeWithParents(1, parent(cond("Accepted", "True", 1))),
			want:  StatusDegraded,
		},
		{
			// A rejection plus a silent parent is not an established total failure.
			name: "one parent rejected, another unassessed",
			route: routeWithParents(1,
				parent(cond("Accepted", "False", 1)),
				parent()),
			want: StatusDegraded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getRouteHealth(tt.route); got != tt.want {
				t.Errorf("getRouteHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}

func hpaWith(conds ...autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
	min := int32(1)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: &min,
			MaxReplicas: 10,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 2,
			DesiredReplicas: 2,
			Conditions:      conds,
		},
	}
}

func TestHPANodeHealth(t *testing.T) {
	t.Run("reports an HPA that cannot fetch metrics", func(t *testing.T) {
		hpa := hpaWith(
			autoscalingv2.HorizontalPodAutoscalerCondition{
				Type: "AbleToScale", Status: "True",
			},
			autoscalingv2.HorizontalPodAutoscalerCondition{
				Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric",
			},
		)
		if got := hpaNodeHealth(hpa); got == StatusHealthy {
			t.Errorf("hpaNodeHealth() = %q, want a non-healthy status for unavailable metrics", got)
		}
	})

	t.Run("reports an HPA that cannot scale", func(t *testing.T) {
		hpa := hpaWith(autoscalingv2.HorizontalPodAutoscalerCondition{
			Type: "AbleToScale", Status: "False", Reason: "FailedGetScale",
		})
		if got := hpaNodeHealth(hpa); got != StatusUnhealthy {
			t.Errorf("hpaNodeHealth() = %q, want %q", got, StatusUnhealthy)
		}
	})

	t.Run("does not manufacture a problem out of a healthy HPA", func(t *testing.T) {
		hpa := hpaWith(
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "AbleToScale", Status: "True"},
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "ScalingActive", Status: "True"},
		)
		if got := hpaNodeHealth(hpa); got != StatusHealthy {
			t.Errorf("hpaNodeHealth() = %q, want %q", got, StatusHealthy)
		}
	})

	t.Run("does not report routine scaling as a problem", func(t *testing.T) {
		// MCP turns degraded nodes into reported problems, so an HPA doing
		// exactly its job must not become one.
		hpa := hpaWith(
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "AbleToScale", Status: "True"},
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "ScalingActive", Status: "True"},
		)
		hpa.Status.CurrentReplicas = 2
		hpa.Status.DesiredReplicas = 5
		if got := hpaNodeHealth(hpa); got == StatusUnhealthy || got == StatusDegraded {
			t.Errorf("hpaNodeHealth() = %q, want scaling not to be reported as a problem", got)
		}
	})

	t.Run("says nothing about an autoscaler the controller has not reported on", func(t *testing.T) {
		// hpadiag.Analyze returns "ok" for a conditionless HPA, so absence of
		// evidence must not reach the graph as health.
		hpa := hpaWith()
		if got := hpaNodeHealth(hpa); got != StatusUnknown {
			t.Errorf("hpaNodeHealth() = %q, want %q for an unreconciled HPA", got, StatusUnknown)
		}
	})

	t.Run("treats a deliberately idle autoscaler as idle, not healthy", func(t *testing.T) {
		zero := int32(0)
		hpa := hpaWith(
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "AbleToScale", Status: "True"},
			autoscalingv2.HorizontalPodAutoscalerCondition{Type: "ScalingActive", Status: "False", Reason: "ScalingDisabled"},
		)
		hpa.Spec.MinReplicas = &zero
		hpa.Status.CurrentReplicas = 0
		hpa.Status.DesiredReplicas = 0
		if got := hpaNodeHealth(hpa); got != StatusNeutral {
			t.Errorf("hpaNodeHealth() = %q, want %q", got, StatusNeutral)
		}
	})

	t.Run("says nothing when there is no HPA", func(t *testing.T) {
		if got := hpaNodeHealth(nil); got != StatusUnknown {
			t.Errorf("hpaNodeHealth(nil) = %q, want %q", got, StatusUnknown)
		}
	})
}

// Expanding a pod group must not re-derive health from the phase: a
// crash-looping pod sits at Phase=Running, so the group has to carry the
// verdict pkg/health already computed.
func TestPodGroupCarriesComputedPodStatus(t *testing.T) {
	crashing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 9,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CrashLoopBackOff",
				}},
			}},
		},
	}
	group := &PodGroup{Key: "prod/Deployment/api", GroupName: "api", Pods: []*corev1.Pod{crashing}}
	node := CreatePodGroupNode(group, nil)

	details, ok := node.Data["pods"].([]map[string]any)
	if !ok || len(details) != 1 {
		t.Fatalf("expected one pod detail, got %#v", node.Data["pods"])
	}
	status, _ := details[0]["status"].(string)
	if status == "" {
		t.Fatal("pod detail carries no status; the frontend would fall back to the phase")
	}
	if status == string(StatusHealthy) {
		t.Errorf("crash-looping pod reported %q — Phase is Running, which is exactly what hides it", status)
	}
	if phase, _ := details[0]["phase"].(string); phase != string(corev1.PodRunning) {
		t.Fatalf("fixture no longer reproduces the trap: phase = %q", phase)
	}
}
