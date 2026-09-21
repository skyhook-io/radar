package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var cnpgClusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
var cnpgBackupGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "backups"}

func cnpgCluster(spec, status map[string]any) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]any{}
	}
	if status == nil {
		status = map[string]any{}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "pg-main", "namespace": "pg"},
		"spec":       spec,
		"status":     status,
	}}
}

func cnpgCondition(condType, status, reason, message string) map[string]any {
	return map[string]any{
		"type": condType, "status": status, "reason": reason, "message": message,
		"lastTransitionTime": "2026-04-28T10:00:00Z",
	}
}

func findIssue(t *testing.T, issues []Issue, reason string) Issue {
	t.Helper()
	for _, i := range issues {
		if i.Reason == reason {
			return i
		}
	}
	t.Fatalf("no issue with reason %q; got %v", reason, reasonsOf(issues))
	return Issue{}
}

// A cluster whose pods are all Ready but whose phase says it is unrecoverable
// must not read as fine.
func TestCNPGTerminalPhasesAreCriticalDespiteReadyInstances(t *testing.T) {
	cases := []struct {
		phase      string
		wantReason string
	}{
		{"Cluster is unrecoverable and needs manual intervention", "CNPGClusterUnrecoverable"},
		{"Cluster cannot proceed to reconciliation due to an unknown plugin being required", "CNPGClusterPluginFailure"},
		{"Cluster cannot proceed to reconciliation due to an error while interacting with plugins", "CNPGClusterPluginFailure"},
		{"Unable to create required cluster objects", "CNPGClusterTerminal"},
		{"Cluster has incomplete or invalid image catalog", "CNPGClusterTerminal"},
		{"Cluster cannot execute instance online upgrade due to missing architecture binary", "CNPGClusterTerminal"},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			u := cnpgCluster(
				map[string]any{"instances": int64(3)},
				map[string]any{"phase": tc.phase, "readyInstances": int64(3)},
			)
			issues := detectCNPGIssues(cnpgClusterGVR, "Cluster", u)
			iss := findIssue(t, issues, tc.wantReason)
			if iss.Severity != SeverityCritical {
				t.Errorf("severity = %q, want critical", iss.Severity)
			}
			if iss.Message != tc.phase {
				t.Errorf("message = %q, want the phase verbatim", iss.Message)
			}
		})
	}
}

func TestCNPGWALArchivingFailureIsCritical(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(2)},
		map[string]any{
			"phase": "Cluster in healthy state", "readyInstances": int64(2),
			"conditions": []any{
				cnpgCondition("Ready", "True", "ClusterIsReady", "Cluster is Ready"),
				cnpgCondition("ContinuousArchiving", "False", "ContinuousArchivingFailing", "unable to upload WAL"),
			},
		},
	)
	iss := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", u), "CNPGWALArchivingFailing")
	if iss.Severity != SeverityCritical {
		t.Errorf("severity = %q, want critical — the cluster serves traffic while the recovery point stalls", iss.Severity)
	}
	if !strings.Contains(iss.Message, "unable to upload WAL") {
		t.Errorf("message should carry the operator's own text, got %q", iss.Message)
	}
	// Must not claim an exact RPO — the condition only proves the last attempt failed.
	if strings.Contains(strings.ToLower(iss.Message), "data loss") {
		t.Errorf("message overclaims: %q", iss.Message)
	}
}

func TestCNPGHealthyClusterRaisesNothing(t *testing.T) {
	// Shape taken from a live CNPG 1.27 cluster.
	u := cnpgCluster(
		map[string]any{"instances": int64(2)},
		map[string]any{
			"phase": "Cluster in healthy state", "readyInstances": int64(2), "instances": int64(2),
			"conditions": []any{
				cnpgCondition("ConsistentSystemID", "True", "Unique", "A single, unique system ID was found across reporting instances."),
				cnpgCondition("Ready", "True", "ClusterIsReady", "Cluster is Ready"),
				cnpgCondition("ContinuousArchiving", "True", "ContinuousArchivingSuccess", "Continuous archiving is working"),
			},
		},
	)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
		t.Fatalf("healthy cluster raised %v", reasonsOf(got))
	}
}

