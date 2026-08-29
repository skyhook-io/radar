package issues

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CloudNativePG phase strings, copied verbatim from upstream
// api/v1/cluster_types.go and api/v1/backup_types.go (verified against CNPG
// 1.27). They are full English sentences, so they are matched on equality —
// a substring match would misfile "Upgrading Postgres major version" under
// "Upgrading cluster".
//
// This taxonomy is mirrored in the frontend at
// packages/k8s-ui/src/components/resources/resource-utils-cnpg.ts. The two must
// agree: a divergence produces a red badge with no issue, or an issue with no
// badge.
const (
	cnpgPhaseHealthy       = "Cluster in healthy state"
	cnpgPhaseFailingOver   = "Failing over"
	cnpgPhaseUnrecoverable = "Cluster is unrecoverable and needs manual intervention"
	cnpgPhaseUnknownPlugin = "Cluster cannot proceed to reconciliation due to an unknown plugin being required"
	cnpgPhaseFailurePlugin = "Cluster cannot proceed to reconciliation due to an error while interacting with plugins"
)

// cnpgTerminalPhases have stopped reconciling and do not self-heal.
var cnpgTerminalPhases = map[string]bool{
	cnpgPhaseUnrecoverable:                            true,
	cnpgPhaseUnknownPlugin:                            true,
	cnpgPhaseFailurePlugin:                            true,
	"Unable to create required cluster objects":       true,
	"Cluster has incomplete or invalid image catalog": true,
	"Cluster cannot execute instance online upgrade due to missing architecture binary": true,
	// Not in 1.27 or 1.28; added upstream after 1.28 (PhaseDefinitionInvalid).
	// Matching is by equality, so a phase the running operator never emits is
	// inert — carrying it costs nothing and keeps a future minor out of the
	// unknown bucket.
	"Invalid cluster definition": true,
}

// cnpgTransientConditionGrace bounds how long a False condition is written off
// as "mid-operation" before the generic detector is allowed to report it.
const cnpgTransientConditionGrace = 30 * time.Minute

// cnpgScheduledBackupGrace absorbs the gap between the moment a backup comes due
// and the moment the operator gets round to starting it. It is reconcile
// latency, not a backup policy: nothing here decides how often backups should
// run, only that the schedule missed a time it set for itself.
const cnpgScheduledBackupGrace = 10 * time.Minute

// cnpgAttentionPhases are stalled waiting on a human. Under
// primaryUpdateStrategy: supervised this is the documented resting state, so it
// only raises an issue under an unsupervised strategy.
var cnpgAttentionPhases = map[string]bool{
	"Waiting for user action": true,
}

// cnpgTransientPhases are mid-operation and self-resolving. Enumerated (rather
// than left to the unknown fallback) so the frontend parity test covers them
// and a genuinely new phase is still distinguishable from a known transient.
//
// "Cluster upgrade delayed" belongs here, not in attention: upstream sets it
// when an upgrade is postponed by the OPERATOR's own rollout-delay config and
// requeued — no human is being waited on, and it has nothing to do with
// primaryUpdateStrategy.
var cnpgTransientPhases = map[string]bool{
	"Setting up primary":                                       true,
	"Creating a new replica":                                   true,
	"Switchover in progress":                                   true,
	"Upgrading cluster":                                        true,
	"Upgrading Postgres major version":                         true,
	"Online upgrade in progress":                               true,
	"Applying configuration":                                   true,
	"Primary instance is being restarted in-place":             true,
	"Primary instance is being restarted without a switchover": true,
	"Promoting to primary cluster":                             true,
	"Waiting for the instances to become active":               true,
	"Cluster upgrade delayed":                                  true,
}

