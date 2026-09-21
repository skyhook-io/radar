package runtimeevidence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/portforward"
)

func testPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "rabbit", Namespace: "lab", UID: "first"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rabbit", Image: "rabbitmq:4-management", Ports: []corev1.ContainerPort{{ContainerPort: 15692}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "rabbit", ContainerID: "containerd://one", ImageID: "sha256:one", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}}}}}
}

const alarms = "rabbitmq_alarms_free_disk_space_watermark 0\nrabbitmq_alarms_memory_used_watermark 1\n"

func testCollector(url string, closed *atomic.Bool) *Collector {
	c := NewCollector()
	c.start = func(context.Context, kubernetes.Interface, *rest.Config, string, string, int) (tunnel, error) {
		return tunnel{url, func() { closed.Store(true) }}, nil
	}
	return c
}
func TestHTTPBoundaries(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body, reason string
	}{{"observed", 200, alarms, ""}, {"denied", 403, "secret", "endpoint_denied"}, {"redirect", 302, alarms, "redirect_refused"}, {"error", 500, alarms, "unexpected_http_status"}, {"oversize", 200, strings.Repeat("x", evidence.MaxBodyBytes+1), "response_too_large"}, {"malformed", 200, "secret", "invalid_alarm_metric"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path != "/metrics" {
					t.Error("wrong path")
				}
				w.Header().Set("Location", "/other")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var closed atomic.Bool
			c := testCollector(server.URL, &closed)
			res := c.Collect(context.Background(), fake.NewClientset(testPod()), &rest.Config{}, evidence.RabbitMQ, "lab", "rabbit")
			if res.Reason != tc.reason {
				t.Fatalf("got %+v", res)
			}
			if (res.Facts != nil) != (tc.reason == "") {
				t.Fatal("facts on unavailable or missing on observed")
			}
			if !closed.Load() || hits.Load() != 1 {
				t.Fatal("tunnel cleanup or redirect boundary failed")
			}
		})
	}
}
func TestTargetRecheck(t *testing.T) {
	for _, mutation := range []string{"uid", "restart", "image", "imageID", "containerID", "stopped", "hostNetwork"} {
		t.Run(mutation, func(t *testing.T) {
			p := testPod()
			client := fake.NewClientset(p)
			calls := 0
			client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				calls++
				next := p.DeepCopy()
				if calls > 1 {
					switch mutation {
					case "uid":
						next.UID = "second"
					case "restart":
						next.Status.ContainerStatuses[0].RestartCount++
					case "image":
						next.Spec.Containers[0].Image = "rabbitmq:other"
					case "imageID":
						next.Status.ContainerStatuses[0].ImageID = "other"
					case "containerID":
						next.Status.ContainerStatuses[0].ContainerID = "other"
					case "stopped":
						next.Status.ContainerStatuses[0].State.Running = nil
					case "hostNetwork":
						next.Spec.HostNetwork = true
					}
				}
				return true, next, nil
			})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(alarms)) }))
			defer srv.Close()
			var closed atomic.Bool
			res := testCollector(srv.URL, &closed).Collect(context.Background(), client, &rest.Config{}, evidence.RabbitMQ, "lab", "rabbit")
			if res.Reason != "target_changed" || res.Facts != nil {
				t.Fatalf("got %+v", res)
			}
		})
	}
}
func TestTargetEligibility(t *testing.T) {
	ep, _ := endpointFor(evidence.RabbitMQ)
	for _, tc := range []struct {
		name, want string
		change     func(*corev1.Pod)
	}{{"hostNetwork", "host_network_unsupported", func(p *corev1.Pod) { p.Spec.HostNetwork = true }}, {"undeclared", "endpoint_port_not_declared", func(p *corev1.Pod) { p.Spec.Containers[0].Ports = nil }}, {"lookalike", "unsupported_image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "attacker.example/rabbitmq:4" }}, {"duplicate", "ambiguous_target", func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, p.Spec.Containers[0]) }}, {"sidecarPort", "ambiguous_target", func(p *corev1.Pod) {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Image: "other", Ports: []corev1.ContainerPort{{ContainerPort: 15692}}})
	}}, {"notStarted", "container_not_running", func(p *corev1.Pod) { p.Status.ContainerStatuses = nil }}} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPod()
			tc.change(p)
			_, reason := identify(p, ep)
			if reason != tc.want {
				t.Fatalf("got %s", reason)
			}
		})
	}
}
func TestCancellationAndBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	var closed atomic.Bool
	c := testCollector(srv.URL, &closed)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res := c.Collect(ctx, fake.NewClientset(testPod()), &rest.Config{}, evidence.RabbitMQ, "lab", "rabbit")
	if res.Reason != "collection_cancelled" || !closed.Load() {
		t.Fatalf("got %+v closed %v", res, closed.Load())
	}
	for i := 0; i < cap(c.slots); i++ {
		c.slots <- struct{}{}
	}
	res = c.Collect(context.Background(), fake.NewClientset(testPod()), &rest.Config{}, evidence.RabbitMQ, "lab", "rabbit")
	if res.Reason != "collection_busy" {
		t.Fatalf("got %+v", res)
	}
}

func TestKubernetesTransportCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: contextTransport{ctx: ctx, base: http.DefaultTransport}}
	done := make(chan error, 1)
	go func() {
		resp, err := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("transport did not cancel")
	}
}

func TestUnsupportedAndNilTargetsDoNotStartTunnel(t *testing.T) {
	var typedNil *fake.Clientset
	c := NewCollector()
	c.start = func(context.Context, kubernetes.Interface, *rest.Config, string, string, int) (tunnel, error) {
		t.Fatal("unexpected tunnel")
		return tunnel{}, nil
	}
	for _, tc := range []struct {
		client          kubernetes.Interface
		adapter         evidence.Adapter
		namespace, want string
	}{{typedNil, evidence.RabbitMQ, "lab", "cluster_unavailable"}, {fake.NewClientset(), evidence.Adapter("other"), "lab", "unsupported_adapter"}, {fake.NewClientset(), evidence.RabbitMQ, "../lab", "invalid_target"}} {
		result := c.Collect(context.Background(), tc.client, &rest.Config{}, tc.adapter, tc.namespace, "rabbit")
		if result.Reason != tc.want {
			t.Fatalf("got %+v", result)
		}
	}
}

type delayedForwarder struct {
	stop      <-chan struct{}
	ready     chan struct{}
	releasing chan struct{}
	released  chan struct{}
}

func (f *delayedForwarder) ForwardPorts() error {
	close(f.ready)
	<-f.stop
	close(f.releasing)
	<-f.released
	return nil
}
func (f *delayedForwarder) GetPorts() ([]portforward.ForwardedPort, error) {
	return []portforward.ForwardedPort{{Local: 12345, Remote: 15692}}, nil
}
func TestTunnelCloseWaitsForResourceRelease(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		stop, ready := make(chan struct{}), make(chan struct{})
		f := &delayedForwarder{stop: stop, ready: ready, releasing: make(chan struct{}), released: make(chan struct{})}
		tunnel, err := runTunnel(ctx, f, stop, ready)
		if err != nil {
			t.Fatal(err)
		}
		if cancelled {
			cancel()
		}
		done := make(chan struct{})
		go func() { tunnel.close(); close(done) }()
		<-f.releasing
		select {
		case <-done:
			t.Fatal("cleanup returned before resources released")
		default:
		}
		close(f.released)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cleanup did not finish")
		}
		tunnel.close()
		cancel()
	}
}

func TestVaultPlaintextAgainstTLSIsUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("plaintext request reached TLS application handler")
	}))
	defer server.Close()
	var closed atomic.Bool
	c := testCollector(strings.Replace(server.URL, "https://", "http://", 1), &closed)
	pod := testPod()
	pod.Spec.Containers[0].Image = "hashicorp/vault:1.20.4"
	pod.Spec.Containers[0].Ports[0].ContainerPort = 8200
	result := c.Collect(context.Background(), fake.NewClientset(pod), &rest.Config{}, evidence.Vault, "lab", "rabbit")
	if result.Outcome != "unavailable" || result.Facts != nil || result.Reason != "endpoint_response_unavailable" || result.HTTPStatus != 400 {
		t.Fatalf("TLS mismatch fabricated facts or wrong reason: %+v", result)
	}
	if !closed.Load() {
		t.Fatal("tunnel not closed")
	}
}