// CNPG sets LastBackupSucceeded=False/BackupStarted while a backup is merely
// starting up. Raising an issue there fires on every backup run.
func TestCNPGBackupStartedIsNotAFailure(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(1)},
		map[string]any{
			"phase": "Cluster in healthy state", "readyInstances": int64(1),
			"conditions": []any{cnpgCondition("LastBackupSucceeded", "False", "BackupStarted", "New Backup starting up")},
		},
	)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
		t.Fatalf("in-flight backup raised %v", reasonsOf(got))
	}
}

func TestCNPGLastBackupFailedIsAWarning(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(1)},
		map[string]any{
			"phase": "Cluster in healthy state", "readyInstances": int64(1),
			"conditions": []any{cnpgCondition("LastBackupSucceeded", "False", "LastBackupFailed", "no credentials")},
		},
	)
	iss := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", u), "CNPGLastBackupFailed")
	if iss.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", iss.Severity)
	}
}

// Under `supervised` the operator is SUPPOSED to wait for a human — flagging it
// would light permanently on every supervised cluster.
func TestCNPGWaitingForUserRespectsSupervisedStrategy(t *testing.T) {
	status := map[string]any{"phase": "Waiting for user action", "readyInstances": int64(2)}

	supervised := cnpgCluster(map[string]any{"instances": int64(2), "primaryUpdateStrategy": "supervised"}, status)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", supervised); len(got) != 0 {
		t.Fatalf("supervised cluster raised %v", reasonsOf(got))
	}

	unsupervised := cnpgCluster(map[string]any{"instances": int64(2), "primaryUpdateStrategy": "unsupervised"}, status)
	iss := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", unsupervised), "CNPGClusterWaitingForUser")
	if iss.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", iss.Severity)
	}
}

func TestCNPGTransientPhaseDoesNotRaiseDegraded(t *testing.T) {
	// Mid-operation with fewer ready instances is expected, not a defect.
	u := cnpgCluster(
		map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Creating a new replica", "readyInstances": int64(2)},
	)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
		t.Fatalf("transient phase raised %v", reasonsOf(got))
	}
}

func TestCNPGDegradedInstancesUnderHealthyPhase(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Cluster in healthy state", "readyInstances": int64(1)},
	)
	iss := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", u), "CNPGClusterDegraded")
	if iss.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", iss.Severity)
	}

	down := cnpgCluster(
		map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Cluster in healthy state", "currentPrimary": "pg-1", "readyInstances": int64(0)},
	)
	if got := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", down), "CNPGClusterDegraded"); got.Severity != SeverityCritical {
		t.Errorf("all instances down: severity = %q, want critical", got.Severity)
	}
}

func TestCNPGUnknownPhaseRaisesNothing(t *testing.T) {
	// A phase from a future CNPG minor must not be guessed at.
	u := cnpgCluster(
		map[string]any{"instances": int64(1)},
		map[string]any{"phase": "Reticulating splines", "readyInstances": int64(1)},
	)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
		t.Fatalf("unknown phase raised %v", reasonsOf(got))
	}
}

func TestCNPGBackupPhases(t *testing.T) {
	backup := func(phase, errMsg string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Backup",
			"metadata":   map[string]any{"name": "b1", "namespace": "pg"},
			"status":     map[string]any{"phase": phase, "error": errMsg},
		}}
	}

	walIssues := detectCNPGIssues(cnpgBackupGVR, "Backup", backup("walArchivingFailing", "S3 timeout"))
	iss := findIssue(t, walIssues, "CNPGWALArchivingFailing")
	if iss.Severity != SeverityCritical {
		t.Errorf("walArchivingFailing severity = %q, want critical", iss.Severity)
	}
	if !strings.Contains(iss.Message, "S3 timeout") {
		t.Errorf("message should carry status.error, got %q", iss.Message)
	}

	failed := findIssue(t, detectCNPGIssues(cnpgBackupGVR, "Backup", backup("failed", "boom")), "CNPGBackupFailed")
	if failed.Severity != SeverityWarning {
		t.Errorf("failed severity = %q, want warning", failed.Severity)
	}

	// Every non-failure phase CNPG 1.27 ships.
	for _, phase := range []string{"pending", "started", "running", "finalizing", "completed", ""} {
		if got := detectCNPGIssues(cnpgBackupGVR, "Backup", backup(phase, "")); len(got) != 0 {
			t.Errorf("phase %q raised %v", phase, reasonsOf(got))
		}
	}
}

