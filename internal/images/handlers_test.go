package images

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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
			{"missing namespace", "", "app", "gcr.io/project/app:v1", true, true, 400, "pod and namespace are required"},
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

func TestImageRoutesAuthenticatedRequirePod(t *testing.T) {
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, q := range []url.Values{
			{"image": {"10.0.0.5:8080/x:1"}},
			{"image": {"10.0.0.5:8080/x:1"}, "namespace": {"team"}, "pullSecrets": {"pod-secret"}},
			{"image": {"10.0.0.5:8080/x:1"}, "pod": {"app"}},
		} {
			q.Set("path", "/file.txt")
			f := &fakeImageInspector{}
			h := &Handlers{inspector: f, authorize: func(*http.Request, string) (bool, bool) {
				t.Fatal("Pod authorization called without a Pod and namespace")
				return false, false
			}}
			w := serveImageTest(h, route, q, true)
			if w.Code != http.StatusBadRequest || f.calls != 0 {
				t.Fatalf("%s %v: got %d %s, calls=%d", route, q, w.Code, w.Body.String(), f.calls)
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

// A caller who can read a Pod in one namespace gets nothing from another
// namespace's Pods, images or pull secrets.
func TestImageRoutesDenyOtherNamespaces(t *testing.T) {
	theirs := imageTestPod()
	theirs.Namespace = "payments"
	theirs.Spec.Containers[0].Image = "payments.example/ledger:v1"
	theirs.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "payments-secret"}}
	imageTestCache(t, imageTestPod(), theirs, imageTestSecret("payments", "payments-secret", "payments.example", "payments-user"))
	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name, namespace, image string
			want                   int
			message                string
		}{
			{"their Pod", "payments", "payments.example/ledger:v1", 403, "rbac_denied: requires get pods in namespace payments"},
			{"their image through my Pod", "team", "payments.example/ledger:v1", 403, "image is not used by pod team/app"},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				f := &fakeImageInspector{check: func(InspectRequest) { t.Fatal("inspection reached before authorization") }}
				h := &Handlers{inspector: f, authorize: func(_ *http.Request, ns string) (bool, bool) { return ns == "team", true }}
				w := serveImageTest(h, route, url.Values{"image": {tc.image}, "namespace": {tc.namespace}, "pod": {"app"}, "pullSecrets": {"payments-secret"}, "path": {"/file.txt"}}, true)
				if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.message) {
					t.Fatalf("got %d %s; want %d %q", w.Code, w.Body.String(), tc.want, tc.message)
				}
			})
		}
		t.Run(route+"/their secret named for my image", func(t *testing.T) {
			f := &fakeImageInspector{check: func(req InspectRequest) {
				if len(req.PullSecretNames) != 0 {
					t.Fatalf("caller secrets retained: %v", req.PullSecretNames)
				}
				assertImageCredentials(t, req.keychain, "payments.example/ledger:v1", "")
			}}
			h := &Handlers{inspector: f, authorize: func(_ *http.Request, ns string) (bool, bool) { return ns == "team", true },
				authorizeSecret: func(*http.Request, string, string) (bool, bool) { return true, true }}
			w := serveImageTest(h, route, url.Values{"image": {"gcr.io/project/app:v1"}, "namespace": {"team"}, "pod": {"app"}, "pullSecrets": {"payments-secret"}, "path": {"/file.txt"}}, true)
			if w.Code != 200 || f.calls != 1 {
				t.Fatalf("got %d %s, calls=%d", w.Code, w.Body.String(), f.calls)
			}
		})
	}
}

