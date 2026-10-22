package images

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

type fakeImageInspector struct {
	check func(InspectRequest)
	err   error
	calls int
}

func (f *fakeImageInspector) GetMetadata(_ context.Context, req InspectRequest) (*ImageMetadata, error) {
	f.calls++
	f.check(req)
	return &ImageMetadata{Image: req.Image}, f.err
}

func (f *fakeImageInspector) Inspect(_ context.Context, req InspectRequest) (*ImageFilesystem, error) {
	f.calls++
	f.check(req)
	return &ImageFilesystem{Image: req.Image}, f.err
}

func (f *fakeImageInspector) GetFileContent(_ context.Context, req InspectRequest, _ string) ([]byte, string, error) {
	f.calls++
	f.check(req)
	return []byte("test content"), "file.txt", f.err
}

func imageTestCache(t *testing.T, objects ...runtime.Object) {
	t.Helper()
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	err := k8s.InitScopedTestResourceCache(fake.NewClientset(objects...), map[string]k8score.ResourceScope{
		"pods": {Enabled: true}, "secrets": {Enabled: true}, "serviceaccounts": {Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func imageTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app"},
		Spec: corev1.PodSpec{
			Containers:          []corev1.Container{{Name: "app", Image: "gcr.io/project/app:v1"}},
			InitContainers:      []corev1.Container{{Name: "init", Image: "gcr.io/project/init:v1"}},
			EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "gcr.io/project/debug:v1"}}},
			ImagePullSecrets:    []corev1.LocalObjectReference{{Name: "pod-secret"}},
			ServiceAccountName:  "app-sa",
		},
		Status: corev1.PodStatus{
			ContainerStatuses:          []corev1.ContainerStatus{{Name: "app", ImageID: "docker-pullable://gcr.io/project/app@sha256:" + strings.Repeat("a", 64)}},
			InitContainerStatuses:      []corev1.ContainerStatus{{Name: "init", ImageID: "gcr.io/project/init@sha256:" + strings.Repeat("b", 64)}},
			EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "debug", ImageID: "docker-pullable://gcr.io/project/debug@sha256:" + strings.Repeat("c", 64)}},
		},
	}
}

