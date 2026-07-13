package portforward

import "testing"

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

	// GetAddress peeks across owners (read-only reuse) and is context-scoped.
	if GetAddress(OwnerTraffic, "ctxA") == "" {
		t.Fatal("GetAddress should surface traffic's forward for ctxA")
	}
	if GetAddress(OwnerTraffic, "ctxB") != "" {
		t.Fatal("GetAddress must not match a different context")
	}
	if !IsConnectedForContext("ctxA") || IsConnectedForContext("ctxB") {
		t.Fatal("IsConnectedForContext scoping wrong")
	}
}

// TestGetAddressPrefersOwn verifies GetAddress returns the caller's own forward
// when it has one, rather than an arbitrary peer's — so a caller reuses its own
// live forward instead of probing an incompatible one.
func TestGetAddressPrefersOwn(t *testing.T) {
	saved := reg
	t.Cleanup(func() { reg = saved })
	reg = &registry{forwards: map[Owner]*metricsForward{}}
	reg.forwards[OwnerPrometheus] = &metricsForward{active: true, localPort: 1111, contextName: "ctxA"}
	reg.forwards[OwnerTraffic] = &metricsForward{active: true, localPort: 2222, contextName: "ctxA"}

	if got := GetAddress(OwnerPrometheus, "ctxA"); got != "http://localhost:1111" {
		t.Fatalf("prometheus got %q, want its own :1111", got)
	}
	if got := GetAddress(OwnerTraffic, "ctxA"); got != "http://localhost:2222" {
		t.Fatalf("traffic got %q, want its own :2222", got)
	}
	// With no own forward, fall back to the peer's.
	Stop(OwnerPrometheus)
	if got := GetAddress(OwnerPrometheus, "ctxA"); got != "http://localhost:2222" {
		t.Fatalf("prometheus fallback got %q, want peer :2222", got)
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
