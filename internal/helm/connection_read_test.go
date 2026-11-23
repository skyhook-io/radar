package helm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

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
