package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	radarremotecommand "github.com/skyhook-io/radar/pkg/remotecommand"
)

type recordedRequest struct {
	method, path, authorization string
	impersonation               []string
}

// recordingAPIServer answers every request and records the credentials it
// arrived with. SubjectAccessReviews are allowed only for verbs in allow.
func recordingAPIServer(t *testing.T, allow map[string]bool) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "impersonate-") {
				rec.impersonation = append(rec.impersonation, k)
			}
		}
		mu.Lock()
		seen = append(seen, rec)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
			var sar authv1.SubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&sar)
			verb := ""
			if a := sar.Spec.ResourceAttributes; a != nil {
				verb = a.Verb + " " + a.Resource
			}
			sar.Status.Allowed = allow[verb]
			_ = json.NewEncoder(w).Encode(sar)
			return
		}
		_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","items":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), seen...)
	}
}

func withCredentialMode(t *testing.T, m pkgauth.CredentialMode) {
	t.Helper()
	pkgauth.SetCredentialMode(m)
	t.Cleanup(func() { pkgauth.SetCredentialMode(pkgauth.CredentialsImpersonate) })
}

func listPods(t *testing.T, cfg *rest.Config) error {
	t.Helper()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().Pods("shop").List(context.Background(), metav1.ListOptions{})
	return err
}

func TestUserRESTConfig_ImpersonateModeUnchanged(t *testing.T) {
	srv, seen := recordingAPIServer(t, nil)
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token"}
	if err := listPods(t, UserRESTConfig(base, &pkgauth.User{Username: "alice", Groups: []string{"radar:owner"}})); err != nil {
		t.Fatal(err)
	}
	got := seen()
	if len(got) != 1 || got[0].authorization != "Bearer sa-token" || len(got[0].impersonation) == 0 {
		t.Fatalf("impersonate mode request = %+v", got)
	}
}

func TestUserRESTConfig_TokenUserSendsOnlyTheirToken(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	srv, seen := recordingAPIServer(t, nil)
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token", BearerTokenFile: "/var/run/secrets/does-not-exist"}
	user := &pkgauth.User{Username: "oidc:alice", Groups: []string{"admins"}, Token: "alice-token"}
	if err := listPods(t, UserRESTConfig(base, user)); err != nil {
		t.Fatal(err)
	}
	got := seen()
	if len(got) != 1 {
		t.Fatalf("requests = %+v", got)
	}
	if got[0].authorization != "Bearer alice-token" || len(got[0].impersonation) != 0 {
		t.Fatalf("token user request = %+v, want only the user's bearer token", got[0])
	}
}

func TestUserRESTConfig_HumanWithoutTokenMakesNoLiveCall(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	srv, seen := recordingAPIServer(t, nil)
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token"}
	err := listPods(t, UserRESTConfig(base, &pkgauth.User{Username: "oidc:alice", Groups: []string{"admins"}}))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want 403", err)
	}
	if status, ok := err.(apierrors.APIStatus); !ok || string(status.Status().Reason) != pkgauth.UserTokenRequiredReason {
		t.Fatalf("reason = %v, want %s", err, pkgauth.UserTokenRequiredReason)
	}
	if !IsUserTokenRequired(err.Error()) {
		t.Fatalf("IsUserTokenRequired(%q) = false", err.Error())
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("request reached the API server: %+v", got)
	}
}

func TestUserRESTConfig_SystemIdentityReviewedBeforeEachCall(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	srv, seen := recordingAPIServer(t, map[string]bool{"list pods": true})
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	system := &pkgauth.User{Username: "radar:system:alerts:o1", Groups: []string{"radar:system"}, System: true}

	if err := listPods(t, UserRESTConfig(base, system)); err != nil {
		t.Fatalf("allowed list: %v", err)
	}
	got := seen()
	if len(got) != 2 || !strings.HasSuffix(got[0].path, "/subjectaccessreviews") || got[1].path != "/api/v1/namespaces/shop/pods" {
		t.Fatalf("requests = %+v, want SAR then list", got)
	}
	for _, r := range got {
		if r.authorization != "Bearer sa-token" || len(r.impersonation) != 0 {
			t.Fatalf("system lane request = %+v, want the SA token without impersonation", r)
		}
	}

	client, _ := kubernetes.NewForConfig(UserRESTConfig(base, system))
	_, err := client.CoreV1().Secrets("shop").Get(context.Background(), "db", metav1.GetOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("denied get secrets: err = %v, want 403", err)
	}
	for _, r := range seen()[2:] {
		if strings.Contains(r.path, "/secrets") {
			t.Fatalf("denied request reached the API server: %+v", r)
		}
	}
}

func TestUserRESTConfig_RefusedExecReturnsErrorInsteadOfPanicking(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	srv, seen := recordingAPIServer(t, nil)
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	execURL, _ := url.Parse(srv.URL + "/api/v1/namespaces/shop/pods/web/exec?command=sh&stdin=true&stdout=true&tty=true")
	for name, user := range map[string]*pkgauth.User{
		"no token":      {Username: "oidc:alice", Groups: []string{"admins"}},
		"denied system": {Username: "radar:ai:o1", Groups: []string{"radar:ai:reader"}, System: true},
	} {
		exec, err := radarremotecommand.NewExecutor(UserRESTConfig(base, user), execURL)
		if err != nil {
			t.Fatal(err)
		}
		err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{Stdin: strings.NewReader(""), Stdout: io.Discard, Tty: true})
		if !apierrors.IsForbidden(err) {
			t.Errorf("%s: exec err = %v, want 403", name, err)
		}
	}
	for _, r := range seen() {
		if strings.Contains(r.path, "/exec") {
			t.Fatalf("refused exec reached the API server: %+v", r)
		}
	}
}