// Kinds without a curated detector must return nothing so the generic
// condition path still gets its turn (detectCuratedConditionIssues skips the
// generic walk only when the curated result is non-empty).
func TestCNPGUncuratedKindsFallThrough(t *testing.T) {
	for _, kind := range []string{"Pooler", "ScheduledBackup", "Database"} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1", "kind": kind,
			"metadata": map[string]any{"name": "x", "namespace": "pg"},
			"status":   map[string]any{"phase": "whatever"},
		}}
		if got := detectCNPGIssues(cnpgClusterGVR, kind, u); got != nil {
			t.Errorf("kind %s returned %v, want nil so the generic path runs", kind, reasonsOf(got))
		}
	}
}

// A cluster can be unrecoverable AND failing to archive WAL at the same time.
// Issue IDs are category-only unless a Fingerprint is supplied, so without one
// these two collapse into a single row and the archiving cause — the whole
// point of the detector — is the one that gets dropped.
func TestCNPGConcurrentCausesGetDistinctIdentities(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(2)},
		map[string]any{
			"phase": "Cluster is unrecoverable and needs manual intervention", "readyInstances": int64(2),
			"conditions": []any{
				cnpgCondition("ContinuousArchiving", "False", "ContinuousArchivingFailing", "unable to upload WAL"),
				cnpgCondition("LastBackupSucceeded", "False", "LastBackupFailed", "no credentials"),
			},
		},
	)
	issues := detectCNPGIssues(cnpgClusterGVR, "Cluster", u)
	if len(issues) != 3 {
		t.Fatalf("expected 3 concurrent causes, got %d: %v", len(issues), reasonsOf(issues))
	}

	ids := map[string]string{}
	for _, i := range issues {
		if i.Fingerprint == "" {
			t.Errorf("issue %q has no Fingerprint — it will collide on ID", i.Reason)
		}
		if prev, dup := ids[i.ID]; dup {
			t.Errorf("issues %q and %q share ID %q — one will be dropped", prev, i.Reason, i.ID)
		}
		ids[i.ID] = i.Reason
	}
}

// A backup-condition issue must never suppress the instance-shortfall issue:
// they are unrelated causes, and letting a backup warning hide a cluster with
// zero ready instances buries the actual outage.
func TestCNPGBackupConditionDoesNotSuppressOutage(t *testing.T) {
	for _, cond := range []map[string]any{
		cnpgCondition("LastBackupSucceeded", "False", "LastBackupFailed", "no credentials"),
		cnpgCondition("ContinuousArchiving", "False", "ContinuousArchivingFailing", "cannot upload"),
	} {
		u := cnpgCluster(
			map[string]any{"instances": int64(3)},
			map[string]any{
				"phase": "Cluster in healthy state", "currentPrimary": "pg-1", "readyInstances": int64(0),
				"conditions": []any{cond},
			},
		)
		issues := detectCNPGIssues(cnpgClusterGVR, "Cluster", u)
		degraded := findIssue(t, issues, "CNPGClusterDegraded")
		if degraded.Severity != SeverityCritical {
			t.Errorf("%v: outage severity = %q, want critical", cond["type"], degraded.Severity)
		}
	}
}

// Upstream sets PhaseUpgradeDelayed when the OPERATOR's rollout-delay config
// postpones an upgrade and requeues — it is self-resolving and unrelated to
// primaryUpdateStrategy, so it must not raise a waiting-for-user issue.
func TestCNPGUpgradeDelayedIsTransientNotAttention(t *testing.T) {
	for _, strategy := range []string{"unsupervised", "supervised", ""} {
		u := cnpgCluster(
			map[string]any{"instances": int64(2), "primaryUpdateStrategy": strategy},
			map[string]any{"phase": "Cluster upgrade delayed", "readyInstances": int64(1)},
		)
		if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
			t.Errorf("strategy %q: rollout-delay raised %v", strategy, reasonsOf(got))
		}
	}
}

