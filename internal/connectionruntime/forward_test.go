package connectionruntime

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
)

func TestMetricsEditStopsOnlyTheDisplacedForward(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	started := make(chan struct{})
	tunnelClosed := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"ServiceList","apiVersion":"v1","items":[{"metadata":{"name":"prometheus-server","namespace":"monitoring"},"spec":{"selector":{"app":"prometheus"},"ports":[{"name":"http","port":9090,"targetPort":9090}]}}]}`)
	})
	mux.HandleFunc("GET /api/v1/namespaces/monitoring/services/prometheus-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"Service","apiVersion":"v1","spec":{"selector":{"app":"prometheus"}}}`)
	})
	mux.HandleFunc("GET /api/v1/namespaces/monitoring/pods", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"pod"},"status":{"phase":"Running"}}]}`)
	})
	mux.HandleFunc("POST /api/v1/namespaces/monitoring/pods/pod/portforward", func(w http.ResponseWriter, r *http.Request) {
		if _, err := httpstream.Handshake(r, w, []string{"portforward.k8s.io"}); err != nil {
			return
		}
		conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(stream httpstream.Stream, replied <-chan struct{}) error {
			if stream.Headers().Get(corev1.StreamType) != corev1.StreamTypeData {
				return nil
			}
			go func() {
				<-replied
				defer stream.Close()
				request, err := http.ReadRequest(bufio.NewReader(stream))
				if err != nil {
					return
				}
				defer request.Body.Close()
				if request.URL.Query().Get("query") == "blocked" {
					close(started)
					_, _ = io.Copy(io.Discard, stream)
					return
				}
				_, _ = fmt.Fprintf(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(metricsAnswer), metricsAnswer)
			}()
			return nil
		})
		if conn != nil {
			<-conn.CloseChan()
			close(tunnelClosed)
		}
	})
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	installTestBuild(t, &rest.Config{Host: api.URL})
	runtime := New(nil, Settings{})
	before := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	forwards := k8s.Current().PortForwards()

	oldResult := make(chan error, 1)
	go func() {
		_, err := before.Metrics.Query(t.Context(), "blocked")
		oldResult <- err
	}()
	await(t, started)

	var newCalls atomic.Int32
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "blocked" {
			t.Error("old query reached the replacement")
		}
		newCalls.Add(1)
		_, _ = io.WriteString(w, metricsAnswer)
	}))
	t.Cleanup(replacement.Close)
	edit := runtime.PrepareShared(k8s.Current(), runtime.Snapshot(), func(settings *Settings) {
		settings.Metrics.URL = replacement.URL
	})
	if err := edit.Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := await(t, oldResult); err == nil {
		t.Fatal("retired forwarded request completed successfully")
	}
	await(t, tunnelClosed)
	if forwards != k8s.Current().PortForwards() || forwards.Closed() {
		t.Fatal("metrics edit closed the connection's shared forward manager")
	}
	if _, err := SetFor(k8s.Current()).Metrics.Query(t.Context(), "new"); err != nil || newCalls.Load() == 0 {
		t.Fatalf("replacement did not serve: %v", err)
	}
}
