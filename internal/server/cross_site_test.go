package server

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/cloud"
)

// A page on another site can send a POST without a preflight when the content
// type is one a form could produce, so these routes are reachable before CORS is
// ever consulted. Every state-changing route must refuse that.
func TestRequireSameOriginRefusesCrossSiteWrites(t *testing.T) {
	s := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.requireSameOrigin(next)

	for _, tc := range []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"cross-site POST as a browser sends it", "POST",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"}, 403},
		{"cross-site POST without fetch metadata", "POST",
			map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}, 403},
		{"split-origin host app without a tunnel marker", "POST",
			map[string]string{"Origin": "https://app.example.com", "Sec-Fetch-Site": "same-site"}, 403},
		{"sandboxed iframe sends a null Origin", "POST",
			map[string]string{"Origin": "null", "Content-Type": "text/plain"}, 403},
		{"same-origin write from Radar's own page", "POST",
			map[string]string{"Origin": "http://localhost:9280", "Sec-Fetch-Site": "same-origin"}, 200},
		{"script or AI client, no Origin at all", "POST", map[string]string{}, 200},
		{"cross-site DELETE", "DELETE",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site fetch GET", "GET",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site <img> GET, which carries no Origin", "GET",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"}, 403},
		{"cross-site HEAD", "HEAD", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"GET from another localhost port is same-site", "GET",
			map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"}, 403},
		{"same-origin GET from Radar's own page", "GET", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"GET typed into the address bar", "GET", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"GET from curl, no fetch metadata", "GET", map[string]string{}, 200},
		{"cross-site CORS preflight is still answered", "OPTIONS",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost:9280/api/resources/apply", strings.NewReader(""))
			r.Host = "localhost:9280"
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// The Hub forwards the browser's own Origin, which is not Radar's. Cloud writes
// are allowed on the strength of the tunnel marker instead, so that branch needs
// its own case: a bare cross-site POST is refused, the same POST over the tunnel
// is allowed.
func TestRequireSameOriginTrustsTheAuthenticatedTunnel(t *testing.T) {
	s := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	newReq := func() *http.Request {
		r := httptest.NewRequest("POST", "http://localhost:9280/api/resources/apply", strings.NewReader(""))
		r.Host = "localhost:9280"
		r.Header.Set("Origin", "https://app.skyhook.io")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		return r
	}

	w := httptest.NewRecorder()
	s.requireSameOrigin(next).ServeHTTP(w, newReq())
	if w.Code != http.StatusForbidden {
		t.Fatalf("without the tunnel marker: status = %d, want 403", w.Code)
	}

	w = httptest.NewRecorder()
	cloud.AuthenticatedTunnelHandler(s.requireSameOrigin(next)).ServeHTTP(w, newReq())
	if w.Code != http.StatusOK {
		t.Fatalf("over the authenticated tunnel: status = %d, want 200", w.Code)
	}
}

// A cross-site GET is still allowed over the Hub tunnel and from an origin the
// operator listed in trusted origins, the same two ways a write is.
func TestRequireSameOriginAllowsCrossSiteReadsFromTunnelAndTrustedOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	newReq := func(origin string) *http.Request {
		r := httptest.NewRequest("GET", "http://localhost:9280/api/resources/pods", nil)
		r.Host = "localhost:9280"
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	s := &Server{trustedOrigins: trustedOriginSet(t, "https://portal.example.com")}
	for _, tc := range []struct {
		name   string
		h      http.Handler
		origin string
		want   int
	}{
		{"untrusted origin", s.requireSameOrigin(next), "https://evil.example", 403},
		{"trusted origin", s.requireSameOrigin(next), "https://portal.example.com", 200},
		{"over the authenticated tunnel", cloud.AuthenticatedTunnelHandler(s.requireSameOrigin(next)), "https://app.skyhook.io", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.h.ServeHTTP(w, newReq(tc.origin))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// The guard is worthless if it stops being mounted. This walks the real route
// table and sends a cross-site request to every /api route, writes and reads,
// so a new route registered outside the /api group, or a reordered r.Use,
// fails here instead of silently going unprotected. It runs with and without a base path,
// because the base path mounts the same routes under a prefix.
//
// Routes outside /api must be listed in writesOutsideAPI or readsOutsideAPI
// with the reason they are safe without this guard. The MCP
// mounts are stubs here (internal/mcp imports this package); their own
// cross-origin check is pinned in internal/mcp. OIDC routes are not mounted
// in this config: /auth/backchannel-logout is called server to server by the
// identity provider and carries a signed token.
func TestAPIRoutesAreGuardedOnTheRealRouter(t *testing.T) {
	writesOutsideAPI := map[string]string{
		"/*":                               "frontend static files, read only for every method",
		"/.well-known/*":                   "always 404",
		"/mcp/.well-known/*":               "always 404",
		"/mcp-readonly/.well-known/*":      "always 404",
		"/mcp-investigation/.well-known/*": "always 404",
		"/mcp/*":                           "MCP, guarded by its own http.CrossOriginProtection",
		"/mcp-readonly/*":                  "MCP, guarded by its own http.CrossOriginProtection",
		"/mcp-investigation/*":             "MCP, guarded by its own http.CrossOriginProtection",
	}
	readsOutsideAPI := map[string]string{
		"/*":                               "frontend static files",
		"/.well-known/*":                   "always 404",
		"/mcp/.well-known/*":               "always 404",
		"/mcp-readonly/.well-known/*":      "always 404",
		"/mcp-investigation/.well-known/*": "always 404",
		"/mcp/*":                           "MCP, answers GET with nothing a page can use",
		"/mcp-readonly/*":                  "MCP, answers GET with nothing a page can use",
		"/mcp-investigation/*":             "MCP, answers GET with nothing a page can use",
		"/metrics":                         "Prometheus scrape, no side effects",
		"/.well-known/oauth-authorization-server": "OAuth metadata for MCP clients, static",
		"/.well-known/oauth-protected-resource":   "OAuth metadata for MCP clients, static",
		"/debug/pprof/":                           "local profiling, not mounted in cloud mode",
	}
	// A key ending in "/" covers every route under it.
	listed := func(m map[string]string, route string) bool {
		if _, ok := m[route]; ok {
			return true
		}
		for k := range m {
			if strings.HasSuffix(k, "/") && strings.HasPrefix(route, k) {
				return true
			}
		}
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	param := regexp.MustCompile(`\{[^}]*\}`)

	for _, basePath := range []string{"", "/radar"} {
		srv := New(Config{DevMode: true, BasePath: basePath, MCPHandler: stub, MCPReadOnlyHandler: stub, MCPInvestigationHandler: stub})
		ts := httptest.NewServer(srv.Handler())
		// Under a base path the app routes sit behind a wrapping handler that
		// chi.Walk cannot see into, so walk a copy of the same route table and
		// send the requests through the real server.
		routes := chi.NewRouter()
		srv.setupAppRoutes(routes)

		guarded, guardedReads := 0, 0
		err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			read := false
			switch method {
			case http.MethodOptions, http.MethodConnect, http.MethodTrace:
				return nil
			case http.MethodGet, http.MethodHead:
				read = true
			}
			if !strings.HasPrefix(route, "/api/") {
				if !read && !listed(writesOutsideAPI, route) {
					t.Errorf("%s %s accepts writes outside /api, so requireSameOrigin does not cover it; move it under /api or list it in writesOutsideAPI with a reason", method, route)
				}
				if read && !listed(readsOutsideAPI, route) {
					t.Errorf("%s %s is a read outside /api, so requireSameOrigin does not cover it; move it under /api or list it in readsOutsideAPI with a reason", method, route)
				}
				return nil
			}
			url := ts.URL + basePath + param.ReplaceAllString(strings.ReplaceAll(route, "*", "x"), "x")
			var reqBody io.Reader
			if !read {
				reqBody = strings.NewReader("{}")
			}
			req, err := http.NewRequest(method, url, reqBody)
			if err != nil {
				return err
			}
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			if read {
				// As an <img> or link on another site sends it: no Origin.
				req.Header.Set("Sec-Fetch-Mode", "no-cors")
			} else {
				req.Header.Set("Origin", "https://evil.example")
				req.Header.Set("Content-Type", "text/plain")
			}
			// A refusal answers at once. Without the guard, a GET reaches the
			// real handler, and the streaming ones never finish, so a broken
			// guard must fail here instead of hanging the test.
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("base path %q: cross-site %s %s: %v, want the cross-origin refusal", basePath, method, route, err)
				return nil
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "cross_origin_refused") {
				t.Errorf("base path %q: cross-site %s %s got %d %s, want the cross-origin refusal", basePath, method, route, resp.StatusCode, body)
			}
			if read {
				guardedReads++
			} else {
				guarded++
			}
			return nil
		})
		// When the guard is broken, some handlers that were reached keep
		// running after their client gave up, and Close would wait for them
		// forever. Report the failures instead.
		ts.CloseClientConnections()
		if !t.Failed() {
			ts.Close()
		}
		srv.Stop()
		if err != nil {
			t.Fatalf("base path %q: walk: %v", basePath, err)
		}
		// Sanity floor: if the walk stops seeing the API, the test must not pass
		// by checking nothing.
		if guarded < 80 {
			t.Errorf("base path %q: only %d state-changing /api routes found, want at least 80", basePath, guarded)
		}
		if guardedReads < 150 {
			t.Errorf("base path %q: only %d read /api routes found, want at least 150", basePath, guardedReads)
		}
	}
}

// An exemption lets any website make that call, so the reason has to be written
// down next to it.
func TestCrossSiteExemptRoutesCarryAReason(t *testing.T) {
	for _, e := range crossSiteExemptRoutes {
		if e.why == "" {
			t.Fatalf("exempt route %s %s carries no reason", e.method, e.prefix)
		}
	}
}

// A prefix must not leak past a path segment, or exempting a webhook would also
// exempt a neighbouring admin route.
func TestCrossSiteExemptMatchesWholeSegments(t *testing.T) {
	original := crossSiteExemptRoutes
	t.Cleanup(func() { crossSiteExemptRoutes = original })
	crossSiteExemptRoutes = []struct {
		method string
		prefix string
		why    string
	}{{method: "POST", prefix: "/api/hooks", why: "test"}}

	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/api/hooks", true},
		{"/api/hooks/github", true},
		{"/api/hooks-admin/delete", false},
		{"/api/hooksomething", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost:9280"+tc.path, nil)
			if got := crossSiteExempt(r); got != tc.want {
				t.Fatalf("crossSiteExempt(%s) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// Deployment shapes Radar cannot see into. Plain HTTP on a LAN address gets no
// fetch metadata, so Origin against Host is the only signal; a proxy that
// rewrites Host needs the operator to trust the address instead.
func TestBrowserOriginAllowedAcrossDeploymentShapes(t *testing.T) {
	trusting := New(Config{TrustedOrigins: []string{"https://portal.example.com", "http://radar.internal"}})
	defer trusting.Stop()
	for _, tc := range []struct {
		name    string
		server  *Server
		host    string
		headers map[string]string
		want    bool
	}{
		{"plain HTTP on a LAN address, no fetch metadata", &Server{}, "10.0.0.5:9280",
			map[string]string{"Origin": "http://10.0.0.5:9280"}, true},
		{"proxy rewrote Host, nothing trusted", &Server{}, "radar.radar.svc:9280",
			map[string]string{"Origin": "http://radar.internal"}, false},
		{"proxy rewrote Host, address trusted", trusting, "radar.radar.svc:9280",
			map[string]string{"Origin": "http://radar.internal"}, true},
		{"user-initiated request (Sec-Fetch-Site none)", &Server{}, "localhost:9280",
			map[string]string{"Origin": "chrome-extension://abc", "Sec-Fetch-Site": "none"}, true},
		{"trusted origin, even cross-site", trusting, "radar.example.com",
			map[string]string{"Origin": "https://portal.example.com", "Sec-Fetch-Site": "cross-site"}, true},
		{"trusted origin matches with an explicit default port", trusting, "radar.example.com",
			map[string]string{"Origin": "https://Portal.Example.com:443"}, true},
		{"trusted list does not admit a neighbour", trusting, "radar.example.com",
			map[string]string{"Origin": "https://evil.example.com", "Sec-Fetch-Site": "cross-site"}, false},
		{"trusted https origin does not admit its http twin", trusting, "radar.example.com",
			map[string]string{"Origin": "http://portal.example.com", "Sec-Fetch-Site": "cross-site"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/resources/apply", nil)
			r.Host = tc.host
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := tc.server.browserOriginAllowed(r); got != tc.want {
				t.Fatalf("browserOriginAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The only person who reads this 403 is an operator whose proxy hid Radar's
// address, and it reaches them as the detail of the failed action. It has to
// carry a code the UI can branch on and name the fix.
func TestRefusedWriteExplainsTheFix(t *testing.T) {
	h := (&Server{}).requireSameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodDelete, "/api/resources/ConfigMap/default/x", nil)
	r.Host = "radar.radar.svc:9280"
	r.Header.Set("Origin", "http://radar.internal")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var body struct {
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "cross_origin_refused" {
		t.Fatalf("error_code = %q", body.ErrorCode)
	}
	for _, want := range []string{"http://radar.internal", "radar.radar.svc:9280", "Host header", "trustedOrigins", "RADAR_TRUSTED_ORIGINS", "--trusted-origins"} {
		if !strings.Contains(body.Error, want) {
			t.Fatalf("message %q does not mention %q", body.Error, want)
		}
	}
}

func TestParseTrustedOrigins(t *testing.T) {
	got, err := ParseTrustedOrigins(" https://radar.example.com , http://10.0.0.5:9280,,http://[::1]:9280")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, " ") != "https://radar.example.com http://10.0.0.5:9280 http://[::1]:9280" {
		t.Fatalf("got %v", got)
	}

	for _, bad := range []string{
		"*",
		"https://*.example.com",
		"radar.example.com",
		"ftp://radar.example.com",
		"https://radar.example.com/radar",
		"https://user@radar.example.com",
		"https://radar.example.com?x=1",
		"https://radar.example.com:99999",
		"null",
	} {
		if got, err := ParseTrustedOrigins(bad); err == nil || len(got) != 0 {
			t.Fatalf("ParseTrustedOrigins(%q) = %v, %v; want the entry refused", bad, got, err)
		}
	}

	// One bad entry costs that entry only; the good ones still apply.
	got, err = ParseTrustedOrigins("https://*.example.com,https://radar.example.com")
	if err == nil || !strings.Contains(err.Error(), "*.example.com") {
		t.Fatalf("want the wildcard entry reported, got %v", err)
	}
	if len(got) != 1 || got[0] != "https://radar.example.com" {
		t.Fatalf("want the valid entry kept, got %v", got)
	}
}

// The shared-listener lane must work for the browser it exists for: a page
// served from the same non-loopback authority is same-origin and must pass,
// while a genuinely foreign origin must not.
func TestBrowserOriginAllowedAcceptsTheServingAuthority(t *testing.T) {
	cases := []struct {
		name, host, origin string
		devMode            bool
		fetchSite          string
		want               bool
	}{
		{name: "no origin (non-browser)", host: "10.0.0.5:9280", want: true},
		{name: "same non-loopback authority", host: "10.0.0.5:9280", origin: "http://10.0.0.5:9280", want: true},
		{name: "hostname case is ignored", host: "Radar.Example.com:9280", origin: "http://radar.example.com:9280", want: true},
		{name: "same loopback authority", host: "127.0.0.1:9280", origin: "http://127.0.0.1:9280", want: true},
		{name: "vite dev proxy, loopback to loopback", host: "localhost:9280", origin: "http://localhost:9273", devMode: true, want: true},
		{name: "vite port outside dev mode", host: "localhost:9280", origin: "http://localhost:9273", want: false},
		{name: "unrelated port in dev mode", host: "localhost:9280", origin: "http://localhost:9274", devMode: true, want: false},
		{name: "bracketed IPv6 Vite proxy", host: "[::1]", origin: "http://[::1]:9273", devMode: true, want: true},
		{name: "foreign origin", host: "10.0.0.5:9280", origin: "http://evil.example", want: false},
		{name: "lookalike hostname", host: "10.0.0.5:9280", origin: "http://localhost.evil.com", want: false},
		{name: "different port on the same non-loopback host", host: "10.0.0.5:9280", origin: "http://10.0.0.5:9999", want: false},
		{name: "loopback origin against a non-loopback host", host: "10.0.0.5:9280", origin: "http://127.0.0.1:9280", want: false},
		{name: "cross-site metadata without origin", host: "10.0.0.5:9280", fetchSite: "cross-site", want: false},
		{name: "same-origin metadata survives rewritten host", host: "internal:9280", origin: "https://radar.example.com", fetchSite: "same-origin", want: true},
		{name: "unparseable origin", host: "10.0.0.5:9280", origin: "://nope", want: false},
		{name: "opaque (null) origin", host: "10.0.0.5:9280", origin: "null", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/cloud/install/prepare", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			s := &Server{devMode: tc.devMode}
			if got := s.browserOriginAllowed(r); got != tc.want {
				t.Fatalf("browserOriginAllowed(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func TestBrowserOriginAllowedRejectsSchemeDowngrade(t *testing.T) {
	cases := []struct {
		name           string
		tls            bool
		forwardedProto string
		origin         string
		want           bool
	}{
		{"https request, http origin (downgrade)", true, "", "http://10.0.0.5:9280", false},
		{"https request, https origin", true, "", "https://10.0.0.5:9280", true},
		{"forwarded https, http origin (downgrade)", false, "https", "http://10.0.0.5:9280", false},
		{"forwarded https, https origin", false, "https", "https://10.0.0.5:9280", true},
		{"plain http request, http origin", false, "", "http://10.0.0.5:9280", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/cloud/install/prepare", nil)
			r.Host = "10.0.0.5:9280"
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.forwardedProto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwardedProto)
			}
			r.Header.Set("Origin", tc.origin)
			if got := (&Server{}).browserOriginAllowed(r); got != tc.want {
				t.Fatalf("browserOriginAllowed(tls=%v xfp=%q origin=%q) = %v, want %v", tc.tls, tc.forwardedProto, tc.origin, got, tc.want)
			}
		})
	}
}
