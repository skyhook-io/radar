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
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
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
			probe, err := PrepareCandidate(ManagerConfig{Source: SourceKubecost, URL: server.URL, APIKey: "candidate-key", ClusterID: "test"})
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
	install := func(name string, answer func() string) kubecostTarget {
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
		mux.HandleFunc("POST /api/v1/namespaces/cost/pods/aggregator-0/portforward", func(w http.ResponseWriter, r *http.Request) {
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
		return target
	}

	a := install("a", func() string {
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
	b := install("b", func() string { return `{"code":200,"data":[{"b":{"properties":{"cluster":"b"},"totalCost":1}}]}` })
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
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "b"})
	if !bConn.lease.alive() || k8s.Current().PortForwards() != b.forwards {
		t.Fatal("readmission discarded B's live tunnel")
	}
	resume()
	if err := <-result; !errors.Is(err, ErrKubecostNoData) {
		t.Fatalf("A returned %v, want the released probe’s empty allocation", err)
	}
	if a.forwards.IsConnectedForContext("a") || !bConn.lease.alive() {
		t.Fatal("failed probe did not stop exactly A's forward")
	}
	stillServing := probeB()
	if stillServing.Address != bConn.Address {
		t.Fatal("B had to reopen its tunnel after A failed")
	}
	stillServing.lease.stop()
	k8s.ResetAllSubsystems()
	if bConn.lease.alive() {
		t.Fatal("connection teardown left B's tunnel open")
	}
}
