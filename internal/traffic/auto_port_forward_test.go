package traffic

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/portforward"
)

func TestHubbleConnectWithAutoPortForwardDisabled(t *testing.T) {
	portforward.SetAutoDisabled(true)
	t.Cleanup(func() { portforward.SetAutoDisabled(false) })
	h := NewHubbleSource(fake.NewSimpleClientset(relayService(reserveClosedPort(t), intstr.FromInt(4245))))
	h.relayNamespace = "kube-system"

	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Connected || !strings.Contains(strings.ToLower(info.Error), "automatic port-forwarding is turned off") || strings.Contains(info.Error, "fallback failed") {
		t.Fatalf("Connect = {connected:%v err:%q}, want the direct failure and the turned-off forward", info.Connected, info.Error)
	}
}

func TestCarettaConnectWithAutoPortForwardDisabled(t *testing.T) {
	portforward.SetAutoDisabled(true)
	t.Cleanup(func() { portforward.SetAutoDisabled(false) })
	c := sourceWithServices(t, "caretta", carettaStoreSvc("caretta", "caretta-vm"))

	info, err := c.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Connected || !strings.Contains(strings.ToLower(info.Error), "automatic port-forwarding is turned off") ||
		strings.Contains(info.Error, "Failed to start port-forward") || !strings.Contains(info.Error, "Settings → Metrics") {
		t.Fatalf("Connect = {connected:%v err:%q}, want the turned-off explanation and where to set a URL", info.Connected, info.Error)
	}
}