func TestUserRESTConfig_SystemLaneReadsOnlyAsAuthenticated(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	var mu sync.Mutex
	var specs []authv1.SubjectAccessReviewSpec
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
			var sar authv1.SubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&sar)
			mu.Lock()
			specs = append(specs, sar.Spec)
			mu.Unlock()
			sar.Status.Allowed = true
			_ = json.NewEncoder(w).Encode(sar)
			return
		}
		mu.Lock()
		reached = append(reached, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"major":"1","minor":"36","gitVersion":"v1.36.1"}`))
	}))
	t.Cleanup(srv.Close)
	base := &rest.Config{Host: srv.URL + "/k8s/clusters/c-1", BearerToken: "sa-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	system := &pkgauth.User{Username: "radar:system:alerts:o1", Groups: []string{"radar:system"}, System: true}

	client, _ := kubernetes.NewForConfig(UserRESTConfig(base, system))
	if _, err := client.Discovery().ServerVersion(); err != nil {
		t.Fatalf("/version through a proxy path: %v", err)
	}
	if err := client.CoreV1().Pods("shop").Delete(context.Background(), "web", metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("system delete: err = %v, want 403 without asking", err)
	}
	exec, _ := radarremotecommand.NewExecutor(UserRESTConfig(base, system), mustURL(t, srv.URL+"/k8s/clusters/c-1/api/v1/namespaces/shop/pods/web/exec?command=sh"))
	if err := exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{Stdout: io.Discard}); !apierrors.IsForbidden(err) {
		t.Fatalf("system exec: err = %v, want 403", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(specs) != 1 {
		t.Fatalf("reviews = %+v, want only the /version read reviewed", specs)
	}
	v := specs[0]
	if v.NonResourceAttributes == nil || v.NonResourceAttributes.Path != "/version" || !slices.Contains(v.Groups, "system:authenticated") {
		t.Fatalf("/version review = %+v, want a non-resource review as an authenticated user", v)
	}
	for _, r := range reached {
		if !strings.HasSuffix(r, "/version") {
			t.Fatalf("a write or exec reached the API server: %s", r)
		}
	}
}

func TestUserRESTConfig_SystemReviewOutageIs503(t *testing.T) {
	withCredentialMode(t, pkgauth.CredentialsUserToken)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "etcd is sad", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	base := &rest.Config{Host: srv.URL, BearerToken: "sa-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	system := &pkgauth.User{Username: "radar:system:x", Groups: []string{"radar:system"}, System: true}
	err := listPods(t, UserRESTConfig(base, system))
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("err = %v, want 503 so an outage isn't read as denied", err)
	}
}

func TestReviewUserToken_SendsOnlyTheTokenAndCaches(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"username":"oidc:alice","uid":"u1","groups":["oidc:admins","system:authenticated"],"extra":{"k":["v"]}}}}`))
	}))
	t.Cleanup(srv.Close)
	swapGlobals(t, &rest.Config{Host: srv.URL, BearerToken: "sa-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}})

	for i := 0; i < 2; i++ {
		u, err := ReviewUserToken(context.Background(), "alice-token")
		if err != nil {
			t.Fatal(err)
		}
		if u.Username != "oidc:alice" || u.UID != "u1" || !slices.Contains(u.Groups, "oidc:admins") || u.Extra["k"][0] != "v" {
			t.Fatalf("review = %+v", u)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 1 || auths[0] != "Bearer alice-token" {
		t.Fatalf("API calls = %v, want one SelfSubjectReview with only the user's token", auths)
	}
}

func TestReviewUserToken_ConcurrentCallsShareOneReview(t *testing.T) {
	var calls atomic.Int32
	arrived, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(arrived)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"username":"oidc:bob"}}}`))
	}))
	t.Cleanup(srv.Close)
	swapGlobals(t, &rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	tokenReviewMu.Lock()
	tokenReviewCache = map[[sha256.Size]byte]tokenReviewEntry{}
	tokenReviewMu.Unlock()

	// The first caller starts the review, then leaves.
	ctx, cancel := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := ReviewUserToken(ctx, "bob-burst-token")
		firstErr <- err
	}()
	<-arrived
	cancel()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller err = %v, want context.Canceled", err)
	}

	// Later callers join the review still in flight and get its answer.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := ReviewUserToken(context.Background(), "bob-burst-token")
			if err == nil && u.Username != "oidc:bob" {
				err = fmt.Errorf("username = %q", u.Username)
			}
			errs <- err
		}()
	}
	time.Sleep(50 * time.Millisecond) // let them reach the in-flight call
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("API calls = %d, want 1", n)
	}
}

func TestJWTExpiry(t *testing.T) {
	if _, ok := jwtExpiry("not-a-jwt"); ok {
		t.Error("malformed token parsed")
	}
	if exp, ok := jwtExpiry("h.eyJleHAiOjE3MDAwMDAwMDB9.s"); !ok || exp.Unix() != 1700000000 {
		t.Errorf("exp = %v, %v", exp, ok)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
