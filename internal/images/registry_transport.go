package images

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errBlockedAddress marks a registry request refused by the network guard.
var errBlockedAddress = errors.New("registry address not allowed")

// errCredentialsOverHTTP is the guard refusing to send credentials in clear.
var errCredentialsOverHTTP = fmt.Errorf("%w: registry credentials require HTTPS", errBlockedAddress)

// maxRegistryResponseBytes bounds every response body read from a registry
// for a config lookup: manifests, indexes, token responses and the config
// blob. Real ones are kilobytes; this stops a hostile or broken registry from
// streaming an unbounded body into memory.
const maxRegistryResponseBytes = 4 << 20

type registryHostKey struct{}

// namedEndpoint is the registry a lookup is for, as the Pod named it. port is
// empty when the reference had none (the scheme's default port).
type namedEndpoint struct{ host, port string }

// withRegistryHost records the registry a lookup is for. The guard lets that
// endpoint, and only that endpoint, sit on a private or loopback address:
// internal registries are normal, but a redirect or token realm pointing
// somewhere else internal is not.
func withRegistryHost(ctx context.Context, registry string) context.Context {
	host, port := splitHostPort(registry)
	return context.WithValue(ctx, registryHostKey{}, namedEndpoint{host: host, port: port})
}

// isNamed reports whether host:port is the endpoint the lookup was for.
func isNamed(ctx context.Context, host, port string) bool {
	n, _ := ctx.Value(registryHostKey{}).(namedEndpoint)
	if n.host == "" || !strings.EqualFold(n.host, host) {
		return false
	}
	if n.port == "" {
		return port == "443" || port == "80"
	}
	return n.port == port
}

// guardedTransport is the transport runtime-version lookups use. Image
// references come from Pod specs, which anyone who can create a Pod
// controls, and Radar's network position differs from a node's. Every
// request, including redirects and token endpoints, is resolved here and
// sent only to an address that passed the policy:
//   - link-local (cloud metadata), unspecified and multicast: never;
//   - loopback: never in-cluster (it is Radar's own Pod), and locally only
//     for the registry endpoint the Pod named (a local dev registry);
//   - private ranges: only for the registry endpoint (host and port) the Pod
//     named, unless anyPrivate is set.
//
// Direct connections dial the checked address. Through an HTTP proxy, HTTPS
// requests are tunneled with a CONNECT to the checked address, so the proxy
// can't resolve the name somewhere else. A proxied request that can't be
// pinned that way (plain HTTP, a name that doesn't resolve here, a non-HTTP
// proxy) is refused in-cluster and left to the proxy locally, where the proxy
// is the user's own.
type guardedTransport struct {
	requireTLS bool
	// tlsForCredentials refuses only plain-HTTP requests that may carry
	// credentials: the node, too, follows a registry's redirect to an HTTP
	// download URL, and the client sends no credentials there.
	tlsForCredentials bool
	// uncapped lifts the response size cap, for layer downloads: a layer
	// can be gigabytes. go-containerregistry still bounds manifest and token
	// bodies and checks each blob's size and digest.
	uncapped bool
	// anyPrivate allows private addresses on every request, not only the
	// named registry: self-hosted registries often serve tokens or blobs
	// from a second internal host.
	anyPrivate bool
	// pinnedOnly refuses proxied requests Radar can't pin to a checked
	// address, outside a cluster too: with auth on, the caller is not the
	// proxy's owner.
	pinnedOnly     bool
	inCluster      bool
	proxy          func(*http.Request) (*url.URL, error)
	resolver       *net.Resolver
	dialer         *net.Dialer
	connectTimeout time.Duration
	// pinned never uses a proxy on its own; its dialer tunnels through one
	// when the request carries a pinned proxy route.
	pinned *http.Transport
	// unpinned lets the proxy resolve and dial: local mode only.
	unpinned *http.Transport
}

// forLookup returns a transport with its own connection pools, for one
// lookup. Pools aren't shared across lookups: a connection one lookup opened
// to the registry it named (maybe on a private address) must never carry
// another lookup's request, even after that name resolves elsewhere.
func (g *guardedTransport) forLookup() *guardedTransport {
	return newGuardedTransportWith(g.inCluster, g.proxy, g.resolver)
}

func (g *guardedTransport) closeIdle() {
	g.pinned.CloseIdleConnections()
	g.unpinned.CloseIdleConnections()
}

func newGuardedTransport(inCluster bool) *guardedTransport {
	return newGuardedTransportWith(inCluster, http.ProxyFromEnvironment, net.DefaultResolver)
}

