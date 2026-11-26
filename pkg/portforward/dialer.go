package portforward

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/http/httpguts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	portconstants "k8s.io/apimachinery/pkg/util/portforward"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

type upgradeDial func(...string) (httpstream.Connection, string, error)

func (dial upgradeDial) Dial(protocols ...string) (httpstream.Connection, string, error) {
	return dial(protocols...)
}

// NewDialerWithContext binds both upgrade requests to the tunnel lifetime. The
// client-go dialers construct their own requests with background contexts.
func NewDialerWithContext(ctx context.Context, cfg *rest.Config, target *url.URL) (httpstream.Dialer, error) {
	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	proxy := cfg.Proxy
	if proxy == nil {
		proxy = utilnet.NewProxierWithNoProxyCIDR(http.ProxyFromEnvironment)
	}
	attempt := upgradeAttempt{ctx: ctx, cfg: cfg, target: target, tls: tlsConfig, proxy: proxy}
	attempt.raw, err = rest.HTTPWrappersForConfig(cfg, roundTripFunc(attempt.roundTripSPDY))
	if err != nil {
		return nil, err
	}
	return upgradeDial(func(protocols ...string) (httpstream.Connection, string, error) {
		conn, protocol, wsErr := attempt.websocket(protocols...)
		var failedUpgrade *streamhttp.UpgradeFailureError
		if ctx.Err() != nil || !(errors.As(wsErr, &failedUpgrade) || errors.Is(wsErr, websocket.ErrBadHandshake) || streamhttp.IsHTTPSProxyError(wsErr)) {
			return conn, protocol, wsErr
		}
		conn, protocol, err := attempt.spdy(protocols...)
		if err != nil && ctx.Err() == nil && failedUpgrade != nil {
			if status, ok := failedUpgrade.Cause.(*apierrors.StatusError); ok {
				err = errors.Join(err, fmt.Errorf("websocket upgrade: %w", status))
			}
		}
		return conn, protocol, err
	}), nil
}

type upgradeAttempt struct {
	ctx    context.Context
	cfg    *rest.Config
	target *url.URL
	tls    *tls.Config
	proxy  func(*http.Request) (*url.URL, error)
	raw    http.RoundTripper
}

func (a upgradeAttempt) websocket(protocols ...string) (httpstream.Connection, string, error) {
	subprotocols := make([]string, 0, len(protocols))
	for _, p := range protocols {
		subprotocols = append(subprotocols, portconstants.WebsocketsSPDYTunnelingPrefix+p)
	}
	var socket *websocket.Conn
	ws, err := rest.HTTPWrappersForConfig(a.cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u := *req.URL
		switch u.Scheme {
		case "https":
			u.Scheme = "wss"
		case "http":
			u.Scheme = "ws"
		default:
			return nil, fmt.Errorf("unknown port-forward URL scheme %q", u.Scheme)
		}
		dialer := websocket.Dialer{
			Proxy: a.proxy, TLSClientConfig: a.tls, Subprotocols: subprotocols,
			ReadBufferSize: 33 * 1024, WriteBufferSize: 33 * 1024,
		}
		dial := a.cfg.Dial
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		// Gorilla reads CONNECT and upgrade responses directly from the socket.
		var detach func() bool
		dialer.NetDialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
			conn, err := dial(dialCtx, network, address)
			if err == nil {
				detach = context.AfterFunc(req.Context(), func() { conn.Close() })
			}
			return conn, err
		}
		conn, resp, err := dialer.DialContext(req.Context(), u.String(), req.Header)
		if detach != nil && !detach() && err == nil {
			conn.Close()
			resp.Body.Close()
			return nil, req.Context().Err()
		}
		if err != nil {
			if resp != nil {
				defer resp.Body.Close()
			}
			if !errors.Is(err, websocket.ErrBadHandshake) {
				return nil, err
			}
			if resp != nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				var status metav1.Status
				switch {
				case json.Unmarshal(body, &status) == nil && (status.Kind == "Status" || status.Status == metav1.StatusFailure) && status.Message != "":
					err = &apierrors.StatusError{ErrStatus: status}
				case len(body) > 0:
					err = fmt.Errorf("%w (%s): %s", err, resp.Status, strings.TrimSpace(string(body)))
				default:
					err = fmt.Errorf("%w (%s)", err, resp.Status)
				}
			}
			return nil, &streamhttp.UpgradeFailureError{Cause: err}
		}
		if !slices.Contains(subprotocols, conn.Subprotocol()) {
			conn.Close()
			resp.Body.Close()
			return nil, &streamhttp.UpgradeFailureError{Cause: fmt.Errorf("unexpected subprotocol %q", conn.Subprotocol())}
		}
		socket = conn
		return resp, nil
	}))
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, a.target.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := ws.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	resp.Body.Close()
	if socket == nil {
		return nil, "", fmt.Errorf("upgrade transport did not provide a websocket")
	}
	tunnel := portforward.NewTunnelingConnection("client", socket)
	conn, err := spdy.NewClientConnectionWithPings(tunnel, portforward.PingPeriod)
	if err != nil {
		tunnel.Close()
	}
	return conn, strings.TrimPrefix(socket.Subprotocol(), portconstants.WebsocketsSPDYTunnelingPrefix), err
}

