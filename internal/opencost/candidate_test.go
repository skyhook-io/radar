package opencost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/internal/portforward/forwardtest"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

func TestKubecostCandidateRequiresClusterData(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"empty allocation", `{"code":200,"data":[{}]}`, 200, true},
		{"cluster allocation", `{"code":200,"data":[{"test":{"properties":{"cluster":"test"},"totalCost":1}}]}`, 200, false},
		{"unauthorized", `{"error":"unauthorized"}`, 401, true},
		{"malformed", `not-json`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-API-KEY") != "candidate-key" {
					t.Error("candidate key missing")
				}
				if !strings.Contains(r.URL.Query().Get("filter"), "cluster") {
					t.Error("candidate probe was not cluster filtered")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			probe, err := PrepareCandidateFor(k8s.Current(), k8s.IsInCluster(), ManagerConfig{Source: SourceKubecost, URL: server.URL, APIKey: "candidate-key", ClusterID: "test"})
			if err == nil {
				err = probe(context.Background())
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("probe error = %v", err)
			}
			if tc.status == 401 && !errors.Is(err, ErrKubecostAuthentication) {
				t.Fatalf("authentication failure lost: %v", err)
			}
		})
	}
}

func TestFailedKubecostProbeAfterSwitchKeepsReplacementForward(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	probing, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()

	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "aggregator", Namespace: "cost"}}
	endpoint := kubecostAggregatorEndpoint{targetPort: 9004, servicePort: 9004}
	install := func(name string, answer func() string) (kubecostTarget, <-chan struct{}) {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v1/namespaces/cost/services/aggregator", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"kind":"Service","apiVersion":"v1","metadata":{"name":"aggregator","namespace":"cost"},"spec":{"selector":{"app":"aggregator"}}}`)
		})
		mux.HandleFunc("GET /api/v1/namespaces/cost/pods", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"aggregator-0"},"status":{"phase":"Running"}}]}`)
		})
		ended := make(chan struct{})
		signalEnd := sync.OnceFunc(func() { close(ended) })
		tunnel := kubecostTestTunnel(answer)
		mux.HandleFunc("POST /api/v1/namespaces/cost/pods/aggregator-0/portforward", func(w http.ResponseWriter, r *http.Request) {
			defer signalEnd()
			tunnel(w, r)
		})
		api := httptest.NewServer(mux)
		t.Cleanup(api.Close)
		cfg := &rest.Config{Host: api.URL}
		if _, err := k8s.InstallTestServingConnection(name, cfg, nil, fake.NewSimpleClientset().Discovery()); err != nil {
			t.Fatal(err)
		}
		k8s.SetTestClient(kubernetes.NewForConfigOrDie(cfg))
		target := captureKubecostTarget()
		t.Cleanup(target.forwards.Close)
		return target, ended
	}

	a, aEnded := install("a", func() string {
		close(probing)
		<-release
		return `{"code":200,"data":[{}]}`
	})
	result := make(chan error, 1)
	go func() {
		_, err := connectDiscoveredKubecostEndpoint(t.Context(), "", "a", service, endpoint, portforward.OwnerCost, a)
		result <- err
	}()
	select {
	case <-probing:
	case err := <-result:
		t.Fatalf("A never reached its forwarded probe: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("A did not reach its forwarded probe")
	}
	b, _ := install("b", func() string { return `{"code":200,"data":[{"b":{"properties":{"cluster":"b"},"totalCost":1}}]}` })
	probeB := func() Connection {
		t.Helper()
		c, err := connectDiscoveredKubecostEndpoint(t.Context(), "", "b", service, endpoint, portforward.OwnerCost, b)
		if err != nil {
			t.Fatalf("probe B: %v", err)
		}
		return c
	}
	bConn := probeB()
	k8s.RetireServingConnection()
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "b"})
	if !bConn.lease.alive() || k8s.Current().PortForwards() != b.forwards {
		t.Fatal("readmission discarded B's live tunnel")
	}
	resume()
	if err := <-result; !errors.Is(err, ErrKubecostNoData) {
		t.Fatalf("A returned %v, want the released probe’s empty allocation", err)
	}
	select {
	case <-aEnded:
	case <-time.After(time.Second):
		t.Fatal("A's forward stayed open after its probe failed")
	}
	if !bConn.lease.alive() {
		t.Fatal("A's failed probe stopped B's forward")
	}
	if _, _, err := probeDiscoveredKubecostURL(t.Context(), bConn.Address, "", bConn.ClusterID); err != nil {
		t.Fatalf("B's original tunnel stopped serving after A failed: %v", err)
	}
	k8s.ResetAllSubsystems()
	if bConn.lease.alive() {
		t.Fatal("connection teardown left B's tunnel open")
	}
}

func TestKubecostReselectionReplacesRejectedTunnel(t *testing.T) {
	const allocation = `{"code":200,"data":[{"test":{"properties":{"cluster":"test"},"totalCost":1}}]}`
	var upgrades atomic.Int32
	var rejectFirst atomic.Bool
	first := kubecostTestTunnel(func() string {
		if rejectFirst.Load() {
			return `{"code":200,"data":[{}]}`
		}
		return allocation
	})
	replacement := kubecostTestTunnel(func() string { return allocation })
	cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && upgrades.Add(1) == 1 {
			first(w, r)
			return
		}
		replacement(w, r)
	})
	installLocalKubecostTestConnection(t, cfg)
	client := prometheuspkg.NewClient(fake.NewSimpleClientset(), cfg, "ctx", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })

	m := &Manager{config: ManagerConfig{Source: SourceAuto, ClusterID: "test", ClusterIDContext: "ctx"}}
	t.Cleanup(m.Reset)
	old, err := m.connectKubecost(t.Context(), m.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.commitProvisionalSelection(m.generation, old); err != nil {
		t.Fatal(err)
	}
	m.retryAt = time.Now().Add(-time.Second)
	rejectFirst.Store(true)
	if _, _, err := probeDiscoveredKubecostURL(t.Context(), old.Address, "", old.ClusterID); !errors.Is(err, ErrKubecostNoData) || !old.lease.alive() {
		t.Fatalf("expected a rejected probe over the still-open tunnel, got %v", err)
	}
	next, err := m.Selected(t.Context())
	if err != nil || next.Source != SourceKubecost {
		t.Fatalf("Kubecost re-selection failed: %v (tunnels opened: %d)", err, upgrades.Load())
	}
	if upgrades.Load() != 2 || next.Address == old.Address || !next.lease.alive() || old.lease.alive() {
		t.Fatalf("re-selection did not replace the rejected tunnel: old=%s new=%s tunnels opened=%d", old.Address, next.Address, upgrades.Load())
	}
}

func installLocalKubecostTestConnection(t *testing.T, cfg *rest.Config) *portforward.Manager {
	t.Helper()
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	if _, err := k8s.InstallTestServingConnection("ctx", cfg, nil, fake.NewSimpleClientset().Discovery()); err != nil {
		t.Fatal(err)
	}
	k8s.SetTestClient(kubernetes.NewForConfigOrDie(cfg))
	labels := map[string]string{"app": "aggregator"}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "tcp-api", Port: 9004}},
		},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "aggregator", Namespace: "ns", Labels: labels},
		Spec:       appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewSimpleClientset(service, statefulSet), map[string]k8score.ResourceScope{
		string(k8score.Services):     {Enabled: true},
		string(k8score.StatefulSets): {Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	forwards := k8s.Current().PortForwards()
	t.Cleanup(forwards.Close)
	return forwards
}

func kubecostTestTunnel(answer func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use SPDY", http.StatusBadRequest)
			return
		}
		if _, err := httpstream.Handshake(r, w, []string{"portforward.k8s.io"}); err != nil {
			return
		}
		conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(stream httpstream.Stream, replied <-chan struct{}) error {
			go func() {
				<-replied
				defer stream.Close()
				if stream.Headers().Get(corev1.StreamType) != corev1.StreamTypeData {
					return
				}
				if _, err := http.ReadRequest(bufio.NewReader(stream)); err != nil {
					return
				}
				body := answer()
				fmt.Fprintf(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}()
			return nil
		})
		if conn != nil {
			<-conn.CloseChan()
		}
	}
}