// Mirror of the TS "badge/issue agreement on instance readiness" suite in
// resource-utils-cnpg.test.ts. Same fixtures, asserted from the detector side:
// a green badge with a Degraded issue (or the reverse) is the drift this
// integration was repaired to prevent.
func TestCNPGBadgeAndIssueAgreeOnReadiness(t *testing.T) {
	cases := []struct {
		name      string
		phase     string
		desired   int64
		ready     int64
		wantIssue bool
	}{
		{"healthy phase, shortfall — badge Degraded", "Cluster in healthy state", 3, 1, true},
		{"healthy phase, count met — badge Healthy", "Cluster in healthy state", 3, 3, false},
		{"transient phase explains the shortfall — badge shows the phase", "Creating a new replica", 3, 1, false},
		{"unrecognized phase, shortfall — badge Degraded", "Some future phase", 3, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := cnpgCluster(
				map[string]any{"instances": tc.desired},
				map[string]any{"phase": tc.phase, "readyInstances": tc.ready},
			)
			issues := detectCNPGIssues(cnpgClusterGVR, "Cluster", u)
			var degraded bool
			for _, i := range issues {
				if i.Reason == "CNPGClusterDegraded" {
					degraded = true
				}
			}
			if degraded != tc.wantIssue {
				t.Errorf("CNPGClusterDegraded = %v, want %v (issues: %v)", degraded, tc.wantIssue, reasonsOf(issues))
			}
		})
	}
}

// Under `supervised` the operator waits for a human, which is exactly when
// instances sit below the desired count. The phase explains that, so the
// shortfall must not fire — the badge shows the attention phase, and an issue
// saying "degraded" alongside it is the disagreement this detector prevents.
func TestCNPGSupervisedWaitSuppressesShortfall(t *testing.T) {
	u := cnpgCluster(
		map[string]any{"instances": int64(3), "primaryUpdateStrategy": "supervised"},
		map[string]any{"phase": "Waiting for user action", "readyInstances": int64(1)},
	)
	if got := detectCNPGIssues(cnpgClusterGVR, "Cluster", u); len(got) != 0 {
		t.Fatalf("supervised wait with a shortfall raised %v", reasonsOf(got))
	}

	// Unsupervised still reports the wait itself, and still only once.
	un := cnpgCluster(
		map[string]any{"instances": int64(3), "primaryUpdateStrategy": "unsupervised"},
		map[string]any{"phase": "Waiting for user action", "readyInstances": int64(1)},
	)
	got := detectCNPGIssues(cnpgClusterGVR, "Cluster", un)
	if len(got) != 1 || got[0].Reason != "CNPGClusterWaitingForUser" {
		t.Fatalf("unsupervised wait = %v, want exactly CNPGClusterWaitingForUser", reasonsOf(got))
	}
}

