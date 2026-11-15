package k8s

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A SubjectAccessReview that errors (a timeout behind the informer start)
// is not a denial: the all-false result must not be served from cache once
// the apiserver answers, and the response must say the checks were incomplete.
func TestCheckCapabilitiesForUserDoesNotCacheFailedChecks(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "timeout", http.StatusGatewayTimeout)
			return
		}
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		review.Status.Allowed = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	clientMu.Lock()
	oldClient := k8sClient
	k8sClient = client
	clientMu.Unlock()
	InvalidateUserCapabilitiesCache()
	t.Cleanup(func() {
		clientMu.Lock()
		k8sClient = oldClient
		clientMu.Unlock()
		InvalidateUserCapabilitiesCache()
	})

	caps, err := CheckCapabilitiesForUser(t.Context(), "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Exec || caps.Logs {
		t.Fatalf("failed checks must fail closed, got exec=%v logs=%v", caps.Exec, caps.Logs)
	}
	if !caps.ChecksIncomplete {
		t.Error("failed checks must be reported as incomplete")
	}

	failing.Store(false)
	userCapabilitiesCache.Range(func(key, value any) bool {
		// Past the short error TTL, still well inside the full one.
		value.(*userCapEntry).expiresAt = value.(*userCapEntry).expiresAt.Add(-capabilitiesErrorTTL)
		return true
	})
	caps, err = CheckCapabilitiesForUser(t.Context(), "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Exec || !caps.Logs || caps.ChecksIncomplete {
		t.Errorf("after the apiserver answers, got exec=%v logs=%v incomplete=%v; want the real grant", caps.Exec, caps.Logs, caps.ChecksIncomplete)
	}
}
