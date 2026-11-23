// Package forwardtest serves a single Service and Pod for port-forward lifecycle tests.
package forwardtest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
)

func Server(t *testing.T, upgrade http.HandlerFunc) *rest.Config {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/namespaces/ns/services/svc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"kind":"Service","apiVersion":"v1","spec":{"selector":{"app":"svc"}}}`))
	})
	mux.HandleFunc("GET /api/v1/namespaces/ns/pods", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"pod"},"status":{"phase":"Running"}}]}`))
	})
	mux.HandleFunc("/api/v1/namespaces/ns/pods/pod/portforward", upgrade)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &rest.Config{Host: server.URL}
}

func ServeTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use SPDY", http.StatusBadRequest)
		return
	}
	if _, err := httpstream.Handshake(r, w, []string{"portforward.k8s.io"}); err != nil {
		return
	}
	conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(httpstream.Stream, <-chan struct{}) error { return nil })
	if conn != nil {
		<-conn.CloseChan()
	}
}
