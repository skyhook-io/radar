package portforward

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	clientportforward "k8s.io/client-go/tools/portforward"
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

func TestWebSocketStatusSurvivesRefusedSPDYFallback(t *testing.T) {
	for _, code := range []int{401, 403, 404} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var post atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					post.Store(true)
					http.Error(w, "SPDY upgrade refused", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":%d,"message":"port-forward authorization refused"}`, code)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			dialer, err := NewDialerWithContext(t.Context(), &rest.Config{}, target)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = dialer.Dial("portforward.k8s.io")
			if !post.Load() {
				t.Fatal("WebSocket refusal did not fall back to SPDY")
			}
			var status *apierrors.StatusError
			if !errors.As(err, &status) || status.Status().Code != int32(code) || !strings.Contains(err.Error(), "port-forward authorization refused") {
				t.Fatalf("final error lost Kubernetes Status: %v", err)
			}
		})
	}
}

func TestWebSocketRetryUsesRoundTripContext(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	upgrader := websocket.Upgrader{Subprotocols: []string{"SPDY/3.1+portforward.k8s.io"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			close(arrived)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		tunnel := clientportforward.NewTunnelingConnection("test-server", conn)
		peer, err := spdy.NewServerConnection(tunnel, func(httpstream.Stream, <-chan struct{}) error { return nil })
		if err != nil {
			t.Error(err)
			return
		}
		<-peer.CloseChan()
	}))
	defer server.Close()
	defer close(release)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	first, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	cfg := &rest.Config{WrapTransport: func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(req.Clone(first))
			if err == nil {
				resp.Body.Close()
				return nil, errors.New("stalled first RoundTrip unexpectedly succeeded")
			}
			retry, cancelRetry := context.WithCancel(req.Context())
			defer cancelRetry()
			return next.RoundTrip(req.Clone(retry))
		})
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dialer, err := NewDialerWithContext(ctx, cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, _, err := dialer.Dial("portforward.k8s.io")
		if conn != nil {
			defer conn.Close()
			stream, streamErr := conn.CreateStream(http.Header{"Test": {"retry"}})
			if stream != nil {
				stream.Close()
			}
			err = streamErr
		}
		done <- err
	}()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("first RoundTrip did not reach the server: %v", err)
	}
	cancelFirst()
	select {
	case err := <-done:
		if err != nil || attempts.Load() != 2 {
			t.Fatalf("retry failed after cancelling first RoundTrip: attempts=%d, error=%v", attempts.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled first RoundTrip blocked the retry")
	}
}

func TestSPDYDefaultProxyHonorsNoProxyCIDR(t *testing.T) {
	const childEnv = "RADAR_TEST_SPDY_NO_PROXY_CIDR"
	if os.Getenv(childEnv) == "" {
		// net/http caches its first environment lookup for the process.
		t.Setenv(childEnv, "1")
		t.Setenv("HTTP_PROXY", "http://proxy.invalid:8080")
		t.Setenv("NO_PROXY", "192.000.002.000/24")
		cmd := exec.Command(os.Args[0], "-test.run=^TestSPDYDefaultProxyHonorsNoProxyCIDR$", "-test.v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("CIDR proxy subprocess: %v\n%s", err, out)
		}
		return
	}

	target := &url.URL{Scheme: "http", Host: "192.0.2.12:80"}
	cfg := &rest.Config{
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			return nil, fmt.Errorf("dialed %s", addr)
		},
		WrapTransport: func(next http.RoundTripper) http.RoundTripper {
			return roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet {
					return nil, websocket.ErrBadHandshake
				}
				return next.RoundTrip(req)
			})
		},
	}
	dialer, err := NewDialerWithContext(t.Context(), cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dialer.Dial("portforward.k8s.io")
	if err == nil || !strings.Contains(err.Error(), "dialed 192.0.2.12:80") {
		t.Fatalf("SPDY failed to bypass proxy for NO_PROXY CIDR: %v", err)
	}
}

func TestSPDYFallbackConnectsThroughPlaintextTargetProxy(t *testing.T) {
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			http.Error(w, "WebSocket upgrade rejected", http.StatusBadRequest)
			return
		}
		posts.Add(1)
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
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != target.Host {
			http.Error(w, "proxy accepts only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		upstream, err := net.DialTimeout("tcp", target.Host, time.Second)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Close()
		rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := rw.Flush(); err != nil {
			t.Error(err)
			return
		}
		done := make(chan struct{})
		go func() {
			io.Copy(upstream, rw)
			upstream.(*net.TCPConn).CloseWrite()
			close(done)
		}()
		io.Copy(client, upstream)
		client.Close()
		<-done
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dialer, err := NewDialerWithContext(ctx, &rest.Config{Proxy: http.ProxyURL(proxyURL)}, target)
	if err != nil {
		t.Fatal(err)
	}
	conn, protocol, err := dialer.Dial("portforward.k8s.io")
	if err != nil {
		t.Fatalf("SPDY fallback through CONNECT-only proxy: %v", err)
	}
	defer conn.Close()
	if protocol != "portforward.k8s.io" || connects.Load() != 2 || gets.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("protocol=%q, CONNECT=%d, tunneled GET=%d POST=%d", protocol, connects.Load(), gets.Load(), posts.Load())
	}
}

func TestSPDYPingUsesClientGoInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		peerErr := make(chan error, 1)
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			if _, err := http.ReadRequest(reader); err != nil {
				peerErr <- err
				return
			}
			if _, err := io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n"); err != nil {
				peerErr <- err
				return
			}
			var frame [12]byte
			server.SetReadDeadline(time.Now().Add(11 * time.Second))
			if _, err := io.ReadFull(reader, frame[:]); err != nil {
				peerErr <- err
				return
			}
			if header := [8]byte(frame[:8]); header != [8]byte{0x80, 3, 0, 6, 0, 0, 0, 4} {
				peerErr <- fmt.Errorf("first SPDY frame was not a PING: %x", frame)
				return
			}
			peerErr <- nil
		}()
		target := &url.URL{Scheme: "http", Host: "localhost"}
		cfg := &rest.Config{Dial: func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		}}
		attempt := upgradeAttempt{ctx: t.Context(), target: target, cfg: cfg, proxy: http.ProxyFromEnvironment}
		attempt.raw = roundTripFunc(attempt.roundTripSPDY)
		start := time.Now()
		conn, _, err := attempt.spdy("portforward.k8s.io")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := <-peerErr; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Fatalf("first SPDY ping after %s, want client-go's 5s", elapsed)
		}
	})
}
