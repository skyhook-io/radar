package helmhistory

import (
	"testing"
	"time"
)

func TestAnalyzeCurrentFailedUpgrade(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 2, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-2*time.Hour)),
		rev(2, "failed", `Upgrade "cart" failed: timed out waiting for the condition`, now.Add(-5*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindUpgradeFailed || got.LastOperation.Status != StatusFailed {
		t.Fatalf("LastOperation = %#v, want upgrade failed", got.LastOperation)
	}
	if got.LastOperation.Source != SourceStatus {
		t.Fatalf("source = %q, want %q", got.LastOperation.Source, SourceStatus)
	}
	if len(got.Operations) != 0 {
		t.Fatalf("Operations = %#v, want no duplicate of live failed revision", got.Operations)
	}
}

func TestAnalyzeUpgradeRolledBackAfterFailure(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 3, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-3*time.Hour)),
		rev(2, "failed", `Upgrade "cart" failed: timed out waiting for the condition`, now.Add(-10*time.Minute)),
		rev(3, "deployed", "Rollback to 1", now.Add(-9*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindUpgradeRolledBack {
		t.Fatalf("kind = %q, want %q", got.LastOperation.Kind, KindUpgradeRolledBack)
	}
	if got.LastOperation.FailedRevision != 2 || got.LastOperation.RollbackRevision != 3 || got.LastOperation.TargetRevision != 1 {
		t.Fatalf("revisions = failed:%d rollback:%d target:%d", got.LastOperation.FailedRevision, got.LastOperation.RollbackRevision, got.LastOperation.TargetRevision)
	}
	if got.LastOperation.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %q, want %q", got.LastOperation.Confidence, ConfidenceMedium)
	}
	if got.LastOperation.FailureDescription != `Upgrade "cart" failed: timed out waiting for the condition` {
		t.Fatalf("failureDescription = %q", got.LastOperation.FailureDescription)
	}
}

func TestAnalyzeUpgradeFailureDescriptionIsCaseInsensitive(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 2, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-2*time.Hour)),
		rev(2, "failed", `upgrade "cart" failed: timed out waiting for the condition`, now.Add(-5*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindUpgradeFailed {
		t.Fatalf("kind = %q, want %q", got.LastOperation.Kind, KindUpgradeFailed)
	}
}

func TestAnalyzeQuotedUpgradeDescriptionRequiresFailedPrefix(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 2, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-2*time.Hour)),
		rev(2, "failed", `Upgrade "cart" canceled by operator`, now.Add(-5*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindReleaseFailed {
		t.Fatalf("kind = %q, want %q", got.LastOperation.Kind, KindReleaseFailed)
	}
}

func TestAnalyzeNormalizesRevisionStatuses(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 3, []Revision{
		rev(1, " Superseded ", "Install complete", now.Add(-3*time.Hour)),
		rev(2, " Failed ", `Upgrade "cart" failed: timed out waiting for the condition`, now.Add(-10*time.Minute)),
		rev(3, " Deployed ", "Rollback to 1", now.Add(-9*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindUpgradeRolledBack {
		t.Fatalf("kind = %q, want %q", got.LastOperation.Kind, KindUpgradeRolledBack)
	}
}

func TestAnalyzeManualRollback(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 3, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-3*time.Hour)),
		rev(2, "superseded", "Upgrade complete", now.Add(-2*time.Hour)),
		rev(3, "deployed", "Rollback to 1", now.Add(-time.Hour)),
	}, Options{Now: now})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindRollback || got.LastOperation.TargetRevision != 1 {
		t.Fatalf("LastOperation = %#v, want rollback to 1", got.LastOperation)
	}
}

func TestAnalyzeCustomizedRollbackDescriptionDoesNotClaimRollback(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 3, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-3*time.Hour)),
		rev(2, "failed", `Upgrade "cart" failed: timed out waiting for the condition`, now.Add(-10*time.Minute)),
		rev(3, "deployed", "operator restored previous chart", now.Add(-9*time.Minute)),
	}, Options{Now: now})

	if got.LastOperation != nil {
		t.Fatalf("LastOperation = %#v, want nil because rollback target is not durable", got.LastOperation)
	}
	if len(got.Operations) != 1 || got.Operations[0].Kind != KindUpgradeFailed {
		t.Fatalf("Operations = %#v, want failed operation only", got.Operations)
	}
}

func TestAnalyzeStalePending(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 4, []Revision{
		rev(4, "pending-upgrade", "Preparing upgrade", now.Add(-18*time.Minute)),
	}, Options{Now: now, PendingStuckAfter: 10 * time.Minute})

	if got.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if got.LastOperation.Kind != KindPending || got.LastOperation.Status != StatusStuck {
		t.Fatalf("LastOperation = %#v, want stuck pending", got.LastOperation)
	}
}

func TestAnalyzeRecentPendingIsNotStuck(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 4, []Revision{
		rev(4, "pending-upgrade", "Preparing upgrade", now.Add(-2*time.Minute)),
	}, Options{Now: now, PendingStuckAfter: 10 * time.Minute})

	if got.LastOperation != nil {
		t.Fatalf("LastOperation = %#v, want nil for recent pending", got.LastOperation)
	}
}

func TestAnalyzeCapsOperations(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 7, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-7*time.Hour)),
		rev(2, "deployed", "Rollback to 1", now.Add(-6*time.Hour)),
		rev(3, "failed", `Upgrade "cart" failed: failed`, now.Add(-5*time.Hour)),
		rev(4, "deployed", "Rollback to 2", now.Add(-4*time.Hour)),
		rev(5, "failed", `Upgrade "cart" failed: failed`, now.Add(-3*time.Hour)),
		rev(6, "deployed", "Rollback to 4", now.Add(-2*time.Hour)),
		rev(7, "deployed", "Upgrade complete", now.Add(-time.Hour)),
	}, Options{Now: now, MaxOperations: 2})

	if len(got.Operations) != 2 {
		t.Fatalf("len(Operations) = %d, want 2", len(got.Operations))
	}
	if got.Operations[0].RollbackRevision != 6 || got.Operations[1].RollbackRevision != 4 {
		t.Fatalf("Operations not newest first: %#v", got.Operations)
	}
}

func TestAnalyzeOldRollbackIsNotLastAfterLaterUpgrade(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	got := Analyze("cart", 4, []Revision{
		rev(1, "superseded", "Install complete", now.Add(-4*time.Hour)),
		rev(2, "failed", `Upgrade "cart" failed: failed`, now.Add(-3*time.Hour)),
		rev(3, "superseded", "Rollback to 1", now.Add(-2*time.Hour)),
		rev(4, "deployed", "Upgrade complete", now.Add(-time.Hour)),
	}, Options{Now: now})

	if got.LastOperation != nil {
		t.Fatalf("LastOperation = %#v, want nil because current revision is a later successful upgrade", got.LastOperation)
	}
	if len(got.Operations) != 1 || got.Operations[0].Kind != KindUpgradeRolledBack {
		t.Fatalf("Operations = %#v, want historical rolled-back operation", got.Operations)
	}
}

func rev(version int, status, description string, updated time.Time) Revision {
	return Revision{
		Revision:    version,
		Status:      status,
		Description: description,
		Updated:     updated,
	}
}