// CNPG reports Ready=False/ClusterIsNotReady throughout a switchover or replica
// creation. That reason is not in conditions' transient set, so without an
// explicit opt-out the generic CRD walk re-emits the warning the phase buckets
// just suppressed.
func TestCNPGTransientPhasesSuppressGenericConditionWalk(t *testing.T) {
	for _, phase := range []string{
		"Switchover in progress", "Creating a new replica", "Upgrading cluster",
		"Upgrading Postgres major version", "Waiting for user action",
	} {
		u := cnpgCluster(map[string]any{"instances": int64(3)}, map[string]any{"phase": phase})
		if !cnpgSuppressesGenericConditions("postgresql.cnpg.io", "Cluster", u) {
			t.Errorf("phase %q should suppress the generic condition walk", phase)
		}
	}

	// Outside those windows a False Ready is still worth surfacing — blanket
	// ownership would silence real reasons like DetachedVolume.
	for _, phase := range []string{
		"Cluster in healthy state", "Cluster is unrecoverable and needs manual intervention", "",
	} {
		u := cnpgCluster(map[string]any{"instances": int64(3)}, map[string]any{"phase": phase})
		if cnpgSuppressesGenericConditions("postgresql.cnpg.io", "Cluster", u) {
			t.Errorf("phase %q must NOT suppress the generic walk", phase)
		}
	}

	// The age bound applies to TRANSIENT phases only. A stuck mid-operation
	// cluster must surface; a supervised cluster parked on "Waiting for user
	// action" is the documented resting state and must stay quiet however long
	// it lasts, or a supported configuration becomes permanently lit.
	stale := []any{map[string]any{"type": "Ready", "status": "False",
		"reason": "ClusterIsNotReady", "lastTransitionTime": "2020-01-01T00:00:00Z"}}

	stuckTransient := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Creating a new replica", "readyInstances": int64(2), "conditions": stale})
	if cnpgSuppressesGenericConditions("postgresql.cnpg.io", "Cluster", stuckTransient) {
		t.Error("a transient phase stuck past the grace must stop suppressing")
	}

	parkedSupervised := cnpgCluster(
		map[string]any{"instances": int64(3), "primaryUpdateStrategy": "supervised"},
		map[string]any{"phase": "Waiting for user action", "readyInstances": int64(2), "conditions": stale})
	if !cnpgSuppressesGenericConditions("postgresql.cnpg.io", "Cluster", parkedSupervised) {
		t.Error("a supervised wait must keep suppressing however long it lasts")
	}

	// Scoped to CNPG Clusters only.
	u := cnpgCluster(map[string]any{}, map[string]any{"phase": "Switchover in progress"})
	if cnpgSuppressesGenericConditions("postgresql.cnpg.io", "Pooler", u) {
		t.Error("suppression must not extend to other CNPG kinds")
	}
	if cnpgSuppressesGenericConditions("cluster.x-k8s.io", "Cluster", u) {
		t.Error("suppression must not extend to other groups' Cluster kinds")
	}
}

// No phase excuses a WAS-UP database with nothing serving. currentPrimary is
// present (the cluster elected a primary before), and readyInstances is OMITTED
// — the shape CNPG actually emits for zero ready. Suppressing the shortfall for
// attention/transient phases is right for a PARTIAL shortfall, but a was-up
// cluster with nothing serving is an outage, and once the generic condition walk
// is also suppressed for those phases, silence here means no signal anywhere.
func TestCNPGZeroReadyIsNeverExcusedByPhase(t *testing.T) {
	cases := []struct {
		phase    string
		strategy string
	}{
		{"Waiting for user action", "supervised"},
		{"Waiting for user action", "unsupervised"},
		{"Cluster upgrade delayed", ""},
		{"Switchover in progress", ""},
		{"Creating a new replica", ""},
		{"Upgrading Postgres major version", ""},
	}
	for _, tc := range cases {
		t.Run(tc.phase+"/"+tc.strategy, func(t *testing.T) {
			spec := map[string]any{"instances": int64(3)}
			if tc.strategy != "" {
				spec["primaryUpdateStrategy"] = tc.strategy
			}
			// currentPrimary set, readyInstances omitted, no Ready=False condition:
			// a was-up cluster reporting nothing ready, escalated immediately.
			u := cnpgCluster(spec, map[string]any{"phase": tc.phase, "currentPrimary": "pg-1"})
			iss := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", u), "CNPGClusterDegraded")
			if iss.Severity != SeverityCritical {
				t.Errorf("severity = %q, want critical for a fully-down cluster", iss.Severity)
			}
		})
	}

	// A partial shortfall under the same phases stays suppressed — that is the
	// legitimate operator-workflow case.
	for _, phase := range []string{"Waiting for user action", "Switchover in progress"} {
		u := cnpgCluster(
			map[string]any{"instances": int64(3), "primaryUpdateStrategy": "supervised"},
			map[string]any{"phase": phase, "currentPrimary": "pg-1", "readyInstances": int64(2)},
		)
		for _, i := range detectCNPGIssues(cnpgClusterGVR, "Cluster", u) {
			if i.Reason == "CNPGClusterDegraded" {
				t.Errorf("phase %q: partial shortfall should stay suppressed", phase)
			}
		}
	}
}

