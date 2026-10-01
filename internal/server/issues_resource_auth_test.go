package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
)

func resourceIssuesRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rt := chi.NewRouter()
	rt.Get("/api/issues/resource/{kind}/{namespace}/{name}", s.handleResourceIssues)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, requestWithUser(http.MethodGet, path, &auth.User{Username: "pg-user"}))
	return w
}

// Fixture: TestMain seeds Deployment broken/stuck-app with no available replicas.
func TestResourceIssuesRequireGetOnTheSubjectKind(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	s.permCache.Set("pg-user", nil, perms)

	perms.SetCanI("get", "apps", "deployments", "broken", false)
	w := resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/stuck-app?group=apps")
	if w.Code != http.StatusForbidden {
		t.Fatalf("denied get on deployments: status %d, want 403 (body %s)", w.Code, w.Body.String())
	}

	perms.SetCanI("get", "apps", "deployments", "broken", true)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/stuck-app?group=apps&coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("allowed: status %d (body %s)", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) == 0 {
		t.Fatal("allowed caller got no issues for the broken Deployment")
	}
	if resp.Coverage != resourceIssuesCoverageOK {
		t.Fatalf("coverage %q, want ok for a synced typed kind", resp.Coverage)
	}

	w = resourceIssuesRequest(t, s, "/api/issues/resource/deployments/broken/stuck-app")
	if w.Code != http.StatusOK {
		t.Fatalf("plural kind: status %d (body %s)", w.Code, w.Body.String())
	}
}

func TestResourceIssuesWithholdUnreadableSubjectsAndMembers(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "apps", "deployments", "pg", true)
	perms.SetCanI("get", "", "pods", "pg", false)
	perms.SetCanI("get", "apps", "statefulsets", "pg", false)
	s.permCache.Set("pg-user", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues/resource/Deployment/pg/app", &auth.User{Username: "pg-user"})

	in := []issues.Issue{
		{ID: "a", Group: "apps", Kind: "Deployment", Namespace: "pg", Name: "app", Members: []issues.Ref{
			{Kind: "Pod", Namespace: "pg", Name: "app-1"},
			{Kind: "Pod", Namespace: "pg", Name: "app-2"},
			{Group: "apps", Kind: "Deployment", Namespace: "pg", Name: "app"},
		}},
		{ID: "b", Group: "apps", Kind: "StatefulSet", Namespace: "pg", Name: "db"},
	}
	out, withheld := s.withholdUnreadableIssueRefs(r, in)
	if withheld.Issues != 1 || withheld.Members != 2 {
		t.Fatalf("withheld %+v, want 1 issue and 2 members", withheld)
	}
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("kept %+v, want only the Deployment's issue", out)
	}
	if len(out[0].Members) != 1 || !out[0].MembersTruncated {
		t.Fatalf("members %+v truncated=%v, want the readable one and truncated", out[0].Members, out[0].MembersTruncated)
	}
}

func TestResourceIssuesWithholdNothingWithoutAuth(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})
	r := httptest.NewRequest(http.MethodGet, "/api/issues/resource/Deployment/pg/app", nil)
	in := []issues.Issue{{ID: "a", Kind: "Pod", Namespace: "pg", Name: "p"}}
	if out, withheld := s.withholdUnreadableIssueRefs(r, in); len(out) != 1 || withheld != (ResourceIssuesWithheld{}) {
		t.Fatalf("auth off: out %+v withheld %+v", out, withheld)
	}
}

func TestResourceIssuesCoverageOfAnUnwatchedKind(t *testing.T) {
	if got := resourceIssuesCoverage("Cluster", "postgresql.cnpg.io", "pg"); got != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage %q, want notWatched for a kind Radar has not discovered", got)
	}
}
