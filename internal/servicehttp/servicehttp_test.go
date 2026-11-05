package servicehttp

import (
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"slices"

	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestServiceResponsePreservesFailureAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		truncated bool
	}{
		{"application error", http.StatusServiceUnavailable, "queue unavailable", false},
		{"large body", http.StatusOK, strings.Repeat("x", curlMaxBodyBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Impersonate-User") != "" {
					t.Error("Kubernetes identity reached application")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			result, err := request(t.Context(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.status || result.Truncated != tc.truncated || result.BodyBytes > curlMaxBodyBytes {
				t.Fatalf("result=%+v", result)
			}
			if !tc.truncated && result.Body != tc.body {
				t.Fatalf("lost failure body: %q", result.Body)
			}
		})
	}
}

func TestServiceResponseCancellationAfterHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result, err := request(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || result.Body != "partial" || result.Error == "" {
		t.Fatalf("partial response passed as complete: %+v", result)
	}
}

func TestServiceAuthorizationUsesCallerAndTarget(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			seen := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = true
				var review authv1.SubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
				}
				a := review.Spec.ResourceAttributes
				if review.Spec.User != "alice" || !slices.Contains(review.Spec.Groups, "team-a") || a == nil || a.Namespace != "team-a" || a.Name != "app" || a.Verb != "get" || a.Resource != "services" || a.Subresource != "proxy" {
					t.Errorf("wrong authorization: %+v", review.Spec)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{Kind: "SubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}, Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}})
			}))
			defer api.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			prev := k8s.SetTestClient(client)
			defer k8s.SetTestClient(prev)
			ctx := auth.ContextWithUser(t.Context(), &auth.User{Username: "alice", Groups: []string{"team-a"}})
			got, err := authorizeCurl(ctx, "team-a", "app")
			if err != nil || got != allowed || !seen {
				t.Fatalf("allowed=%v err=%v seen=%v", got, err, seen)
			}
		})
	}
}

func TestServiceAuthorizationNoAuthListener(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			seen := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = true
				var review authv1.SelfSubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
				}
				a := review.Spec.ResourceAttributes
				if a == nil || a.Namespace != "team-a" || a.Name != "app" || a.Resource != "services" || a.Subresource != "proxy" || a.Verb != "get" {
					t.Errorf("wrong self review: %+v", a)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}, Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}})
			}))
			defer api.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			prev := k8s.SetTestClient(client)
			defer k8s.SetTestClient(prev)
			got, err := authorizeCurl(t.Context(), "team-a", "app")
			if err != nil || got != allowed || !seen {
				t.Fatalf("serviceaccount check: %v %v %v", got, err, seen)
			}
		})
	}
}

func TestServiceFlowAuthorizationTargetAndResponse(t *testing.T) {
	priorMode := k8s.ForceInCluster
	k8s.ForceInCluster = true
	defer func() { k8s.ForceInCluster = priorMode }()
	for _, tc := range []struct {
		name        string
		allowed     bool
		serviceType corev1.ServiceType
		selector    map[string]string
		wantStatus  int
	}{
		{"selector", true, corev1.ServiceTypeClusterIP, map[string]string{"app": "fixture"}, 0},
		{"denied", false, corev1.ServiceTypeClusterIP, map[string]string{"app": "fixture"}, 403},
		{"alias", true, corev1.ServiceTypeExternalName, nil, 400},
		{"manual endpoints", true, corev1.ServiceTypeClusterIP, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{Kind: "SubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}, Status: authv1.SubjectAccessReviewStatus{Allowed: tc.allowed}})
			}))
			defer api.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			prev := k8s.SetTestClient(client)
			defer k8s.SetTestClient(prev)
			cacheClient := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}, Spec: corev1.ServiceSpec{Type: tc.serviceType, Selector: tc.selector, ExternalName: "api.team-b.svc.cluster.local", Ports: []corev1.ServicePort{{Port: 8080}}}})
			if err := k8s.InitTestResourceCache(cacheClient); err != nil {
				t.Fatal(err)
			}
			defer k8s.ResetResourceCache()
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Impersonate-User") != "" {
					t.Error("credentials reached app")
				}
				w.WriteHeader(503)
				fmt.Fprint(w, "application says unavailable")
			}))
			defer app.Close()
			calls := 0
			got, err := do(auth.ContextWithUser(t.Context(), &auth.User{Username: "alice"}), Request{Namespace: "team-a", Name: "app", Port: "8080", Path: "/health?full=true"}, func(ctx context.Context, target string) (Response, error) {
				calls++
				if target != "http://app.team-a.svc.cluster.local:8080/health?full=true" {
					t.Errorf("wrong target %s", target)
				}
				return request(ctx, app.URL+"/health?full=true")
			})
			if tc.wantStatus != 0 {
				failure, ok := err.(*Error)
				if !ok || failure.Status != tc.wantStatus || calls != 0 {
					t.Fatalf("refusal: %v calls=%d", err, calls)
				}
			} else if err != nil || got.Status != 503 || got.Body != "application says unavailable" || calls != 1 {
				t.Fatalf("response=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}
