package helm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestResolveNoAuthListNamespaces_ChecksClusterSecretsWithFallbackNamespaces(t *testing.T) {
	var capture selfSubjectAccessReviewCapture
	client := newNoAuthNamespaceClient(t, nil, errors.New("namespace list denied"), true, nil, &capture)
	withNoAuthNamespaceClient(t, client, "backend-fallback")

	got := ResolveNoAuthListNamespaces(context.Background())
	if got != nil {
		t.Fatalf("namespaces = %v, want nil for cluster-wide Helm list", got)
	}
	capture.assertClusterSecretList(t)
}

func TestResolveNoAuthListNamespaces_FansOutFallbackWhenClusterSecretsDenied(t *testing.T) {
	var capture selfSubjectAccessReviewCapture
	client := newNoAuthNamespaceClient(t, nil, errors.New("namespace list denied"), false, nil, &capture)
	withNoAuthNamespaceClient(t, client, "backend-fallback")

	got := ResolveNoAuthListNamespaces(context.Background())
	if !slices.Equal(got, []string{"backend-fallback"}) {
		t.Fatalf("namespaces = %v, want backend fallback namespace", got)
	}
	capture.assertClusterSecretList(t)
}

func TestResolveNoAuthListNamespaces_FansOutWhenClusterSecretsCheckErrors(t *testing.T) {
	var capture selfSubjectAccessReviewCapture
	client := newNoAuthNamespaceClient(t, []string{"beta", "alpha"}, nil, false, errors.New("self subject access review unavailable"), &capture)
	withNoAuthNamespaceClient(t, client, "")

	got := ResolveNoAuthListNamespaces(context.Background())
	if !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("namespaces = %v, want discovered namespaces", got)
	}
	capture.assertClusterSecretList(t)
}

type selfSubjectAccessReviewCapture struct {
	calls     int
	namespace string
	resource  string
	verb      string
}

func (c *selfSubjectAccessReviewCapture) assertClusterSecretList(t *testing.T) {
	t.Helper()
	if c.calls != 1 {
		t.Fatalf("self subject access review calls = %d, want 1", c.calls)
	}
	if c.namespace != "" || c.resource != "secrets" || c.verb != "list" {
		t.Fatalf("self subject access review = namespace=%q resource=%q verb=%q, want cluster-wide list secrets", c.namespace, c.resource, c.verb)
	}
}

func withNoAuthNamespaceClient(t *testing.T, client *kubernetes.Clientset, fallback string) {
	t.Helper()
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })
	k8s.SetFallbackNamespace(fallback)
	t.Cleanup(func() { k8s.SetFallbackNamespace("") })
}

func newNoAuthNamespaceClient(t *testing.T, namespaces []string, namespaceListErr error, ssarAllowed bool, ssarErr error, capture *selfSubjectAccessReviewCapture) *kubernetes.Clientset {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces":
			if namespaceListErr != nil {
				writeK8sStatus(t, w, http.StatusForbidden, "Forbidden", namespaceListErr.Error())
				return
			}
			items := make([]corev1.Namespace, 0, len(namespaces))
			for _, name := range namespaces {
				items = append(items, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
			}
			writeTestJSON(t, w, corev1.NamespaceList{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NamespaceList"},
				Items:    items,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Fatalf("decode self subject access review: %v", err)
			}
			if attrs := review.Spec.ResourceAttributes; attrs != nil {
				capture.namespace = attrs.Namespace
				capture.resource = attrs.Resource
				capture.verb = attrs.Verb
			}
			capture.calls++
			if ssarErr != nil {
				writeK8sStatus(t, w, http.StatusInternalServerError, "InternalError", ssarErr.Error())
				return
			}
			writeTestJSON(t, w, authv1.SelfSubjectAccessReview{
				TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"},
				Status:   authv1.SubjectAccessReviewStatus{Allowed: ssarAllowed},
			})
		default:
			writeK8sStatus(t, w, http.StatusNotFound, "NotFound", "unexpected test request")
		}
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{
		Host: server.URL,
		ContentConfig: rest.ContentConfig{
			ContentType: "application/json",
		},
	})
	if err != nil {
		t.Fatalf("create test client: %v", err)
	}
	return client
}

func writeK8sStatus(t *testing.T, w http.ResponseWriter, code int, reason, message string) {
	t.Helper()
	w.WriteHeader(code)
	writeTestJSON(t, w, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Reason:   metav1.StatusReason(reason),
		Message:  message,
		Code:     int32(code),
	})
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("write JSON response: %v", err)
	}
}
