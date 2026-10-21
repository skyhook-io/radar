package probe

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ── P0 probe trio: DNS classification, HTTP status semantics, TLS cert inspection ──

func TestClassifyHTTPStatus_AuthAndRedirect(t *testing.T) {
	cases := []struct {
		code int
		tone Tone
		want string
	}{
		{401, ToneReached, "authentication required"},
		{407, ToneReached, "authentication required"},
		{403, ToneReached, "forbidden"},
		{404, ToneReached, "HTTP 404 · reached"},
		{301, ToneReached, "redirect"},
		{200, ToneHealthy, "HTTP 200"},
		{500, ToneReached, "reached, server error"},
		{503, ToneReached, "reached, server error"},
		{502, ToneDegraded, "front door couldn't reach the backend"},
		{504, ToneDegraded, "front door couldn't reach the backend"},
	}
	for _, c := range cases {
		tone, detail := classifyHTTPStatus(c.code)
		if tone != c.tone || !strings.Contains(detail, c.want) {
			t.Errorf("HTTP %d → %s/%q, want %s containing %q", c.code, tone, detail, c.tone, c.want)
		}
	}
}

func TestClassifyDNSError(t *testing.T) {
	if got := classifyDNSError(&net.DNSError{IsNotFound: true}); !strings.Contains(got, "NXDOMAIN") {
		t.Errorf("not-found → %q, want NXDOMAIN", got)
	}
	if got := classifyDNSError(&net.DNSError{IsTimeout: true}); !strings.Contains(got, "timeout") {
		t.Errorf("timeout → %q, want timeout", got)
	}
	if got := classifyDNSError(&net.DNSError{IsTemporary: true}); !strings.Contains(got, "SERVFAIL") {
		t.Errorf("temporary → %q, want SERVFAIL", got)
	}
}

func TestClassifyCertError(t *testing.T) {
	if got := classifyCertError(x509.CertificateInvalidError{Reason: x509.Expired}); !strings.Contains(got, "EXPIRED") {
		t.Errorf("expired → %q, want EXPIRED", got)
	}
	if got := classifyCertError(x509.HostnameError{}); !strings.Contains(got, "host name") {
		t.Errorf("hostname → %q, want host-name mismatch", got)
	}
	if got := classifyCertError(x509.UnknownAuthorityError{}); !strings.Contains(got, "untrusted") {
		t.Errorf("unknown-authority → %q, want untrusted", got)
	}
}

// An expired or wrong-host cert is a deterministic, everyone-affected breakage -
// reached but DEGRADED - while an unknown/private CA is only untrusted from this
// vantage (reached). certErrorTone must split them; lumping expired in with
// private-CA would hand the strictly-worse state the milder ToneReached.
func TestCertErrorTone(t *testing.T) {
	if got := certErrorTone(x509.CertificateInvalidError{Reason: x509.Expired}); got != ToneDegraded {
		t.Errorf("expired → %q, want degraded (everyone-affected breakage)", got)
	}
	if got := certErrorTone(x509.HostnameError{}); got != ToneDegraded {
		t.Errorf("hostname mismatch → %q, want degraded (everyone-affected breakage)", got)
	}
	if got := certErrorTone(x509.UnknownAuthorityError{}); got != ToneReached {
		t.Errorf("unknown CA → %q, want reached (vantage-dependent private CA)", got)
	}
}

// Remediation must name the real cause: "trust the CA" is right for a private CA
// but misleading for expired / wrong-host (no client trusts an expired cert).
func TestCertErrorDetail(t *testing.T) {
	exp := certErrorDetail("TLS reached", x509.CertificateInvalidError{Reason: x509.Expired})
	if strings.Contains(exp, "trusts the CA") {
		t.Errorf("expired detail = %q, must NOT say 'trust the CA'", exp)
	}
	if !strings.Contains(exp, "EXPIRED") {
		t.Errorf("expired detail = %q, want it to name expiry", exp)
	}
	ca := certErrorDetail("TLS reached", x509.UnknownAuthorityError{})
	if !strings.Contains(ca, "trusts the CA") {
		t.Errorf("unknown-CA detail = %q, want the 'trust the CA' remediation", ca)
	}
}

