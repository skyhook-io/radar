// Package probe runs reachability probes - DNS / TCP / TLS / HTTP - against
// a target the caller already knows about from declared config. The package
// is narrow by design: it does not discover targets, crawl URLs, or run as
// a service. The caller passes a concrete address; this package answers
// "from where this binary is running right now, can I reach it?" with
// strict timeouts and an explicit vantage label on every result.
//
// Vantage matters: a probe failure from a laptop means "your laptop can't
// reach this", not "this is broken." The Result struct carries it so
// downstream consumers can frame the verdict for the operator accordingly.
//
// What this package does NOT do:
//
//   - Accept user-supplied URLs. Targets must come from observed cluster
//     config (Service ports, Ingress addresses, Gateway listeners). The
//     trace composer enforces this; this package trusts its callers.
//   - Follow redirects in HTTP probes. One shot per probe - multi-hop
//     traces should be modeled as separate probe targets, not as redirect
//     chains.
//   - Send a request body, auth headers, or cookies. Every probe is the
//     minimum signal: "did the layer succeed?"
//   - Retry. Time budget for the whole trace is bounded; let the caller
//     decide whether to re-run.
//
// See internal/trace/probes.go for how probes are orchestrated per entry
// kind, and docs/diagnose.md §"Active probes" for the vantage routing
// matrix.
package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// dialControl is the net.Dialer.Control hook applied to every direct probe.
// It's a package var so tests that exercise the probes against loopback
// httptest servers can disable the SSRF guard; production always uses
// denyInternalControl.
var dialControl = denyInternalControl

// denyInternalControl is a net.Dialer.Control hook that refuses DIRECT probe
// connections to loopback, unspecified (0.0.0.0/::), and link-local addresses - most importantly the
// cloud metadata endpoint 169.254.169.254. A user who can set an Ingress host
// or Gateway address could otherwise point it at an internal target and have
// Radar's probe reach it on their behalf (SSRF). Legitimate probe targets are
// declared external hosts and cluster Service/Pod IPs (private ranges like
// 10.0.0.0/8 stay allowed - those are normal cluster networking); loopback and
// link-local are never a legitimate target. Applied to TCP/TLS/HTTP; the
// apiserver-proxy path doesn't dial directly so it's unaffected.
func denyInternalControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	// IsUnspecified catches 0.0.0.0 / ::, which on Linux a connect() routes to
	// 127.0.0.1 / ::1 - so an unspecified address reaches localhost just like a
	// loopback dial (some Gateway controllers report 0.0.0.0 in status.addresses).
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("refusing to probe internal address %s (SSRF guard)", ip)
	}
	// Cloud metadata endpoints that are NOT link-local (so the check above misses
	// them): Alibaba's IMDS at 100.100.100.100 (CGNAT range) and the IPv6 IMDS ULA
	// fd00:ec2::254. Deny explicitly so a probe target can't be pointed at them.
	if ip.Equal(net.ParseIP("100.100.100.100")) || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return fmt.Errorf("refusing to probe cloud-metadata address %s (SSRF guard)", ip)
	}
	return nil
}

// isCertTrustError reports whether err is a TLS certificate VERIFICATION
// failure (unknown CA, hostname mismatch, expired) as opposed to a transport
// or protocol failure. The TCP+TLS transport completed and the server
// presented a cert - we reached it - so this is never an unreachable service;
// treating it as a hard failure would condemn a healthy internal endpoint
// behind a private CA. Whether the failure is benign-from-here (private CA →
// ToneReached) or a real degradation (expired / wrong host → ToneDegraded) is
// decided downstream by certErrorTone.
func isCertTrustError(err error) bool {
	if err == nil {
		return false
	}
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	if errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) {
		return true
	}
	return strings.Contains(err.Error(), "x509:")
}

// Vantage names where the running binary sits on the network. Detection is
// best-effort: in-cluster means "looks like we're a Pod" (KUBERNETES_SERVICE_HOST
// is set), local means "looks like we're on the operator's machine". A
// downstream caller can override via DetectVantage(... overrides ...).
type Vantage string

const (
	VantageInCluster Vantage = "in-cluster"
	VantageLocal     Vantage = "local"
)

// Source names WHO issued a probe. Vantage says where on the network the
// request started; Source says which process started it. They are independent:
// when Radar runs as a Pod, its own dials and a throwaway probe Job are both
// in-cluster over the data path, but they carry different identities (Radar's
// ServiceAccount vs the target namespace's default), different sidecar
// exposure, and are separate observations that must not overwrite each other.
const (
	SourceRadar    = "radar"     // Radar's own process, inline with the trace
	SourceProbeJob = "probe-job" // a throwaway Job Radar created in the cluster

	// SkipClassInformational marks a probe that RAN and was deliberately kept
	// informational (a throwaway identity's failure or degraded answer must not
	// condemn the path). A disposition, never a coverage gap: consumers must not
	// describe it as "never tried".
	SkipClassInformational = "informational"

	// SkipClassDenied marks a probe the identity was not allowed to send. The
	// request never reached the workload, so it says nothing about reachability -
	// and unlike other skips it names something the operator can grant.
	SkipClassDenied = "denied"
)