func (a upgradeAttempt) roundTripSPDY(req *http.Request) (*http.Response, error) {
	// Transport detaches its dial from the request so a later request can reuse it.
	// This upgrade has a single socket that cancellation must reach during TLS and CONNECT.
	setup, cancel := context.WithCancel(req.Context())
	defer cancel()
	dial := a.cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	var detach func() bool
	dialTracked := func(_ context.Context, network, address string) (net.Conn, error) {
		ctx, cancelDial := context.WithTimeout(setup, 30*time.Second)
		defer cancelDial()
		conn, err := dial(ctx, network, address)
		if err == nil {
			detach = context.AfterFunc(setup, func() { conn.Close() })
		}
		return conn, err
	}
	transport := &http.Transport{
		DialContext:         dialTracked,
		TLSClientConfig:     a.tls,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               a.proxy,
		Protocols:           new(http.Protocols),
		DisableKeepAlives:   true,
	}
	transport.Protocols.SetHTTP1(true)
	if req.URL.Scheme == "http" {
		proxyURL, err := a.proxy(req)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		if proxyURL != nil && (proxyURL.Scheme == "http" || proxyURL.Scheme == "https" || proxyURL.Scheme == "") {
			// net/http would send an absolute-form POST through a plaintext proxy route.
			transport.Proxy = nil
			transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
				port := "80"
				if proxyURL.Scheme == "https" {
					port = "443"
				}
				proxyAddr := net.JoinHostPort(proxyURL.Hostname(), cmp.Or(proxyURL.Port(), port))
				conn, err := dialTracked(dialCtx, network, proxyAddr)
				if err != nil {
					return nil, err
				}
				if proxyURL.Scheme == "https" {
					cfg := a.tls.Clone()
					if cfg == nil {
						cfg = &tls.Config{}
					}
					if cfg.ServerName == "" {
						cfg.ServerName = proxyURL.Hostname()
					}
					cfg.NextProtos = []string{"http/1.1"}
					tlsConn := tls.Client(conn, cfg)
					ctx, cancel := context.WithTimeout(setup, transport.TLSHandshakeTimeout)
					defer cancel()
					if err := tlsConn.HandshakeContext(ctx); err != nil {
						return nil, err
					}
					conn = tlsConn
				}
				return connectProxy(conn, proxyURL, address)
			}
		}
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if !detach() {
		resp.Body.Close()
		return nil, setup.Err()
	}
	return resp, nil
}

func connectProxy(conn net.Conn, proxyURL *url.URL, address string) (net.Conn, error) {
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{}, Host: address}
	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		req.Header = http.Header{"Proxy-Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password))}}
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proxy refused CONNECT: %s", resp.Status)
	}
	// A successful CONNECT switches to the tunnel immediately after the headers.
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (a upgradeAttempt) spdy(protocols ...string) (httpstream.Connection, string, error) {
	var socket net.Conn
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { socket = info.Conn }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(a.ctx, trace), http.MethodPost, a.target.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", spdy.HeaderSpdy31)
	for _, p := range protocols {
		req.Header.Add(httpstream.HeaderProtocolVersion, p)
	}
	resp, err := a.raw.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || !httpguts.HeaderValuesContainsToken(resp.Header.Values("Connection"), "Upgrade") || !strings.EqualFold(resp.Header.Get("Upgrade"), spdy.HeaderSpdy31) {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var status struct{ Message, Error string }
		_ = json.Unmarshal(body, &status)
		message := cmp.Or(strings.TrimSpace(status.Message), strings.TrimSpace(status.Error), strings.TrimSpace(string(body)), "empty server response")
		return nil, "", fmt.Errorf("unable to upgrade connection: %s", message)
	}
	if socket == nil {
		resp.Body.Close()
		return nil, "", fmt.Errorf("upgrade transport did not provide a connection")
	}
	tunnel := &upgradedConn{Conn: socket, body: resp.Body}
	// client-go's SPDY round tripper uses 5s. Its WebSocket tunnel uses 10s.
	conn, err := spdy.NewClientConnectionWithPings(tunnel, 5*time.Second)
	if err != nil {
		tunnel.Close()
	}
	return conn, resp.Header.Get(httpstream.HeaderProtocolVersion), err
}

// net/http's upgraded body also reads bytes buffered with the 101 response.
type upgradedConn struct {
	net.Conn
	body io.ReadCloser
}

func (c *upgradedConn) Read(p []byte) (int, error) { return c.body.Read(p) }
func (c *upgradedConn) Close() error               { return c.body.Close() }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
