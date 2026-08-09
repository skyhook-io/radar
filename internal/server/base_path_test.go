package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/skyhook-io/radar/pkg/auth"
)

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty", raw: "", want: ""},
		{name: "root", raw: "/", want: ""},
		{name: "adds leading slash", raw: "radar", want: "/radar"},
		{name: "trims trailing slash", raw: "/radar/", want: "/radar"},
		{name: "nested path", raw: "/tools/radar/", want: "/tools/radar"},
		{name: "query rejected", raw: "/radar?x=1", wantErr: true},
		{name: "fragment rejected", raw: "/radar#top", wantErr: true},
		{name: "url rejected", raw: "https://example.com/radar", wantErr: true},
		{name: "route pattern rejected", raw: "/{tenant}/radar", wantErr: true},
		{name: "dot segment rejected", raw: "/radar/../api", wantErr: true},
		{name: "quote rejected", raw: `/rad"ar`, wantErr: true},
		{name: "angle bracket rejected", raw: "/rad<script>ar", wantErr: true},
		{name: "backslash rejected", raw: `/rad\ar`, wantErr: true},
		{name: "space rejected", raw: "/rad ar", wantErr: true},
		{name: "percent encoding rejected", raw: "/rad%2Far", wantErr: true},
		{name: "unreserved characters allowed", raw: "/radar-v2_1.0~beta", want: "/radar-v2_1.0~beta"},
		// Scheme-relative forms would turn the redirect Location into an
		// off-origin URL, so they must never normalize successfully.
		{name: "protocol-relative rejected", raw: "//evil.com", wantErr: true},
		{name: "backslash second position rejected", raw: `/\evil.com`, wantErr: true},
		{name: "double backslash rejected", raw: `\\evil.com`, wantErr: true},
		{name: "encoded protocol-relative rejected", raw: "/%2f%2fevil.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBasePath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeBasePath(%q) returned nil error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBasePath(%q) returned error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeBasePath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRewriteFrontendIndexInjectsBasePathRuntimeConfig(t *testing.T) {
	input := []byte(`<!doctype html>
<html>
  <head>
    <link rel="icon" href="/favicon.svg">
    <script type="module" crossorigin src="/assets/index.js"></script>
    <link rel="modulepreload" crossorigin href="./assets/vendor.js">
  </head>
  <body>
    <img src="/images/radar/radar-icon.svg">
  </body>
</html>`)

	got := string(rewriteFrontendIndex(input, "/radar"))
	for _, want := range []string{
		`window.__RADAR_RUNTIME_CONFIG__={"basePath":"/radar","apiBase":"/radar/api","assetBase":"/radar"};`,
		`href="/radar/favicon.svg"`,
		`src="/radar/assets/index.js"`,
		`href="/radar/assets/vendor.js"`,
		`src="/radar/images/radar/radar-icon.svg"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten index missing %q:\n%s", want, got)
		}
	}

	runtimeIdx := strings.Index(got, `window.__RADAR_RUNTIME_CONFIG__`)
	moduleIdx := strings.Index(got, `<script type="module"`)
	if runtimeIdx == -1 || moduleIdx == -1 || runtimeIdx > moduleIdx {
		t.Fatalf("runtime config must be emitted before module script:\n%s", got)
	}
}

func TestRewriteFrontendIndexRootLeavesHTMLUnchanged(t *testing.T) {
	input := []byte(`<html><head><script type="module" src="/assets/index.js"></script></head></html>`)
	got := rewriteFrontendIndex(input, "")
	if string(got) != string(input) {
		t.Fatalf("root base path should not rewrite index:\ngot  %s\nwant %s", got, input)
	}
}

func TestRewriteFrontendIndexRootMakesRelativeAssetsAbsolute(t *testing.T) {
	input := []byte(`<html><head><script type="module" src="./assets/index.js"></script><link rel="stylesheet" href="./assets/index.css"></head></html>`)
	got := string(rewriteFrontendIndex(input, ""))
	for _, want := range []string{
		`src="/assets/index.js"`,
		`href="/assets/index.css"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten root index missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `__RADAR_RUNTIME_CONFIG__`) {
		t.Fatalf("root base path should not inject runtime config:\n%s", got)
	}
}