// guardTestRegistry serves an image whose layer is over the config-lookup
// response cap, and counts requests; wrap rewrites requests before the
// registry sees them.
func guardTestRegistry(t *testing.T, wrap func(http.ResponseWriter, *http.Request) bool) (image string, configDigest string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	var armed atomic.Bool
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if wrap != nil && armed.Load() && wrap(w, r) {
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	image = strings.TrimPrefix(srv.URL, "http://") + "/library/app:v1"
	img, err := random.Image(maxRegistryResponseBytes+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	config, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(image, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	hits.Store(0)
	armed.Store(true)
	return image, config.String(), hits
}

func serveGuardedImage(t *testing.T, image string, inCluster bool, route string, objects ...runtime.Object) *httptest.ResponseRecorder {
	t.Helper()
	pod := imageTestPod()
	pod.Spec.Containers[0].Image = image
	pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-secret"}}
	imageTestCache(t, append(objects, pod)...)
	inspector := &Inspector{cacheDir: t.TempDir(), newGuard: func() *guardedTransport { return newLayerTransport(inCluster) }}
	h := &Handlers{inspector: inspector,
		authorize:       func(*http.Request, string) (bool, bool) { return true, true },
		authorizeSecret: func(*http.Request, string, string) (bool, bool) { return true, true }}
	return serveImageTest(h, route, url.Values{"image": {image}, "namespace": {"team"}, "pod": {"app"}, "path": {"/missing"}}, true)
}

func TestImageRoutesRegistryNetworkGuard(t *testing.T) {
	t.Run("local registry the Pod names, with a layer over the lookup cap", func(t *testing.T) {
		image, _, hits := guardTestRegistry(t, nil)
		for _, route := range []string{"metadata", "inspect"} {
			if w := serveGuardedImage(t, image, false, route); w.Code != 200 || hits.Load() == 0 {
				t.Fatalf("%s: got %d %s, hits=%d", route, w.Code, w.Body.String(), hits.Load())
			}
		}
	})
	t.Run("loopback in-cluster", func(t *testing.T) {
		image, _, hits := guardTestRegistry(t, nil)
		for _, route := range []string{"metadata", "inspect", "file"} {
			w := serveGuardedImage(t, image, true, route)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "doesn't connect to this registry address") || hits.Load() != 0 {
				t.Fatalf("%s: got %d %s, hits=%d", route, w.Code, w.Body.String(), hits.Load())
			}
		}
	})
	t.Run("link-local", func(t *testing.T) {
		for _, inCluster := range []bool{false, true} {
			w := serveGuardedImage(t, "169.254.169.254/library/app:v1", inCluster, "metadata")
			if w.Code != 403 || !strings.Contains(w.Body.String(), "doesn't connect to this registry address") {
				t.Fatalf("inCluster=%v: got %d %s", inCluster, w.Code, w.Body.String())
			}
		}
	})
	redirectToLoopback := func(t *testing.T, only func(path, config string) bool) {
		var internalHits atomic.Int32
		internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { internalHits.Add(1) }))
		t.Cleanup(internal.Close)
		internalURL, _ := url.Parse(internal.URL)
		var config string
		image, config, _ := guardTestRegistry(t, func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodGet || !only(r.URL.Path, config) {
				return false
			}
			// localhost and 127.0.0.1 are different hosts to the guard.
			http.Redirect(w, r, "http://localhost:"+internalURL.Port()+r.URL.Path, http.StatusTemporaryRedirect)
			return true
		})
		for _, route := range []string{"metadata", "inspect"} {
			w := serveGuardedImage(t, image, false, route)
			if w.Code != 403 || internalHits.Load() != 0 {
				t.Fatalf("%s: got %d %s, internal hits=%d", route, w.Code, w.Body.String(), internalHits.Load())
			}
		}
	}
	t.Run("blobs redirected to loopback", func(t *testing.T) {
		redirectToLoopback(t, func(path, _ string) bool { return strings.Contains(path, "/blobs/") })
	})
	t.Run("only the config redirected to loopback", func(t *testing.T) {
		redirectToLoopback(t, func(path, config string) bool { return strings.HasSuffix(path, "/blobs/"+config) })
	})
	t.Run("blobs redirected to the metadata address", func(t *testing.T) {
		image, _, _ := guardTestRegistry(t, func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/blobs/") {
				return false
			}
			http.Redirect(w, r, "http://169.254.169.254"+r.URL.Path, http.StatusTemporaryRedirect)
			return true
		})
		for _, route := range []string{"metadata", "inspect"} {
			w := serveGuardedImage(t, image, false, route)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "doesn't connect to this registry address") {
				t.Fatalf("%s: got %d %s", route, w.Code, w.Body.String())
			}
		}
	})
	t.Run("token server at the metadata address", func(t *testing.T) {
		image, _, _ := guardTestRegistry(t, func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://169.254.169.254/token",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return true
		})
		for _, route := range []string{"metadata", "inspect", "file"} {
			w := serveGuardedImage(t, image, false, route)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "doesn't connect to this registry address") {
				t.Fatalf("%s: got %d %s", route, w.Code, w.Body.String())
			}
		}
	})
	t.Run("credentials over plain HTTP", func(t *testing.T) {
		var sentCredentials atomic.Int32
		image, _, _ := guardTestRegistry(t, func(w http.ResponseWriter, r *http.Request) bool {
			if r.Header.Get("Authorization") != "" {
				sentCredentials.Add(1)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return true
		})
		registryHost, _, _ := strings.Cut(image, "/")
		w := serveGuardedImage(t, image, false, "metadata", imageTestSecret("team", "registry-secret", registryHost, "pod-user"))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "only sends registry credentials over HTTPS") || sentCredentials.Load() != 0 {
			t.Fatalf("got %d %s, credentialed requests=%d", w.Code, w.Body.String(), sentCredentials.Load())
		}
	})
}

