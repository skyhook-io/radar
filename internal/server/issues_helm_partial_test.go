package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func useStalledHelmStorageWithCache(t *testing.T) *stalledHelmStorage {
	storage := useStalledHelmStorage(t)
	// Runs first: stop background Helm reads before the server goes away and
	// keep their results out of the next test.
	t.Cleanup(testServerSrv.helmIssues().invalidate)
	return storage
}

func getPartialIssues(t *testing.T) (*issuesapi.HelmIssuesStatus, time.Duration) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop&partial=true", nil)
	recorder := httptest.NewRecorder()
	started := time.Now()
	testServerSrv.handleIssues(recorder, request)
	elapsed := time.Since(started)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var body issuesapi.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HelmIssues == nil {
		t.Fatal("partial response has no helm_issues status")
	}
	return body.HelmIssues, elapsed
}

// A partial=true caller gets its issues once the Helm budget runs out, told
// Helm hasn't been checked yet. The read keeps going without it, and a later
// poll shows the result.
func TestPartialIssuesConvergeWhenHelmStorageOutlastsBudget(t *testing.T) {
	storage := useStalledHelmStorageWithCache(t)
	previousBudget := nativeHelmIssuesBudget
	nativeHelmIssuesBudget = 300 * time.Millisecond
	t.Cleanup(func() { nativeHelmIssuesBudget = previousBudget })

	first, elapsed := getPartialIssues(t)
	if elapsed > 2*time.Second {
		t.Errorf("issues answered after %s; the Helm budget was 300ms", elapsed.Round(time.Millisecond))
	}
	if first.State != issuesapi.HelmIssuesNotCheckedYet || !first.Reading {
		t.Errorf("first status = %+v, want not checked yet with a read running", first)
	}
	select {
	case <-storage.listCanceled:
		t.Fatal("the Helm read stopped with the response; a cluster slower than the budget would never be checked")
	case <-time.After(200 * time.Millisecond):
	}

	close(storage.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, _ := getPartialIssues(t)
		if next.State == issuesapi.HelmIssuesCurrent {
			if next.CheckedAt == nil || next.AgeSeconds == nil {
				t.Errorf("current status without its read time: %+v", next)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Helm issues never became current after Helm storage answered: %+v", next)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// With the budget already spent the response doesn't wait, but still starts
// the read the next poll will use.
func TestIssuesHelmBudgetAlreadySpentAnswersAtOnce(t *testing.T) {
	storage := useStalledHelmStorageWithCache(t)
	request := httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop&partial=true", nil)
	started := time.Now()
	found, status := testServerSrv.nativeHelmIssuesForRequest(request, []string{"shop"}, issues.Filters{}, time.Now().Add(-time.Millisecond))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("answered after %s with the budget already spent", elapsed.Round(time.Millisecond))
	}
	if len(found) != 0 || status == nil || status.State != issuesapi.HelmIssuesNotCheckedYet {
		t.Fatalf("got %v %+v, want no issues and not checked yet", found, status)
	}
	select {
	case <-storage.listStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no Helm read was started for the next poll")
	}
}

// Without partial=true there is no Helm status and the response waits for
// Helm storage, however slow.
func TestIssuesWithoutPartialWaitForHelmStorage(t *testing.T) {
	storage := useStalledHelmStorageWithCache(t)
	previousBudget := nativeHelmIssuesBudget
	nativeHelmIssuesBudget = 50 * time.Millisecond
	t.Cleanup(func() { nativeHelmIssuesBudget = previousBudget })

	request := httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		testServerSrv.handleIssues(recorder, request)
		close(done)
	}()
	<-storage.listStarted
	select {
	case <-done:
		t.Fatal("issues answered before Helm storage did")
	case <-time.After(300 * time.Millisecond):
	}
	close(storage.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("issues handler did not return after Helm storage answered")
	}
	if strings.Contains(recorder.Body.String(), "helm_issues") {
		t.Fatalf("default response carries a Helm status: %s", recorder.Body.String())
	}
}

// rbacHelmStorage is an API server for an authenticated user who may list
// Secrets only in "default": SubjectAccessReviews answer that way, and a
// cluster-wide Secret list is forbidden. It records every Secret list path.
type rbacHelmStorage struct {
	mu    sync.Mutex
	lists []string
}

func (s *rbacHelmStorage) listed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lists...)
}