func TestFrontendHandlerRewritesIndexFallbackButNotAssets(t *testing.T) {
	fsys := http.FS(fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)},
		"assets/index.js": &fstest.MapFile{Data: []byte(`console.log("ok")`)},
	})
	handler := frontendHandler(fsys, "/radar")

	indexResp := httptest.NewRecorder()
	handler.ServeHTTP(indexResp, httptest.NewRequest(http.MethodGet, "/topology", nil))
	if indexResp.Code != http.StatusOK {
		t.Fatalf("client-route fallback status = %d, want 200", indexResp.Code)
	}
	indexBody := indexResp.Body.String()
	if !strings.Contains(indexBody, `window.__RADAR_RUNTIME_CONFIG__={"basePath":"/radar","apiBase":"/radar/api","assetBase":"/radar"};`) {
		t.Fatalf("client-route fallback did not inject runtime config:\n%s", indexBody)
	}
	if !strings.Contains(indexBody, `src="/radar/assets/index.js"`) {
		t.Fatalf("client-route fallback did not rewrite asset src:\n%s", indexBody)
	}

	// Root-relative: basePathHandler strips the prefix before the frontend
	// handler sees the request.
	assetResp := httptest.NewRecorder()
	handler.ServeHTTP(assetResp, httptest.NewRequest(http.MethodGet, "/assets/index.js", nil))
	if assetResp.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", assetResp.Code)
	}
	assetBody, err := io.ReadAll(assetResp.Result().Body)
	if err != nil {
		t.Fatalf("read asset response: %v", err)
	}
	if string(assetBody) != `console.log("ok")` {
		t.Fatalf("asset body = %q", assetBody)
	}
}

func TestServerMountsRoutesUnderBasePath(t *testing.T) {
	srv := New(Config{DevMode: true, BasePath: "/radar"})

	rootResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rootResp, httptest.NewRequest(http.MethodGet, "/?namespace=default", nil))
	if rootResp.Code != http.StatusFound {
		t.Fatalf("root status = %d, want 302", rootResp.Code)
	}
	if got := rootResp.Header().Get("Location"); got != "/radar/?namespace=default" {
		t.Fatalf("root redirect Location = %q, want /radar/?namespace=default", got)
	}

	unprefixedResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unprefixedResp, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if unprefixedResp.Code != http.StatusNotFound {
		t.Fatalf("unprefixed API status = %d, want 404", unprefixedResp.Code)
	}

	prefixedResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(prefixedResp, httptest.NewRequest(http.MethodGet, "/radar/api/health", nil))
	if prefixedResp.Code != http.StatusOK {
		t.Fatalf("prefixed API status = %d, want 200", prefixedResp.Code)
	}

	bareResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bareResp, httptest.NewRequest(http.MethodGet, "/radar?namespace=default", nil))
	if bareResp.Code != http.StatusMovedPermanently {
		t.Fatalf("bare prefix status = %d, want 301", bareResp.Code)
	}
	if got := bareResp.Header().Get("Location"); got != "/radar/?namespace=default" {
		t.Fatalf("bare prefix Location = %q, want /radar/?namespace=default", got)
	}
}

