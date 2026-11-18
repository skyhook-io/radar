package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// The MCP issues and dashboard tools drop CloudNativePG's expected primary
// budget only for a caller who can make each exact read behind it; anyone else
// gets the same warning as an unrecognised budget, in the list and the counts.
func TestCNPGPrimaryPDBFollowsTheCallersEvidence(t *testing.T) {
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	if err := k8s.NewCNPGPDBTestFixture(2).Install(""); err != nil {
		t.Fatal(err)
	}
	ns := k8s.CNPGPDBTestNamespace
	for _, deny := range []string{"", "nodes", "clusters", "pods"} {
		t.Run("deny="+deny, func(t *testing.T) {
			perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{ns}}
			perms.SetCanI("list", "postgresql.cnpg.io", "clusters", ns, deny != "clusters")
			perms.SetCanI("list", "", "pods", ns, deny != "pods")
			perms.SetCanI("list", "", "nodes", "", deny != "nodes")
			user := "db-reader-" + deny
			getPermCache().Set(user, nil, perms)
			t.Cleanup(getPermCache().Invalidate)
			ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: user})
			verified := deny == ""

			result, _, err := handleIssuesTool(ctx, nil, issuesInput{Namespace: ns, Kind: "PodDisruptionBudget"})
			if err != nil {
				t.Fatal(err)
			}
			var issuesResp issuesapi.Response
			if err := json.Unmarshal([]byte(extractText(t, result)), &issuesResp); err != nil {
				t.Fatal(err)
			}
			rows := map[string]issuesapi.Issue{}
			for _, i := range issuesResp.Issues {
				rows[i.Name] = i
			}
			got, shown := rows["pg-primary"]
			if _, ok := rows["plain"]; !ok || shown == verified || issuesResp.Total != len(rows) {
				t.Fatalf("issues tool rows=%+v total=%d verified=%v", rows, issuesResp.Total, verified)
			}
			if shown && (got.Message != rows["plain"].Message || got.Action != rows["plain"].Action || !got.FirstSeen.Equal(rows["plain"].FirstSeen)) {
				t.Fatalf("restricted caller can tell the budgets apart: %+v vs %+v", got, rows["plain"])
			}

			result, _, err = handleGetDashboard(ctx, nil, dashboardInput{Namespace: ns})
			if err != nil {
				t.Fatal(err)
			}
			var dash mcpDashboard
			if err := json.Unmarshal([]byte(extractText(t, result)), &dash); err != nil {
				t.Fatal(err)
			}
			pdbs := map[string]mcpProblem{}
			for _, p := range dash.Problems {
				if p.Kind == "PodDisruptionBudget" {
					pdbs[p.Name] = p
				}
			}
			_, onDash := pdbs["pg-primary"]
			if _, ok := pdbs["plain"]; !ok || onDash == verified {
				t.Fatalf("dashboard problems=%+v verified=%v", pdbs, verified)
			}
			total := 0
			for _, n := range dash.ProblemsBySeverity {
				total += n
			}
			if dash.TotalProblems != len(dash.Problems) || total != dash.TotalProblems {
				t.Fatalf("dashboard counts disagree with its list: total=%d bySeverity=%v problems=%d", dash.TotalProblems, dash.ProblemsBySeverity, len(dash.Problems))
			}
			if onDash && pdbs["pg-primary"].Message != pdbs["plain"].Message {
				t.Fatalf("dashboard rows differ: %+v", pdbs)
			}
		})
	}
}