// cnpgSuppressesGenericConditions reports whether the generic False-condition
// walk must stay quiet for this object.
//
// The curated seam treats an empty result as "nothing to say", not "handled",
// so the generic walk still runs after this detector deliberately stays silent.
// During a switchover, replica creation or an upgrade CNPG sets Ready=False
// with reason ClusterIsNotReady — which is NOT in conditions'
// transientConditionReasons, so the noise floor doesn't catch it and a warning
// leaks for the exact windows the phase buckets exist to silence.
//
// Deliberately scoped to the transient/attention phases rather than the whole
// kind: outside those windows a False Ready is worth surfacing, and blanket
// ownership would silence reasons like DetachedVolume on a settled cluster.
func cnpgSuppressesGenericConditions(group, kind string, u *unstructured.Unstructured) bool {
	if group != "postgresql.cnpg.io" || kind != "Cluster" {
		return false
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")

	// Attention phases suppress WITHOUT an age bound. "Waiting for user action"
	// under primaryUpdateStrategy: supervised is the documented resting state and
	// legitimately persists until a human acts — bounding it would turn a
	// supported configuration into a permanently-lit issue, the alert-fatigue
	// trap this detector exists to avoid. (Under an unsupervised strategy the
	// curated detector emits its own issue, so the generic walk is already
	// skipped before this is consulted.)
	if cnpgAttentionPhases[phase] {
		return true
	}
	if !cnpgTransientPhases[phase] {
		return false
	}
	// A transient phase that never advances is not transient. CNPG clusters do
	// get stuck mid-operation (upstream #3365: "Creating a new replica" pinned at
	// 2/3 with Ready=False indefinitely), and an unbounded suppression would keep
	// that silent forever — worse than the noise it was added to remove. Bound it
	// by the condition's own age so a genuinely stuck cluster surfaces.
	if _, _, _, since, ok := conditions.FindFalseCondition(u); ok && since > cnpgTransientConditionGrace {
		return false
	}
	return true
}

func detectCNPGIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	switch kind {
	case "Cluster":
		return detectCNPGClusterIssues(gvr, kind, u)
	case "Backup":
		return detectCNPGBackupIssues(gvr, kind, u)
	case "Database", "Publication", "Subscription":
		return detectCNPGDeclarativeIssues(gvr, kind, u)
	case "ScheduledBackup":
		return detectCNPGScheduledBackupIssues(gvr, kind, u)
	}
	return nil
}

// The operator publishes when it expects the next backup to run. Once that
// moment has passed and no backup has happened, backups have stopped.
//
// This is the one backup failure with nothing else to see: every other detector
// here fires on something the operator reported as failed. A schedule that
// simply stops firing reports nothing, so the cluster stays green while its
// recovery point ages. The last-backup timestamp is the only evidence, and
// until now nothing compared it to the present.
//
// The expectation is the operator's, not ours. We never decide how often a
// backup should run — that is the cluster owner's policy. We only report that
// the schedule missed the time it set for itself.
func detectCNPGScheduledBackupIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	// Suspended is a deliberate state. The operator stops updating
	// nextScheduleTime, so the stored value goes stale immediately and reporting
	// it as missed would fire on every intentionally paused schedule.
	if suspended, found, err := unstructured.NestedBool(u.Object, "spec", "suspend"); err == nil && found && suspended {
		return nil
	}

	next, found, err := unstructured.NestedString(u.Object, "status", "nextScheduleTime")
	if err != nil || !found || next == "" {
		return nil
	}
	due, err := time.Parse(time.RFC3339, next)
	if err != nil {
		return nil
	}

	overdue := time.Since(due)
	if overdue <= cnpgScheduledBackupGrace {
		return nil
	}

	ns, name := u.GetNamespace(), u.GetName()
	return []Issue{newConditionIssue(gvr, kind, ns, name, SeverityWarning,
		"CNPGScheduledBackupMissed",
		"No backup has run since this schedule was due",
		due, true, "CNPGScheduledBackupMissed", u.GetCreationTimestamp().Time)}
}

