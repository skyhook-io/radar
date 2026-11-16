package server

import (
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
		// A chunk an older build named. The page answers it, and must not be
		// cached under the chunk's URL.
		{"/assets/monaco-OldHash1.js", "no-cache", true},
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
