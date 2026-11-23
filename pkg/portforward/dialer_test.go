package portforward

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
)

func TestWebSocketPortForwardNegotiatesOnAuthenticatedConnection(t *testing.T) {
	const protocol = "SPDY/3.1+portforward.k8s.io"
	upgrader := websocket.Upgrader{Subprotocols: []string{protocol}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("upgrade method=%s, authorization=%q", r.Method, r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", 401)
			return
		}
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.Close()
		for {
			if _, _, err := socket.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := &rest.Config{Host: server.URL, BearerToken: "test-token", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	dialer, err := NewDialerWithContext(t.Context(), config, target)
	if err != nil {
		t.Fatal(err)
	}
	conn, selected, err := dialer.Dial("portforward.k8s.io")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if selected != "portforward.k8s.io" {
		t.Fatalf("selected protocol %q", selected)
	}
}

func TestCancelWebSocketProxyHandshake(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer proxy.Close()
	defer close(release)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	target := &url.URL{Scheme: "https", Host: "cluster.invalid"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dialer, err := NewDialerWithContext(ctx, &rest.Config{Host: target.String(), Proxy: http.ProxyURL(proxyURL)}, target)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := dialer.Dial("portforward.k8s.io"); done <- err }()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("proxy never received CONNECT: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled CONNECT succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled CONNECT survived cancellation")
	}
}
