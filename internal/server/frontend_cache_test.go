package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFrontendHandlerCacheHeaders(t *testing.T) {
	fsys := http.FS(fstest.MapFS{
		"index.html":                       &fstest.MapFile{Data: []byte(`<!doctype html><script type="module" src="./assets/index-BcIBzLE5.js"></script>`)},
		"assets/index-BcIBzLE5.js":         &fstest.MapFile{Data: []byte(`console.log("ok")`)},
		"assets/index-BcIBzLE5.css":        &fstest.MapFile{Data: []byte(`body{}`)},
		"assets/editor.worker-Bm_d-HZ0.js": &fstest.MapFile{Data: []byte(`self.onmessage=null`)},
		"assets/skyhook/logo.svg":          &fstest.MapFile{Data: []byte(`<svg/>`)},
		"favicon.svg":                      &fstest.MapFile{Data: []byte(`<svg/>`)},
	})
	handler := frontendHandler(fsys, "")

	cases := []struct {
		path         string
		cacheControl string
		html         bool
	}{
		{"/assets/index-BcIBzLE5.js", "max-age=31536000, immutable", false},
		{"/assets/index-BcIBzLE5.css", "max-age=31536000, immutable", false},
		{"/assets/editor.worker-Bm_d-HZ0.js", "max-age=31536000, immutable", false},
		// Copied from web/public under a fixed name: a new build can change it.
		{"/assets/skyhook/logo.svg", "", false},
		{"/favicon.svg", "", false},
		{"/", "no-cache", true},
		{"/index.html", "no-cache", true},
		{"/topology", "no-cache", true},
	}
	t.Run("only a complete answer is cached", func(t *testing.T) {
		for _, h := range []struct{ name, value string }{
			{"If-Match", `"another-version"`},
		} {
			req := httptest.NewRequest(http.MethodGet, "/assets/index-BcIBzLE5.js", nil)
			req.Header.Set(h.name, h.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("%s %s: status = 200, want a failed answer", h.name, h.value)
			}
			if got := rec.Header().Get("Cache-Control"); got != "" {
				t.Errorf("%s: %d response Cache-Control = %q, want none", h.name, rec.Code, got)
			}
		}
	})

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("GET %s Cache-Control = %q, want %q", tc.path, got, tc.cacheControl)
		}
		if isHTML := strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"); isHTML != tc.html {
			t.Errorf("GET %s served HTML = %v, want %v", tc.path, isHTML, tc.html)
		}
	}
}

// The handler as Radar serves it, behind the compression middleware.
func TestFrontendThroughCompression(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "")
	script := strings.Repeat(`console.log("radar");`, 100)
	page := `<!doctype html><title>Radar</title>`
	handler := compressMiddleware()(frontendHandler(http.FS(fstest.MapFS{
		"index.html":               &fstest.MapFile{Data: []byte(page)},
		"assets/index-BcIBzLE5.js": &fstest.MapFile{Data: []byte(script)},
		"assets/skyhook/logo.svg":  &fstest.MapFile{Data: []byte(`<svg/>`)},
	}), ""))
	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	varyCount := func(rec *httptest.ResponseRecorder) int {
		n := 0
		for _, v := range rec.Header().Values("Vary") {
			for _, f := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
					n++
				}
			}
		}
		return n
	}

	t.Run("compressed and uncompressed copies both declare Vary", func(t *testing.T) {
		gz := get("/assets/index-BcIBzLE5.js", "Accept-Encoding", "gzip")
		if gz.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("gzip request: Content-Encoding = %q", gz.Header().Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(gz.Body)
		if err != nil {
			t.Fatal(err)
		}
		if body, _ := io.ReadAll(zr); string(body) != script {
			t.Fatalf("gzip body does not decode to the asset")
		}
		plain := get("/assets/index-BcIBzLE5.js")
		if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != script {
			t.Fatalf("identity request: Content-Encoding %q, body length %d", plain.Header().Get("Content-Encoding"), plain.Body.Len())
		}
		for name, rec := range map[string]*httptest.ResponseRecorder{"gzip": gz, "identity": plain} {
			if varyCount(rec) == 0 {
				t.Errorf("%s: Vary = %q, want Accept-Encoding", name, rec.Header().Values("Vary"))
			}
			if rec.Header().Get("Cache-Control") != "max-age=31536000, immutable" {
				t.Errorf("%s: Cache-Control = %q", name, rec.Header().Get("Cache-Control"))
			}
		}
	})

	t.Run("a request that skips the compressor still declares Vary", func(t *testing.T) {
		rec := get("/assets/index-BcIBzLE5.js", "Accept-Encoding", "gzip", "Accept", "text/event-stream")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("status %d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if varyCount(rec) == 0 {
			t.Errorf("Vary = %q, want Accept-Encoding", rec.Header().Values("Vary"))
		}
	})

	t.Run("a range request gets the whole response, compressed", func(t *testing.T) {
		for path, want := range map[string]string{
			"/assets/index-BcIBzLE5.js": script,
			"/":                         page,
			"/index.html":               page,
			"/topology":                 page,
		} {
			rec := get(path, "Accept-Encoding", "gzip", "Range", "bytes=0-6")
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Range") != "" {
				t.Errorf("%s: status %d, Content-Range %q, want the whole response", path, rec.Code, rec.Header().Get("Content-Range"))
				continue
			}
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Errorf("%s: not gzip: %v", path, err)
				continue
			}
			if body, _ := io.ReadAll(zr); string(body) != want {
				t.Errorf("%s: body = %q, want %q", path, body, want)
			}
		}
		plain := get("/assets/index-BcIBzLE5.js", "Range", "bytes=0-6")
		if plain.Code != http.StatusOK || plain.Header().Get("Content-Range") != "" || plain.Body.String() != script {
			t.Errorf("uncompressed range request: status %d, Content-Range %q, body length %d", plain.Code, plain.Header().Get("Content-Range"), plain.Body.Len())
		}
		if missing := get("/assets/monaco-OldHash1.js", "Accept-Encoding", "gzip", "Range", "bytes=0-6"); missing.Code != http.StatusNotFound {
			t.Errorf("missing asset with Range: status %d, want 404", missing.Code)
		}
	})

	t.Run("an API response sent with a Range header is still compressed", func(t *testing.T) {
		big := strings.Repeat(`{"k":"v"},`, 500)
		api := compressMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "["+big+"{}]")
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Range", "bytes=0-6")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Range") != "" || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("status %d, Content-Range %q, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Range"), rec.Header().Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		if body, err := io.ReadAll(zr); err != nil || string(body) != "["+big+"{}]" {
			t.Fatalf("body does not decode to the response (%v)", err)
		}
	})

	t.Run("a missing asset is 404, not the page", func(t *testing.T) {
		for _, path := range []string{"/assets/monaco-OldHash1.js", "/assets/skyhook", "/assets/"} {
			rec := get(path, "Accept-Encoding", "gzip")
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404", path, rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
			}
			if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Errorf("%s: served HTML", path)
			}
		}
		if rec := get("/topology"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("client route: status %d, Content-Type %q, want the page", rec.Code, rec.Header().Get("Content-Type"))
		}
	})
}
