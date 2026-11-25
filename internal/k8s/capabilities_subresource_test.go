package k8s

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCanIAsSendsSubresourceInItsOwnField(t *testing.T) {
	var got authv1.ResourceAttributes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		got = *review.Spec.ResourceAttributes
		review.Status.Allowed = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}

	if allowed, apiErr := canIAs(t.Context(), client, "alice", nil, "db", "", "pods/exec", "create"); !allowed || apiErr {
		t.Fatalf("allowed=%v apiErr=%v", allowed, apiErr)
	}
	if got.Resource != "pods" || got.Subresource != "exec" {
		t.Errorf("review = resource %q subresource %q, want pods / exec", got.Resource, got.Subresource)
	}

	canIAs(t.Context(), client, "alice", nil, "db", "", "secrets", "list")
	if got.Resource != "secrets" || got.Subresource != "" {
		t.Errorf("plain resource = %q / %q", got.Resource, got.Subresource)
	}
}