// A declared object the operator could not apply.
//
// This is the one CNPG failure with no other signal anywhere: the CR exists,
// the cluster is healthy, every count is green, and the database simply is not
// there. The operator writes the reason into `status.message` and nothing reads
// it, so the failure is invisible unless someone opens that specific object.
//
// `applied` is deliberately three-valued and only `false` is a failure. Absent
// means the operator has not reconciled it yet — reporting that would raise an
// issue against every declarative object for the first seconds of its life.
func detectCNPGDeclarativeIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	applied, found, err := unstructured.NestedBool(u.Object, "status", "applied")
	if err != nil || !found || applied {
		return nil
	}

	ns, name := u.GetNamespace(), u.GetName()
	// The operator's own words. Without them this says something is wrong and
	// leaves the reader to go find out what, which is the state we are fixing.
	msg, _, _ := unstructured.NestedString(u.Object, "status", "message")
	base := fmt.Sprintf("%s is declared in Kubernetes but was not applied to PostgreSQL", kind)
	if msg == "" {
		base += " and the operator gave no reason"
	}

	return []Issue{newConditionIssue(gvr, kind, ns, name, SeverityWarning,
		"CNPGDeclarativeNotApplied", cnpgMessage(base, "", msg), time.Time{}, false,
		"CNPGDeclarativeNotApplied", u.GetCreationTimestamp().Time)}
}

