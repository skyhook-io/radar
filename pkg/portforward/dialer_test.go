package portforward

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
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

func TestCancelSPDYFallbackClosesHandshakeSocket(t *testing.T) {
	for _, phase := range []string{"TLS", "CONNECT"} {
		t.Run(phase, func(t *testing.T) {
			peer, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			target := &url.URL{Scheme: "https", Host: peer.Addr().String()}
			cfg := &rest.Config{WrapTransport: func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodGet {
						return nil, websocket.ErrBadHandshake
					}
					return next.RoundTrip(req)
				})
			}}
			if phase == "CONNECT" {
				target.Host = "cluster.invalid"
				cfg.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: peer.Addr().String()})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dialer, err := NewDialerWithContext(ctx, cfg, target)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, _, err := dialer.Dial("portforward.k8s.io"); done <- err }()

			peer.SetDeadline(time.Now().Add(5 * time.Second))
			conn, err := peer.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			request := bufio.NewReader(conn)
			if phase == "CONNECT" {
				req, err := http.ReadRequest(request)
				if err != nil || req.Method != http.MethodConnect {
					t.Fatalf("expected proxy CONNECT, got %v, %v", req, err)
				}
			} else if record, err := request.ReadByte(); err != nil || record != 0x16 {
				t.Fatalf("expected TLS handshake record, got %#x, %v", record, err)
			}

			cancel()
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := io.Copy(io.Discard, request); err != nil {
				t.Fatalf("cancelled %s handshake did not close peer socket: %v", phase, err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled upgrade succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled upgrade did not return")
			}
		})
	}
}

func TestEstablishedSPDYTunnelOutlivesUpgradeContext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := &rest.Config{TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	dialer, err := NewDialerWithContext(ctx, cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := dialer.Dial("portforward.k8s.io")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	stream, err := conn.CreateStream(http.Header{})
	if err != nil {
		t.Fatalf("open stream after upgrade context was cancelled: %v", err)
	}
	stream.Close()
}
