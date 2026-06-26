package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
)

func TestDashboardHelmSummaryFromReleasesSortsAndLimits(t *testing.T) {
	releases := []helm.HelmRelease{
		{Name: "healthy-a", Namespace: "apps", Chart: "svc", ChartVersion: "1.0.0", Status: "deployed", ResourceHealth: "healthy"},
		{Name: "degraded", Namespace: "apps", Chart: "svc", ChartVersion: "1.1.0", Status: "deployed", ResourceHealth: "degraded"},
		{Name: "failed", Namespace: "ops", Chart: "svc", ChartVersion: "2.0.0", Status: "failed"},
		{Name: "pending", Namespace: "ops", Chart: "svc", ChartVersion: "2.1.0", Status: "pending-upgrade"},
		{Name: "unhealthy", Namespace: "apps", Chart: "svc", ChartVersion: "1.2.0", Status: "deployed", ResourceHealth: "unhealthy"},
		{Name: "rolled-back", Namespace: "apps", Chart: "svc", ChartVersion: "1.3.0", Status: "deployed", ResourceHealth: "healthy", LastOperation: &helm.HelmOperation{Kind: helmhistory.KindUpgradeRolledBack}},
		{Name: "healthy-b", Namespace: "ops", Chart: "svc", ChartVersion: "3.0.0", Status: "deployed", ResourceHealth: "healthy"},
		{Name: "healthy-c", Namespace: "ops", Chart: "svc", ChartVersion: "4.0.0", Status: "deployed", ResourceHealth: "healthy"},
	}

	got := dashboardHelmSummaryFromReleases(releases)
	if got.Total != len(releases) {
		t.Fatalf("Total = %d, want %d", got.Total, len(releases))
	}
	if got.Restricted {
		t.Fatal("Restricted = true, want false for a readable merged release list")
	}
	if len(got.Releases) != 6 {
		t.Fatalf("len(Releases) = %d, want 6", len(got.Releases))
	}
	wantNames := []string{"failed", "pending", "rolled-back", "unhealthy", "degraded", "healthy-a"}
	for i, want := range wantNames {
		if got.Releases[i].Name != want {
			t.Fatalf("Releases[%d].Name = %q, want %q", i, got.Releases[i].Name, want)
		}
	}
}

func TestDashboardHelmSummaryFromReleasesEmptyReadableList(t *testing.T) {
	got := dashboardHelmSummaryFromReleases(nil)
	if got.Total != 0 {
		t.Fatalf("Total = %d, want 0", got.Total)
	}
	if got.Restricted {
		t.Fatal("Restricted = true, want false for an empty readable release list")
	}
	if len(got.Releases) != 0 {
		t.Fatalf("len(Releases) = %d, want 0", len(got.Releases))
	}
}
