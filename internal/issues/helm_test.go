package issues

import (
	"strings"
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
				Message:  `Release "failed-install" failed: demo is not ready. status: InProgress, message: Available: 0/1 context deadline exceeded`,
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
	if !strings.Contains(failed.Message, "did not become ready before Helm timed out") {
		t.Fatalf("failed issue message = %q, want readiness timeout copy", failed.Message)
	}
	if strings.Contains(failed.Message, "status: InProgress") || strings.Contains(failed.Message, "Available: 0/1") || strings.Contains(failed.Message, "context deadline exceeded") {
		t.Fatalf("failed issue message leaked Helm condition-speak: %q", failed.Message)
	}
	if !strings.Contains(failed.Cause, "workload did not become ready before Helm timed out") {
		t.Fatalf("failed issue cause = %q, want readiness timeout cause", failed.Cause)
	}
	if !strings.Contains(failed.RawMessage, "status: InProgress") || !strings.Contains(failed.RawMessage, "context deadline exceeded") {
		t.Fatalf("failed issue raw message = %q, want original Helm timeout text", failed.RawMessage)
	}

	pending := got[1]
	if pending.Name != "stuck-upgrade" || pending.Severity != SeverityWarning || pending.Reason != "HelmReleasePending" {
		t.Fatalf("pending issue = %#v", pending)
	}
}

func TestNativeHelmReleaseIssueWithoutTimestampHasUnknownOnset(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	got := NativeHelmReleaseIssues([]helm.HelmRelease{{
		Name: "failed", Namespace: "apps",
		LastOperation: &helm.HelmOperation{Kind: helmhistory.KindReleaseFailed, Status: helmhistory.StatusFailed},
	}}, now)
	if len(got) != 1 || !got[0].OnsetUnknown || !got[0].FirstSeen.IsZero() || !got[0].LastSeen.Equal(now) {
		t.Fatalf("timestamp-less Helm issue fabricated onset: %+v", got)
	}
}