// Layer names which network layer this Result attests to. Higher layers
// strictly imply lower layers succeeded - if HTTP returns ok, TCP and DNS
// did too.
type Layer string

const (
	LayerDNS  Layer = "dns"
	LayerTCP  Layer = "tcp"
	LayerTLS  Layer = "tls"
	LayerHTTP Layer = "http"
)

// Path discriminates which route a Service/Pod probe took when more than
// one was feasible. "data" means the probe went straight to the resource
// over the network (kube-proxy path for ClusterIP, direct dial for PodIP);
// "apiserver" means it tunnelled through the Kubernetes API server's proxy
// subresource. Empty for layers where the question doesn't apply (DNS,
// HTTP to an Ingress hostname, etc.). The graph visualization uses Path
// to place each result on the correct arrow.
type Path string

const (
	PathData      Path = "data"
	PathAPIServer Path = "apiserver"
)

// Tone classifies a Result for the UI when OK alone is too coarse. It
// encodes the honest distinction between three different claims that a raw
// OK bool conflates: did we reach the target, did the exact thing we asked
// for succeed, and is the server itself erroring.
//
//	healthy   - reached AND verified (HTTP 2xx; a clean DNS/TCP/TLS layer)
//	reached   - reached an HTTP server, but what we asked for is unproven
//	            (3xx redirect not followed, or 4xx route/auth not what we
//	            hit). This is NOT a reachability failure and must not render
//	            as "verified" nor as "degraded/broken".
//	degraded  - reached, but the server answered 5xx. Traffic passed; the
//	            backend is erroring. Never escalates to "unreachable/broken".
//	unhealthy - could not reach (transport failure: DNS/TCP/TLS/EOF/timeout).
//
// Empty Tone is the default; consumers infer from Skipped + OK in that case.
type Tone string

const (
	ToneHealthy   Tone = "healthy"
	ToneReached   Tone = "reached"
	ToneDegraded  Tone = "degraded"
	ToneUnhealthy Tone = "unhealthy"
)

// classifyHTTPStatus maps an HTTP status code to the reachability vocabulary.
// The scope is the network path, not application health: reaching the target and
// getting any answer means the path works, so 2xx, 3xx, 4xx, and an app's own 5xx
// all read as "reached"; the status code itself is not judged. The one exception
// is a proxy or gateway's own 502/504, which is that proxy reporting it could not
// reach its upstream, a fact about the front-door-to-backend hop and in scope.
// Transport failures never reach here; they set Error and OK=false at the call
// site. The returned tone pairs with OK=true: a status was received, even for a
// 4xx or 5xx.
func classifyHTTPStatus(code int) (Tone, string) {
	switch {
	case code == 502 || code == 504:
		// Bad Gateway / Gateway Timeout: a proxy reporting it could not reach its
		// upstream. That is a network-path statement about the next hop, not an app
		// error, and it is the one 5xx a reachability diagnosis flags.
		return ToneDegraded, fmt.Sprintf("HTTP %d · the front door couldn't reach the backend", code)
	case code >= 500:
		// The app answered with a server error. The request reached it and it
		// responded, so the network path works. A 500 or 503 is application health,
		// which a network-path diagnosis does not judge.
		return ToneReached, fmt.Sprintf("HTTP %d · reached, server error", code)
	case code == 401 || code == 407:
		// The server answered and is demanding auth - a reachability SUCCESS, and a
		// precise signal (the path works; you just need credentials), never "broken".
		return ToneReached, fmt.Sprintf("HTTP %d · reached, authentication required", code)
	case code == 403:
		return ToneReached, fmt.Sprintf("HTTP %d · reached, forbidden (auth or policy)", code)
	case code >= 400:
		return ToneReached, fmt.Sprintf("HTTP %d · reached", code)
	case code >= 300:
		return ToneReached, fmt.Sprintf("HTTP %d · reached, redirect", code)
	default:
		return ToneHealthy, fmt.Sprintf("HTTP %d", code)
	}
}

