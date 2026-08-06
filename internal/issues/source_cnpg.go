package issues

import (
	"fmt"

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
}

// cnpgAttentionPhases are stalled waiting on a human. Under
// primaryUpdateStrategy: supervised this is the documented resting state, so it
// only raises an issue under an unsupervised strategy.
var cnpgAttentionPhases = map[string]bool{
	"Waiting for user action": true,
	"Cluster upgrade delayed": true,
}

func detectCNPGIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	switch kind {
	case "Cluster":
		return detectCNPGClusterIssues(gvr, kind, u)
	case "Backup":
		return detectCNPGBackupIssues(gvr, kind, u)
	}
	return nil
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
	if _, reason, msg, since, ok := conditions.FindFalseCondition(u, "ContinuousArchiving"); ok {
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityCritical,
			"CNPGWALArchivingFailing",
			cnpgMessage("WAL archiving is failing; the recovery point may not be advancing", reason, msg),
			since, "CNPGWALArchivingFailing", created))
	}

	// CNPG also sets LastBackupSucceeded=False with reason BackupStarted while a
	// backup is merely in flight. Treating that as a failure would raise an issue
	// on every backup run — the canonical alert-fatigue trap.
	if _, reason, msg, since, ok := conditions.FindFalseCondition(u, "LastBackupSucceeded"); ok && reason != "BackupStarted" {
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGLastBackupFailed",
			cnpgMessage("The most recent backup attempt failed", reason, msg),
			since, "CNPGLastBackupFailed", created))
	}

	switch {
	case cnpgTerminalPhases[phase]:
		reason := "CNPGClusterTerminal"
		switch phase {
		case cnpgPhaseUnrecoverable:
			reason = "CNPGClusterUnrecoverable"
		case cnpgPhaseUnknownPlugin, cnpgPhaseFailurePlugin:
			reason = "CNPGClusterPluginFailure"
		}
		// Phase carries no lastTransitionTime, so since=0 — newConditionIssue
		// omits issue_timing rather than inventing one from now().
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityCritical,
			reason, phase, 0, "CNPGClusterPhase", created))

	case phase == cnpgPhaseFailingOver:
		out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGClusterFailingOver", phase, 0, "CNPGClusterPhase", created))

	case cnpgAttentionPhases[phase]:
		strategy, _, _ := unstructured.NestedString(u.Object, "spec", "primaryUpdateStrategy")
		if strategy != "supervised" {
			out = append(out, newConditionIssue(gvr, kind, ns, name, SeverityWarning,
				"CNPGClusterWaitingForUser", phase, 0, "CNPGClusterPhase", created))
		}

	case phase == cnpgPhaseHealthy || phase == "":
		// Nothing phase-derived to report.

	default:
		// Unrecognized phase (a newer CNPG minor). Don't invent a severity —
		// the degraded-instances check below still applies.
	}

	// Instance shortfall is only worth reporting when the phase hasn't already
	// explained it; a transient phase like "Creating a new replica" legitimately
	// runs below the desired count.
	if len(out) == 0 && phase == cnpgPhaseHealthy {
		desired, okD, _ := unstructured.NestedInt64(u.Object, "spec", "instances")
		ready, okR, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances")
		if okD && okR && desired > 0 && ready < desired {
			severity := SeverityWarning
			if ready == 0 {
				severity = SeverityCritical
			}
			out = append(out, newConditionIssue(gvr, kind, ns, name, severity,
				"CNPGClusterDegraded",
				fmt.Sprintf("Only %d of %d instances are ready", ready, desired),
				0, "CNPGClusterDegraded", created))
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
			"CNPGWALArchivingFailing", msg, 0, "CNPGWALArchivingFailing", created)}
	case "failed":
		msg := "Backup failed"
		if e, _, _ := unstructured.NestedString(u.Object, "status", "error"); e != "" {
			msg += ": " + e
		}
		return []Issue{newConditionIssue(gvr, kind, ns, name, SeverityWarning,
			"CNPGBackupFailed", msg, 0, "CNPGBackupFailed", created)}
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
