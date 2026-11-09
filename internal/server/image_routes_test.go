package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestImageRoutesPodAuthorization(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	previousClient := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache()}
	s.authConfig.Defaults()
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("list", "", "pods", "team", true)
	perms.SetCanI("get", "", "pods", "team", false)
	s.permCache.Set("denied", nil, perms)
	s.permCache.Set("check-error", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})

	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name, query, user, message string
			status                     int
		}{
			{"missing namespace", "&pod=app", "denied", "pod and namespace are required", 400},
			{"get denied despite list allowed", "&namespace=team&pod=app", "denied", "rbac_denied: requires get pods in namespace team", 403},
			{"check failed", "&namespace=team&pod=app", "check-error", "Could not authorize get pods", 503},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?image=example.com/app:v1&path=/file.txt"+tc.query, nil)
				r.Header.Set("X-Forwarded-User", tc.user)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
					t.Fatalf("got %d %s, want %d %q", w.Code, w.Body.String(), tc.status, tc.message)
				}
			})
		}
	}
	s.permCache = nil
	for _, route := range []string{"metadata", "inspect", "file"} {
		r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?image=example.com/app:v1&path=/file.txt&namespace=team&pod=app", nil)
		r.Header.Set("X-Forwarded-User", "alice")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing permission cache: got %d %s", w.Code, w.Body.String())
		}
	}
}

func TestImageRoutesPullSecretPermissions(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app"}, Spec: corev1.PodSpec{
			Containers:       []corev1.Container{{Name: "app", Image: "registry.example/app:v1"}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "allowed-secret"}, {Name: "denied-secret"}},
		}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "allowed-secret"}, Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{"username":"pod-user","password":"password"}}}`)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "denied-secret"}, Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{"username":"denied-user","password":"password"}}}`)}},
	), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "secrets": {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	previousTransport := images.SetTestRegistryTransport(nil)
	t.Cleanup(func() { images.SetTestRegistryTransport(previousTransport) })
	for _, tc := range []struct {
		name, wantUser  string
		secretGet, exec bool
	}{
		{"secret get", "pod-user", true, false},
		{"exec", "denied-user", false, true},
		{"pod read only", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
				return tc.secretGet && a.Resource == "secrets" && a.Name == "allowed-secret"
			})
			s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache()}
			s.authConfig.Defaults()
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
			perms.SetCanI("get", "", "pods", "team", true)
			perms.SetCanI("get", "", "secrets", "team", true)
			perms.SetCanI("create", "", "pods/exec", "team", tc.exec)
			s.permCache.Set("alice", nil, perms)
			router := chi.NewRouter()
			s.setupAppRoutes(router)
			transport := &imageRouteRegistryTransport{}
			images.SetTestRegistryTransport(transport)
			for _, route := range []string{"metadata", "inspect", "file"} {
				r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?image=registry.example/app:v1&namespace=team&pod=app&path=/file.txt", nil)
				r.Header.Set("X-Forwarded-User", "alice")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("got %d %s", w.Code, w.Body.String())
				}
			}
			wantAuth := ""
			if tc.wantUser != "" {
				wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(tc.wantUser+":password"))
			}
			if len(transport.auths) == 0 || !slices.Contains(transport.auths, wantAuth) {
				t.Fatalf("registry authorizations = %v; want %q", transport.auths, wantAuth)
			}
			for _, a := range transport.auths {
				if a != "" && a != wantAuth {
					t.Fatal("unexpected registry credentials")
				}
			}
			if tc.exec {
				if len(*reviews) != 0 {
					t.Fatalf("exec permission should suffice: %v", *reviews)
				}
			} else {
				if len(*reviews) != 2 {
					t.Fatalf("want one cached exact Secret check per secret: %v", *reviews)
				}
				for _, a := range *reviews {
					if a.Verb != "get" || a.Group != "" || a.Resource != "secrets" || a.Namespace != "team" || (a.Name != "allowed-secret" && a.Name != "denied-secret") {
						t.Fatalf("incorrect Secret SAR: %+v", a)
					}
				}
			}
		})
	}
}

type imageRouteRegistryTransport struct {
	auths          []string
	publicManifest []byte
}

func (f *imageRouteRegistryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	if f.publicManifest != nil {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {string(types.OCIManifestSchema1)}}, Body: io.NopCloser(strings.NewReader(string(f.publicManifest))), Request: r}, nil
	}
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Www-Authenticate": {`Basic realm="registry"`}},
		Body:       io.NopCloser(strings.NewReader(`{"errors":[{"code":"UNAUTHORIZED"}]}`)),
		Request:    r,
	}, nil
}

func TestImageRoutesAnonymousSkipsSecretReviews(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app"}, Spec: corev1.PodSpec{
			Containers:       []corev1.Container{{Name: "app", Image: "registry.example/app:v1"}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pod-secret"}},
		}},
	), map[string]k8score.ResourceScope{"pods": {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	reviews := fakeSARServer(t, func(authv1.ResourceAttributes) bool { return false })
	s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache()}
	s.authConfig.Defaults()
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", "", "pods", "team", true)
	s.permCache.Set("alice", nil, perms)
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	manifest, err := empty.Image.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	previousTransport := images.SetTestRegistryTransport(&imageRouteRegistryTransport{publicManifest: manifest})
	t.Cleanup(func() { images.SetTestRegistryTransport(previousTransport) })
	r := httptest.NewRequest(http.MethodGet, "/api/images/metadata?image=registry.example/app:v1&namespace=team&pod=app", nil)
	r.Header.Set("X-Forwarded-User", "alice")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(*reviews) != 0 {
		t.Fatalf("anonymous fetch got %d %s, SARs=%v", w.Code, w.Body.String(), *reviews)
	}
}

func TestImageRoutesNonAuthoritativeSecretChecksNotCached(t *testing.T) {
	for _, failure := range []string{"evaluation error", "request error"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			restoreFixtureCache(t)
			k8s.ResetResourceCache()
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app"}, Spec: corev1.PodSpec{
					Containers:       []corev1.Container{{Name: "app", Image: "registry.example/app:v1"}},
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pod-secret"}},
				}},
			), map[string]k8score.ResourceScope{"pods": {Enabled: true}}); err != nil {
				t.Fatal(err)
			}
			var checks atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checks.Add(1)
				if failure == "request error" {
					http.Error(w, "authorization unavailable", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(authv1.SubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{EvaluationError: "authorization unavailable"}})
			}))
			t.Cleanup(api.Close)
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			t.Cleanup(func() { k8s.SetTestClient(previousClient) })
			s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache()}
			s.authConfig.Defaults()
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
			perms.SetCanI("get", "", "pods", "team", true)
			perms.SetCanI("create", "", "pods/exec", "team", false)
			s.permCache.Set("alice", nil, perms)
			router := chi.NewRouter()
			s.setupAppRoutes(router)
			previousTransport := images.SetTestRegistryTransport(&imageRouteRegistryTransport{})
			t.Cleanup(func() { images.SetTestRegistryTransport(previousTransport) })
			for range 2 {
				r := httptest.NewRequest(http.MethodGet, "/api/images/metadata?image=registry.example/app:v1&namespace=team&pod=app", nil)
				r.Header.Set("X-Forwarded-User", "alice")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("got %d %s", w.Code, w.Body.String())
				}
			}
			if checks.Load() != 2 {
				t.Fatalf("non-authoritative check ran %d times, want 2", checks.Load())
			}
			if _, cached := perms.CanI("get", "", "secrets/pod-secret", "team"); cached {
				t.Fatal("non-authoritative result cached")
			}
		})
	}
}