func TestCertExpiryNote(t *testing.T) {
	// +2h over the day boundary so integer-day truncation lands on the intended count.
	soon := &x509.Certificate{NotAfter: time.Now().Add(3*24*time.Hour + 2*time.Hour)}
	if got := certExpiryNote(soon); !strings.Contains(got, "EXPIRES in 3d") {
		t.Errorf("3-day cert → %q, want 'EXPIRES in 3d' (warns, uppercase)", got)
	}
	far := &x509.Certificate{NotAfter: time.Now().Add(90*24*time.Hour + 2*time.Hour)}
	if got := certExpiryNote(far); !strings.Contains(got, "expires in 90d") || strings.Contains(got, "EXPIRES") {
		t.Errorf("90-day cert → %q, want lowercase 'expires in 90d' (no warn)", got)
	}
}

// withLoopbackProbes disables the SSRF dial guard for tests that probe loopback
// httptest servers (production never probes loopback). Restored on cleanup.
func withLoopbackProbes(t *testing.T) {
	t.Helper()
	prev := dialControl
	dialControl = nil
	t.Cleanup(func() { dialControl = prev })
}

// TestHTTPStatusFromError pins how a proxied backend status is recovered. The
// apiserver relays a backend non-2xx as a typed k8s StatusError (Result's own
// StatusCode stays 0), so this extraction is the only thing that lets a backend
// 404 read as "reached" instead of a false "broken" - the exact bug a real
// cluster surfaced that an httptest-only suite could not.
func TestHTTPStatusFromError(t *testing.T) {
	nf := apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "x") // code 404
	if got := httpStatusFromError(nf); got != 404 {
		t.Errorf("httpStatusFromError(NotFound) = %d, want 404", got)
	}
	ise := apierrors.NewInternalError(context.DeadlineExceeded) // code 500
	if got := httpStatusFromError(ise); got != 500 {
		t.Errorf("httpStatusFromError(InternalError) = %d, want 500", got)
	}
	if got := httpStatusFromError(nil); got != 0 {
		t.Errorf("httpStatusFromError(nil) = %d, want 0", got)
	}
}

