package issuecorrelation

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestHelmRecentChangesIncludesCurrentRevision(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := HelmRecentChanges([]helm.HelmRelease{
		{
			Name:         "cart",
			Namespace:    "apps",
			Chart:        "cart",
			ChartVersion: "1.2.3",
			Status:       "deployed",
			Revision:     4,
			Updated:      now.Add(-10 * time.Minute),
		},
	}, "", Window{From: now.Add(-time.Hour), To: now, OpenEnded: true})

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %#v", len(got), got)
	}
	change := got[0]
	if change.Source != HelmChangeSource || change.Kind != "HelmRelease" || change.Namespace != "apps" || change.Name != "cart" {
		t.Fatalf("change identity = %#v", change)
	}
	if change.ChangeType != "helm_release_revision" || change.ChangeCategory != issuesapi.ChangeCategoryLifecycle {
		t.Fatalf("change type/category = %q/%q", change.ChangeType, change.ChangeCategory)
	}
}

func TestHelmRecentChangesPrefersActiveOperationOverDuplicateRevision(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-5 * time.Minute)
	got := HelmRecentChanges([]helm.HelmRelease{
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
	}, "", Window{From: now.Add(-time.Hour), To: now, OpenEnded: true})

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
	got := HelmRecentChanges([]helm.HelmRelease{
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
	}, "", Window{From: now.Add(-time.Hour), To: now, OpenEnded: true})

	if len(got) != 0 {
		t.Fatalf("flux-owned Helm release changes = %#v, want none", got)
	}
}

func TestHelmRecentChangesFiltersByNameAndWindow(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := HelmRecentChanges([]helm.HelmRelease{
		{Name: "cart", Namespace: "apps", Revision: 2, Updated: now.Add(-2 * time.Hour)},
		{Name: "api", Namespace: "apps", Revision: 3, Updated: now.Add(-5 * time.Minute)},
	}, "api", Window{From: now.Add(-time.Hour), To: now, OpenEnded: true})

	if len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("filtered changes = %#v, want only api", got)
	}
}

// k3s keeps no change history of its own, so a failed revision of a release
// a HelmChart installs is still a recent change (unlike a Flux-owned one).
func TestHelmRecentChangesKeepsK3sOwnedReleases(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := HelmRecentChanges([]helm.HelmRelease{{
		Name: "traefik", Namespace: "kube-system", Status: "deployed", Revision: 7,
		Updated:       now.Add(-5 * time.Minute),
		Controller:    &helm.ReleaseController{Tool: "k3s", Kind: "HelmChart", Group: "helm.cattle.io", Namespace: "kube-system", Name: "traefik"},
		LastOperation: &helm.HelmOperation{Kind: helmhistory.KindUpgradeFailed, Status: helmhistory.StatusFailed, Revision: 7, Updated: now.Add(-5 * time.Minute)},
	}}, "", Window{From: now.Add(-time.Hour), To: now, OpenEnded: true})
	if len(got) != 1 || got[0].Name != "traefik" {
		t.Fatalf("k3s-owned Helm release changes = %#v, want the failed revision", got)
	}
}

func TestMergeHelmOperationsIncludesLiveOperation(t *testing.T) {
	history := []helm.HelmOperation{
		{
			Kind:             helmhistory.KindUpgradeRolledBack,
			Status:           helmhistory.StatusRolledBack,
			FailedRevision:   2,
			RollbackRevision: 3,
			TargetRevision:   1,
		},
	}
	live := &helm.HelmOperation{
		Kind:     helmhistory.KindUpgradeFailed,
		Status:   helmhistory.StatusFailed,
		Revision: 4,
	}

	got := MergeHelmOperations(history, live)

	if len(got) != 2 {
		t.Fatalf("len(operations) = %d, want 2: %#v", len(got), got)
	}
	if got[0].Kind != helmhistory.KindUpgradeFailed || got[0].Revision != 4 {
		t.Fatalf("operations[0] = %#v, want live failed upgrade rev 4", got[0])
	}
	if got[1].Kind != helmhistory.KindUpgradeRolledBack {
		t.Fatalf("operations[1].Kind = %q, want %q", got[1].Kind, helmhistory.KindUpgradeRolledBack)
	}
}

func TestMergeHelmOperationsDeduplicatesLastOperation(t *testing.T) {
	live := helm.HelmOperation{
		Kind:     helmhistory.KindUpgradeFailed,
		Status:   helmhistory.StatusFailed,
		Revision: 4,
	}

	got := MergeHelmOperations([]helm.HelmOperation{live}, &live)

	if len(got) != 1 {
		t.Fatalf("len(operations) = %d, want 1: %#v", len(got), got)
	}
}
