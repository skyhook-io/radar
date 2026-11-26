package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/portforward/forwardtest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func publishedForward(m *Manager, owner, namespace, service string, port int) *Handle {
	h := &Handle{manager: m, owner: owner, active: true, stopCh: make(chan struct{}),
		info: ConnectionInfo{Connected: true, Address: fmt.Sprintf("http://localhost:%d", port), LocalPort: port,
			Namespace: namespace, ServiceName: service, ContextName: m.contextName}}
	m.forwards[h] = struct{}{}
	return h
}

func prometheusManager() *Manager {
	labels := map[string]string{"app": "prometheus"}
	return NewManager(fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "monitoring"}, Spec: corev1.ServiceSpec{Selector: labels}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-0", Namespace: "monitoring", Labels: labels}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	), &rest.Config{}, "ctxA")
}

// TestOwnerScoping verifies that each owner's forward is independent: stopping
// one owner never tears down another's, and status is scoped correctly.
// This is the invariant that stops prometheus discovery and the
// traffic subsystem from clobbering each other's metrics tunnel.
func TestOwnerScoping(t *testing.T) {
	m := NewManager(nil, nil, "ctxA")

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
		_, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090)
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
	if len(m.forwards) != 0 {
		t.Fatal("forward remained published")
	}
	if _, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090); err == nil {
		t.Fatal("closed manager accepted Start")
	}
}

func TestStaleHandleDoesNotStopReplacement(t *testing.T) {
	m := NewManager(nil, nil, "ctxA")
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
	lost := make(chan *Handle, 2)
	SetOnForwardLost(func(h *Handle) { lost <- h })

	start := func() *Handle {
		t.Helper()
		info, err := m.Start(OwnerPrometheus, context.Background(), "monitoring", "prometheus", 9090)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return info
	}

	start().Stop()
	select {
	case got := <-lost:
		t.Fatalf("Stop reported a lost forward: %s", got.Address())
	case <-time.After(100 * time.Millisecond):
	}

	info := start()
	close(podGone)
	select {
	case got := <-lost:
		if got != info || got.owner != OwnerPrometheus || got.Address() != info.Address() {
			t.Fatalf("lost handle = %p (%s %s), want %p (%s %s)", got, got.owner, got.Address(), info, OwnerPrometheus, info.Address())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward that ended on its own was not reported")
	}
	if info.Info().Connected {
		t.Fatal("lost forward still reported as connected")
	}
}

func TestHandleLiveness(t *testing.T) {
	m := NewManager(nil, nil, "ctxA")
	defer m.Close()
	live := publishedForward(m, OwnerTraffic, "", "", 4321)
	stopped := publishedForward(m, OwnerPrometheus, "", "", 1234)
	stopped.Stop()

	if !live.Alive() {
		t.Fatal("live forward reported stopped")
	}
	if stopped.Alive() {
		t.Fatal("stopped forward reported live")
	}
}

func TestHandleDoneWithoutForward(t *testing.T) {
	for _, handle := range []*Handle{nil, {}} {
		select {
		case <-handle.Done():
		default:
			t.Fatal("Done is open without a tunnel")
		}
	}
}

func TestStartRefusesWhenAutoDisabled(t *testing.T) {
	SetAutoDisabled(true)
	t.Cleanup(func() { SetAutoDisabled(false) })
	client := fake.NewClientset()

	m := NewManager(client, &rest.Config{Host: "https://example.invalid"}, "ctxA")
	info, err := m.Start(OwnerCost, context.Background(), "monitoring", "prometheus", 9090)
	if !errors.Is(err, ErrAutoDisabled) || info != nil {
		t.Fatalf("Start = %v, %v; want nil, ErrAutoDisabled", info, err)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Fatalf("disabled start called the API: %v", actions)
	}
}

func TestSameTargetStartsWhileFirstIsPending(t *testing.T) {
	m := prometheusManager()
	t.Cleanup(m.Close)
	firstStarted, readyFirst := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	m.run = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stop, ready chan struct{}) error {
		if runs.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-readyFirst:
			case <-stop:
				return nil
			}
		}
		close(ready)
		<-stop
		return nil
	}
	type startResult struct {
		handle *Handle
		err    error
	}
	firstResult := make(chan startResult, 1)
	go func() {
		h, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090)
		firstResult <- startResult{h, err}
	}()
	select {
	case <-firstStarted:
	case got := <-firstResult:
		t.Fatalf("first Start did not reach its runner: %v", got.err)
	case <-time.After(2 * time.Second):
		t.Fatal("first Start did not reach its runner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	second, err := m.Start(OwnerPrometheus, ctx, "monitoring", "prometheus", 9090)
	if err != nil || runs.Load() != 2 {
		t.Fatalf("second Start failed to establish independently while first was pending: %v (runners: %d)", err, runs.Load())
	}
	close(readyFirst)
	got := <-firstResult
	if got.err != nil || !got.handle.Alive() {
		t.Fatalf("first Start failed after its runner was released: %v", got.err)
	}
	first := got.handle
	first.Stop()
	if first.Stop() || !second.Info().Connected {
		t.Fatal("first handle could stop a tunnel again, or stopped the other tunnel")
	}
	if !second.Stop() || second.Info().Connected {
		t.Fatal("second handle did not stop its tunnel")
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("first tunnel survived Stop")
	}
}

func TestSameTargetStartsIndependentTunnels(t *testing.T) {
	serverDone := make(chan struct{}, 2)
	cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use SPDY", http.StatusBadRequest)
			return
		}
		defer func() { serverDone <- struct{}{} }()
		if _, err := httpstream.Handshake(r, w, []string{"portforward.k8s.io"}); err != nil {
			return
		}
		conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(stream httpstream.Stream, replied <-chan struct{}) error {
			if stream.Headers().Get(corev1.StreamType) == corev1.StreamTypeData {
				go func() {
					<-replied
					defer stream.Close()
					io.WriteString(stream, "serving")
				}()
			}
			return nil
		})
		if conn != nil {
			<-conn.CloseChan()
		}
	})
	m := NewManager(kubernetes.NewForConfigOrDie(cfg), cfg, "ctx")
	defer m.Close()
	owner, err := m.Start(OwnerTraffic, t.Context(), "ns", "svc", 9090)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Start(OwnerTraffic, t.Context(), "ns", "svc", 9090)
	if err != nil {
		t.Fatal(err)
	}
	if second.Info().LocalPort == owner.Info().LocalPort {
		t.Fatal("second Start reused the first tunnel's local port")
	}
	defer second.Stop()
	owner.Stop()
	if !second.Alive() {
		t.Fatal("second handle died when the first handle stopped")
	}
	select {
	case <-owner.Done():
	default:
		t.Fatal("first tunnel survived Stop")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("stopping the first tunnel did not close its server connection")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(second.Info().LocalPort))
	client, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(time.Second))
	var got [len("serving")]byte
	if _, err := io.ReadFull(client, got[:]); err != nil || string(got[:]) != "serving" {
		t.Fatalf("read forwarded data = %q, %v", got, err)
	}
	if !second.Stop() {
		t.Fatal("second handle did not stop its tunnel")
	}
	select {
	case <-second.Done():
	default:
		t.Fatal("second tunnel survived Stop")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("stopping the second tunnel did not close its server connection")
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
			m := NewManager(kubernetes.NewForConfigOrDie(cfg), cfg, "ctx")
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
				_, err := m.Start(OwnerTraffic, ctx, "ns", "svc", 8080)
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
