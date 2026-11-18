package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	authv1 "k8s.io/api/authorization/v1"
)

func installCNPGPDBFixture(t *testing.T, instances int) {
	t.Helper()
	t.Cleanup(func() { k8s.ResetResourceCache(); _ = k8s.InitTestResourceCache(testFakeClient) })
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	if err := k8s.NewCNPGPDBTestFixture(instances).Install(""); err != nil {
		t.Fatal(err)
	}
}

// cnpgPDBCaller is a proxy-auth caller with namespace access to the fixture's
// namespace whose SARs are denied for deny (a resource name).
func cnpgPDBCaller(t *testing.T, deny string) (*Server, *auth.User) {
	t.Helper()
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "db-reader-" + deny}
	s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{k8s.CNPGPDBTestNamespace}})
	fakeSARServer(t, func(a authv1.ResourceAttributes) bool { return a.Resource != deny })
	return s, user
}

// warningShape is what a caller can compare between two PDB rows.
func warningShape(i issuesapi.Issue) issuesapi.Issue {
	return issuesapi.Issue{Message: i.Message, RawMessage: i.RawMessage, Cause: i.Cause, Action: i.Action, Severity: i.Severity, Reason: i.Reason,
		Category: i.Category, FirstSeen: i.FirstSeen, OnsetUnknown: i.OnsetUnknown, IssueTiming: i.IssueTiming, IssueTimingBasis: i.IssueTimingBasis}
}

func listPDBIssues(t *testing.T, s *Server, user *auth.User) (map[string]issuesapi.Issue, int) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleIssues(w, requestWithUser(http.MethodGet, "/api/issues?namespaces="+k8s.CNPGPDBTestNamespace+"&kind=PodDisruptionBudget", user))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp issuesapi.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	rows := map[string]issuesapi.Issue{}
	for _, i := range resp.Issues {
		rows[i.Name] = i
	}
	return rows, resp.Total
}

func TestCNPGPrimaryPDBIssuesFollowTheCallersEvidence(t *testing.T) {
	installCNPGPDBFixture(t, 2)
	for _, tc := range []struct {
		deny     string
		verified bool
	}{
		{"", true},
		{"nodes", false},
		{"clusters", false},
		{"pods", false},
	} {
		t.Run("deny="+tc.deny, func(t *testing.T) {
			s, user := cnpgPDBCaller(t, tc.deny)
			rows, total := listPDBIssues(t, s, user)
			plain, ok := rows["plain"]
			if !ok {
				t.Fatalf("unrecognised budget missing: %+v", rows)
			}
			got, shown := rows["pg-primary"]
			if shown == tc.verified {
				t.Fatalf("pg-primary shown=%v for a caller who verified=%v", shown, tc.verified)
			}
			if total != len(rows) {
				t.Fatalf("total %d counts rows not returned: %+v", total, rows)
			}
			if shown && !reflect.DeepEqual(warningShape(got), warningShape(plain)) {
				t.Fatalf("restricted caller can tell the budgets apart:\n%+v\n%+v", warningShape(got), warningShape(plain))
			}
			if shown && got.FirstSeen.IsZero() {
				t.Fatal("budget-owned onset lost")
			}

			router := chi.NewRouter()
			router.Get("/api/issues/resource/{kind}/{namespace}/{name}", s.handleResourceIssues)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, requestWithUser(http.MethodGet, "/api/issues/resource/PodDisruptionBudget/"+k8s.CNPGPDBTestNamespace+"/pg-primary?group=policy&coverage=1", user))
			if w.Code != http.StatusOK {
				t.Fatalf("resource issues status=%d body=%s", w.Code, w.Body.String())
			}
			var resp ResourceIssuesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if (len(resp.Issues) > 0) != !tc.verified {
				t.Fatalf("resource issues = %+v", resp.Issues)
			}
			if resp.Withheld != nil && resp.Withheld.Issues != 0 {
				t.Fatalf("an expected finding is not withheld: %+v", resp.Withheld)
			}

			problems := dashboardPDBProblems(t, s, user)
			if _, ok := problems["pg-primary"]; ok == tc.verified {
				t.Fatalf("dashboard pg-primary shown=%v verified=%v", ok, tc.verified)
			}
			if p, ok := problems["pg-primary"]; ok && (p.Message != problems["plain"].Message || p.OnsetUnknown || p.Duration != problems["plain"].Duration) {
				t.Fatalf("dashboard rows differ: %+v vs %+v", p, problems["plain"])
			}
		})
	}
}

func TestCNPGSingleInstancePDBCopyNeedsExactClusterRead(t *testing.T) {
	installCNPGPDBFixture(t, 1)
	for _, deny := range []string{"", "clusters"} {
		t.Run("deny="+deny, func(t *testing.T) {
			s, user := cnpgPDBCaller(t, deny)
			rows, _ := listPDBIssues(t, s, user)
			got, plain := rows["pg-primary"], rows["plain"]
			if deny == "" && (got.Message == plain.Message || got.Severity != plain.Severity || got.Reason != plain.Reason) {
				t.Fatalf("single-instance copy missing: %+v", got)
			}
			if deny != "" && !reflect.DeepEqual(warningShape(got), warningShape(plain)) {
				t.Fatalf("caller without Clusters can tell the budgets apart: %+v", got)
			}
		})
	}
}

func TestIssueEvidenceAccessChecksExactReadsOnly(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("db-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"db"}})
	reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool { return false })
	access := s.issueEvidenceAccess(requestWithUser(http.MethodGet, "/api/issues", &auth.User{Username: "db-reader"}))
	clusters := issues.EvidenceRead{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: "db", Verb: "list"}
	if !access(clusters) || len(*reviews) != 0 {
		t.Fatalf("other findings' Cluster reads must keep namespace granularity (reviews %+v)", *reviews)
	}
	clusters.Exact = true
	if access(clusters) || len(*reviews) != 1 || (*reviews)[0] != (authv1.ResourceAttributes{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: "db", Verb: "list"}) {
		t.Fatalf("exact read not checked exactly: %+v", *reviews)
	}
}

func dashboardPDBProblems(t *testing.T, s *Server, user *auth.User) map[string]DashboardProblem {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleDashboard(w, requestWithUser(http.MethodGet, "/api/dashboard?namespace="+k8s.CNPGPDBTestNamespace, user))
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", w.Code, w.Body.String())
	}
	var resp DashboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := map[string]DashboardProblem{}
	for _, p := range resp.Problems {
		if p.Kind == "PodDisruptionBudget" {
			out[p.Name] = p
		}
	}
	if _, ok := out["plain"]; !ok {
		t.Fatalf("dashboard lost the unrecognised budget: %+v", resp.Problems)
	}
	return out
}

// Local and shared-without-auth modes read as Radar's own identity.
func TestCNPGPrimaryPDBIssuesWithoutAuth(t *testing.T) {
	installCNPGPDBFixture(t, 2)
	rows, _ := listPDBIssues(t, newAuthServer(auth.Config{Mode: "none"}), nil)
	if _, ok := rows["pg-primary"]; ok {
		t.Fatalf("expected budget shown without auth: %+v", rows)
	}
	if _, ok := rows["plain"]; !ok {
		t.Fatal("unrecognised budget missing")
	}
}
