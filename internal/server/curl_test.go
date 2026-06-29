package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A curld Service must not be able to bounce the curl to a redirect target —
// don't chase a Service-controlled 3xx. curlDialClient must not follow redirects.
func TestCurlDialClientDoesNotFollowRedirects(t *testing.T) {
	var downstreamHits int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&downstreamHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, downstream.URL+"/elsewhere", http.StatusFound)
	}))
	defer proxy.Close()

	resp, err := curlDialClient().Get(proxy.URL + "/x")
	if err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected the 302 to be returned (not followed), got %d", resp.StatusCode)
	}
	if hits := atomic.LoadInt32(&downstreamHits); hits != 0 {
		t.Errorf("redirect was followed — downstream host hit %d time(s)", hits)
	}
}

func TestCurlNameValidation(t *testing.T) {
	valid := []string{"kube-system", "kube-dns", "my-svc", "a", "svc.with.dots", "argocd-server"}
	for _, s := range valid {
		if !curlNameRe.MatchString(s) {
			t.Errorf("expected %q to be a valid name", s)
		}
	}
	invalid := []string{"", "x/../../etc", "a:b", "UPPER", "-leading", "trailing-", "has space", "name/sub", "a%2f"}
	for _, s := range invalid {
		if curlNameRe.MatchString(s) {
			t.Errorf("expected %q to be rejected as a name", s)
		}
	}
}

func TestCurlPortValidation(t *testing.T) {
	valid := []string{"80", "9153", "443", "1", "65535"}
	for _, s := range valid {
		if !curlPortRe.MatchString(s) {
			t.Errorf("expected %q to be a valid port", s)
		}
	}
	// Direct dial needs a numeric host:port — named ports and anything that could
	// alter the dial target must be rejected.
	invalid := []string{"", "http", "web-ui", "80:proxy", "a/b", "8080a", "12.34", "123456"}
	for _, s := range invalid {
		if curlPortRe.MatchString(s) {
			t.Errorf("expected %q to be rejected as a port", s)
		}
	}
}
