package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestNativeHelmReleaseIssues(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-15 * time.Minute)

	releases := []helm.HelmRelease{
		{
			Name:             "failed-install",
			Namespace:        "apps",
			StorageNamespace: "helm-storage",
			LastOperation: &helm.HelmOperation{
				Kind:     helmhistory.KindReleaseFailed,
				Status:   helmhistory.StatusFailed,
				Revision: 1,
				Updated:  updated,
				Message:  `Release "failed-install" failed: context deadline exceeded`,
			},
		},
		{
			Name:      "stuck-upgrade",
			Namespace: "apps",
			LastOperation: &helm.HelmOperation{
				Kind:          helmhistory.KindPending,
				Status:        helmhistory.StatusStuck,
				Revision:      2,
				PendingStatus: "pending-upgrade",
				Updated:       updated.Add(time.Minute),
			},
		},
		{
			Name:      "recovered-rollback",
			Namespace: "apps",
			LastOperation: &helm.HelmOperation{
				Kind:             helmhistory.KindUpgradeRolledBack,
				Status:           helmhistory.StatusRolledBack,
				FailedRevision:   2,
				RollbackRevision: 3,
				TargetRevision:   1,
				Updated:          updated,
			},
		},
		{
			Name:                     "flux-owned",
			Namespace:                "apps",
			ManagedByFluxHelmRelease: "flux-system/flux-owned",
			LastOperation: &helm.HelmOperation{
				Kind:     helmhistory.KindUpgradeFailed,
				Status:   helmhistory.StatusFailed,
				Revision: 4,
				Updated:  updated,
			},
		},
	}

	got := NativeHelmReleaseIssues(releases, now)
	if len(got) != 2 {
		t.Fatalf("len(NativeHelmReleaseIssues) = %d, want 2: %#v", len(got), got)
	}

	failed := got[0]
	if failed.Name != "failed-install" || failed.Namespace != "helm-storage" || failed.Group != NativeHelmGroup {
		t.Fatalf("failed issue ref = %s/%s/%s, group=%q", failed.Kind, failed.Namespace, failed.Name, failed.Group)
	}
	if failed.Severity != SeverityCritical || failed.Category != issuesapi.CategoryHelmReleaseFailed || failed.CategoryGroup != issuesapi.GroupControlPlane {
		t.Fatalf("failed issue category/severity = %s/%s/%s", failed.Severity, failed.Category, failed.CategoryGroup)
	}
	if !failed.Stuck || failed.FirstSeen != updated || failed.LastSeen != now {
		t.Fatalf("failed issue timing/stuck = stuck:%v first:%v last:%v", failed.Stuck, failed.FirstSeen, failed.LastSeen)
	}

	pending := got[1]
	if pending.Name != "stuck-upgrade" || pending.Severity != SeverityWarning || pending.Reason != "HelmReleasePending" {
		t.Fatalf("pending issue = %#v", pending)
	}
}
