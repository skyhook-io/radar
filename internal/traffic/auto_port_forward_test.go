package traffic

import (
	"context"
	"errors"
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

func TestComposeConnectErrorWithAutoPortForwardDisabled(t *testing.T) {
	route := composeConnectError("10.0.0.1:80", errDirectUnreachable, portforward.ErrAutoDisabled)
	if !strings.Contains(route, "needs a network route") {
		t.Errorf("unreachable relay = %q, want routing advice", route)
	}
	tlsFailed := composeConnectError("10.0.0.1:80", errors.New("TLS gRPC connection test failed"), portforward.ErrAutoDisabled)
	if strings.Contains(tlsFailed, "network route") || !strings.Contains(tlsFailed, "TLS gRPC connection test failed") {
		t.Errorf("reachable relay with a failed connection = %q, want the connection error, not routing advice", tlsFailed)
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