func useRBACHelmStorage(t *testing.T) *rbacHelmStorage {
	t.Helper()
	storage := &rbacHelmStorage{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
		case strings.HasSuffix(r.URL.Path, "/subjectaccessreviews"):
			var review authorizationv1.SubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&review)
			attrs := review.Spec.ResourceAttributes
			review.Status.Allowed = attrs != nil && attrs.Resource == "secrets" && attrs.Namespace == "default"
			_ = json.NewEncoder(w).Encode(review)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			storage.mu.Lock()
			storage.lists = append(storage.lists, r.URL.Path)
			storage.mu.Unlock()
			if r.URL.Path != "/api/v1/namespaces/default/secrets" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"secrets is forbidden"}`)
				return
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	// JSON, so the fake server can read the SubjectAccessReviews.
	config := &rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	previousConfig := k8s.SetTestConfig(config)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helm.ResetClient()
		k8s.SetTestClient(previousClient)
		k8s.SetTestConfig(previousConfig)
	})
	t.Cleanup(testServerSrv.helmIssues().invalidate)
	return storage
}

// The namespace scope for Helm is resolved on the caller's context, not the
// Helm budget. Resolved on an expired budget, the Secret permission checks
// fail closed, the scope widens to cluster-wide, that list is forbidden, and
// the 403 read as "no Helm issues": a false all-clear for a user who can in
// fact read Helm in "default".
func TestPartialIssuesResolveHelmScopeOutsideTheBudget(t *testing.T) {
	storage := useRBACHelmStorage(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	t.Cleanup(s.helmIssues().stop)
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: nil})
	request := requestWithUser(http.MethodGet, "/api/issues?partial=true", &auth.User{Username: "alice"})

	_, status := s.nativeHelmIssuesForRequest(request, nil, issues.Filters{}, time.Now().Add(-time.Millisecond))
	if status == nil || status.State == issuesapi.HelmIssuesCurrent {
		t.Fatalf("status = %+v, want not current: no read had finished", status)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, status = s.nativeHelmIssuesForRequest(request, nil, issues.Filters{}, time.Now().Add(time.Second))
		if status.State != issuesapi.HelmIssuesNotCheckedYet {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Helm read never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.State != issuesapi.HelmIssuesCurrent {
		t.Fatalf("status = %+v, want current from the default namespace (lists: %v)", status, storage.listed())
	}
	for _, path := range storage.listed() {
		if path != "/api/v1/namespaces/default/secrets" {
			t.Fatalf("Helm read listed %s; want only the namespace alice can read (all: %v)", path, storage.listed())
		}
	}
}

// A caller that has already left gets no read started for it: its
// permission checks would fail closed and widen the scope to a doomed read.
func TestPartialIssuesSkipHelmReadForACallerThatLeft(t *testing.T) {
	storage := useStalledHelmStorageWithCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/issues?partial=true", nil).WithContext(ctx)
	found, status := testServerSrv.nativeHelmIssuesForRequest(request, nil, issues.Filters{}, time.Now().Add(time.Second))
	if found != nil || status != nil {
		t.Fatalf("got %v %+v for a caller that left", found, status)
	}
	time.Sleep(50 * time.Millisecond)
	if storage.started.Load() {
		t.Fatal("a Helm read started for a caller that had already left")
	}
}

// A partial response says Helm couldn't be checked when there is no Helm
// client (mid context switch), rather than leaving helm_issues out, which
// would read as checked.
func TestPartialIssuesWithoutAHelmClientSayHelmIsUnavailable(t *testing.T) {
	helm.ResetClient()
	request := httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop&partial=true", nil)
	_, status := testServerSrv.nativeHelmIssuesForRequest(request, []string{"shop"}, issues.Filters{}, time.Now().Add(time.Second))
	if status == nil || status.State != issuesapi.HelmIssuesUnavailable {
		t.Fatalf("status = %+v, want unavailable", status)
	}
	if _, status = testServerSrv.nativeHelmIssuesForRequest(request, []string{"shop"}, issues.Filters{}, time.Time{}); status != nil {
		t.Fatalf("default response carries a Helm status: %+v", status)
	}
}
