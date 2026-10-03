package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestCNPGLocalCanIAsksTheKubeconfigIdentity(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var review authv1.SelfSubjectAccessReview
		_ = json.NewDecoder(r.Body).Decode(&review)
		attrs := review.Spec.ResourceAttributes
		review.Status.Allowed = !(attrs.Resource == "pods" && attrs.Subresource == "proxy" && attrs.Verb == "get")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	defer srv.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	cnpgLocalCanIMu.Lock()
	cnpgLocalCanIMemo = map[string]cnpgLocalCanIEntry{}
	cnpgLocalCanIMu.Unlock()

	if allowed, known := cnpgLocalCanI(context.Background(), cnpgGrantGetPodsProxy, "pgrt"); allowed || !known {
		t.Fatalf("pods/proxy = %v known=%v, want denied", allowed, known)
	}
	if allowed, known := cnpgLocalCanI(context.Background(), cnpgGrantCreateBackups, "pgrt"); !allowed || !known {
		t.Fatalf("create backups = %v known=%v, want allowed", allowed, known)
	}
	cnpgLocalCanI(context.Background(), cnpgGrantGetPodsProxy, "pgrt")
	if calls.Load() != 2 {
		t.Errorf("reviews = %d, want the repeat answered from the memo", calls.Load())
	}
}
