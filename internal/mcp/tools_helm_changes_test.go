package mcp

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestHelmRecentChangesIncludesCurrentRevision(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := helmRecentChanges([]helm.HelmRelease{
		{
			Name:         "cart",
			Namespace:    "apps",
			Chart:        "cart",
			ChartVersion: "1.2.3",
			Status:       "deployed",
			Revision:     4,
			Updated:      now.Add(-10 * time.Minute),
		},
	}, "", time.Hour, now)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %#v", len(got), got)
	}
	change := got[0]
	if change.Source != helmChangeSource || change.Kind != "HelmRelease" || change.Namespace != "apps" || change.Name != "cart" {
		t.Fatalf("change identity = %#v", change)
	}
	if change.ChangeType != "helm_release_revision" || change.ChangeCategory != issuesapi.ChangeCategoryLifecycle {
		t.Fatalf("change type/category = %q/%q", change.ChangeType, change.ChangeCategory)
	}
}

func TestHelmRecentChangesPrefersActiveOperationOverDuplicateRevision(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-5 * time.Minute)
	got := helmRecentChanges([]helm.HelmRelease{
		{
			Name:      "cart",
			Namespace: "apps",
			Status:    "failed",
			Revision:  5,
			Updated:   updated,
			LastOperation: &helm.HelmOperation{
				Kind:     helmhistory.KindUpgradeFailed,
				Status:   helmhistory.StatusFailed,
				Revision: 5,
				Updated:  updated,
				Message:  `Upgrade "cart" failed: timed out waiting for the condition`,
			},
		},
	}, "", time.Hour, now)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %#v", len(got), got)
	}
	if got[0].ChangeType != string(helmhistory.KindUpgradeFailed) {
		t.Fatalf("changeType = %q, want %q", got[0].ChangeType, helmhistory.KindUpgradeFailed)
	}
	if got[0].ChangeCategory != issuesapi.ChangeCategorySpecConfig {
		t.Fatalf("operation change category = %q, want %q", got[0].ChangeCategory, issuesapi.ChangeCategorySpecConfig)
	}
	if got[0].Summary == "" {
		t.Fatal("operation change should carry a summary")
	}
}

func TestHelmRecentChangesSkipsFluxOwnedReleases(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := helmRecentChanges([]helm.HelmRelease{
		{
			Name:                     "flux-owned",
			Namespace:                "apps",
			Status:                   "deployed",
			Revision:                 7,
			Updated:                  now.Add(-5 * time.Minute),
			ManagedByFluxHelmRelease: "flux-system/flux-owned",
			LastOperation: &helm.HelmOperation{
				Kind:     helmhistory.KindUpgradeFailed,
				Status:   helmhistory.StatusFailed,
				Revision: 7,
				Updated:  now.Add(-5 * time.Minute),
			},
		},
	}, "", time.Hour, now)

	if len(got) != 0 {
		t.Fatalf("flux-owned Helm release changes = %#v, want none", got)
	}
}

func TestHelmRecentChangesFiltersByNameAndWindow(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := helmRecentChanges([]helm.HelmRelease{
		{Name: "cart", Namespace: "apps", Revision: 2, Updated: now.Add(-2 * time.Hour)},
		{Name: "api", Namespace: "apps", Revision: 3, Updated: now.Add(-5 * time.Minute)},
	}, "api", time.Hour, now)

	if len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("filtered changes = %#v, want only api", got)
	}
}
