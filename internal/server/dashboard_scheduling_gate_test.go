package server

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

// A pod deliberately held by a scheduling gate counts as healthy on the dashboard
// rollup, not as a warning — while an ungated pod stuck Pending past the threshold
// still counts as a warning.
func TestGetDashboardHealth_SchedulingGatedCountsHealthy(t *testing.T) {
	// The server package deliberately leaves the shared global cache connected
	// between tests (most cache-using tests re-init rather than reset), so don't
	// ResetTestState here — tearing it down disconnects the cache for every
	// later-ordered test that relies on a connected cluster.
	oldCreate := metav1.NewTime(time.Now().Add(-30 * time.Minute))

	gated := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gated", Namespace: "prod", CreationTimestamp: oldCreate},
		Spec:       corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "kueue.x-k8s.io/admission"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	ungated := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ungated", Namespace: "prod", CreationTimestamp: oldCreate},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	if err := k8s.InitTestResourceCache(kubernetesfake.NewClientset(gated, ungated)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	dh, _ := (&Server{}).getDashboardHealth(k8s.GetResourceCache(), "prod")
	if dh.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1 (the gated pod counts as healthy)", dh.Healthy)
	}
	if dh.Warning != 1 {
		t.Errorf("Warning = %d, want 1 (only the ungated aged Pending pod)", dh.Warning)
	}
	if dh.Error != 0 {
		t.Errorf("Error = %d, want 0", dh.Error)
	}
}
