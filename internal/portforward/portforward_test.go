package portforward

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// TestOwnerScoping verifies that each owner's forward is independent: stopping
// one owner never tears down another's, and status/reuse lookups are scoped
// correctly. This is the invariant that stops prometheus discovery and the
// traffic subsystem from clobbering each other's metrics tunnel.
func TestOwnerScoping(t *testing.T) {
	saved := reg
	t.Cleanup(func() { reg = saved })
	reg = &registry{forwards: map[Owner]*metricsForward{}}

	// Two owners hold live forwards in the same context.
	reg.forwards[OwnerPrometheus] = &metricsForward{active: true, localPort: 1111, namespace: "monitoring", serviceName: "prometheus", contextName: "ctxA"}
	reg.forwards[OwnerTraffic] = &metricsForward{active: true, localPort: 2222, namespace: "caretta", serviceName: "caretta-vm", contextName: "ctxA"}

	if got := GetConnectionInfo(OwnerPrometheus); !got.Connected || got.LocalPort != 1111 {
		t.Fatalf("prometheus info = %+v", got)
	}
	if got := GetConnectionInfo(OwnerTraffic); !got.Connected || got.LocalPort != 2222 {
		t.Fatalf("traffic info = %+v", got)
	}

	// Stopping prometheus's forward must not touch traffic's — the core fix.
	Stop(OwnerPrometheus)
	if GetConnectionInfo(OwnerPrometheus).Connected {
		t.Fatal("prometheus forward not stopped")
	}
	if !GetConnectionInfo(OwnerTraffic).Connected {
		t.Fatal("traffic forward was torn down by prometheus Stop (cross-owner clobber)")
	}

	// GetAddressForService peeks across owners (read-only reuse) and is
	// context-scoped, matching on the target service.
	if GetAddressForService(OwnerPrometheus, "ctxA", "caretta", "caretta-vm") == "" {
		t.Fatal("GetAddressForService should surface traffic's caretta-vm forward for ctxA")
	}
	if GetAddressForService(OwnerPrometheus, "ctxB", "caretta", "caretta-vm") != "" {
		t.Fatal("GetAddressForService must not match a different context")
	}
	if !IsConnectedForContext("ctxA") || IsConnectedForContext("ctxB") {
		t.Fatal("IsConnectedForContext scoping wrong")
	}
}

// TestGetAddressForServiceIsTargetAware pins that a caller must NOT adopt a
// forward bound to a different backend. With only a prometheus-owned forward to
// monitoring/prometheus-operated live, a Caretta lookup for caretta/caretta-vm
// must return empty (no cross-adoption), forcing a dedicated forward — the
// mismatched backend answers the generic probe but holds no caretta metrics. The
// symmetric case (resource metrics adopting caretta-vm) is guarded the same way.
func TestGetAddressForServiceIsTargetAware(t *testing.T) {
	saved := reg
	t.Cleanup(func() { reg = saved })
	reg = &registry{forwards: map[Owner]*metricsForward{}}

	// Only the general-metrics forward is up (owner=prometheus → prometheus-operated).
	reg.forwards[OwnerPrometheus] = &metricsForward{
		active: true, localPort: 15329, namespace: "monitoring", serviceName: "prometheus-operated", contextName: "ctxA",
	}

	// A Caretta lookup for its own backend must reject the mismatched service
	// rather than adopt the general Prometheus forward that happens to be live.
	if got := GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm"); got != "" {
		t.Fatalf("target-aware lookup adopted the wrong forward: got %q, want empty", got)
	}
	// Conversely, resource metrics may reuse the prometheus-operated forward by
	// exact match — that is the legitimate cross/own-owner reuse.
	if got := GetAddressForService(OwnerPrometheus, "ctxA", "monitoring", "prometheus-operated"); got != "http://localhost:15329" {
		t.Fatalf("matching own forward: got %q, want :15329", got)
	}

	// Once Caretta's own dedicated forward exists, it is reused by exact match.
	reg.forwards[OwnerTraffic] = &metricsForward{
		active: true, localPort: 22222, namespace: "caretta", serviceName: "caretta-vm", contextName: "ctxA",
	}
	if got := GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm"); got != "http://localhost:22222" {
		t.Fatalf("own matching forward: got %q, want :22222", got)
	}
	// A peer forward that DOES target the requested service is still reusable.
	delete(reg.forwards, OwnerTraffic)
	reg.forwards[OwnerPrometheus] = &metricsForward{
		active: true, localPort: 33333, namespace: "caretta", serviceName: "caretta-vm", contextName: "ctxA",
	}
	if got := GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm"); got != "http://localhost:33333" {
		t.Fatalf("matching peer forward: got %q, want :33333", got)
	}
	// Context scoping still holds.
	if got := GetAddressForService(OwnerTraffic, "ctxB", "caretta", "caretta-vm"); got != "" {
		t.Fatalf("must not match a different context, got %q", got)
	}
}