func imageTestSecret(namespace, secretName, registry, username string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: secretName},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"` + registry + `":{"username":"` + username + `","password":"test-password"}}}`)},
	}
}

func serveImageTest(h *Handlers, route string, query url.Values, authenticated bool) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Route("/api", h.RegisterRoutes)
	r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?"+query.Encode(), nil)
	if authenticated {
		r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestImageRoutesAuthenticatedGuard(t *testing.T) {
	imageTestCache(t, imageTestPod())
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name, namespace, pod, image string
			allowed, authoritative      bool
			want                        int
			message                     string
		}{
			{"missing namespace", "", "app", "gcr.io/project/app:v1", true, true, 400, "namespace parameter is required"},
			{"denied", "team", "app", "gcr.io/project/app:v1", false, true, 403, "rbac_denied: requires get pods in namespace team"},
			{"check failed", "team", "app", "gcr.io/project/app:v1", false, false, 503, "Could not authorize"},
			{"non-authoritative allow", "team", "app", "gcr.io/project/app:v1", true, false, 503, "Could not authorize"},
			{"unrelated image", "team", "app", "gcr.io/project/other:v1", true, true, 403, "image is not used by pod team/app"},
			{"wrong repository same digest", "team", "app", "gcr.io/project/other@sha256:" + strings.Repeat("a", 64), true, true, 403, "image is not used by pod team/app"},
			{"pod missing", "team", "absent", "gcr.io/project/app:v1", true, true, 404, "Pod not found"},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				f := &fakeImageInspector{check: func(InspectRequest) { t.Fatal("inspection reached before authorization") }}
				checks := 0
				h := &Handlers{inspector: f, authorize: func(r *http.Request, ns string) (bool, bool) {
					checks++
					if ns != tc.namespace || auth.UserFromContext(r.Context()).Username != "alice" {
						t.Fatal("wrong authorization identity or namespace")
					}
					return tc.allowed, tc.authoritative
				}}
				w := serveImageTest(h, route, url.Values{"image": {tc.image}, "namespace": {tc.namespace}, "pod": {tc.pod}, "path": {"/file.txt"}, "pullSecrets": {"other-secret"}}, true)
				if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.message) {
					t.Fatalf("got %d %s; want %d %q", w.Code, w.Body.String(), tc.want, tc.message)
				}
				if f.calls != 0 {
					t.Fatal("inspector called")
				}
				if (tc.namespace == "" || tc.pod == "") && checks != 0 {
					t.Fatal("authorization called with missing Pod context")
				}
			})
		}
	}
}

func TestImageRoutesAuthenticatedPodCredentials(t *testing.T) {
	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"auths":{"unmatched.example":{"auth":"YW1iaWVudDpzZWNyZXQ="}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	pod := imageTestPod()
	imageTestCache(t, pod,
		imageTestSecret("team", "pod-secret", "gcr.io", "pod-user"),
		imageTestSecret("team", "other-secret", "other.example", "query-user"),
		imageTestSecret("team", "sa-secret", "sa.example", "sa-user"),
		imageTestSecret("elsewhere", "pod-secret", "gcr.io", "wrong-namespace-user"),
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-sa"}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "sa-secret"}}},
	)
	previousGoogle := google.Keychain
	google.Keychain = rejectingImageKeychain{t}
	t.Cleanup(func() { google.Keychain = previousGoogle })
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, image := range []string{
			pod.Spec.Containers[0].Image, pod.Spec.InitContainers[0].Image, pod.Spec.EphemeralContainers[0].Image,
			strings.TrimPrefix(pod.Status.ContainerStatuses[0].ImageID, "docker-pullable://"),
			pod.Status.InitContainerStatuses[0].ImageID,
			strings.TrimPrefix(pod.Status.EphemeralContainerStatuses[0].ImageID, "docker-pullable://"),
		} {
			t.Run(route+"/"+image, func(t *testing.T) {
				f := &fakeImageInspector{check: func(req InspectRequest) {
					if req.keychain == nil {
						t.Fatal("missing Pod-only keychain")
					}
					if len(req.PullSecretNames) != 0 {
						t.Fatalf("query secrets retained: %v", req.PullSecretNames)
					}
					assertImageCredentials(t, req.keychain, "gcr.io/project/app:v1", "pod-user")
					for _, registry := range []string{"other.example", "sa.example", "unmatched.example"} {
						assertImageCredentials(t, req.keychain, registry+"/app:v1", "")
					}
				}}
				h := &Handlers{inspector: f, authorize: func(*http.Request, string) (bool, bool) { return true, true }, authorizeSecret: func(r *http.Request, ns, secret string) (bool, bool) {
					if ns != "team" || secret != "pod-secret" || auth.UserFromContext(r.Context()).Username != "alice" {
						t.Fatal("wrong secret authorization identity or target")
					}
					return true, true
				}}
				w := serveImageTest(h, route, url.Values{"image": {image}, "namespace": {"team"}, "pod": {"app"}, "pullSecrets": {"other-secret,sa-secret"}, "path": {"/file.txt"}}, true)
				if w.Code != 200 || f.calls != 1 {
					t.Fatalf("got %d %s, calls=%d", w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
}

type rejectingImageKeychain struct{ t *testing.T }

func (k rejectingImageKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	k.t.Fatal("Radar's Google keychain was used")
	return authn.Anonymous, nil
}

func assertImageCredentials(t *testing.T, keychain authn.Keychain, image, username string) {
	t.Helper()
	ref, err := name.ParseReference(image)
	if err != nil {
		t.Fatal(err)
	}
	a, err := keychain.Resolve(ref.Context())
	if err != nil {
		t.Fatal(err)
	}
	config, err := a.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	if config.Username != username {
		t.Fatalf("username = %q; want %q", config.Username, username)
	}
	if username == "" && *config != (authn.AuthConfig{}) {
		t.Fatal("expected anonymous credentials")
	}
}

func TestImageRoutesAuthOffUnchanged(t *testing.T) {
	imageTestCache(t, imageTestPod(), &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Namespace: "team", Name: "app-sa"},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "sa-secret"}},
	})
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name, namespace, pod, query string
			secrets                     []string
		}{
			{name: "no Pod context"},
			{name: "explicit secrets", namespace: "team", pod: "app", query: "other-secret,extra", secrets: []string{"other-secret", "extra"}},
			{name: "Pod and SA discovery", namespace: "team", pod: "app", secrets: []string{"pod-secret", "sa-secret"}},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				f := &fakeImageInspector{check: func(req InspectRequest) {
					if req.keychain != nil || !slices.Equal(req.PullSecretNames, tc.secrets) {
						t.Fatalf("unexpected local request: %+v", req)
					}
				}}
				h := &Handlers{inspector: f, authorize: func(*http.Request, string) (bool, bool) { t.Fatal("local authorization called"); return false, false }}
				w := serveImageTest(h, route, url.Values{"image": {"unrelated.example/image:v1"}, "namespace": {tc.namespace}, "pod": {tc.pod}, "pullSecrets": {tc.query}, "path": {"/file.txt"}}, false)
				if w.Code != 200 || f.calls != 1 {
					t.Fatalf("got %d %s, calls=%d", w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
}

func TestImageRoutesErrorsRedacted(t *testing.T) {
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, message := range []string{"registry response body secret-token", "unauthorized secret-token", "not found secret-token"} {
			f := &fakeImageInspector{check: func(InspectRequest) {}, err: errors.New(message)}
			w := serveImageTest(&Handlers{inspector: f}, route, url.Values{"image": {"example.com/app:v1"}, "path": {"/file.txt"}}, false)
			if w.Code < 400 || strings.Contains(w.Body.String(), "secret-token") {
				t.Fatalf("unsafe error response: %d %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestImageRoutesMissingAuthorizer(t *testing.T) {
	for _, route := range []string{"metadata", "inspect", "file"} {
		w := serveImageTest(&Handlers{}, route, url.Values{"image": {"example.com/app:v1"}, "namespace": {"team"}, "pod": {"app"}, "path": {"/file.txt"}}, true)
		if w.Code != 503 {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
}

func TestImagePodKeychainAnonymousFallback(t *testing.T) {
	imageTestCache(t)
	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"auths":{"gcr.io":{"auth":"YW1iaWVudDpzZWNyZXQ="}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousGoogle := google.Keychain
	google.Keychain = rejectingImageKeychain{t}
	t.Cleanup(func() { google.Keychain = previousGoogle })
	for _, secrets := range [][]corev1.LocalObjectReference{nil, {{Name: "missing"}}, {{Name: ""}}} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{ImagePullSecrets: secrets}}
		assertImageCredentials(t, getPodKeychain("team", pod, false, func(string) bool { return true }), "gcr.io/project/app:v1", "")
	}
	k8s.ResetResourceCache()
	assertImageCredentials(t, getPodKeychain("team", imageTestPod(), false, func(string) bool { return true }), "gcr.io/project/app:v1", "")
}

func TestImageRoutesPullSecretAuthorization(t *testing.T) {
	pod := imageTestPod()
	pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: "denied-secret"})
	imageTestCache(t, pod,
		imageTestSecret("team", "pod-secret", "gcr.io", "pod-user"),
		imageTestSecret("team", "denied-secret", "denied.example", "denied-user"),
	)
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name                   string
			allowed, authoritative bool
			wantUser               string
		}{
			{"allowed", true, true, "pod-user"},
			{"pod read only", false, true, ""},
			{"check failed", false, false, ""},
			{"non-authoritative allow", true, false, ""},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				var checked []string
				f := &fakeImageInspector{check: func(req InspectRequest) {
					assertImageCredentials(t, req.keychain, "gcr.io/project/app:v1", tc.wantUser)
					assertImageCredentials(t, req.keychain, "denied.example/app:v1", "")
				}}
				h := &Handlers{
					inspector: f,
					authorize: func(*http.Request, string) (bool, bool) { return true, true },
					authorizeSecret: func(r *http.Request, ns, secret string) (bool, bool) {
						if ns != "team" || auth.UserFromContext(r.Context()).Username != "alice" {
							t.Fatal("wrong authorization target or identity")
						}
						checked = append(checked, secret)
						return tc.allowed && secret == "pod-secret", tc.authoritative
					},
				}
				w := serveImageTest(h, route, url.Values{"image": {pod.Spec.Containers[0].Image}, "namespace": {"team"}, "pod": {"app"}, "path": {"/file.txt"}}, true)
				if w.Code != 200 || f.calls != 1 || !slices.Equal(checked, []string{"pod-secret", "denied-secret"}) {
					t.Fatalf("got %d %s, calls=%d, checked=%v", w.Code, w.Body.String(), f.calls, checked)
				}
			})
		}
	}
}

func TestImageRoutesExecHolderUsesAmbientCredentials(t *testing.T) {
	pulledDigest := "gcr.io/project/app@sha256:" + strings.Repeat("a", 64)
	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"auths":{"gcr.io":{"auth":"YW1iaWVudDpzZWNyZXQ="}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousGoogle := google.Keychain
	google.Keychain = imageAnonymousKeychain{resolve: func() {}}
	t.Cleanup(func() { google.Keychain = previousGoogle })
	for _, tc := range []struct {
		name                   string
		pulled                 bool
		allowed, authoritative bool
		wantUser, wantImage    string
		wantExecCheck          bool
	}{
		{"exec allowed", true, true, true, "ambient", pulledDigest, true},
		{"exec denied", true, false, true, "", "gcr.io/project/app:v1", true},
		{"exec check failed", true, false, false, "", "gcr.io/project/app:v1", true},
		{"non-authoritative allow", true, true, false, "", "gcr.io/project/app:v1", true},
		{"image never pulled", false, true, true, "", "gcr.io/project/app:v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := imageTestPod()
			pod.Spec.ImagePullSecrets = nil
			if !tc.pulled {
				pod.Status.ContainerStatuses = nil
			}
			imageTestCache(t, pod)
			execChecked := false
			f := &fakeImageInspector{check: func(req InspectRequest) {
				if req.Image != tc.wantImage {
					t.Fatalf("image = %q; want %q", req.Image, tc.wantImage)
				}
				assertImageCredentials(t, req.keychain, req.Image, tc.wantUser)
			}}
			h := &Handlers{
				inspector: f,
				authorize: func(*http.Request, string) (bool, bool) { return true, true },
				authorizeExec: func(r *http.Request, ns string) (bool, bool) {
					if ns != "team" || auth.UserFromContext(r.Context()).Username != "alice" {
						t.Fatal("wrong authorization target or identity")
					}
					execChecked = true
					return tc.allowed, tc.authoritative
				},
				authorizeSecret: func(*http.Request, string, string) (bool, bool) { return false, true },
			}
			w := serveImageTest(h, "metadata", url.Values{"image": {"gcr.io/project/app:v1"}, "namespace": {"team"}, "pod": {"app"}}, true)
			if w.Code != 200 || f.calls != 1 || execChecked != tc.wantExecCheck {
				t.Fatalf("got %d %s, calls=%d, execChecked=%v", w.Code, w.Body.String(), f.calls, execChecked)
			}
		})
	}
}

func TestImageRoutesAuthenticatedWithoutPodAnonymousOnly(t *testing.T) {
	imageTestCache(t, imageTestPod(), imageTestSecret("team", "pod-secret", "gcr.io", "pod-user"))
	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"auths":{"gcr.io":{"auth":"YW1iaWVudDpzZWNyZXQ="}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousGoogle := google.Keychain
	google.Keychain = rejectingImageKeychain{t}
	t.Cleanup(func() { google.Keychain = previousGoogle })
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, namespace := range []string{"", "team"} {
			f := &fakeImageInspector{check: func(req InspectRequest) {
				if req.keychain == nil || len(req.PullSecretNames) != 0 {
					t.Fatalf("expected explicit anonymous-only keychain: %+v", req)
				}
				assertImageCredentials(t, req.keychain, "gcr.io/project/app:v1", "")
			}}
			h := &Handlers{inspector: f,
				authorize: func(*http.Request, string) (bool, bool) {
					t.Fatal("Pod authorization called without Pod")
					return false, false
				},
				authorizeSecret: func(*http.Request, string, string) (bool, bool) {
					t.Fatal("Secret authorization called without Pod")
					return false, false
				},
			}
			w := serveImageTest(h, route, url.Values{"image": {"gcr.io/project/app:v1"}, "namespace": {namespace}, "pullSecrets": {"pod-secret"}, "path": {"/file.txt"}}, true)
			if w.Code != 200 || f.calls != 1 {
				t.Fatalf("got %d %s, calls=%d", w.Code, w.Body.String(), f.calls)
			}
		}
	}
}

func TestImageRoutesPodCacheSyncing(t *testing.T) {
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	client := fake.NewClientset()
	client.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("Pod list temporarily unavailable")
	})
	if err := k8s.InitTestPromotedSyncingCache(client, time.Second, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if cache, readiness := k8s.ReadableCacheForKind("pods"); cache == nil || readiness != k8s.KindPending {
		t.Fatalf("expected a pending Pod cache, got %v", readiness)
	}
	for _, route := range []string{"metadata", "inspect", "file"} {
		h := &Handlers{
			inspector: &fakeImageInspector{check: func(InspectRequest) { t.Fatal("inspector called while Pods are syncing") }},
			authorize: func(*http.Request, string) (bool, bool) { return true, true },
		}
		w := serveImageTest(h, route, url.Values{"image": {"gcr.io/project/app:v1"}, "namespace": {"team"}, "pod": {"app"}, "path": {"/file.txt"}}, true)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Pod cache is still syncing") {
			t.Fatalf("%s: got %d %s", route, w.Code, w.Body.String())
		}
	}
}
