package portforward

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/portforward/forwardtest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func publishedForward(m *Manager, owner, namespace, service string, port int) *Handle {
	f := &forward{manager: m, owner: owner, owners: 1, active: true, stopCh: make(chan struct{}),
		info: ConnectionInfo{Connected: true, Address: fmt.Sprintf("http://localhost:%d", port), LocalPort: port,
			Namespace: namespace, ServiceName: service, ContextName: "ctxA"}}
	m.forwards[f] = struct{}{}
	return &Handle{f: f}
}

func prometheusManager() *Manager {
	labels := map[string]string{"app": "prometheus"}
	return NewManager(fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "monitoring"}, Spec: corev1.ServiceSpec{Selector: labels}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-0", Namespace: "monitoring", Labels: labels}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	), &rest.Config{})
}

// TestOwnerScoping verifies that each owner's forward is independent: stopping
// one owner never tears down another's, and status/reuse lookups are scoped
// correctly. This is the invariant that stops prometheus discovery and the
// traffic subsystem from clobbering each other's metrics tunnel.
func TestOwnerScoping(t *testing.T) {
	m := NewManager(nil, nil)

	// Two owners hold live forwards in the same context.
	prometheus := publishedForward(m, OwnerPrometheus, "monitoring", "prometheus", 1111)
	traffic := publishedForward(m, OwnerTraffic, "caretta", "caretta-vm", 2222)

	if got := prometheus.Info(); !got.Connected || got.LocalPort != 1111 {
		t.Fatalf("prometheus info = %+v", got)
	}
	if got := traffic.Info(); !got.Connected || got.LocalPort != 2222 {
		t.Fatalf("traffic info = %+v", got)
	}

	// Stopping prometheus's forward must not touch traffic's — the core fix.
	prometheus.Stop()
	if prometheus.Info().Connected {
		t.Fatal("prometheus forward not stopped")
	}
	if !traffic.Info().Connected {
		t.Fatal("traffic forward was torn down by prometheus Stop (cross-owner clobber)")
	}

	// GetAddressForService peeks across owners (read-only reuse) and is
	// context-scoped, matching on the target service.
	if m.GetAddressForService(OwnerPrometheus, "ctxA", "caretta", "caretta-vm").Address() == "" {
		t.Fatal("GetAddressForService should surface traffic's caretta-vm forward for ctxA")
	}
	if m.GetAddressForService(OwnerPrometheus, "ctxB", "caretta", "caretta-vm").Address() != "" {
		t.Fatal("GetAddressForService must not match a different context")
	}
	peer := m.GetAddressForService(OwnerPrometheus, "ctxA", "caretta", "caretta-vm")
	if !peer.SameForward(traffic) || peer.Stop() || !traffic.Info().Connected {
		t.Fatal("borrower stopped its peer's tunnel")
	}
	if !m.IsConnectedForContext("ctxA") || m.IsConnectedForContext("ctxB") {
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
	m := NewManager(nil, nil)

	// Only the general-metrics forward is up (owner=prometheus → prometheus-operated).
	publishedForward(m, OwnerPrometheus, "monitoring", "prometheus-operated", 15329)

	// A Caretta lookup for its own backend must reject the mismatched service
	// rather than adopt the general Prometheus forward that happens to be live.
	if got := m.GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm").Address(); got != "" {
		t.Fatalf("target-aware lookup adopted the wrong forward: got %q, want empty", got)
	}
	// Conversely, resource metrics may reuse the prometheus-operated forward by
	// exact match — that is the legitimate cross/own-owner reuse.
	if got := m.GetAddressForService(OwnerPrometheus, "ctxA", "monitoring", "prometheus-operated").Address(); got != "http://localhost:15329" {
		t.Fatalf("matching own forward: got %q, want :15329", got)
	}

	// Once Caretta's own dedicated forward exists, it is reused by exact match.
	traffic := publishedForward(m, OwnerTraffic, "caretta", "caretta-vm", 22222)
	if got := m.GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm").Address(); got != "http://localhost:22222" {
		t.Fatalf("own matching forward: got %q, want :22222", got)
	}
	// A peer forward that DOES target the requested service is still reusable.
	traffic.Stop()
	publishedForward(m, OwnerPrometheus, "caretta", "caretta-vm", 33333)
	if got := m.GetAddressForService(OwnerTraffic, "ctxA", "caretta", "caretta-vm").Address(); got != "http://localhost:33333" {
		t.Fatalf("matching peer forward: got %q, want :33333", got)
	}
	// Context scoping still holds.
	if got := m.GetAddressForService(OwnerTraffic, "ctxB", "caretta", "caretta-vm").Address(); got != "" {
		t.Fatalf("must not match a different context, got %q", got)
	}
}

func TestCloseDuringEstablishmentStopsUnpublishedForward(t *testing.T) {
	m := prometheusManager()
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.run = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stop, ready chan struct{}) error {
		close(started)
		<-release
		close(ready)
		<-stop
		close(stopped)
		return nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090, "ctxA")
		result <- err
	}()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("Start failed before the tunnel ran: %v", err)
	}
	m.Close()
	close(release)
	if err := <-result; err == nil {
		t.Fatal("closed manager published a forward")
	}
	<-stopped
	if m.IsConnectedForContext("ctxA") {
		t.Fatal("forward remained published")
	}
	if _, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090, "ctxA"); err == nil {
		t.Fatal("closed manager accepted Start")
	}
}