// TestStopBumpsEpochWhileEstablishing pins the invariant that a Stop lands even
// while a forward is still coming up (not yet active): stopForwardLocked must
// bump epoch for an inactive forward too, so the in-flight Start sees the change
// and refuses to publish a connection the caller already asked to stop. Reverting
// to an early-return-when-inactive reintroduces the "Stop misses in-flight
// establish" bug.
func TestStopBumpsEpochWhileEstablishing(t *testing.T) {
	f := &metricsForward{} // establishing: not yet active
	before := f.epoch
	stopForwardLocked(f)
	if f.epoch == before {
		t.Fatal("epoch not bumped for an inactive forward — a Stop during establishment would be silently lost")
	}
}

func TestStopIfAddressDoesNotStopReplacement(t *testing.T) {
	saved := reg
	t.Cleanup(func() { reg = saved })
	reg = &registry{forwards: map[Owner]*metricsForward{
		OwnerCost: {active: true, localPort: 2222},
	}}

	if StopIfAddress(OwnerCost, "http://localhost:1111") {
		t.Fatal("stale address unexpectedly stopped the current forward")
	}
	if !GetConnectionInfo(OwnerCost).Connected {
		t.Fatal("stale cleanup stopped the replacement forward")
	}
	if !StopIfAddress(OwnerCost, "http://localhost:2222") {
		t.Fatal("current address did not stop its forward")
	}
	if GetConnectionInfo(OwnerCost).Connected {
		t.Fatal("current forward remains connected after address-scoped stop")
	}
}

func TestForwardReuseIncludesTargetPort(t *testing.T) {
	forward := &metricsForward{
		active: true, namespace: "kubecost", serviceName: "kubecost-aggregator", targetPort: 9004, contextName: "ctxA",
	}
	if !forward.matches("kubecost", "kubecost-aggregator", 9004, "ctxA") {
		t.Fatal("matching target was not reusable")
	}
	if forward.matches("kubecost", "kubecost-aggregator", 9008, "ctxA") {
		t.Fatal("forward to 9004 was reused for a 9008 request")
	}
}

// TestForwardLostNotifiesOnlyUnexpectedExit pins the lost-forward signal: a
// forward whose run ends on its own (the pod went away) is reported with the
// address it served; one ended by Stop is not.
func TestForwardLostNotifiesOnlyUnexpectedExit(t *testing.T) {
	saved := reg
	savedRun, savedFind := runPortForward, findPodForService
	t.Cleanup(func() {
		reg = saved
		runPortForward, findPodForService = savedRun, savedFind
		SetOnForwardLost(nil)
	})
	reg = &registry{forwards: map[Owner]*metricsForward{}}
	findPodForService = func(context.Context, kubernetes.Interface, string, string) (string, error) {
		return "prometheus-0", nil
	}
	podGone := make(chan struct{})
	runPortForward = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stopCh chan struct{}, readyCh chan struct{}) error {
		close(readyCh)
		select {
		case <-stopCh:
			return nil
		case <-podGone:
			return errors.New("lost connection to pod")
		}
	}
	lost := make(chan string, 2)
	SetOnForwardLost(func(owner Owner, address string) { lost <- owner + " " + address })

	start := func() *ConnectionInfo {
		t.Helper()
		info, err := StartWithClients(OwnerPrometheus, context.Background(), "monitoring", "prometheus", 9090, "ctxA", fake.NewSimpleClientset(), &rest.Config{})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return info
	}

	start()
	Stop(OwnerPrometheus)
	select {
	case got := <-lost:
		t.Fatalf("Stop reported a lost forward: %s", got)
	case <-time.After(100 * time.Millisecond):
	}

	info := start()
	close(podGone)
	select {
	case got := <-lost:
		if want := OwnerPrometheus + " " + info.Address; got != want {
			t.Fatalf("lost = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward that ended on its own was not reported")
	}
	if GetConnectionInfo(OwnerPrometheus).Connected {
		t.Fatal("lost forward still reported as connected")
	}
}

func TestIsActiveAddress(t *testing.T) {
	saved := reg
	t.Cleanup(func() { reg = saved })
	reg = &registry{forwards: map[Owner]*metricsForward{}}
	reg.forwards[OwnerTraffic] = &metricsForward{active: true, localPort: 4321}
	reg.forwards[OwnerPrometheus] = &metricsForward{active: false, localPort: 1234}

	if !IsActiveAddress("http://localhost:4321") {
		t.Fatal("another owner's live forward should count")
	}
	if IsActiveAddress("http://localhost:1234") {
		t.Fatal("an inactive forward should not count")
	}
}
