package issues

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
)

const NativeHelmGroup = "helm.sh"

func NativeHelmReleaseIssues(releases []helm.HelmRelease, now time.Time) []Issue {
	out := make([]Issue, 0, len(releases))
	for _, rel := range releases {
		if rel.ManagedByFluxHelmRelease != "" || rel.LastOperation == nil {
			continue
		}
		op := rel.LastOperation
		severity, reason, ok := nativeHelmIssueReason(*op)
		if !ok {
			continue
		}
		firstSeen := op.Updated
		if firstSeen.IsZero() {
			firstSeen = rel.Updated
		}
		if firstSeen.IsZero() {
			firstSeen = now
		}
		storageNamespace := rel.StorageNamespace
		if storageNamespace == "" {
			storageNamespace = rel.Namespace
		}
		iss := Issue{
			Severity:  severity,
			Source:    SourceProblem,
			Kind:      "HelmRelease",
			Group:     NativeHelmGroup,
			Namespace: storageNamespace,
			Name:      rel.Name,
			Reason:    reason,
			Message:   nativeHelmIssueMessage(rel, *op),
			Cause:     nativeHelmIssueCause(*op),
			Action:    nativeHelmIssueAction(*op),
			Stuck:     true,
			FirstSeen: firstSeen,
			LastSeen:  now,
		}
		classifyIssue(&iss)
		enrichIdentity(&iss)
		out = append(out, iss)
	}
	return out
}

func nativeHelmIssueReason(op helm.HelmOperation) (Severity, string, bool) {
	switch op.Kind {
	case helmhistory.KindReleaseFailed:
		return SeverityCritical, "HelmReleaseFailed", true
	case helmhistory.KindUpgradeFailed:
		return SeverityCritical, "HelmUpgradeFailed", true
	case helmhistory.KindPending:
		if op.Status == helmhistory.StatusStuck {
			return SeverityWarning, "HelmReleasePending", true
		}
	}
	return "", "", false
}

func nativeHelmIssueMessage(rel helm.HelmRelease, op helm.HelmOperation) string {
	if op.Message != "" {
		return op.Message
	}
	switch op.Kind {
	case helmhistory.KindPending:
		return fmt.Sprintf("Helm release %q is stuck in %s at rev %d.", rel.Name, op.PendingStatus, op.Revision)
	default:
		return fmt.Sprintf("Helm release %q failed at rev %d.", rel.Name, op.Revision)
	}
}

func nativeHelmIssueCause(op helm.HelmOperation) string {
	if op.Kind == helmhistory.KindPending {
		return "A Helm install, upgrade, or rollback has remained pending past the stuck-operation threshold."
	}
	return "The latest native Helm release revision is failed."
}

func nativeHelmIssueAction(op helm.HelmOperation) string {
	if op.Kind == helmhistory.KindPending {
		return "Open the Helm release details, check whether the operation is still running, then inspect hooks, Jobs, Pods, events, logs, and owned resources."
	}
	return "Open the Helm release history and hook diagnostics, then inspect the failed revision, values, hooks, and owned resources."
}
