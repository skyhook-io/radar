package mcp

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/readreason"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// listReports lists Trivy vulnerability reports and returns the namespaces
// of the rows, the coverage, or the code of a failed read.
func listReports(t *testing.T, ctx context.Context, namespace string) ([]string, listWireCoverage, readreason.Code) {
	t.Helper()
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "vulnerabilityreports", Group: vulnReportGVR.Group, Namespace: namespace, Context: "none"})
	if err != nil {
		return nil, listWireCoverage{}, readErrorCode(t, err)
	}
	rows, coverage := listBody(t, result)
	out := []string{}
	for _, r := range rows {
		out = append(out, r["namespace"].(string))
	}
	slices.Sort(out)
	return out, coverage, ""
}

func readErrorCode(t *testing.T, err error) readreason.Code {
	t.Helper()
	var re *readError
	if !errors.As(err, &re) {
		t.Fatalf("want a coded read error, got %v", err)
	}
	return re.code
}

func getReport(ctx context.Context, namespace string) error {
	_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "VulnerabilityReport", Group: vulnReportGVR.Group, Namespace: namespace, Name: "replicaset-web-php", Context: "none"})
	return err
}

// MCP list_resources and get_resource gate Trivy reports by the caller's own
// permission on the report kind, like Secrets, not by namespace access: an
// ExposedSecretReport quotes the leaked credential. A namespace withheld that
// way is a user_forbidden gap, never an empty answer.
func TestTrivyReportsFollowPerKindRBAC(t *testing.T) {
	setupTrivyReports(t)
	t.Cleanup(func() { getPermCache().Invalidate() })
	g, res := vulnReportGVR.Group, vulnReportGVR.Resource

	if got, coverage, code := listReports(t, context.Background(), ""); code != "" || coverage.Status != "complete" || !slices.Equal(got, []string{"team-a", "team-b"}) {
		t.Fatalf("auth off: list in %v, %+v, %q", got, coverage, code)
	}

	ctx := withRestrictedUser(t, "bob", []string{"team-a"})
	// A permission check that can't complete withholds the reports too.
	if got, _, code := listReports(t, ctx, ""); code != readreason.UserPermissionCheckFailed || len(got) != 0 {
		t.Fatalf("unchecked report RBAC: list in %v, code %q, want user_permission_check_failed and no rows", got, code)
	}
	bob := getPermCache().Get("bob", nil)
	bob.SetCanI("list", g, res, "team-a", false)
	bob.SetCanI("get", g, res, "team-a", false)
	if got, _, code := listReports(t, ctx, ""); code != readreason.UserForbidden || len(got) != 0 {
		t.Fatalf("no report RBAC: list in %v, code %q, want user_forbidden and no rows", got, code)
	}
	if err := getReport(ctx, "team-a"); err == nil || readErrorCode(t, err) != readreason.UserForbidden {
		t.Fatalf("no report RBAC: get err = %v, want user_forbidden", err)
	}

	ctx = withRestrictedUser(t, "alice", []string{"team-a", "team-b"})
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("list", g, res, "team-a", true)
	perms.SetCanI("list", g, res, "team-b", false)
	perms.SetCanI("get", g, res, "team-a", true)
	perms.SetCanI("get", g, res, "team-b", false)
	got, coverage, code := listReports(t, ctx, "")
	if code != "" || !slices.Equal(got, []string{"team-a"}) || coverage.Status != "partial" || !slices.Equal(coverage.GapNamespaces[string(readreason.UserForbidden)], []string{"team-b"}) {
		t.Fatalf("granted in team-a: list in %v, %+v, %q; want team-a rows and a user_forbidden gap for team-b", got, coverage, code)
	}
	if err := getReport(ctx, "team-a"); err != nil {
		t.Fatalf("granted in team-a: get team-a: %v", err)
	}
	if err := getReport(ctx, "team-b"); err == nil || readErrorCode(t, err) != readreason.UserForbidden {
		t.Fatalf("granted in team-a: get team-b err = %v, want user_forbidden", err)
	}

	ctx = pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "carol"})
	carol := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	carol.SetCanI("list", g, res, "", false)
	carol.SetCanI("list", g, res, "team-a", false)
	carol.SetCanI("list", g, res, "team-b", true)
	getPermCache().Set("carol", nil, carol)
	// As with Secrets, reading every namespace needs the cluster-wide grant;
	// naming the namespace reads the reports it holds.
	if got, _, code := listReports(t, ctx, ""); code != readreason.UserForbidden || len(got) != 0 {
		t.Fatalf("cluster-wide pod reader granted in team-b: all-namespace list in %v, code %q, want user_forbidden", got, code)
	}
	if got, _, code := listReports(t, ctx, "team-b"); code != "" || !slices.Equal(got, []string{"team-b"}) {
		t.Fatalf("cluster-wide pod reader granted in team-b: team-b list in %v, code %q", got, code)
	}
}
