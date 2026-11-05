package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
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

func TestAuthorizeCurlReviewsCaller(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tokenMode bool
		noToken   bool
		system    bool
		groups    []string
	}{
		{name: "impersonation", groups: []string{"operators"}},
		{name: "no explicit groups"},
		{name: "authenticated group already present", groups: []string{"operators", "system:authenticated"}},
		{name: "token", tokenMode: true, groups: []string{"operators"}},
		{name: "token missing", tokenMode: true, noToken: true},
		{name: "system in token mode", tokenMode: true, system: true, noToken: true, groups: []string{"radar:system"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousMode := pkgauth.CurrentCredentialMode()
			mode := pkgauth.CredentialsImpersonate
			if tc.tokenMode {
				mode = pkgauth.CredentialsUserToken
			}
			pkgauth.SetCredentialMode(mode)
			t.Cleanup(func() { pkgauth.SetCredentialMode(previousMode) })
			calls := 0
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path, kind, token := "/apis/authorization.k8s.io/v1/subjectaccessreviews", "SubjectAccessReview", "Bearer sa-token"
				selfReview := tc.tokenMode && !tc.system
				if selfReview {
					path, kind, token = "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "SelfSubjectAccessReview", "Bearer alice-token"
				}
				if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get("Authorization") != token || r.Header.Get("Impersonate-User") != "" {
					t.Errorf("wrong review request: %s %s %v", r.Method, r.URL.Path, r.Header)
				}
				var review authv1.SubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if selfReview {
					if review.Spec.User != "" || len(review.Spec.Groups) != 0 {
						t.Errorf("SSAR supplied a subject: %+v", review.Spec)
					}
				} else {
					groups := slices.Clone(tc.groups)
					if !slices.Contains(groups, "system:authenticated") {
						groups = append(groups, "system:authenticated")
					}
					if review.Spec.User != "alice" || !slices.Equal(review.Spec.Groups, groups) {
						t.Errorf("wrong caller identity: %+v", review.Spec)
					}
				}
				if a := review.Spec.ResourceAttributes; a == nil || !reflect.DeepEqual(*a, authv1.ResourceAttributes{Namespace: "apps", Verb: "get", Resource: "services", Subresource: "proxy", Name: "demo"}) {
					t.Errorf("wrong review attributes: %+v", a)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": kind, "status": map[string]bool{"allowed": true}})
			}))
			defer api.Close()
			config := &rest.Config{Host: api.URL, BearerToken: "sa-token", TLSClientConfig: rest.TLSClientConfig{Insecure: true}, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}}
			previousConfig := k8s.SetTestConfig(config)
			defer k8s.SetTestConfig(previousConfig)
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previousClient)
			user := &auth.User{Username: "alice", Groups: tc.groups, System: tc.system}
			if !tc.noToken {
				user.Token = "alice-token"
			}
			request := requestWithUser(http.MethodPost, "/curl/service", user)
			allowed, err := (&Server{}).authorizeCurl(request.Context(), request, "apps", "demo")
			wantAllowed, wantCalls := true, 1
			if tc.tokenMode && !tc.system && tc.noToken {
				wantAllowed, wantCalls = false, 0
			}
			if err != nil || allowed != wantAllowed || calls != wantCalls {
				t.Fatalf("allowed=%v err=%v reviews=%d, want allowed=%v reviews=%d", allowed, err, calls, wantAllowed, wantCalls)
			}
		})
	}
}
