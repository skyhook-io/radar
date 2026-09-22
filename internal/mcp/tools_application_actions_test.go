package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestApplicationHintNeedsDirectReadinessEvidence(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: false}}}}
	c := collector.Candidate{Application: evidence.Vault, Target: collector.Target{Container: "app"}}
	if applicationActionReason(p, c) == "" {
		t.Fatal("Vault readiness hint absent")
	}
	c.Application = evidence.RabbitMQ
	if applicationActionReason(p, c) == "" {
		t.Fatal("Rabbit readiness hint absent")
	}
	c.Application = evidence.NATS
	if applicationActionReason(p, c) != "" {
		t.Fatal("unsolicited JetStream hint")
	}
	c.Application = evidence.Vault
	p.Status.ContainerStatuses[0].Ready = true
	if applicationActionReason(p, c) != "" {
		t.Fatal("healthy hint")
	}
	p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: "sidecar", Ready: false})
	if applicationActionReason(p, c) != "" {
		t.Fatal("sidecar failure hinted")
	}
	p.Status.ContainerStatuses[0].Ready = false
	p.Spec.Containers[0].ReadinessProbe = nil
	if applicationActionReason(p, c) != "" {
		t.Fatal("no readiness evidence")
	}
}
func TestApplicationHintRestrictedContext(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	ctx := context.WithValue(context.Background(), runtimeLocalCallerKey{}, true)
	if !applicationActionsAllowed(ctx) {
		t.Fatal("eligible local blocked")
	}
	if applicationActionsAllowed(context.WithValue(ctx, applicationActionsDisabledKey{}, true)) {
		t.Fatal("restricted mount suggested unavailable tool")
	}
	if applicationActionsAllowed(context.Background()) {
		t.Fatal("unknown caller allowed")
	}
}
func TestApplicationToolEnum(t *testing.T) {
	s := applicationEvidenceInputSchema()
	got := s.Properties["application"].Enum
	if len(got) != 3 || got[0] != "rabbitmq" || got[1] != "nats" || got[2] != "vault" {
		t.Fatalf("%v", got)
	}
	if s.Properties["adapter"] != nil {
		t.Fatal("stale adapter argument")
	}
}