// The all-down verdict distinguishes the three states that a bare zero-ready
// count collapses together: a first bootstrap (never elected a primary), a
// deliberate not-serving state (hibernation / all-fenced), and a genuine
// regression. Only the last raises an issue.
func TestCNPGZeroReadyDistinguishesBootstrapFromRegression(t *testing.T) {
	degraded := func(u *unstructured.Unstructured) bool {
		for _, i := range detectCNPGIssues(cnpgClusterGVR, "Cluster", u) {
			if i.Reason == "CNPGClusterDegraded" {
				return true
			}
		}
		return false
	}

	// First bootstrap: reported (phase) but no primary ever elected, readyInstances
	// omitted. Nothing was serving to lose, so no alarm.
	bootstrap := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Setting up primary"})
	if degraded(bootstrap) {
		t.Error("a first bootstrap with no elected primary must not raise CNPGClusterDegraded")
	}

	// Hibernation is a deliberate scale-to-zero, signalled by annotation.
	hibernated := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"currentPrimary": "pg-1"})
	hibernated.SetAnnotations(map[string]string{"cnpg.io/hibernation": "on"})
	if degraded(hibernated) {
		t.Error("a hibernated cluster must not raise CNPGClusterDegraded")
	}

	// All instances fenced: PostgreSQL stopped on purpose, pods still up.
	fenced := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"currentPrimary": "pg-1"})
	fenced.SetAnnotations(map[string]string{"cnpg.io/fencedInstances": `["*"]`})
	if degraded(fenced) {
		t.Error("an all-fenced cluster must not raise CNPGClusterDegraded")
	}

	// A partially-fenced cluster is NOT carved out — "*" is the only all-down token.
	partialFence := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Waiting for the instances to become active", "currentPrimary": "pg-1"})
	partialFence.SetAnnotations(map[string]string{"cnpg.io/fencedInstances": `["pg-1-2"]`})
	if !degraded(partialFence) {
		t.Error("a partial fence must not suppress the outage of a was-up cluster")
	}

	// Regression: was up (currentPrimary), now nothing ready — readyInstances
	// omitted, the shape CNPG emits at zero. Must raise the outage.
	regression := cnpgCluster(map[string]any{"instances": int64(3)},
		map[string]any{"phase": "Waiting for the instances to become active", "currentPrimary": "pg-1"})
	if !degraded(regression) {
		t.Error("a was-up cluster with nothing ready must raise CNPGClusterDegraded")
	}
}

// The regression escalation is debounced on the Ready=False transition so a
// single instance restarting during a routine rollout does not read as a total
// outage. Past the grace, or with no Ready condition at all, it escalates.
func TestCNPGDownGraceDebouncesRoutineRestart(t *testing.T) {
	degraded := func(readyLTT string) bool {
		status := map[string]any{
			"phase":          "Waiting for the instances to become active",
			"currentPrimary": "pg-1",
		}
		if readyLTT != "" {
			status["conditions"] = []any{map[string]any{
				"type": "Ready", "status": "False", "reason": "ClusterIsNotReady",
				"lastTransitionTime": readyLTT,
			}}
		}
		u := cnpgCluster(map[string]any{"instances": int64(3)}, status)
		for _, i := range detectCNPGIssues(cnpgClusterGVR, "Cluster", u) {
			if i.Reason == "CNPGClusterDegraded" {
				return true
			}
		}
		return false
	}

	recent := time.Now().Add(-time.Minute).Format(time.RFC3339)
	if degraded(recent) {
		t.Error("a Ready=False younger than the grace must be debounced, not escalated")
	}

	old := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	if !degraded(old) {
		t.Error("a Ready=False older than the grace must escalate")
	}

	// No Ready condition on a was-up cluster: nothing is debouncing, so escalate
	// immediately rather than inventing a start time from creationTimestamp.
	if !degraded("") {
		t.Error("a was-up cluster with no Ready condition must escalate immediately")
	}
}