// index.html asset URLs get re-rooted under the prefix, but only the ones that
// address this origin: a protocol-relative URL prefixed into "/radar//cdn…"
// becomes a local path that 404s, and it would break only under a base path.
func TestPrefixAttrPathsLeavesOtherOriginsAlone(t *testing.T) {
	in := `<link href="//cdn.example.com/a.css"><script src="https://cdn.example.com/b.js"></script>` +
		`<link href="./assets/c.css"><script src="/assets/d.js"></script>`

	underPrefix := prefixAttrPaths(in, "/radar")
	for _, want := range []string{
		`href="//cdn.example.com/a.css"`,     // other origin, untouched
		`src="https://cdn.example.com/b.js"`, // scheme-qualified, untouched
		`href="/radar/assets/c.css"`,         // Vite-relative, re-rooted
		`src="/radar/assets/d.js"`,           // root-absolute, re-rooted
	} {
		if !strings.Contains(underPrefix, want) {
			t.Errorf("under /radar: missing %s\ngot: %s", want, underPrefix)
		}
	}

	// At the root the relative form still has to become absolute so deep client
	// routes don't resolve assets against the current path.
	atRoot := prefixAttrPaths(in, "")
	for _, want := range []string{
		`href="//cdn.example.com/a.css"`,
		`href="/assets/c.css"`,
		`src="/assets/d.js"`,
	} {
		if !strings.Contains(atRoot, want) {
			t.Errorf("at root: missing %s\ngot: %s", want, atRoot)
		}
	}
}

// Both base-path redirects echo the request's query string, so their Location
// must stay same-origin no matter what the caller sends: a value that browsers
// read as scheme-relative ("//host", "/\host") would be an open redirect.
func TestBasePathRedirectsStaySameOrigin(t *testing.T) {
	hostileQueries := []string{
		"//evil.com", `/\evil.com`, "http://evil.com", "https:%2f%2fevil.com",
		"a=b&c=//evil.com", "%0d%0aSet-Cookie:+evil=1", "?=//evil.com",
	}

	for _, basePath := range []string{"/radar", "/tools/radar"} {
		srv := New(Config{DevMode: true, BasePath: basePath})
		for _, q := range hostileQueries {
			for _, target := range []string{"/?" + q, basePath + "?" + q} {
				resp := httptest.NewRecorder()
				srv.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))

				loc := resp.Header().Get("Location")
				if !strings.HasPrefix(loc, basePath+"/") {
					t.Errorf("GET %s: Location = %q, want it to start with %q", target, loc, basePath+"/")
					continue
				}
				// A Location the browser resolves against another origin always
				// begins "//" or "/\" — the prefix check above already rules
				// that out, but assert it directly so the intent is explicit.
				if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) {
					t.Errorf("GET %s: Location = %q is scheme-relative (open redirect)", target, loc)
				}
				if strings.ContainsAny(loc, "\r\n") {
					t.Errorf("GET %s: Location = %q contains CR/LF (header injection)", target, loc)
				}
			}
		}
	}
}

// A base path must not become an authentication bypass. chi's Mount leaves
// r.URL.Path prefixed and the auth middleware treats paths outside /api, /mcp
// and /debug as public static content, so any prefix left in place by the time
// the middleware runs makes every endpoint read as public — including the
// /debug/pprof heap dump of the whole informer cache.
func TestBasePathDoesNotBypassAuth(t *testing.T) {
	protected := []string{"/api/config", "/api/resources/pods", "/debug/pprof/heap", "/api/auth/whoami"}

	// Proxy mode only: the exemption logic under test lives in
	// auth.Authenticate and is mode-independent, and OIDC mode would need a
	// reachable issuer for discovery.
	for _, basePath := range []string{"", "/radar", "/tools/radar"} {
		srv := New(Config{
			DevMode:    true,
			BasePath:   basePath,
			AuthConfig: auth.Config{Mode: "proxy"},
		})
		for _, path := range protected {
			resp := httptest.NewRecorder()
			srv.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, basePath+path, nil))
			if resp.Code != http.StatusUnauthorized {
				t.Errorf("basePath=%q GET %s: status = %d, want 401 (unauthenticated request must be rejected)",
					basePath, basePath+path, resp.Code)
			}
		}

		// /api/health stays public for kubelet probes, under the prefix too.
		healthResp := httptest.NewRecorder()
		srv.Handler().ServeHTTP(healthResp, httptest.NewRequest(http.MethodGet, basePath+"/api/health", nil))
		if healthResp.Code != http.StatusOK {
			t.Errorf("basePath=%q: health status = %d, want 200", basePath, healthResp.Code)
		}
	}
}