// Every CNPG issue carries a Fingerprint because one cluster genuinely has
// several independent causes at once — WAL archiving can be failing WHILE the
// phase is unrecoverable. Without one the issue ID is category-only (see
// discriminator in identity.go) and all but one cause is dropped, which would
// silently lose the archiving signal this detector exists to surface.
//
// The phase-derived issues deliberately SHARE a fingerprint: a cluster has
// exactly one phase, so they are mutually exclusive, and a shared token keeps
// the issue identity stable as the phase moves between buckets.
func detectCNPGClusterIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	var out []Issue
	ns, name := u.GetNamespace(), u.GetName()
	created := u.GetCreationTimestamp().Time
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")

	// WAL archiving is the headline signal: the cluster keeps serving traffic
	// normally while the recovery point stops advancing, so nothing else in the
	// object looks wrong. Critical even though the cluster is "up".
	//
	// The condition proves the LAST ARCHIVE ATTEMPT failed — it is not an exact
	// RPO, and the message deliberately doesn't claim one.
	if condition, ok := conditions.FindFalseConditionWithTime(u, "ContinuousArchiving"); ok {
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityCritical,
			"CNPGWALArchivingFailing",
			cnpgMessage("The last WAL archival did not complete; recovery-point advancement is uncertain", condition.Reason, condition.Message),
			condition.LastTransitionTime, condition.HasLastTransitionTime,
			"CNPGWALArchivingFailing", created))
	}

	// CNPG also sets LastBackupSucceeded=False with reason BackupStarted while a
	// backup is merely in flight. Treating that as a failure would raise an issue
	// on every backup run — the canonical alert-fatigue trap.
	if condition, ok := conditions.FindFalseConditionWithTime(u, "LastBackupSucceeded"); ok && condition.Reason != "BackupStarted" {
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGLastBackupFailed",
			cnpgMessage("The most recent backup attempt failed", condition.Reason, condition.Message),
			condition.LastTransitionTime, condition.HasLastTransitionTime,
			"CNPGLastBackupFailed", created))
	}

	desired, okD, _ := unstructured.NestedInt64(u.Object, "spec", "instances")
	ready, okR, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances")
	// No phase excuses a database with nothing serving. "Waiting for user action"
	// under a supervised strategy legitimately explains a PARTIAL shortfall, but
	// not a cluster that is entirely down — that is an outage, not operator
	// intent, and silencing it is how a total failure ends up with no issue at
	// all. Mirrors the badge, which treats zero ready as hard-down ahead of
	// every phase branch except terminal and failing-over.
	allDown := okD && okR && desired > 0 && ready == 0

	phaseExplained := false
	switch {
	case cnpgTerminalPhases[phase]:
		phaseExplained = true
		reason := "CNPGClusterTerminal"
		switch phase {
		case cnpgPhaseUnrecoverable:
			reason = "CNPGClusterUnrecoverable"
		case cnpgPhaseUnknownPlugin, cnpgPhaseFailurePlugin:
			reason = "CNPGClusterPluginFailure"
		}
		// Phase carries no lastTransitionTime, so newConditionIssue omits
		// issue_timing rather than inventing one from now().
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityCritical,
			reason, phase, time.Time{}, false, "CNPGClusterPhase", created))

	case phase == cnpgPhaseFailingOver:
		// A failover explains a shortfall, but not a cluster with nothing
		// serving — that is a total outage, and letting the phase absorb it
		// downgraded it to this warning while the badge showed red.
		phaseExplained = !allDown
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGClusterFailingOver", phase, time.Time{}, false, "CNPGClusterPhase", created))

	case cnpgAttentionPhases[phase]:
		// The phase explains the state either way — primaryUpdateStrategy only
		// decides whether it is worth an issue. Leaving phaseExplained false
		// under `supervised` let the shortfall check emit CNPGClusterDegraded
		// while the badge showed the attention phase, and a supervised cluster
		// waiting on a human is precisely when instances are legitimately below
		// the desired count.
		phaseExplained = !allDown
		strategy, _, _ := unstructured.NestedString(u.Object, "spec", "primaryUpdateStrategy")
		if strategy != "supervised" {
			out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
				"CNPGClusterWaitingForUser", phase, time.Time{}, false, "CNPGClusterPhase", created))
		}

	case cnpgTransientPhases[phase]:
		// Mid-operation: running below the desired instance count is expected,
		// so the shortfall check below is suppressed without raising an issue —
		// unless nothing is serving at all.
		phaseExplained = !allDown

	case phase == cnpgPhaseHealthy || phase == "":
		// Nothing phase-derived to report.

	default:
		// Unrecognized phase (a newer CNPG minor). Don't invent a severity —
		// the degraded-instances check below still applies.
	}

	// Instance shortfall is reported unless the PHASE already explains it. This
	// must not key on len(out): a WAL-archiving or failed-backup issue is an
	// unrelated cause, and letting either suppress the shortfall would hide a
	// cluster with zero ready instances behind a backup warning.
	if !phaseExplained {
		if okD && okR && desired > 0 && ready < desired {
			severity := SeverityWarning
			if ready == 0 {
				severity = SeverityCritical
			}
			out = append(out, newConditionIssue(gvr, kind, ns, name, severity,
				"CNPGClusterDegraded",
				fmt.Sprintf("Only %d of %d instances are ready", ready, desired),
				time.Time{}, false, "CNPGClusterDegraded", created))
		}
	}

	return out
}

func detectCNPGBackupIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	ns, name := u.GetNamespace(), u.GetName()
	created := u.GetCreationTimestamp().Time

	switch phase {
	case "walArchivingFailing":
		// Archiving is broken upstream of this Backup, so every subsequent
		// backup and the whole recovery window are affected — not one object.
		msg := "WAL archiving is failing, so this backup and the cluster's recovery window are both affected"
		if e, _, _ := unstructured.NestedString(u.Object, "status", "error"); e != "" {
			msg += ": " + e
		}
		return []Issue{newConditionIssue(gvr, kind, ns, name, SeverityCritical,
			"CNPGWALArchivingFailing", msg, time.Time{}, false, "CNPGWALArchivingFailing", created)}
	case "failed":
		msg := "Backup failed"
		if e, _, _ := unstructured.NestedString(u.Object, "status", "error"); e != "" {
			msg += ": " + e
		}
		return []Issue{newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGBackupFailed", msg, time.Time{}, false, "CNPGBackupFailed", created)}
	}
	return nil
}

func cnpgMessage(base, reason, msg string) string {
	if msg != "" {
		return base + ": " + msg
	}
	if reason != "" {
		return base + " (" + reason + ")"
	}
	return base
}