// A lost recovery point is a backup problem, and the Problems surface says so
// by category. Filing it under the generic controller category rendered it as
// "Controller reports a problem" and left it out of any backup-shaped filter,
// while Velero's identical failures were categorised correctly.
func TestCNPGBackupSignalsUseTheBackupCategory(t *testing.T) {
	walFailing := cnpgCluster(
		map[string]any{"instances": int64(2)},
		map[string]any{
			"phase":          "Cluster in healthy state",
			"readyInstances": int64(2),
			"conditions": []any{
				cnpgCondition("ContinuousArchiving", "False", "ContinuousArchivingFailing", "unreachable"),
				cnpgCondition("LastBackupSucceeded", "False", "BackupFailed", "upload rejected"),
			},
		})

	got := detectCNPGIssues(cnpgClusterGVR, "Cluster", walFailing)
	for _, reason := range []string{"CNPGWALArchivingFailing", "CNPGLastBackupFailed"} {
		if c := findIssue(t, got, reason).Category; c != issuesapi.CategoryBackupFailed {
			t.Errorf("%s category = %q, want %q", reason, c, issuesapi.CategoryBackupFailed)
		}
	}

	backup := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]any{"name": "b1", "namespace": "pg"},
		"status":     map[string]any{"phase": "failed", "error": "boom"},
	}}
	if c := findIssue(t, detectCNPGIssues(cnpgBackupGVR, "Backup", backup), "CNPGBackupFailed").Category; c != issuesapi.CategoryBackupFailed {
		t.Errorf("CNPGBackupFailed category = %q, want %q", c, issuesapi.CategoryBackupFailed)
	}
}

// The cluster's own health is not a backup problem. Without this the backup
// filter would collect every unrelated CNPG fault and stop meaning anything.
func TestCNPGClusterHealthKeepsTheControllerCategory(t *testing.T) {
	unrecoverable := cnpgCluster(
		map[string]any{"instances": int64(2)},
		map[string]any{"phase": cnpgPhaseUnrecoverable, "readyInstances": int64(2)})

	got := findIssue(t, detectCNPGIssues(cnpgClusterGVR, "Cluster", unrecoverable), "CNPGClusterUnrecoverable")
	if got.Category != issuesapi.CategoryOperatorConditionFail {
		t.Errorf("category = %q, want %q", got.Category, issuesapi.CategoryOperatorConditionFail)
	}
}

// A future CNPG minor can name a Backup failure anything it likes; the object
// it happened to is what makes it backup-shaped.
func TestCNPGBackupKindIsBackupShapedWhateverTheReason(t *testing.T) {
	in := classifyInput{Source: SourceCondition, APIGroup: "postgresql.cnpg.io", Kind: "Backup", Reason: "SomethingCNPGInventsLater"}
	if got := Classify(in); got != issuesapi.CategoryBackupFailed {
		t.Errorf("category = %q, want %q", got, issuesapi.CategoryBackupFailed)
	}
}

// `status.applied` is three-valued and only `false` is a failure. Verified
// against CNPG 1.27: demo-broken reports false with the operator's SQL error,
// demo-app reports true, and both are briefly absent before first reconcile.
func TestCNPGDeclarativeNotApplied(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}
	obj := func(status map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Database",
			"metadata":   map[string]any{"name": "demo-broken", "namespace": "pg"},
		}}
		if status != nil {
			u.Object["status"] = status
		}
		return u
	}

	msg := `while creating database "demo_broken": ERROR: role "nobody-owns-this" does not exist (SQLSTATE 42704)`
	got := detectCNPGIssues(gvr, "Database", obj(map[string]any{"applied": false, "message": msg}))
	if len(got) != 1 {
		t.Fatalf("applied=false produced %d issues, want 1", len(got))
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("severity = %v, want warning", got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "nobody-owns-this") {
		t.Errorf("message drops the operator's reason: %q", got[0].Message)
	}
	if !got[0].OnsetUnknown || !got[0].FirstSeen.IsZero() {
		t.Errorf("applied=false has no transition timestamp; onset = unknown:%v first:%v", got[0].OnsetUnknown, got[0].FirstSeen)
	}

	// Pending is not failure. Reporting it would raise an issue against every
	// declarative object for the first seconds of its life.
	if n := len(detectCNPGIssues(gvr, "Database", obj(nil))); n != 0 {
		t.Errorf("no status produced %d issues, want 0", n)
	}
	if n := len(detectCNPGIssues(gvr, "Database", obj(map[string]any{}))); n != 0 {
		t.Errorf("absent applied produced %d issues, want 0", n)
	}
	if n := len(detectCNPGIssues(gvr, "Database", obj(map[string]any{"applied": true}))); n != 0 {
		t.Errorf("applied=true produced %d issues, want 0", n)
	}

	// All three kinds, and the message names the one in front of you.
	for _, kind := range []string{"Database", "Publication", "Subscription"} {
		out := detectCNPGIssues(gvr, kind, obj(map[string]any{"applied": false}))
		if len(out) != 1 {
			t.Fatalf("%s produced %d issues, want 1", kind, len(out))
		}
		if !strings.Contains(out[0].Message, kind) {
			t.Errorf("%s message does not name the kind: %q", kind, out[0].Message)
		}
		if !strings.Contains(out[0].Message, "gave no reason") {
			t.Errorf("%s with no operator message should say so: %q", kind, out[0].Message)
		}
	}
}

var cnpgScheduledBackupGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "scheduledbackups"}

func cnpgScheduledBackup(suspend bool, nextScheduleTime string) *unstructured.Unstructured {
	spec := map[string]any{"schedule": "0 0 0 * * *", "suspend": suspend}
	status := map[string]any{}
	if nextScheduleTime != "" {
		status["nextScheduleTime"] = nextScheduleTime
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "ScheduledBackup",
		"metadata":   map[string]any{"name": "nightly", "namespace": "pg"},
		"spec":       spec,
		"status":     status,
	}}
}

// A schedule that stops firing is the one backup failure with nothing else to
// see: no condition goes False, no object reports an error, and the cluster
// stays green while its recovery point ages.
func TestCNPGScheduledBackupMissed(t *testing.T) {
	rfc := func(d time.Duration) string { return time.Now().Add(d).Format(time.RFC3339) }

	tests := []struct {
		name    string
		obj     *unstructured.Unstructured
		wantHit bool
	}{
		{"due 200 days ago and nothing ran", cnpgScheduledBackup(false, rfc(-200*24*time.Hour)), true},
		{"due just past the grace window", cnpgScheduledBackup(false, rfc(-cnpgScheduledBackupGrace-time.Minute)), true},
		// The operator has simply not picked it up yet. Firing here would raise an
		// issue on every healthy schedule for the seconds around each run.
		{"due moments ago, inside the grace window", cnpgScheduledBackup(false, rfc(-time.Minute)), false},
		{"not due yet", cnpgScheduledBackup(false, rfc(time.Hour)), false},
		// Suspended stops the operator maintaining the field, so the stored value
		// goes stale at once and would report every paused schedule as missed.
		{"suspended long past its last next-run time", cnpgScheduledBackup(true, rfc(-200*24*time.Hour)), false},
		{"operator has not published a next run", cnpgScheduledBackup(false, ""), false},
		{"unparseable next run", cnpgScheduledBackup(false, "not-a-timestamp"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectCNPGIssues(cnpgScheduledBackupGVR, "ScheduledBackup", tc.obj)
			if tc.wantHit {
				iss := findIssue(t, got, "CNPGScheduledBackupMissed")
				if iss.Severity != SeverityWarning {
					t.Errorf("severity = %v, want warning", iss.Severity)
				}
				due, err := time.Parse(time.RFC3339, tc.obj.Object["status"].(map[string]any)["nextScheduleTime"].(string))
				if err != nil || iss.OnsetUnknown || !iss.FirstSeen.Equal(due) {
					t.Errorf("onset = unknown:%v first:%v, want published due time %v (parse err %v)", iss.OnsetUnknown, iss.FirstSeen, due, err)
				}
				// It must file as a backup problem, or the backup filter answers a
				// different question than it claims to. Classified through the
				// production path so the test cannot drift from how rows are
				// actually labelled.
				classifyIssue(&iss)
				if iss.Category != issuesapi.CategoryBackupFailed {
					t.Errorf("category = %v, want backup_failed", iss.Category)
				}
				return
			}
			for _, i := range got {
				if i.Reason == "CNPGScheduledBackupMissed" {
					t.Fatalf("unexpected missed-backup issue: %s", i.Message)
				}
			}
		})
	}
}