// Self-hosted registries often serve tokens or blobs from a second internal
// host, so image inspection allows any private address; loopback and
// link-local keep the inventory's rules.
func TestLayerTransportAllowsAnyPrivateHost(t *testing.T) {
	for _, tc := range []struct {
		ip        string
		inCluster bool
		want      bool
	}{
		{"10.0.0.5", true, true},
		{"192.168.1.10", false, true},
		{"100.64.0.1", true, true},
		{"fd00::1", true, true},
		{"127.0.0.1", true, false},
		{"127.0.0.1", false, false},
		{"169.254.169.254", false, false},
		{"8.8.8.8", true, true},
	} {
		if got := newLayerTransport(tc.inCluster).ipAllowed(net.ParseIP(tc.ip), false); got != tc.want {
			t.Errorf("%s inCluster=%v: got %v", tc.ip, tc.inCluster, got)
		}
	}
	if newGuardedTransport(true).ipAllowed(net.ParseIP("10.0.0.5"), false) {
		t.Error("the runtime inventory still limits private addresses to the named registry")
	}
}

// Plain HTTP is refused only for requests that carry credentials: a
// registry's redirect to an HTTP download URL carries none.
func TestLayerTransportRefusesOnlyCredentialsOverHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	send := func(method, authorization string) error {
		req := httptest.NewRequest(method, srv.URL+"/blob", nil).WithContext(withRegistryHost(context.Background(), host))
		req.RequestURI = ""
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := newLayerTransport(false).RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := send(http.MethodGet, ""); err != nil || hits.Load() != 1 {
		t.Fatalf("a request without credentials: err %v, hits %d", err, hits.Load())
	}
	if err := send(http.MethodGet, "Basic dXNlcjpwYXNz"); !errors.Is(err, errCredentialsOverHTTP) || hits.Load() != 1 {
		t.Fatalf("a request with credentials: err %v, hits %d", err, hits.Load())
	}
	// An OAuth token exchange sends its refresh token in the POST body.
	if err := send(http.MethodPost, ""); !errors.Is(err, errCredentialsOverHTTP) || hits.Load() != 1 {
		t.Fatalf("a token exchange: err %v, hits %d", err, hits.Load())
	}
}

// With auth on, the caller is not the proxy's owner: outside a cluster too,
// image inspection never lets a proxy pick the address.
func TestLayerTransportLocalProxyFailsClosed(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	for _, target := range []string{"http://registry.does-not-resolve.invalid/v2/", "http://203.0.113.7/v2/"} {
		g := newLayerTransport(false)
		g.proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if _, err := g.RoundTrip(req); !isBlocked(err) || proxied.Load() != 0 {
			t.Fatalf("%s: err %v, proxied %d", target, err, proxied.Load())
		}
	}
}

