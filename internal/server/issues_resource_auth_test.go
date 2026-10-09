package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func TestResourceIssuesHonourAResourceNamesGrant(t *testing.T) {
	fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
		return a.Group == "apps" && a.Resource == "deployments" && a.Namespace == "broken" && a.Name == "stuck-app" && a.Verb == "get"
	})
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("pg-user", nil, &auth.UserPermissions{AllowedNamespaces: nil})

	w := resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/stuck-app?group=apps&coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("named grant: status %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) == 0 {
		t.Fatal("named grant got no issues for the broken Deployment")
	}

	if w := resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/other-app?group=apps"); w.Code != http.StatusForbidden {
		t.Fatalf("a name outside the grant: status %d, want 403", w.Code)
	}
}

func restoreFixtureCache(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
}

func TestResourceIssuesCoverageOfAKindWatchedElsewhere(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(testFakeClient, map[string]k8score.ResourceScope{
		"deployments": {Enabled: true, Namespace: "default"},
	}); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	if got := resourceIssuesCoverage("Deployment", "apps", "broken"); got != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage in an unwatched namespace %q, want notWatched", got)
	}
	if got := resourceIssuesCoverage("Deployment", "apps", "default"); got != resourceIssuesCoverageOK {
		t.Fatalf("coverage in the watched namespace %q, want ok", got)
	}
}

func TestResourceIssuesCountNodeIssuesTheListGateDropped(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	since := metav1.NewTime(time.Now().Add(-time.Hour))
	client := fake.NewClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", CreationTimestamp: since},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastTransitionTime: since,
		}}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "", "nodes", "", true)
	perms.SetCanI("list", "", "nodes", "", false)
	s.permCache.Set("pg-user", nil, perms)

	w := resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/n1?coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (body %s)", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("a caller who cannot list nodes got node issues: %+v", resp.Issues)
	}
	if resp.Withheld == nil || resp.Withheld.Issues == 0 {
		t.Fatalf("withheld %+v: the NotReady node's issue vanished instead of being counted", resp.Withheld)
	}

	perms.SetCanI("list", "", "nodes", "", true)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/n1?coverage=1")
	resp = ResourceIssuesResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) == 0 || resp.Withheld != nil {
		t.Fatalf("a caller who can list nodes: issues %d withheld %+v", len(resp.Issues), resp.Withheld)
	}
}

func TestResourceIssuesCountWithheldWithAndWithoutTheGroupParam(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dangling"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "gone"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "someone"}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "rbac.authorization.k8s.io", "clusterrolebindings", "", true)
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", false)
	s.permCache.Set("pg-user", nil, perms)

	for _, path := range []string{
		"/api/issues/resource/ClusterRoleBinding/_/dangling?coverage=1",
		"/api/issues/resource/ClusterRoleBinding/_/dangling?coverage=1&group=rbac.authorization.k8s.io",
	} {
		w := resourceIssuesRequest(t, s, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d (body %s)", path, w.Code, w.Body.String())
		}
		var resp ResourceIssuesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		if len(resp.Issues) != 0 || resp.Withheld == nil || resp.Withheld.Issues == 0 {
			t.Fatalf("%s: issues %d withheld %+v, want the dangling binding's issue counted as withheld", path, len(resp.Issues), resp.Withheld)
		}
	}

	w := resourceIssuesRequest(t, s, "/api/issues/resource/ClusterRoleBinding/_/dangling")
	var bare []issues.Issue
	if err := json.Unmarshal(w.Body.Bytes(), &bare); err != nil || len(bare) != 0 {
		t.Fatalf("bare path: %v, %d issues", err, len(bare))
	}
}
