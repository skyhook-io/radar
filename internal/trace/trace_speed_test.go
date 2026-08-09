package trace

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func speedDeps(t *testing.T) Deps {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod", Labels: map[string]string{"app": "api"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: []corev1.ContainerPort{{ContainerPort: 80}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now())},
		}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(svc, pod)); err != nil {
		t.Fatalf("init: %v", err)
	}
	return Deps{Cache: k8s.GetResourceCache(), Dynamic: k8s.GetDynamicResourceCache(), Discovery: k8s.GetResourceDiscovery(), Issues: issues.NewCacheProvider()}
}

// A cancelled/expired context must yield a FAST, honest unknown-partial - never a
// hang and never a confident verdict over an incomplete walk.
func TestBuildTrace_TotalBudget_CancelledCtxReturnsHonestPartial(t *testing.T) {
	deps := speedDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before the build starts
	start := time.Now()
	tr, err := BuildTraceWithOptions(ctx, deps, "Service", "prod", "api", Options{Probe: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("cancelled ctx took %v - must return fast, not hang", elapsed)
	}
	if tr.Verdict != VerdictUnknown {
		t.Errorf("verdict = %q, want unknown on timeout", tr.Verdict)
	}
	if !strings.Contains(tr.Reason, "timed out") {
		t.Errorf("reason = %q, want a timed-out note", tr.Reason)
	}
}

// A tiny total budget must bound the WHOLE call (static + probes): it returns well
// within budget rather than letting the probe phase run its own full 3s.
func TestBuildTrace_TotalBudget_BoundsTheWholeCall(t *testing.T) {
	deps := speedDeps(t)
	start := time.Now()
	tr, _ := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{Probe: true, TotalBudget: time.Millisecond})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("1ms total budget took %v - the probe phase must inherit the total deadline", elapsed)
	}
	if tr == nil {
		t.Fatal("nil trace")
	}
}

// A normal budget must produce a real verdict, not a spurious timeout.
func TestBuildTrace_TotalBudget_FastPathUnaffected(t *testing.T) {
	deps := speedDeps(t)
	var tr *Trace
	waitFor(t, func() bool {
		x, err := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{})
		if err != nil {
			return false
		}
		tr = x
		return tr.Verdict != ""
	})
	if strings.Contains(tr.Reason, "timed out") {
		t.Errorf("fast path should not time out, reason=%q", tr.Reason)
	}
}