// A copy of a private image's manifest, served from another registry, has the
// same digest. It must not unlock the layers another caller cached.
func TestImageCacheNeedsTheImagesOwnRepository(t *testing.T) {
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(withholdBlobs bool) (string, *atomic.Int32) {
		var blobHits atomic.Int32
		var armed atomic.Bool
		reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if armed.Load() && strings.Contains(r.URL.Path, "/blobs/") {
				blobHits.Add(1)
				if withholdBlobs {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
			reg.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		image := strings.TrimPrefix(srv.URL, "http://") + "/private/app:v1"
		ref, err := name.ParseReference(image, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		armed.Store(true)
		return image, &blobHits
	}
	victim, victimBlobs := serve(false)
	replay, _ := serve(true)
	inspector := &Inspector{cacheDir: t.TempDir(), newGuard: func() *guardedTransport { return newLayerTransport(false) }}
	ctx := context.Background()

	victimFS, err := inspector.Inspect(ctx, InspectRequest{Image: victim, guarded: true})
	if err != nil {
		t.Fatal(err)
	}
	file := firstImageFile(victimFS.Root)
	if file == "" {
		t.Fatal("test image has no files")
	}
	if _, _, err := inspector.GetFileContent(ctx, InspectRequest{Image: victim, guarded: true}, file); err != nil {
		t.Fatalf("the victim reads its own file: %v", err)
	}
	downloaded := victimBlobs.Load()
	if _, err := inspector.Inspect(ctx, InspectRequest{Image: victim, guarded: true}); err != nil || victimBlobs.Load() != downloaded {
		t.Fatalf("the same repository reuses its cache: err %v, blob requests %d then %d", err, downloaded, victimBlobs.Load())
	}
	if fs, err := inspector.Inspect(ctx, InspectRequest{Image: replay, guarded: true}); err == nil {
		t.Fatalf("a replayed manifest read the cached layers: %d files", fs.TotalFiles)
	}
	if _, _, err := inspector.GetFileContent(ctx, InspectRequest{Image: replay, guarded: true}, file); err == nil {
		t.Fatal("a replayed manifest read a cached file")
	}
	if meta, err := inspector.GetMetadata(ctx, InspectRequest{Image: replay, guarded: true}); err != nil || meta.Cached || meta.Filesystem != nil {
		t.Fatalf("a replayed manifest's metadata came from the cache: err %v, %+v", err, meta)
	}
}

func firstImageFile(node *FileNode) string {
	if node == nil {
		return ""
	}
	if node.Type == "file" {
		return node.Path
	}
	for _, child := range node.Children {
		if path := firstImageFile(child); path != "" {
			return path
		}
	}
	return ""
}

// The cache never answers before the registry has: a caller whose own
// credentials can't read the manifest fails even after another caller cached
// the image.
func TestImageCacheDoesNotOutliveRegistryAccess(t *testing.T) {
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var locked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _, ok := r.BasicAuth(); locked.Load() && (!ok || user != "alice") {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	image := strings.TrimPrefix(srv.URL, "http://") + "/private/app:v1"
	ref, err := name.ParseReference(image, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	locked.Store(true)
	alice := &pullSecretKeychain{auths: map[string]DockerConfigEntry{ref.Context().RegistryStr(): {Username: "alice", Password: "secret"}}}
	inspector := &Inspector{cacheDir: t.TempDir()}
	ctx := context.Background()

	fs, err := inspector.Inspect(ctx, InspectRequest{Image: image, keychain: alice})
	if err != nil {
		t.Fatal(err)
	}
	file := firstImageFile(fs.Root)
	for name, keychain := range map[string]authn.Keychain{"no credentials": authn.NewMultiKeychain(), "wrong credentials": &pullSecretKeychain{auths: map[string]DockerConfigEntry{ref.Context().RegistryStr(): {Username: "bob", Password: "guess"}}}} {
		bob := InspectRequest{Image: image, keychain: keychain}
		if _, err := inspector.Inspect(ctx, bob); err == nil {
			t.Fatalf("%s: inspect served from the cache", name)
		}
		if _, _, err := inspector.GetFileContent(ctx, bob, file); err == nil {
			t.Fatalf("%s: a file served from the cache", name)
		}
		if _, err := inspector.GetMetadata(ctx, bob); err == nil {
			t.Fatalf("%s: metadata served from the cache", name)
		}
	}
	if _, _, err := inspector.GetFileContent(ctx, InspectRequest{Image: image, keychain: alice}, file); err != nil {
		t.Fatalf("alice still reads her file: %v", err)
	}
}