func newGuardedTransportWith(inCluster bool, proxy func(*http.Request) (*url.URL, error), resolver *net.Resolver) *guardedTransport {
	g := &guardedTransport{
		inCluster: inCluster,
		proxy:     proxy,
		resolver:  resolver,
		dialer:    &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		// The CONNECT handshake: request and response headers only.
		connectTimeout: 10 * time.Second,
	}
	newTransport := func() *http.Transport {
		return &http.Transport{
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	g.pinned = newTransport()
	g.pinned.DialContext = g.dialContext
	g.unpinned = newTransport()
	g.unpinned.Proxy = func(r *http.Request) (*url.URL, error) { return g.proxy(r) }
	g.unpinned.DialContext = g.dialer.DialContext
	return g
}

type proxyRouteKey struct{}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && (g.requireTLS || g.tlsForCredentials && mayCarryCredentials(req)) {
		return nil, errCredentialsOverHTTP
	}
	ctx := req.Context()
	host, port := req.URL.Hostname(), req.URL.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[req.URL.Scheme]
	}
	proxyURL, err := g.proxy(req)
	if err != nil {
		proxyURL = nil
	}

	// Checked per request, not only when dialing: redirects and token
	// endpoints are requests too.
	ips, lookupErr := g.lookup(ctx, host)
	if lookupErr != nil {
		if proxyURL != nil && !g.inCluster && !g.pinnedOnly {
			return g.send(g.unpinned, req)
		}
		if proxyURL != nil {
			return nil, fmt.Errorf("%w: %s can't be checked before the proxy resolves it", errBlockedAddress, host)
		}
		return nil, lookupErr
	}
	named := isNamed(ctx, host, port)
	for _, ip := range ips {
		if !g.ipAllowed(ip, named) {
			return nil, fmt.Errorf("%w: %s", errBlockedAddress, host)
		}
	}
	if proxyURL == nil {
		return g.send(g.pinned, req)
	}
	if req.URL.Scheme == "https" && proxyURL.Scheme == "http" {
		return g.send(g.pinned, req.WithContext(context.WithValue(ctx, proxyRouteKey{}, proxyURL)))
	}
	if !g.inCluster && !g.pinnedOnly {
		return g.send(g.unpinned, req)
	}
	return nil, fmt.Errorf("%w: %s can't be pinned through the proxy", errBlockedAddress, host)
}

// mayCarryCredentials reports a request with credentials in a header, or a
// POST: a pull only POSTs to exchange a refresh token, sent in the body.
func mayCarryCredentials(req *http.Request) bool {
	return req.Header.Get("Authorization") != "" || (req.Method != http.MethodGet && req.Method != http.MethodHead)
}

func (g *guardedTransport) send(rt *http.Transport, req *http.Request) (*http.Response, error) {
	resp, err := rt.RoundTrip(req)
	if err != nil || resp.Body == nil || g.uncapped {
		return resp, err
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body, left: maxRegistryResponseBytes}
	return resp, nil
}

// dialContext connects to the first allowed address of addr, directly or
// through a CONNECT tunnel when the request carries a proxy route. It dials
// the address that was checked, not the name, so a second DNS answer can't
// swap in a forbidden address.
func (g *guardedTransport) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := g.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	named := isNamed(ctx, host, port)
	proxyURL, _ := ctx.Value(proxyRouteKey{}).(*url.URL)
	lastErr := fmt.Errorf("%w: %s", errBlockedAddress, host)
	for _, ip := range ips {
		if !g.ipAllowed(ip, named) {
			continue
		}
		target := net.JoinHostPort(ip.String(), port)
		var conn net.Conn
		if proxyURL != nil {
			conn, err = g.connectVia(ctx, proxyURL, target)
		} else {
			conn, err = g.dialer.DialContext(ctx, network, target)
		}
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// connectVia opens a CONNECT tunnel to target (an IP:port) through an HTTP
// proxy. The proxy itself is configuration, not tenant input, so it isn't
// checked against the address policy.
func (g *guardedTransport) connectVia(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if proxyURL.Port() == "" {
		proxyAddr = net.JoinHostPort(proxyURL.Hostname(), "80")
	}
	conn, err := g.dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	// http.Transport dials without the request's deadline, so the handshake
	// gets its own: a proxy that accepts and never answers must not hold the
	// connection (and this goroutine) forever.
	_ = conn.SetDeadline(time.Now().Add(g.connectTimeout))
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if u := proxyURL.User; u != nil {
		pass, _ := u.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)) + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	// The body of a CONNECT response is never read: after a 200 the bytes
	// belong to the tunnel, whatever framing headers the proxy sent.
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy refused the tunnel: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (g *guardedTransport) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

func (g *guardedTransport) ipAllowed(ip net.IP, named bool) bool {
	if g.anyPrivate && ip != nil && (ip.IsPrivate() || isSharedAddressSpace(ip)) {
		return true
	}
	return registryIPAllowed(ip, g.inCluster, named)
}

// registryIPAllowed is the address policy; see guardedTransport.
func registryIPAllowed(ip net.IP, inCluster, namedRegistry bool) bool {
	switch {
	case ip == nil, ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsUnspecified(), ip.IsMulticast():
		return false
	case ip.IsLoopback():
		return !inCluster && namedRegistry
	case ip.IsPrivate(), isSharedAddressSpace(ip):
		return namedRegistry
	}
	return true
}

// isSharedAddressSpace reports RFC 6598 carrier-grade NAT space (100.64/10),
// which net.IP.IsPrivate leaves out and some clusters use for Pods.
func isSharedAddressSpace(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

// splitHostPort splits "host", "host:port", "[v6]" or "[v6]:port".
func splitHostPort(hostport string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), ""
}

type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// At the cap: a body that ends exactly here is fine.
		var one [1]byte
		if n, err := b.ReadCloser.Read(one[:]); n == 0 && err == io.EOF {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("registry response exceeds %d bytes", maxRegistryResponseBytes)
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// isBlocked reports whether err came from the network guard.
func isBlocked(err error) bool {
	return errors.Is(err, errBlockedAddress)
}

// refusedByRegistryClient reports go-containerregistry's own refusal of a
// redirect or token server at a private or link-local IP literal, made
// before the request reaches the network guard.
func refusedByRegistryClient(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "SSRF protection:") || strings.Contains(msg, "is a private or link-local address")
}

// isRefused reports a registry request refused by the network guard or by
// go-containerregistry's own checks.
func isRefused(err error) bool {
	return err != nil && (isBlocked(err) || refusedByRegistryClient(err))
}
