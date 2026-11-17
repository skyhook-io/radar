package k8s

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// reviewServer answers SubjectAccessReviews with answer(attributes).
func reviewServer(t *testing.T, answer func(authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus) *kubernetes.Clientset {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		review.Status = answer(*review.Spec.ResourceAttributes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 1000, Burst: 1000, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCanIAsTreatsAnUnansweredReviewAsAFailedCheck(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          authv1.SubjectAccessReviewStatus
		allowed, apiErr bool
	}{
		{name: "denied", status: authv1.SubjectAccessReviewStatus{}, allowed: false, apiErr: false},
		{name: "authorizer failed", status: authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timed out"}, allowed: false, apiErr: true},
		{name: "authorizer failed, deny on failure", status: authv1.SubjectAccessReviewStatus{Denied: true, EvaluationError: "webhook timed out"}, allowed: false, apiErr: true},
		{name: "allowed despite another authorizer failing", status: authv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "webhook timed out"}, allowed: true, apiErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := reviewServer(t, func(authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus { return tc.status })
			allowed, apiErr := canIAs(t.Context(), client, "alice", nil, "db", "", "pods/exec", "create")
			if allowed != tc.allowed || apiErr != tc.apiErr {
				t.Fatalf("allowed=%v apiErr=%v, want %v %v", allowed, apiErr, tc.allowed, tc.apiErr)
			}
		})
	}
}

func TestCheckCapabilitiesForUserCachesFailedChecksBriefly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  func(authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus
		wantTTL time.Duration
	}{
		{
			name: "settled answers",
			answer: func(authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus {
				return authv1.SubjectAccessReviewStatus{}
			},
			wantTTL: userCapabilitiesTTL,
		},
		{
			name: "exec unanswered",
			answer: func(a authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus {
				if a.Subresource == "exec" {
					return authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timed out"}
				}
				return authv1.SubjectAccessReviewStatus{}
			},
			wantTTL: capabilitiesErrorTTL,
		},
		{
			name: "unanswered cluster-wide but allowed in the namespace",
			answer: func(a authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus {
				if a.Subresource != "exec" {
					return authv1.SubjectAccessReviewStatus{}
				}
				if a.Namespace == "" {
					return authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timed out"}
				}
				return authv1.SubjectAccessReviewStatus{Allowed: true}
			},
			wantTTL: userCapabilitiesTTL,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			InvalidateUserCapabilitiesCache()
			t.Cleanup(InvalidateUserCapabilitiesCache)
			var mu sync.Mutex
			client := reviewServer(t, func(a authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus {
				mu.Lock()
				defer mu.Unlock()
				return tc.answer(a)
			})
			clientMu.Lock()
			k8sClient = client
			clientMu.Unlock()
			SetFallbackNamespace("team-a")

			before := time.Now()
			if _, err := CheckCapabilitiesForUser(t.Context(), "alice", nil); err != nil {
				t.Fatal(err)
			}
			entry, ok := userCapabilitiesCache.Load(pkgauth.IdentityCacheKey("alice", nil))
			if !ok {
				t.Fatal("capabilities were not cached")
			}
			ttl := entry.(*userCapEntry).expiresAt.Sub(before)
			if ttl < tc.wantTTL || ttl > tc.wantTTL+5*time.Second {
				t.Fatalf("cached for %v, want about %v", ttl, tc.wantTTL)
			}
		})
	}
}

func TestCheckNamespaceCapabilitiesForUserReportsAnUnansweredCheck(t *testing.T) {
	defer ResetTestState()
	InvalidateUserCapabilitiesCache()
	t.Cleanup(InvalidateUserCapabilitiesCache)
	client := reviewServer(t, func(a authv1.ResourceAttributes) authv1.SubjectAccessReviewStatus {
		if a.Subresource == "exec" {
			return authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timed out"}
		}
		return authv1.SubjectAccessReviewStatus{}
	})
	clientMu.Lock()
	k8sClient = client
	clientMu.Unlock()

	before := time.Now()
	caps, err := CheckNamespaceCapabilitiesForUser(t.Context(), "alice", nil, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if caps.Exec || !caps.Errors.Exec {
		t.Fatalf("exec = %v, exec check failed = %v; want false, true", caps.Exec, caps.Errors.Exec)
	}
	if caps.Errors.Logs {
		t.Fatal("a settled denial was reported as a failed check")
	}
	entry, ok := userNamespaceCapabilitiesCache.Load(userNamespaceCapabilitiesCacheKey("alice", nil, "team-a"))
	if !ok {
		t.Fatal("namespace capabilities were not cached")
	}
	if ttl := entry.(*userNSCapEntry).expiresAt.Sub(before); ttl > capabilitiesErrorTTL+5*time.Second {
		t.Fatalf("cached for %v, want about %v", ttl, capabilitiesErrorTTL)
	}
}