// Result is one probe outcome. Skipped is true when the vantage routing
// matrix decided this probe wouldn't tell the truth (e.g. probing a
// ClusterIP from the local laptop). Empty Error + OK=true means success;
// a non-empty Error always means failure regardless of OK.
type Result struct {
	Layer   Layer   `json:"layer"`
	Target  string  `json:"target"`
	Vantage Vantage `json:"vantage"`
	Path    Path    `json:"path,omitempty"`
	// Port is the backend port this probe tested (a Service port or container
	// port). 0 for host-level probes (DNS) that aren't port-specific. The
	// coverage projection groups routes by (backend, Port) so a multi-port
	// backend reports each port honestly instead of collapsing to one outcome.
	Port int32 `json:"port,omitempty"`
	// Protocol is the declared L4 protocol of the port this row is about, when
	// it is not TCP. Kubernetes permits TCP and UDP ports with the same number,
	// so the number alone cannot say which declared path a skip row speaks for
	// - and a TCP route absorbing a UDP sibling's row erased a declared path.
	// Empty means TCP (the Kubernetes default).
	Protocol string `json:"protocol,omitempty"`
	// Source is WHO issued this probe, which (Vantage, Path) cannot express:
	// Radar's own process and a throwaway probe Job are both in-cluster/data.
	// Without it the two collide - a Job result silently replaces Radar's own
	// in-cluster observation on merge - and the UI credits Radar's direct dials
	// to a Job that was never created. Empty means SourceRadar.
	Source  string        `json:"source,omitempty"`
	OK      bool          `json:"ok"`
	Tone    Tone          `json:"tone,omitempty"`
	Skipped bool          `json:"skipped,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Latency time.Duration `json:"latencyNs,omitempty"`
	Detail  string        `json:"detail,omitempty"`
	Error   string        `json:"error,omitempty"`
	// Addresses are what a DNS lookup resolved to. Structured so consumers
	// never have to parse Detail.
	Addresses []string `json:"addresses,omitempty"`
	// AddressScope classifies those addresses - public | private | mixed. It
	// describes the ADDRESS, never the journey: a public address does not prove
	// a packet crossed the public internet.
	AddressScope string `json:"addressScope,omitempty"`
	// Command is a copyable command the operator can run to verify what this
	// probe honestly couldn't (a non-HTTP port, an HTTPS backend the proxy
	// can't relay, an address only reachable in-cluster). Set at the skip site
	// where the structured context exists; the UI renders it via CopyableCommand.
	Command string `json:"command,omitempty"`
	// SkipClass is the coverage class of a skip (a vantage gap, a benign no-loss
	// skip, or a genuine coverage gap), stamped at the skip site that has the
	// structured context. The consumer prefers this over re-deriving the class from
	// the human Reason text, which drifts when the copy is reworded. The value space
	// is defined by the consumer (the internal/trace SkipClass constants); probe
	// treats it as an opaque tag.
	SkipClass string `json:"skipClass,omitempty"`
}

// DetectVantage reads the process env on every call so tests can override
// deterministically via t.Setenv. The KUBERNETES_SERVICE_HOST sentinel is
// what kubelet injects into every pod; missing means "not a pod".
func DetectVantage() Vantage {
	if v := os.Getenv("KUBERNETES_SERVICE_HOST"); v != "" {
		return VantageInCluster
	}
	return VantageLocal
}

// DNS resolves host with the system resolver, returning the discovered
// addresses on success. Timeout is enforced by ctx; callers should pass a
// timeout-scoped context (≤200ms is typical).
func DNS(ctx context.Context, host string, vantage Vantage) Result {
	r := Result{Layer: LayerDNS, Target: host, Vantage: vantage}
	if host == "" {
		r.Skipped = true
		r.Reason = "empty host"
		return r
	}
	start := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	r.Latency = time.Since(start)
	if err != nil {
		r.Error = sanitizeError(err)
		r.Detail = classifyDNSError(err)
		return r
	}
	r.OK = true
	r.Detail = "resolved to " + strings.Join(addrs, ", ")
	r.Addresses = addrs
	r.AddressScope = classifyAddressScope(addrs)
	return r
}

// Address scopes for a resolved name. Reported so a consumer never has to parse
// Detail, and so "where did this request actually go" stops being unanswerable.
const (
	ScopePublic  = "public"  // every resolved address is globally routable
	ScopePrivate = "private" // none are - RFC1918/CGNAT/link-local/loopback/ULA
	ScopeMixed   = "mixed"   // both, e.g. split-horizon DNS
)

// classifyAddressScope reports whether a name resolved to globally routable
// addresses. It describes the ADDRESS, never the journey: a public address does
// not prove a packet crossed the public internet (VPN, hairpin NAT and
// split-horizon DNS all break that inference), and the UI must say so.
func classifyAddressScope(addrs []string) string {
	var public, private int
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || isCGNAT(ip) {
			private++
			continue
		}
		public++
	}
	switch {
	case public > 0 && private > 0:
		return ScopeMixed
	case public > 0:
		return ScopePublic
	case private > 0:
		return ScopePrivate
	}
	return ""
}

// isCGNAT reports 100.64.0.0/10 - carrier-grade NAT. net.IP.IsPrivate covers
// RFC1918 and ULA but not this, and it is common enough in managed clusters
// (and in Tailscale-style overlays) that treating it as public would be wrong.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

// classifyDNSError turns a resolver error into the operator's mental model - the
// difference between "wrong name" (NXDOMAIN) and "your DNS is broken" (SERVFAIL/
// timeout) is the difference between two completely different fixes. Rides the
// lookup we already do, so it's free.
func classifyDNSError(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsNotFound:
			return "NXDOMAIN - the name doesn’t exist (typo, or not registered in this DNS)"
		case de.IsTimeout:
			return "DNS timeout - the resolver didn’t answer in time"
		case de.IsTemporary:
			return "SERVFAIL - the resolver returned a temporary error (DNS server problem, not the name)"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "DNS timeout - the resolver didn’t answer in time"
	}
	return "DNS lookup failed"
}

// TCP attempts a single connect+close against addr ("host:port" or
// "ip:port"). On success the connection is closed immediately - we only
// signal that the kernel accepted SYN/ACK, not that any application is
// reading. Timeout is enforced by ctx.
func TCP(ctx context.Context, addr string, vantage Vantage) Result {
	r := Result{Layer: LayerTCP, Target: addr, Vantage: vantage}
	if addr == "" {
		r.Skipped = true
		r.Reason = "empty addr"
		return r
	}
	d := net.Dialer{Control: dialControl}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	r.Latency = time.Since(start)
	if err != nil {
		r.Error = sanitizeError(err)
		return r
	}
	_ = conn.Close()
	r.OK = true
	// Every other layer names what it observed (DNS "resolved to ...", TLS
	// "valid · ...", HTTP "HTTP 200 ..."). A silent TCP success left the route's
	// evidence empty, and an empty evidence string reads downstream as "no test
	// has been run from here" - the exact contradiction beside a passing result
	// that this view exists to prevent.
	r.Detail = "TCP connection succeeded"
	return r
}

// TLS does TCP + a TLS handshake with SNI=serverName. Cert verification is
// the default Go behavior (validates against system roots, checks SNI).
// The Detail field carries the cert's CommonName for one-line
// diagnosability.
func TLS(ctx context.Context, addr, serverName string, vantage Vantage) Result {
	r := Result{Layer: LayerTLS, Target: addr, Vantage: vantage}
	if addr == "" {
		r.Skipped = true
		r.Reason = "empty addr"
		return r
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Control: dialControl}, Config: &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	r.Latency = time.Since(start)
	if err != nil {
		if isCertTrustError(err) {
			// Reached the TLS endpoint; the cert just failed verification.
			r.OK = true
			r.Tone = certErrorTone(err)
			r.Detail = certErrorDetail("TLS endpoint reached", err)
			return r
		}
		r.Error = sanitizeError(err)
		return r
	}
	defer func() { _ = conn.Close() }()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		r.OK = true
		return r
	}
	state := tlsConn.ConnectionState()
	r.OK = true
	if len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		// A valid cert that is merely expiring soon still completed the handshake, so
		// TLS reached and traffic flows - that is not a reachability failure. Surface
		// the expiry in the detail as an advisory; do not degrade the tone (which would
		// read as a TLS failure and mask a following HTTP 2xx).
		r.Detail = "valid · " + certExpiryNote(leaf)
	}
	return r
}

// HTTP performs one GET against url. Redirects are not followed: a 301
// response and the destination it points at are independent results, and
// chasing the redirect would conflate them. The Host header is set to host
// when non-empty so callers can probe via IP while presenting a hostname;
// the same value is set as TLS ServerName so SNI matches what the
// certificate names, not the IP in the URL. Without ServerName Go derives
// SNI from the URL host, so a Gateway HTTPS probe dialed by IP would fail
// cert verify even when the server is healthy.
func HTTP(ctx context.Context, url, host string, vantage Vantage) Result {
	r := Result{Layer: LayerHTTP, Target: url, Vantage: vantage}
	if url == "" {
		r.Skipped = true
		r.Reason = "empty url"
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.Error = sanitizeError(err)
		return r
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("User-Agent", "radar-trace-probe/1 (https://github.com/skyhook-io/radar)")
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if host != "" {
		tlsCfg.ServerName = host
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		// One-shot probe: a fresh transport per call never reuses the connection,
		// so keep-alive only leaks idle sockets/goroutines until idle-timeout/GC
		// under concurrent diagnoses. Disable it so the connection closes promptly.
		Transport: &http.Transport{TLSClientConfig: tlsCfg, DialContext: (&net.Dialer{Control: dialControl}).DialContext, DisableKeepAlives: true},
	}
	start := time.Now()
	resp, err := client.Do(req)
	r.Latency = time.Since(start)
	if err != nil {
		if isCertTrustError(err) {
			r.OK = true
			r.Tone = certErrorTone(err)
			r.Detail = certErrorDetail("TLS reached", err)
			return r
		}
		r.Error = sanitizeError(err)
		return r
	}
	defer func() { _ = resp.Body.Close() }()
	// Getting any HTTP status means the transport reached a server - that is
	// the reachability claim this probe exists to make. The status only
	// refines what was proven; 4xx/5xx are not transport failures and never
	// set Error (which the UI reserves for unreachable-red rows).
	r.OK = true
	r.Tone, r.Detail = classifyHTTPStatus(resp.StatusCode)
	// Name where a redirect points - we don't FOLLOW it (that could cross a trust,
	// auth, or cluster boundary), but the destination is the useful signal.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := strings.TrimSpace(resp.Header.Get("Location")); loc != "" {
			r.Detail += " → " + loc
		}
	}
	// Cert inspection rides the TLS handshake the client already completed - surface
	// expiry (the #1 silent outage) at zero extra cost. Only a VALID cert reaches here
	// (an expired/untrusted one fails verify and is classified on the error path above).
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		// Expiry is an advisory, not a reachability failure: the handshake succeeded
		// and the app answered, so traffic flows. Keep the note in the detail; do not
		// degrade the tone (that would falsely read as a failure).
		r.Detail += " · " + certExpiryNote(leaf)
	}
	return r
}

const certExpiryWarnDays = 14

// certExpiryNote describes a (valid) cert's remaining life in operator terms.
func certExpiryNote(c *x509.Certificate) string {
	days := int(time.Until(c.NotAfter).Hours() / 24)
	if days <= certExpiryWarnDays {
		return fmt.Sprintf("cert EXPIRES in %dd (%s)", days, c.NotAfter.Format("2006-01-02"))
	}
	return fmt.Sprintf("cert expires in %dd", days)
}

// classifyCertError names WHY a cert didn't verify - expired vs name-mismatch vs
// untrusted CA are three different fixes. Derived from the x509 error type, so no
// extra handshake is needed.
func classifyCertError(err error) string {
	var ci x509.CertificateInvalidError
	if errors.As(err, &ci) && ci.Reason == x509.Expired {
		return "the certificate is EXPIRED"
	}
	var he x509.HostnameError
	if errors.As(err, &he) {
		return "the certificate isn’t valid for this host name (clients using it will see a warning)"
	}
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return "the certificate is from an untrusted/private CA (not in this host’s system roots)"
	}
	return "the certificate isn’t trusted from here (private CA, or name not issued for this address)"
}

// certErrorTone splits cert verification failures by whether they are
// vantage-dependent. An unknown/private CA only fails from a vantage that
// doesn't trust it - a client that does trust the CA still reaches the service,
// so it reads ToneReached. An EXPIRED or wrong-host cert is deterministic:
// every client (including a trusting one) sees the same breakage, so it's a
// reached-but-degraded front door, ToneDegraded.
func certErrorTone(err error) Tone {
	var ci x509.CertificateInvalidError
	if errors.As(err, &ci) && ci.Reason == x509.Expired {
		return ToneDegraded
	}
	var he x509.HostnameError
	if errors.As(err, &he) {
		return ToneDegraded
	}
	return ToneReached
}

// certErrorDetail builds the operator one-liner for a cert verification failure.
// Remediation differs by cause: an unknown/private CA is a trust-store problem
// at the client ("trust the CA"), while expired / wrong-host certs break every
// client and need a reissue, so naming "trust the CA" there would mislead.
func certErrorDetail(prefix string, err error) string {
	detail := prefix + ", but " + classifyCertError(err)
	if certErrorTone(err) == ToneReached {
		return detail + " - verify from a client that trusts the CA."
	}
	return detail + "."
}

// ServiceProxy probes a Service via the Kubernetes API server proxy
// (the same path `kubectl proxy` and `kubectl port-forward` use). This is
// what makes "test my Service" work from a laptop: even though the
// ClusterIP isn't routable directly, the kube-apiserver is, and it will
// forward the HTTP request to a backend pod through kube-proxy.
//
// The signal is real but partial: it proves the Service has endpoints and
// at least one backend responds to HTTP. It does NOT prove that traffic
// from a workload in the cluster would reach the same pod the same way -
// the apiserver-proxy and the data-path are different code paths. The
// orchestrator tags the Result detail so operators reading the trace know
// which one ran.
//
// Authentication: the client's identity is the kubeconfig identity. RBAC
// needs `get services/proxy` in the namespace; common in dev/admin
// contexts, sometimes denied in production-restricted ones.
func ServiceProxy(ctx context.Context, client kubernetes.Interface, namespace, name string, port int32, path string, vantage Vantage) Result {
	r := Result{Layer: LayerHTTP, Target: fmt.Sprintf("port %d", port), Vantage: vantage, Path: PathAPIServer}
	if client == nil {
		r.Skipped = true
		r.Reason = "couldn't reach the cluster API from here - reachability can't be tested until the connection is back"
		return r
	}
	portName := fmt.Sprintf("%d", port)
	req := client.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("services").
		Name(name + ":" + portName).
		SubResource("proxy").
		Suffix(proxySuffix(path))
	return proxyResult(ctx, req, r)
}

// proxySuffix normalizes a probe path for the apiserver-proxy Suffix - always a
// leading "/", defaulting to the root.
func proxySuffix(path string) string {
	if path == "" {
		return "/"
	}
	if path[0] != '/' {
		return "/" + path
	}
	return path
}

// PodProxy probes a Pod via the Kubernetes API server proxy. The apiserver
// routes the request through a kubelet hop rather than kube-proxy, so the
// result reflects "the cluster API can reach this pod" - not necessarily
// the same as pod-to-pod direct dial.
func PodProxy(ctx context.Context, client kubernetes.Interface, namespace, name string, port int32, path string, vantage Vantage) Result {
	r := Result{Layer: LayerHTTP, Target: fmt.Sprintf("%s port %d", name, port), Vantage: vantage, Path: PathAPIServer}
	if client == nil {
		r.Skipped = true
		r.Reason = "couldn't reach the cluster API from here - reachability can't be tested until the connection is back"
		return r
	}
	portName := fmt.Sprintf("%d", port)
	req := client.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("pods").
		Name(name + ":" + portName).
		SubResource("proxy").
		Suffix(proxySuffix(path))
	return proxyResult(ctx, req, r)
}

// proxyResult runs a built apiserver-proxy GET and maps the outcome through
// the honest reachability vocabulary. The apiserver forwards the backend's
// HTTP status as the outer status, so capturing the code (not just inferring
// from the error) lets a backend 404/401 read as "reached" instead of a false
// "broken" - probing `/` on a path-routed app must not look like an outage.
//
// The status code is authoritative and is trusted FIRST: client-go reports any
// non-2xx as an error (and even wraps a proxied backend 404 as a typed
// NotFound), so inferring reachability from the error would misread a backend
// 404 as "broken" - verified against a real cluster. The only apiserver-level
// refusal handled specially is RBAC Forbidden, where we never reached the
// backend at all. A non-2xx with NO status code is a genuine transport failure
// between the apiserver and the backend.
func proxyResult(ctx context.Context, req *rest.Request, r Result) Result {
	start := time.Now()
	res := req.Do(ctx)
	r.Latency = time.Since(start)
	var code int
	res.StatusCode(&code)
	err := res.Error()
	if code == 0 {
		// When a backend answers non-2xx the apiserver relays it as a typed
		// k8s StatusError (a backend 404 → NotFound, 500 → InternalError) and
		// Result.StatusCode stays 0 - the real HTTP status is on the error.
		code = httpStatusFromError(err)
	}
	// The apiserver ITSELF was unreachable (radar→apiserver dial failed - the
	// cluster is down / the kubeconfig is stale), as opposed to the apiserver
	// RELAYING a backend failure. Nothing about the workload can be tested from
	// here, so this must SKIP - never condemn a healthy workload as "unreachable"
	// because Radar lost its cluster connection. (A relayed backend failure
	// arrives as a typed StatusError with a real status code and is handled below.)
	// A context deadline means the apiserver proxy didn't get a response within our
	// probe budget - almost always a SLOW backend (cold start), not a lost cluster
	// connection (which fails fast with connection-refused). It ALSO satisfies
	// net.Error, so it must be caught BEFORE isClusterUnreachable - otherwise a slow
	// app gets blamed on the kubeconfig. Skip with a backend-timeout reason.
	if code == 0 && isBackendTimeout(err) {
		// A deadline through the relay is genuinely ambiguous - slow backend,
		// cold start, a TLS-only port hanging on plain HTTP, or the relay
		// itself. Naming one cause turned an inconclusive timeout into a
		// diagnosis, so the reason lists the possibilities and concludes nothing.
		r.Skipped = true
		// The measured latency IS the wait: at a deadline it equals the budget
		// this dial was given, so stating it keeps the copy honest even when the
		// configured timeouts change.
		r.Reason = fmt.Sprintf("no response within the %.1fs this check waits - could be a slow or starting backend, a port expecting TLS, or the relay itself; nothing was concluded", r.Latency.Seconds())
		return r
	}
	if code == 0 && isClusterUnreachable(err) {
		r.Skipped = true
		r.Reason = "couldn't reach the cluster API from here - reachability can't be tested until the connection is back"
		return r
	}
	// The managed control-plane TUNNEL (Konnectivity/egress agent) is down - the
	// apiserver itself is fine, but it can't relay to the backend because its proxy
	// plumbing is out. That's a control-plane outage, not the workload being down,
	// so SKIP rather than condemn (matched by proxyUnreachable below, which would
	// paint a healthy workload red). Only the UNAMBIGUOUS infra signatures qualify -
	// "error dialing backend" is left to the condemn path since it can also mean the
	// backend genuinely refused through a working tunnel.
	if code == 0 && isAPIServerTunnelDown(err) {
		r.Skipped = true
		r.Reason = "couldn't reach the backend through the cluster's API-server tunnel (control-plane connectivity) - reachability can't be tested from here"
		return r
	}
	switch {
	case apierrors.IsForbidden(err) && strings.Contains(err.Error(), "proxy"):
		// RBAC denied the proxy verb itself - we never reached the backend.
		// The message references the "services/proxy" / "pods/proxy"
		// subresource, which a backend's own 403 body never does, so this
		// distinguishes an apiserver refusal from an app that answered 403
		// (the latter has a real status code and is "reached" below).
		//
		// This SKIPS rather than fails. A refused request tested nothing, so
		// scoring it as a failure condemned a healthy workload for a
		// permissions gap - red on every trace wherever the identity lacks the
		// subresource, which is the default for Radar's own ServiceAccount.
		r.Skipped = true
		r.SkipClass = SkipClassDenied
		r.Reason = "Permission denied. Your identity lacks get services/proxy or get pods/proxy in this namespace."
		return r
	case code == 502 || code == 503 || code == 504:
		// A backend that genuinely answered 502/503/504 carries a real status code, and
		// its own 5xx body routinely contains generic transport phrases ("connection
		// refused", "dial tcp", "i/o timeout", "eof") that leak into err.Error(). Only
		// the apiserver-exclusive signatures, phrases a backend body cannot produce, may
		// claim the apiserver or proxy could not reach the backend.
		unreachable, tone, detail := classifyProxy5xx(code, err)
		if unreachable {
			r.Error = translateAPIError(err)
			return r
		}
		r.OK, r.Tone, r.Detail = true, tone, detail
		return r
	case code >= 100:
		// A real HTTP status was recovered from the backend - the transport
		// reached a server. Trust the code over proxyUnreachable's loose error-
		// substring match: a backend that genuinely answered (e.g. a 500 whose
		// nginx error page body contains "connection refused") must not be
		// mislabeled apiserver-unreachable. classifyHTTPStatus does the mapping:
		// 2xx verified, 3xx/4xx and an app's own 5xx are "reached", only a 502/504
		// (front door couldn't reach upstream) is degraded. A backend 404 is
		// "reached", never "broken".
		r.OK = true
		r.Tone, r.Detail = classifyHTTPStatus(code)
		return r
	case proxyUnreachable(err):
		// No concrete backend status - the error MESSAGE proves the apiserver/proxy
		// could not reach the backend (no ready endpoints, dial refused, timeout,
		// or a managed control-plane tunnel down: GKE Konnectivity / EKS / AKS).
		// Nothing was reached: confidently unreachable, with a specific message.
		r.Error = translateAPIError(err)
		return r
	case err != nil:
		r.Error = translateAPIError(err)
		return r
	default:
		r.OK = true
		return r
	}
}

// Skipped returns a structured "not attempted, here's why" record so the
// orchestrator can surface the reason instead of silently dropping the
// probe when the current vantage can't route to the target.
func Skipped(layer Layer, target string, vantage Vantage, reason string) Result {
	return Result{Layer: layer, Target: target, Vantage: vantage, Skipped: true, Reason: reason}
}

// SkippedCmd is Skipped plus a copyable command that fills the gap the probe
// couldn't verify itself. Pass an empty command for skips that can't form an
// honest one - those stay prose-only rather than show a bogus command.
func SkippedCmd(layer Layer, target string, vantage Vantage, reason, command string) Result {
	r := Skipped(layer, target, vantage, reason)
	r.Command = command
	return r
}

// isClusterUnreachable reports whether the error is a transport-level failure
// reaching the APISERVER itself (radar→apiserver), as opposed to the apiserver
// relaying a backend failure. A k8s StatusError (or any typed apierror) means
// the apiserver RESPONDED - that's about the backend, never apiserver
// connectivity. A bare net/dial error reaching the apiserver host means Radar
// can't reach the cluster at all: nothing about the workload can be concluded.
// isBackendTimeout reports whether the error is a context deadline - the apiserver
// proxy didn't return within our probe budget. That's a slow/starting BACKEND, not
// a lost cluster connection. context.DeadlineExceeded also satisfies net.Error, so
// callers must check this BEFORE isClusterUnreachable (which would otherwise blame
// the kubeconfig for a slow app).
func isBackendTimeout(err error) bool {
	return err != nil && errors.Is(err, context.DeadlineExceeded)
}

// isAPIServerTunnelDown reports whether the error is an UNAMBIGUOUS managed
// control-plane tunnel outage (Konnectivity / egress agent absent or closed) -
// the apiserver can't relay to ANY backend, so nothing about the workload was
// tested. Distinct from a backend that genuinely refused through a working tunnel
// ("error dialing backend"), which stays a real unreachable. These phrases come
// only from the tunnel infra, never from a backend's own response body.
func isAPIServerTunnelDown(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, sig := range []string{"no agent available", "tunnel closed"} {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

func isClusterUnreachable(err error) bool {
	if err == nil {
		return false
	}
	// The apiserver responded with a typed status → about the backend, not
	// connectivity. (Includes a backend "no endpoints" 503, RBAC 403, etc.)
	var se *apierrors.StatusError
	if errors.As(err, &se) {
		return false
	}
	if apierrors.IsTimeout(err) || apierrors.IsServiceUnavailable(err) ||
		apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err) {
		return false
	}
	// Transport failure reaching the apiserver host itself.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	low := strings.ToLower(err.Error())
	for _, sig := range []string{"connection refused", "dial tcp", "no route to host", "tls handshake timeout", "i/o timeout", "no such host"} {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

// proxyUnreachable reports whether an apiserver-proxy error means the apiserver
// could not reach the backend at all (so nothing was "reached"), as opposed to
// the backend answering with an HTTP status. The apiserver uses 503
// ServiceUnavailable for both "no ready endpoints" and "couldn't dial the
// backend", and a backend's own 503 is generic - so the message signature, not
// the code, is what distinguishes a transport failure from a real response.
func proxyUnreachable(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, sig := range []string{
		"no endpoints available",        // Service has zero ready backends
		"error trying to reach service", // apiserver proxy could not dial
		"connection refused",
		"no route to host",
		"dial tcp",
		"i/o timeout",
		"context deadline exceeded",
		"no such host",
		// Managed control planes (GKE Konnectivity, AKS/EKS egress proxies)
		// tunnel apiserver→backend traffic through an agent. When the tunnel
		// or backend dial fails the apiserver reports these instead of a bare
		// "dial tcp" - without them a truly-unreachable backend on GKE would
		// fall through and read as "reached, server error".
		"error dialing backend",
		"no agent available",
		"proxy error from",
		"while dialing",
		"tunnel closed",
		"backend closed connection",
		// The proxied backend accepted TCP but isn't HTTP (or closed mid-
		// response): nothing HTTP was reached. translateAPIError already
		// treats these as "doesn't speak HTTP/1.1"; keep the classifier
		// consistent so they don't read as "reached, server error".
		"eof",
		"malformed http response",
	} {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

// proxyUnreachableStrict reports apiserver-EXCLUSIVE unreachable signatures -
// phrases only the apiserver/proxy layer emits, which a backend's own response
// body cannot contain. Used where a real backend status code is present (a
// gateway-class 5xx the backend may have answered itself): the generic transport
// substrings proxyUnreachable also matches ("connection refused", "dial tcp",
// "i/o timeout", "eof", ...) routinely appear in a Go backend's 5xx body and
// would false-condemn a reached-but-degraded backend as a transport failure.
// classifyProxy5xx decides how a 502/503/504 recovered from the apiserver proxy
// reads. If the apiserver or proxy reports it could not reach the backend (an
// apiserver-exclusive error signature) that is a real network break, reported as
// unreachable. Otherwise the backend itself answered, so it reads exactly as a
// direct probe of that code would: a forwarded 503 is the app's own error
// (reached), while a 502 or 504 is a proxy-class backend that could not reach its
// upstream (degraded). This keeps the laptop (proxy) and in-cluster vantages from
// disagreeing on the same backend response.
func classifyProxy5xx(code int, err error) (unreachable bool, tone Tone, detail string) {
	if proxyUnreachableStrict(err) {
		return true, "", ""
	}
	tone, detail = classifyHTTPStatus(code)
	return false, tone, detail
}

func proxyUnreachableStrict(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, sig := range []string{
		"no endpoints available",        // Service has zero ready backends
		"error trying to reach service", // apiserver proxy could not dial
		"error dialing backend",         // managed-control-plane tunnel dial failed
		"no agent available",            // Konnectivity/egress agent absent
		"proxy error from",
		"while dialing",
		"tunnel closed",
	} {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

// httpStatusFromError recovers the HTTP status code the apiserver attached to
// a proxied backend response. client-go wraps a non-2xx proxy response as a
// typed StatusError whose Code is the backend's status, but leaves
// Result.StatusCode at 0, so this is the only place the code survives.
func httpStatusFromError(err error) int {
	var se *apierrors.StatusError
	if errors.As(err, &se) {
		return int(se.ErrStatus.Code)
	}
	return 0
}

func sanitizeError(err error) string {
	// Strip the OS-specific prefix that net.Error often adds - operators
	// don't care about "dial tcp" framing, they care about "connection
	// refused" or "i/o timeout". Keep the message short and parseable.
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && i < len(s)-2 {
		return s[i+2:]
	}
	return s
}

// translateAPIError rewrites the most common Kubernetes apiserver error
// strings into operator English. The default `err.Error()` leaks wire
// internals like "(get pods my-pod:8080)" that mean nothing to someone
// debugging connectivity. Anything we don't recognize falls back to the
// generic sanitizer.
func translateAPIError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "no endpoints available"):
		return "No ready backend endpoints - there's nothing to reach on this Service."
	case strings.Contains(low, "could not find the requested resource"):
		return "No backend pod is answering on this port via the Kubernetes API."
	case strings.Contains(low, "forbidden"):
		return "Permission denied. Your kubeconfig identity lacks get services/proxy or get pods/proxy in this namespace."
	case strings.Contains(low, "no route to host"):
		return "No route to host. Pod is scheduled but unreachable from the apiserver node."
	case strings.Contains(low, "connection refused"):
		return "Connection refused. Nothing is listening on the port."
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "context deadline exceeded"):
		return "Timed out. The port accepted no connection before the check gave up."
	case strings.Contains(low, "eof"):
		return "Connection closed before response. Backend likely doesn't speak HTTP/1.1 on this port."
	case strings.Contains(low, "x509:"):
		return "TLS certificate not trusted from here (unknown CA or name mismatch). The service may still be fine for clients that trust its CA."
	case strings.Contains(low, "tls:"):
		return "TLS handshake failed. The port may not speak TLS, or requires a different version/cipher."
	case strings.Contains(low, "error dialing backend"), strings.Contains(low, "no agent available"),
		strings.Contains(low, "proxy error from"), strings.Contains(low, "while dialing"),
		strings.Contains(low, "tunnel closed"):
		// Managed control-plane (GKE Konnectivity / EKS / AKS) tunnel or dial
		// failure between the apiserver and the backend.
		return "The Kubernetes API couldn't reach the backend (control-plane proxy/tunnel could not connect). The backend may be down, or the node/tunnel unreachable."
	case strings.Contains(low, "service unavailable"), strings.Contains(low, "bad gateway"),
		strings.Contains(low, "gateway timeout"), strings.Contains(low, "503"),
		strings.Contains(low, "502"), strings.Contains(low, "504"):
		return "Service unavailable via the Kubernetes API - no ready backend, or the backend couldn't be reached / returned 5xx."
	}
	return sanitizeError(err)
}