// TestIsClusterUnreachable pins the honesty distinction: a transport failure
// reaching the APISERVER itself (cluster down / stale kubeconfig) must read as
// "can't test from here" (→ skip), while the apiserver RELAYING a backend
// failure (a typed StatusError) must NOT - that stays a real backend verdict.
// The cardinal rule: never condemn a healthy workload because Radar lost its
// cluster connection.
func TestIsClusterUnreachable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"apiserver dial refused", errors.New(`Get "https://127.0.0.1:6443/api/v1/.../proxy/": dial tcp 127.0.0.1:6443: connect: connection refused`), true},
		{"apiserver TLS handshake timeout", errors.New(`net/http: TLS handshake timeout`), true},
		{"apiserver no such host", errors.New(`dial tcp: lookup apiserver: no such host`), true},
		// Backend-relayed failures arrive as typed StatusErrors - about the backend, NOT connectivity.
		{"backend no endpoints (503)", apierrors.NewServiceUnavailable("no endpoints available for service \"echo\""), false},
		{"rbac forbidden (403)", apierrors.NewForbidden(schema.GroupResource{Resource: "services/proxy"}, "echo", errors.New("denied")), false},
		{"backend not found (404)", apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "echo"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isClusterUnreachable(c.err); got != c.want {
				t.Errorf("isClusterUnreachable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestProxyUnreachable separates an apiserver "I couldn't reach the backend"
// 5xx (no endpoints / connection refused / timeout - nothing was reached, must
// read as unreachable) from a real backend HTTP response (which is "reached").
// Both come back as 503 ServiceUnavailable from the apiserver, so only the
// message distinguishes them - getting this wrong made a zero-endpoint Service
// render as "reached · server error", the dishonest signal an infra reader
// would never trust.
func TestProxyUnreachable(t *testing.T) {
	unreachable := []string{
		`no endpoints available for service "echo"`,
		"error trying to reach service: dial tcp 10.244.0.20:9999: connect: connection refused",
		"error trying to reach service: dial tcp 10.0.0.5:80: i/o timeout",
		"no route to host",
		// Managed control planes (GKE Konnectivity etc.) - these are the
		// real messages a truly-unreachable backend produces there.
		"error dialing backend: No agent available",
		`proxy error from 127.0.0.1:8131 while dialing 10.0.0.5:8080, code 503: 503 Service Unavailable`,
	}
	for _, m := range unreachable {
		if !proxyUnreachable(errors.New(m)) {
			t.Errorf("proxyUnreachable(%q) = false, want true (nothing was reached)", m)
		}
	}
	// A genuine backend response (the apiserver proxied successfully and the
	// app returned 500/404) is NOT a transport failure - it was reached.
	reached := []string{
		`an error on the server ("unknown") has prevented the request from succeeding`,
		"the server could not find the requested resource",
	}
	for _, m := range reached {
		if proxyUnreachable(errors.New(m)) {
			t.Errorf("proxyUnreachable(%q) = true, want false (backend answered)", m)
		}
	}
	if proxyUnreachable(nil) {
		t.Errorf("proxyUnreachable(nil) = true, want false")
	}
}

// TestProxyUnreachableStrict pins the 502/503/504 branch's honesty fix: only
// apiserver-EXCLUSIVE signatures may confidently claim unreachable when a real
// backend status code is present, because a backend's own 5xx body routinely
// contains the generic transport phrases proxyUnreachable also matches.
func TestProxyUnreachableStrict(t *testing.T) {
	// Apiserver-exclusive: a backend response body can't produce these.
	exclusive := []string{
		`no endpoints available for service "echo"`,
		"error trying to reach service: dial tcp 10.0.0.5:80: i/o timeout",
		"error dialing backend: No agent available",
		`proxy error from 127.0.0.1:8131 while dialing 10.0.0.5:8080, code 503`,
		"tunnel closed",
	}
	for _, m := range exclusive {
		if !proxyUnreachableStrict(errors.New(m)) {
			t.Errorf("proxyUnreachableStrict(%q) = false, want true (apiserver-exclusive)", m)
		}
	}
	// Generic transport phrases that leak into a reached-but-degraded backend's
	// own 5xx body must NOT be treated as unreachable in the strict (code-present)
	// path - that would false-condemn a backend that genuinely answered 503.
	backendBodyLeaks := []string{
		"connection refused",
		"dial tcp: lookup db on 10.96.0.10:53: i/o timeout",
		"context deadline exceeded",
		"no route to host",
		"unexpected EOF while reading upstream",
	}
	for _, m := range backendBodyLeaks {
		if proxyUnreachableStrict(errors.New(m)) {
			t.Errorf("proxyUnreachableStrict(%q) = true, want false (could be a backend 5xx body)", m)
		}
	}
	if proxyUnreachableStrict(nil) {
		t.Errorf("proxyUnreachableStrict(nil) = true, want false")
	}
}

// TestClassifyProxy5xx pins the vantage-agreement decision for a 502/503/504 seen
// through the apiserver proxy: the apiserver or proxy failing to reach the backend
// reads unreachable; a backend-forwarded 503 reads reached (the app's own error,
// matching a direct probe); a forwarded 502 or 504 reads degraded (a proxy-class
// backend that could not reach its upstream). Without this, the same backend
// response would read differently from a laptop than from in-cluster.
func TestClassifyProxy5xx(t *testing.T) {
	// Our stack couldn't reach the backend → unreachable, whatever the code.
	for _, code := range []int{502, 503, 504} {
		if unreachable, _, _ := classifyProxy5xx(code, errors.New(`no endpoints available for service "echo"`)); !unreachable {
			t.Errorf("classifyProxy5xx(%d, no-endpoints) unreachable=false, want true (our stack)", code)
		}
	}
	// Backend forwarded its own status (no apiserver-exclusive signature) → classify by code.
	backendErr := errors.New("an error on the server has prevented the request from succeeding")
	if unreachable, tone, _ := classifyProxy5xx(503, backendErr); unreachable || tone != ToneReached {
		t.Errorf("forwarded 503 = (unreachable=%v tone=%q), want (false, reached): app error, not a network break", unreachable, tone)
	}
	if unreachable, tone, _ := classifyProxy5xx(502, backendErr); unreachable || tone != ToneDegraded {
		t.Errorf("forwarded 502 = (unreachable=%v tone=%q), want (false, degraded)", unreachable, tone)
	}
	if unreachable, tone, _ := classifyProxy5xx(504, backendErr); unreachable || tone != ToneDegraded {
		t.Errorf("forwarded 504 = (unreachable=%v tone=%q), want (false, degraded)", unreachable, tone)
	}
}

// TestClassifyHTTPStatus pins the honest reachability vocabulary: reaching any
// HTTP status proves the transport; the code only refines what was proven.
// 2xx verifies, 3xx/4xx are "reached" (not failures), 5xx is degraded (reached
// but erroring) - never an "unreachable/broken" transport failure.
func TestClassifyHTTPStatus(t *testing.T) {
	cases := []struct {
		code int
		tone Tone
	}{
		{200, ToneHealthy},
		{204, ToneHealthy},
		{301, ToneReached},
		{302, ToneReached},
		{400, ToneReached},
		{401, ToneReached},
		{403, ToneReached},
		{404, ToneReached},
		{429, ToneReached},
		// Scope is the network path, not app health: an app's own 5xx means the request
		// reached the app and it answered, so the path works. Only a proxy's 502/504
		// (could not reach its upstream) is a network-path fact worth flagging.
		{500, ToneReached},  // app error: reached
		{503, ToneReached},  // service unavailable: reached (ambiguous, do not overclaim)
		{505, ToneReached},  // any other app 5xx: reached
		{502, ToneDegraded}, // bad gateway: front door could not reach upstream
		{504, ToneDegraded}, // gateway timeout: front door could not reach upstream
	}
	for _, c := range cases {
		got, detail := classifyHTTPStatus(c.code)
		if got != c.tone {
			t.Errorf("classifyHTTPStatus(%d) tone = %q, want %q", c.code, got, c.tone)
		}
		if detail == "" {
			t.Errorf("classifyHTTPStatus(%d) detail empty", c.code)
		}
	}
}

// TestHTTPProbeTones drives the real HTTP probe against a test server so the
// status→tone mapping is verified end to end, including OK semantics: a 4xx or
// 5xx still reached a server, so OK stays true and Error stays empty (Error is
// reserved for unreachable-red transport failures).
func TestHTTPProbeTones(t *testing.T) {
	withLoopbackProbes(t)
	cases := []struct {
		code     int
		wantTone Tone
		wantOK   bool
	}{
		{200, ToneHealthy, true},
		{301, ToneReached, true},
		{404, ToneReached, true},
		{401, ToneReached, true},
		{500, ToneReached, true},  // app 5xx: reached (app health is not judged)
		{502, ToneDegraded, true}, // proxy could not reach upstream: a network-path fact
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := HTTP(ctx, srv.URL+"/", "", VantageLocal)
		cancel()
		srv.Close()
		if r.Tone != c.wantTone {
			t.Errorf("HTTP %d: tone = %q, want %q", c.code, r.Tone, c.wantTone)
		}
		if r.OK != c.wantOK {
			t.Errorf("HTTP %d: OK = %v, want %v", c.code, r.OK, c.wantOK)
		}
		if r.OK && r.Error != "" {
			t.Errorf("HTTP %d: reached but Error=%q (Error is for transport failures only)", c.code, r.Error)
		}
	}
}

// TestHTTPProbeUnreachable confirms a transport failure (nothing listening) is
// the only case that reads as unreachable: OK=false with an Error set.
func TestHTTPProbeUnreachable(t *testing.T) {
	withLoopbackProbes(t)
	// Reserve a port by opening then closing a server so the address is dead.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/"
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := HTTP(ctx, url, "", VantageLocal)
	if r.OK {
		t.Errorf("dead address: OK = true, want false")
	}
	if r.Error == "" {
		t.Errorf("dead address: Error empty, want a transport error")
	}
	if r.Tone == ToneDegraded || r.Tone == ToneReached {
		t.Errorf("dead address: tone = %q, want unhealthy/empty (a real unreachable)", r.Tone)
	}
}

func TestIsCertTrustError(t *testing.T) {
	if !isCertTrustError(errors.New("x509: certificate signed by unknown authority")) {
		t.Error("unknown-CA x509 should be a cert-trust error (reached, not unreachable)")
	}
	if !isCertTrustError(errors.New("x509: certificate is valid for foo, not bar")) {
		t.Error("hostname mismatch should be a cert-trust error")
	}
	if isCertTrustError(errors.New("dial tcp 10.0.0.1:443: connect: connection refused")) {
		t.Error("connection refused is a transport failure, not cert-trust")
	}
	if isCertTrustError(nil) {
		t.Error("nil is not a cert-trust error")
	}
}

// TestTLSProbe_UntrustedCertReached drives the REAL TLS probe against a server
// presenting an untrusted (self-signed) cert - a genuine x509 handshake, not a
// string mock. The honest result is "reached · cert not trusted from here"
// (OK=true, ToneReached), never a false unreachable: the TCP+TLS transport
// completed and a cert was presented, so we DID reach the endpoint.
func TestTLSProbe_UntrustedCertReached(t *testing.T) {
	withLoopbackProbes(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	r := TLS(ctx, addr, "example.com", VantageLocal)
	if !r.OK || r.Tone != ToneReached || r.Error != "" {
		t.Errorf("untrusted TLS: want OK=true tone=reached no-error, got OK=%v tone=%q err=%q", r.OK, r.Tone, r.Error)
	}
	h := HTTP(ctx, srv.URL+"/", "example.com", VantageLocal)
	if !h.OK || h.Tone != ToneReached || h.Error != "" {
		t.Errorf("untrusted HTTPS: want OK=true tone=reached no-error, got OK=%v tone=%q err=%q", h.OK, h.Tone, h.Error)
	}
}

// TestSSRFGuard_DeniesInternalTargets: the direct TCP/HTTP probes must refuse
// loopback and link-local (cloud metadata 169.254.169.254) targets, so a
// user-controlled Ingress host/Gateway address can't make Radar reach internal
// endpoints on their behalf. Private cluster ranges stay allowed.
func TestSSRFGuard_DeniesInternalTargets(t *testing.T) {
	for _, addr := range []string{"169.254.169.254:80", "127.0.0.1:80"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := TCP(ctx, addr, VantageLocal)
		cancel()
		if r.OK {
			t.Errorf("TCP(%s): want denied, got OK", addr)
		}
		if !strings.Contains(r.Error, "SSRF guard") {
			t.Errorf("TCP(%s): want SSRF-guard error, got %q", addr, r.Error)
		}
	}
	// Control hook itself: a private cluster IP is allowed (normal networking).
	if err := denyInternalControl("tcp", "10.0.0.5:80", nil); err != nil {
		t.Errorf("private cluster IP should be allowed, got %v", err)
	}
	// Unspecified addresses (0.0.0.0 / ::) route to localhost on a connect() - some
	// Gateway controllers report 0.0.0.0 in status.addresses, so the guard must deny
	// them too, or a probe reaches services on Radar's own localhost.
	for _, addr := range []string{"0.0.0.0:80", "[::]:80"} {
		if err := denyInternalControl("tcp", addr, nil); err == nil {
			t.Errorf("denyInternalControl(%s): want denied (unspecified routes to localhost), got nil", addr)
		}
	}
	// Cloud metadata endpoints that are not link-local: Alibaba IMDS (100.100.100.100,
	// CGNAT) and the IPv6 IMDS ULA (fd00:ec2::254) must be denied explicitly.
	for _, addr := range []string{"100.100.100.100:80", "[fd00:ec2::254]:80"} {
		if err := denyInternalControl("tcp", addr, nil); err == nil {
			t.Errorf("denyInternalControl(%s): want denied (cloud metadata), got nil", addr)
		}
	}
}

// TestIsBackendTimeout pins the slow-backend vs cluster-down distinction: a context
// deadline (slow/starting backend answered via the proxy) ALSO satisfies net.Error,
// so it matches isClusterUnreachable too - proxyResult must check isBackendTimeout
// FIRST so a slow app isn't blamed on the kubeconfig.
func TestIsBackendTimeout(t *testing.T) {
	if !isBackendTimeout(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must be a backend timeout")
	}
	if !isBackendTimeout(fmt.Errorf("proxy call failed: %w", context.DeadlineExceeded)) {
		t.Error("a wrapped deadline must be a backend timeout")
	}
	if isBackendTimeout(nil) {
		t.Error("nil is not a timeout")
	}
	if isBackendTimeout(errors.New("connection refused")) {
		t.Error("connection refused is not a backend timeout")
	}
	// The ordering guarantee: a deadline ALSO reads as cluster-unreachable, which is
	// exactly why proxyResult must test isBackendTimeout before isClusterUnreachable.
	if !isClusterUnreachable(context.DeadlineExceeded) {
		t.Error("a deadline also matches isClusterUnreachable (net.Error) - ordering matters")
	}
}

func TestIsAPIServerTunnelDown(t *testing.T) {
	// Unambiguous control-plane tunnel infra outages → skip (couldn't test), never condemn.
	for _, s := range []string{"no agent available", "Tunnel closed unexpectedly", "konnectivity: NO AGENT AVAILABLE"} {
		if !isAPIServerTunnelDown(errors.New(s)) {
			t.Errorf("%q should be a tunnel-down (skip), got false", s)
		}
	}
	// "error dialing backend" is AMBIGUOUS (tunnel OR a backend that refused through a
	// working tunnel) - it must NOT skip; it stays on the condemn path.
	if isAPIServerTunnelDown(errors.New("error dialing backend: connection refused")) {
		t.Error(`"error dialing backend" is ambiguous and must NOT be treated as tunnel-down`)
	}
	if isAPIServerTunnelDown(nil) {
		t.Error("nil is not a tunnel-down")
	}
}

// A successful TCP dial must name what it observed: an empty Detail leaves the
// route's evidence blank, which downstream renders as "no test has been run".
func TestTCP_SuccessCarriesDetail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()
	orig := dialControl
	dialControl = nil // the SSRF guard blocks loopback; this test is about the success Detail
	defer func() { dialControl = orig }()
	r := TCP(context.Background(), ln.Addr().String(), VantageLocal)
	if !r.OK {
		t.Fatalf("dial failed: %+v", r)
	}
	if r.Detail == "" {
		t.Error("a successful TCP probe must carry a Detail - empty evidence reads as 'nothing ran'")
	}
}

// A denied proxy dial never reached the workload, so it must SKIP rather than
// score. Driven through a real client against a real 403 because the branch
// depends on client-go's error typing, which a hand-built error does not
// reproduce faithfully.
func TestServiceProxy_PermissionDeniedSkipsRatherThanFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure",` +
			`"message":"services \"echo\" is forbidden: User \"radar\" cannot get resource \"services/proxy\" in API group \"\" in the namespace \"shop\"",` +
			`"reason":"Forbidden","code":403}`))
	}))
	defer srv.Close()

	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}

	r := ServiceProxy(context.Background(), client, "shop", "echo", 80, "/", VantageLocal)

	if !r.Skipped {
		t.Fatalf("a refused dial must skip, got a scored result: %+v", r)
	}
	if r.OK {
		t.Errorf("OK = true on a refused dial: %+v", r)
	}
	if r.SkipClass != SkipClassDenied {
		t.Errorf("SkipClass = %q, want %q", r.SkipClass, SkipClassDenied)
	}
	if !strings.Contains(r.Reason, "services/proxy") {
		t.Errorf("the reason must name the grant to ask for, got %q", r.Reason)
	}
	if r.Error != "" {
		t.Errorf("a permissions gap is not a probe error: %q", r.Error)
	}
}
