package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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
	f := &forward{manager: m, owner: owner, owners: 1, active: true, stopCh: make(chan struct{}),
		info: ConnectionInfo{Connected: true, Address: fmt.Sprintf("http://localhost:%d", port), LocalPort: port,
			Namespace: namespace, ServiceName: service, ContextName: m.contextName}}
	m.forwards[f] = struct{}{}
	return &Handle{f: f}
}

func prometheusManager() *Manager {
	labels := map[string]string{"app": "prometheus"}
	return NewManager(fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "monitoring"}, Spec: corev1.ServiceSpec{Selector: labels}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-0", Namespace: "monitoring", Labels: labels}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	), &rest.Config{}, "ctxA")
}

// TestOwnerScoping verifies that each owner's forward is independent: stopping
// one owner never tears down another's, and status/reuse lookups are scoped
// correctly. This is the invariant that stops prometheus discovery and the
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

	peer := m.Share(OwnerPrometheus, "caretta", "caretta-vm", 0)
	if peer.Address() == "" || !peer.SameForward(traffic) || !peer.Stop() || !traffic.Info().Connected {
		t.Fatal("sharing or releasing a peer's tunnel disturbed its owner")
	}
	other := NewManager(nil, nil, "ctxB")
	defer other.Close()
	if got := other.Share(OwnerPrometheus, "caretta", "caretta-vm", 0); got != nil {
		t.Fatalf("a second connection shared the first connection's tunnel: %s", got.Address())
	}
}

func TestShareIsServiceAware(t *testing.T) {
	m := NewManager(nil, nil, "ctxA")
	defer m.Close()

	// Only the general-metrics forward is up (owner=prometheus → prometheus-operated).
	publishedForward(m, OwnerPrometheus, "monitoring", "prometheus-operated", 15329)

	// A Caretta lookup for its own backend must reject the mismatched service
	// rather than adopt the general Prometheus forward that happens to be live.
	if got := m.Share(OwnerTraffic, "caretta", "caretta-vm", 0).Address(); got != "" {
		t.Fatalf("target-aware lookup adopted the wrong forward: got %q, want empty", got)
	}
	// Conversely, resource metrics may reuse the prometheus-operated forward by
	// exact match — that is the legitimate cross/own-owner reuse.
	metrics := m.Share(OwnerPrometheus, "monitoring", "prometheus-operated", 0)
	if got := metrics.Address(); got != "http://localhost:15329" {
		t.Fatalf("matching own forward: got %q, want :15329", got)
	}
	metrics.Stop()

	// Once Caretta's own forward exists, it is reused by exact match.
	traffic := publishedForward(m, OwnerTraffic, "caretta", "caretta-vm", 22222)
	own := m.Share(OwnerTraffic, "caretta", "caretta-vm", 0)
	if got := own.Address(); got != "http://localhost:22222" {
		t.Fatalf("own matching forward: got %q, want :22222", got)
	}
	own.Stop()
	// A peer forward that DOES target the requested service is still reusable.
	traffic.Stop()
	publishedForward(m, OwnerPrometheus, "caretta", "caretta-vm", 33333)
	peer := m.Share(OwnerTraffic, "caretta", "caretta-vm", 0)
	if got := peer.Address(); got != "http://localhost:33333" {
		t.Fatalf("matching peer forward: got %q, want :33333", got)
	}
	peer.Stop()
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
	if m.Share(OwnerPrometheus, "monitoring", "prometheus", 9090) != nil {
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

func TestForwardReuseIncludesTargetPort(t *testing.T) {
	forward := &forward{info: ConnectionInfo{Namespace: "kubecost", ServiceName: "kubecost-aggregator", ContextName: "ctxA"}, targetPort: 9004}
	if !forward.matches("kubecost", "kubecost-aggregator", 9004) {
		t.Fatal("matching target was not reusable")
	}
	if forward.matches("kubecost", "kubecost-aggregator", 9008) {
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
		info, err := m.Start(OwnerPrometheus, context.Background(), "monitoring", "prometheus", 9090)
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
		h, err := m.Start(OwnerPrometheus, t.Context(), "monitoring", "prometheus", 9090)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first, second := start(), start()
	if !first.SameForward(second) {
		t.Fatal("identical starts did not reuse the tunnel")
	}
	shared := m.Share(OwnerTraffic, "monitoring", "prometheus", 9090)
	if next := second.Replace(shared); next != shared || second.Alive() || !next.SameForward(first) {
		t.Fatal("replacement did not transfer ownership to the shared handle")
	}
	first.Stop()
	if first.Stop() || !shared.Info().Connected {
		t.Fatal("one release closed a shared tunnel")
	}
	shared.Stop()
	if shared.Info().Connected {
		t.Fatal("released handle is still connected")
	}
}

func TestShareKeepsTunnelServingUntilLastStop(t *testing.T) {
	serverDone := make(chan struct{})
	cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use SPDY", http.StatusBadRequest)
			return
		}
		defer close(serverDone)
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
	shared := m.Share(OwnerPrometheus, "ns", "svc", 9090)
	defer shared.Stop()
	owner.Stop()
	if !shared.Alive() {
		t.Fatal("shared handle died when the original owner stopped")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(shared.Info().LocalPort))
	client, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("dial shared tunnel: %v", err)
	}
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(time.Second))
	var got [len("serving")]byte
	if _, err := io.ReadFull(client, got[:]); err != nil || string(got[:]) != "serving" {
		t.Fatalf("read forwarded data = %q, %v", got, err)
	}
	if !shared.Stop() {
		t.Fatal("shared handle did not release its reference")
	}
	select {
	case <-owner.Done():
	default:
		t.Fatal("tunnel survived its last holder")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("last release did not close the forwarded connection")
	}
}

func TestShareRequiresExactPort(t *testing.T) {
	m := prometheusManager()
	defer m.Close()
	m.run = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _ string, _, _ int, stop, ready chan struct{}) error {
		close(ready)
		<-stop
		return nil
	}
	if _, err := m.Start(OwnerTraffic, t.Context(), "monitoring", "prometheus", 9090); err != nil {
		t.Fatal(err)
	}
	if got := m.Share(OwnerPrometheus, "monitoring", "prometheus", 9091); got != nil {
		t.Fatalf("Share adopted a tunnel for the wrong target port: %s", got.Address())
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