func TestStaleHandleDoesNotStopReplacement(t *testing.T) {
	m := NewManager(nil, nil)
	stale := publishedForward(m, OwnerCost, "ns", "svc", 2222)
	stale.Stop()
	replacement := publishedForward(m, OwnerCost, "ns", "svc", 2222)
	if stale.Stop() {
		t.Fatal("stale address unexpectedly stopped the current forward")
	}
	if !replacement.Info().Connected {
		t.Fatal("stale cleanup stopped the replacement forward")
	}
	if !replacement.Stop() {
		t.Fatal("current address did not stop its forward")
	}
	if replacement.Info().Connected {
		t.Fatal("current forward remains connected after address-scoped stop")
	}
}

func TestForwardReuseIncludesTargetPort(t *testing.T) {
	forward := &forward{info: ConnectionInfo{Namespace: "kubecost", ServiceName: "kubecost-aggregator", ContextName: "ctxA"}, targetPort: 9004}
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
	m := prometheusManager()
	t.Cleanup(func() { SetOnForwardLost(nil) })
	podGone := make(chan struct{})
	m.run = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stopCh chan struct{}, readyCh chan struct{}) error {
		close(readyCh)
		select {
		case <-stopCh:
			return nil
		case <-podGone:
			return errors.New("lost connection to pod")
		}
	}
	lost := make(chan string, 2)
	SetOnForwardLost(func(h *Handle) { lost <- h.f.owner + " " + h.Address() })

	start := func() *Handle {
		t.Helper()
		info, err := m.Start(OwnerPrometheus, context.Background(), "monitoring", "prometheus", 9090, "ctxA")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return info
	}

	start().Stop()
	select {
	case got := <-lost:
		t.Fatalf("Stop reported a lost forward: %s", got)
	case <-time.After(100 * time.Millisecond):
	}

	info := start()
	close(podGone)
	select {
	case got := <-lost:
		if want := OwnerPrometheus + " " + info.Address(); got != want {
			t.Fatalf("lost = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward that ended on its own was not reported")
	}
	if info.Info().Connected {
		t.Fatal("lost forward still reported as connected")
	}
}

func TestIsActiveAddress(t *testing.T) {
	m := NewManager(nil, nil)
	publishedForward(m, OwnerTraffic, "", "", 4321)
	publishedForward(m, OwnerPrometheus, "", "", 1234).Stop()

	if !m.IsActiveAddress("http://localhost:4321") {
		t.Fatal("another owner's live forward should count")
	}
	if m.IsActiveAddress("http://localhost:1234") {
		t.Fatal("an inactive forward should not count")
	}
}

func TestStartRefusesWhenAutoDisabled(t *testing.T) {
	SetAutoDisabled(true)
	t.Cleanup(func() { SetAutoDisabled(false) })
	client := fake.NewClientset()

	m := NewManager(client, &rest.Config{Host: "https://example.invalid"})
	info, err := m.Start(OwnerCost, context.Background(), "monitoring", "prometheus", 9090, "ctxA")
	if !errors.Is(err, ErrAutoDisabled) || info != nil {
		t.Fatalf("Start = %v, %v; want nil, ErrAutoDisabled", info, err)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Fatalf("disabled start called the API: %v", actions)
	}
}

func TestOwningHandlesReleaseSharedTunnelIndependently(t *testing.T) {
	m := prometheusManager()
	t.Cleanup(m.Close)
	m.run = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stop, ready chan struct{}) error {
		close(ready)
		<-stop
		return nil
	}
	start := func() *Handle {
		t.Helper()
		h, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090, "a")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first, second := start(), start()
	if !first.SameForward(second) {
		t.Fatal("identical starts did not reuse the tunnel")
	}
	borrowed := m.GetAddressForService(OwnerTraffic, "a", "monitoring", "prometheus")
	if second.Replace(borrowed) != second {
		t.Fatal("service lookup discarded the owning handle")
	}
	first.Stop()
	if first.Stop() || !second.Info().Connected || !borrowed.Info().Connected {
		t.Fatal("one release closed a shared tunnel")
	}
	second.Stop()
	if borrowed.Info().Connected {
		t.Fatal("borrower kept the tunnel alive")
	}
}

func TestStalledUpgradeEndsWithAttempt(t *testing.T) {
	for _, tt := range []struct{ method, end string }{
		{"GET", "close"}, {"POST", "close"}, {"GET", "timeout"}, {"POST", "caller"},
	} {
		t.Run(tt.method+"/"+tt.end, func(t *testing.T) {
			arrived, disconnected, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method {
					http.Error(w, "upgrade rejected", http.StatusBadRequest)
					return
				}
				close(arrived)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-release:
				}
			})
			defer close(release)
			m := NewManager(kubernetes.NewForConfigOrDie(cfg), cfg)
			defer m.Close()
			run := m.run
			m.run = func(ctx context.Context, c kubernetes.Interface, cfg *rest.Config, ns, pod string, local, remote int, stop, ready chan struct{}) error {
				defer close(exited)
				return run(ctx, c, cfg, ns, pod, local, remote, stop, ready)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := m.Start(OwnerTraffic, ctx, "ns", "svc", 8080, "ctx")
				result <- err
			}()
			select {
			case <-arrived:
			case err := <-result:
				t.Fatalf("upgrade never arrived: %v", err)
			}
			switch tt.end {
			case "close":
				m.Close()
			case "caller":
				cancel()
			}
			if err := <-result; err == nil {
				t.Fatal("stalled forward succeeded")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("upgrade request remained open after Start returned")
			}
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("runner did not exit")
			}
		})
	}
}
