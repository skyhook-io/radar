package portforward

import (
	"cmp"
	"context"
	"crypto/tls"
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
		proxy = http.ProxyFromEnvironment
	}
	// net/http cancels an in-progress HTTP/1 upgrade, including a proxy
	// CONNECT. The client-go SPDY transport blocks in http.ReadResponse.
	transport := &http.Transport{
		TLSClientConfig:   tlsConfig,
		Proxy:             proxy,
		DialContext:       cfg.Dial,
		Protocols:         new(http.Protocols),
		DisableKeepAlives: true,
	}
	transport.Protocols.SetHTTP1(true)
	raw, err := rest.HTTPWrappersForConfig(cfg, transport)
	if err != nil {
		return nil, err
	}
	attempt := upgradeAttempt{ctx: ctx, cfg: cfg, target: target, tls: tlsConfig, raw: raw}
	return portforward.NewFallbackDialer(upgradeDial(attempt.websocket), upgradeDial(attempt.spdy), func(err error) bool {
		return ctx.Err() == nil && (errors.Is(err, websocket.ErrBadHandshake) || streamhttp.IsHTTPSProxyError(err))
	}), nil
}

type upgradeAttempt struct {
	ctx    context.Context
	cfg    *rest.Config
	target *url.URL
	tls    *tls.Config
	raw    http.RoundTripper
}

func (a upgradeAttempt) websocket(protocols ...string) (httpstream.Connection, string, error) {
	dialer := websocket.Dialer{
		Proxy: a.cfg.Proxy, TLSClientConfig: a.tls,
		ReadBufferSize: 33 * 1024, WriteBufferSize: 33 * 1024,
	}
	if dialer.Proxy == nil {
		dialer.Proxy = utilnet.NewProxierWithNoProxyCIDR(http.ProxyFromEnvironment)
	}
	for _, p := range protocols {
		dialer.Subprotocols = append(dialer.Subprotocols, portconstants.WebsocketsSPDYTunnelingPrefix+p)
	}
	// Gorilla reads the proxy CONNECT and upgrade responses directly from
	// the socket. Cancelling DialContext alone only interrupts dialing.
	var stop func() bool
	dialer.NetDialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		dial := a.cfg.Dial
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		conn, err := dial(dialCtx, network, address)
		if err == nil {
			stop = context.AfterFunc(a.ctx, func() { conn.Close() })
		}
		return conn, err
	}
	defer func() {
		if stop != nil {
			stop()
		}
	}()
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
		conn, resp, err := dialer.DialContext(req.Context(), u.String(), req.Header)
		if err != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, err
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
	if !slices.Contains(dialer.Subprotocols, socket.Subprotocol()) {
		socket.Close()
		return nil, "", fmt.Errorf("%w: unexpected subprotocol %q", websocket.ErrBadHandshake, socket.Subprotocol())
	}
	tunnel := portforward.NewTunnelingConnection("client", socket)
	conn, err := spdy.NewClientConnectionWithPings(tunnel, portforward.PingPeriod)
	if err != nil {
		tunnel.Close()
	}
	return conn, strings.TrimPrefix(socket.Subprotocol(), portconstants.WebsocketsSPDYTunnelingPrefix), err
}

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
