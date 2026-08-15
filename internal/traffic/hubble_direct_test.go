package traffic

import (
	"context"
	"net"
	"testing"

	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes/fake"
)

// stubObserver is a minimal Hubble Relay stand-in: just enough Observer
// surface for Connect's ServerStatus connection test to pass.
type stubObserver struct {
	observerpb.UnimplementedObserverServer
}

func (s *stubObserver) ServerStatus(ctx context.Context, _ *observerpb.ServerStatusRequest) (*observerpb.ServerStatusResponse, error) {
	return &observerpb.ServerStatusResponse{}, nil
}

func startStubRelay(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	observerpb.RegisterObserverServer(srv, &stubObserver{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// A manual --hubble-address is dialed as-is: no Service lookup (the fake
// clientset holds no hubble-relay Service), no port-forward (no port-forward
// clients are registered), and the source reports the direct connection
// itself since it never appears in the port-forward registry.
func TestConnectManualHubbleAddress(t *testing.T) {
	addr := startStubRelay(t)

	SetHubbleAddress(addr)
	t.Cleanup(func() { SetHubbleAddress("") })

	h := NewHubbleSource(fake.NewSimpleClientset())
	info, err := h.Connect(context.Background(), "test-context")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !info.Connected {
		t.Fatalf("expected connected, got error: %s", info.Error)
	}
	if info.Address != addr {
		t.Errorf("Address = %q, want %q", info.Address, addr)
	}
	if info.LocalPort != 0 {
		t.Errorf("LocalPort = %d, want 0 for a direct connection", info.LocalPort)
	}

	di := h.DirectConnectionInfo()
	if di == nil || !di.Connected || di.Address != addr {
		t.Fatalf("DirectConnectionInfo = %+v, want connected at %q", di, addr)
	}

	// Reconnecting while healthy reuses the live connection.
	info2, err := h.Connect(context.Background(), "test-context")
	if err != nil || !info2.Connected || info2.Address != addr {
		t.Fatalf("reconnect: info=%+v err=%v", info2, err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if h.DirectConnectionInfo() != nil {
		t.Error("DirectConnectionInfo should be nil after Close")
	}
}

// An unreachable manual address fails loudly instead of falling back to
// discovery or port-forwarding.
func TestConnectManualHubbleAddressUnreachable(t *testing.T) {
	// Reserve a port and close the listener so the dial is refused fast.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	SetHubbleAddress(addr)
	t.Cleanup(func() { SetHubbleAddress("") })

	h := NewHubbleSource(fake.NewSimpleClientset())
	info, err := h.Connect(context.Background(), "test-context")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Connected {
		t.Fatal("expected connection failure for unreachable manual address")
	}
	if info.Error == "" {
		t.Error("expected a populated Error for the failed manual dial")
	}
}
