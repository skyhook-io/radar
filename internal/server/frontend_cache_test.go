package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
			{"Range", "bytes=999-"},
			{"Range", "bytes=0-3"},
		} {
			req := httptest.NewRequest(http.MethodGet, "/assets/index-BcIBzLE5.js", nil)
			req.Header.Set(h.name, h.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("%s %s: status = 200, want a partial or failed answer", h.name, h.value)
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
	handler := compressMiddleware()(frontendHandler(http.FS(fstest.MapFS{
		"index.html":               &fstest.MapFile{Data: []byte(`<!doctype html>`)},
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
			if n := varyCount(rec); n != 1 {
				t.Errorf("%s: Vary lists Accept-Encoding %d times (%q), want once", name, n, rec.Header().Values("Vary"))
			}
			if rec.Header().Get("Cache-Control") != "max-age=31536000, immutable" {
				t.Errorf("%s: Cache-Control = %q", name, rec.Header().Get("Cache-Control"))
			}
		}
	})

	t.Run("a range is the requested bytes of the file", func(t *testing.T) {
		rec := get("/assets/index-BcIBzLE5.js", "Accept-Encoding", "gzip", "Range", "bytes=0-6")
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want none", got)
		}
		if got, want := rec.Header().Get("Content-Range"), "bytes 0-6/"+strconv.Itoa(len(script)); got != want {
			t.Fatalf("Content-Range = %q, want %q", got, want)
		}
		if rec.Body.String() != script[:7] {
			t.Fatalf("body = %q, want %q", rec.Body.String(), script[:7])
		}
		if n := varyCount(rec); n != 1 {
			t.Errorf("Vary lists Accept-Encoding %d times, want once", n)
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
