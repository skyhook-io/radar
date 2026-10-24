package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func listReportNamespaces(t *testing.T, ctx context.Context, namespace string) []string {
	t.Helper()
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "vulnerabilityreports", Group: vulnReportGVR.Group, Namespace: namespace, Context: "none"})
	if err != nil {
		t.Fatalf("list_resources: %v", err)
	}
	var rows []struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &rows); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Namespace)
	}
	slices.Sort(out)
	return out
}

func getReport(ctx context.Context, namespace string) error {
	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "VulnerabilityReport", Group: vulnReportGVR.Group, Namespace: namespace, Name: "replicaset-web-php", Context: "none"})
	return err
}

// MCP list_resources and get_resource gate Trivy reports by the caller's own
// permission on the report kind, like Secrets, not by namespace access.
func TestTrivyReportsFollowPerKindRBAC(t *testing.T) {
	setupTrivyReports(t)
	t.Cleanup(func() { getPermCache().Invalidate() })
	g, res := vulnReportGVR.Group, vulnReportGVR.Resource

	if got := listReportNamespaces(t, context.Background(), ""); !slices.Equal(got, []string{"team-a", "team-b"}) {
		t.Fatalf("auth off: list in %v", got)
	}

	ctx := withRestrictedUser(t, "bob", []string{"team-a"})
	if got := listReportNamespaces(t, ctx, ""); len(got) != 0 {
		t.Fatalf("no report RBAC: list in %v, want none", got)
	}
	if err := getReport(ctx, "team-a"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("no report RBAC: get err = %v, want forbidden", err)
	}

	ctx = withRestrictedUser(t, "alice", []string{"team-a", "team-b"})
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("list", g, res, "team-a", true)
	perms.SetCanI("list", g, res, "team-b", false)
	perms.SetCanI("get", g, res, "team-a", true)
	perms.SetCanI("get", g, res, "team-b", false)
	if got := listReportNamespaces(t, ctx, ""); !slices.Equal(got, []string{"team-a"}) {
		t.Fatalf("granted in team-a: list in %v", got)
	}
	if err := getReport(ctx, "team-a"); err != nil {
		t.Fatalf("granted in team-a: get team-a: %v", err)
	}
	if err := getReport(ctx, "team-b"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("granted in team-a: get team-b err = %v, want forbidden", err)
	}

	ctx = pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "carol"})
	carol := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	carol.SetCanI("list", g, res, "", false)
	carol.SetCanI("list", g, res, "team-a", false)
	carol.SetCanI("list", g, res, "team-b", true)
	getPermCache().Set("carol", nil, carol)
	// As with Secrets, reading every namespace needs the cluster-wide grant;
	// naming the namespace reads the reports it holds.
	if got := listReportNamespaces(t, ctx, ""); len(got) != 0 {
		t.Fatalf("cluster-wide pod reader granted in team-b: all-namespace list in %v, want none", got)
	}
	if got := listReportNamespaces(t, ctx, "team-b"); !slices.Equal(got, []string{"team-b"}) {
		t.Fatalf("cluster-wide pod reader granted in team-b: team-b list in %v", got)
	}
}
