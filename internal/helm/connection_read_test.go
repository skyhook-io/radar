package helm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"helm.sh/helm/v3/pkg/release"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestGetReleaseUsesRequestConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	Initialize()
	t.Cleanup(ResetTestClient)
	install := func(name string, revision int) *atomic.Int32 {
		t.Helper()
		var requests atomic.Int32
		rel := helmTestRelease("payments", "team-a", revision, release.StatusDeployed, name)
		secret := helmReleaseSecret(t, "team-a", rel, true)
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/version":
				writeTestJSON(t, w, map[string]string{"gitVersion": "v1.34.0"})
			case "/api/v1/namespaces/team-a/secrets":
				writeTestJSON(t, w, corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: []corev1.Secret{*secret}})
			default:
				t.Errorf("unexpected request on %s: %s", name, r.URL)
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(api.Close)
		cfg := &rest.Config{Host: api.URL}
		if _, err := k8s.InstallTestServingConnection(name, cfg, nil, fake.NewSimpleClientset().Discovery()); err != nil {
			t.Fatal(err)
		}
		return &requests
	}
	install("cluster-a", 1)
	admission, err := k8s.CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := admission.Bind(t.Context())
	bRequests := install("cluster-b", 2)
	client := GetClient(t.Context())
	for _, scenario := range []struct {
		name string
		user *pkgauth.User
	}{
		{name: "GetRelease"},
		{name: "GetReleaseAsUser", user: &pkgauth.User{Username: "alice"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var detail *HelmReleaseDetail
			var err error
			if scenario.user == nil {
				detail, err = client.GetRelease(ctx, "team-a", "payments", nil)
			} else {
				detail, err = client.GetReleaseAsUser(ctx, "team-a", "payments", scenario.user, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if detail.Revision != 1 || detail.Description != "cluster-a" {
				t.Fatalf("got release revision %d from %s, want revision 1 from cluster-a", detail.Revision, detail.Description)
			}
		})
	}
	if n := bRequests.Load(); n != 0 {
		t.Errorf("got %d requests to cluster-b, want none", n)
	}
}

func TestInventoryStaysOnRequestConnection(t *testing.T) {
	for _, bound := range []bool{true, false} {
		name := "captured at entry"
		if bound {
			name = "already bound"
		}
		t.Run(name, func(t *testing.T) {
			t.Cleanup(k8s.SnapshotTestConnection())
			var namespaces, reviews, secrets, other atomic.Int32
			secret := helmReleaseSecret(t, "team-a", crossNamespaceRelease(), true)
			a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/namespaces":
					namespaces.Add(1)
					writeTestJSON(t, w, corev1.NamespaceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NamespaceList"}, Items: []corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}}})
				case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
					reviews.Add(1)
					writeTestJSON(t, w, authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: false}})
				case "/api/v1/namespaces/team-a/secrets":
					secrets.Add(1)
					writeTestJSON(t, w, corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: []corev1.Secret{*secret}})
				default:
					t.Errorf("unexpected A request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(a.Close)
			b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1); http.NotFound(w, r) }))
			t.Cleanup(b.Close)
			install := func(host string) {
				cfg := &rest.Config{Host: host, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
				if _, err := k8s.InstallTestServingConnection("prod", cfg, nil, fake.NewSimpleClientset().Discovery()); err != nil {
					t.Fatal(err)
				}
				k8s.SetTestClient(kubernetes.NewForConfigOrDie(cfg))
			}
			install(a.URL)
			useCrossNamespaceCache(t)
			t.Cleanup(k8s.SnapshotTestConnection())
			Initialize()
			t.Cleanup(ResetTestClient)
			request := httptest.NewRequest(http.MethodGet, "/helm/inventory", nil)
			if bound {
				admission, err := k8s.CaptureServingConnection()
				if err != nil {
					t.Fatal(err)
				}
				request = request.WithContext(admission.Bind(request.Context()))
				install(b.URL)
			}
			handlers := NewHandlers(nil)
			handlers.ObjectScope = func(*http.Request) *ObjectScope {
				if !bound {
					install(b.URL)
				}
				return adminCaller()
			}
			handlers.ResolveStorageNamespaces = func(r *http.Request) ([]string, bool, bool, []string) {
				names, skipped := ResolveNoAuthStorageScope(r.Context())
				return names, true, true, skipped
			}
			router := chi.NewRouter()
			handlers.RegisterRoutes(router)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, request)
			var inventory ReleaseInventory
			if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &inventory) != nil || len(inventory.Releases) != 1 {
				t.Fatalf("inventory: %d %s", out.Code, out.Body.String())
			}
			row := inventory.Releases[0]
			if row.Name != "payments" || row.HealthIssue != "ImagePullBackOff" || row.ResourceHealth != "unhealthy" {
				t.Fatalf("expected A's release and failing Pod, got %+v", row)
			}
			if namespaces.Load() != 1 || reviews.Load() != 1 || secrets.Load() != 1 || other.Load() != 0 {
				t.Errorf("namespace lists %d, permission checks %d, release lists %d, requests to B %d; want 1, 1, 1, 0", namespaces.Load(), reviews.Load(), secrets.Load(), other.Load())
			}
		})
	}
}
