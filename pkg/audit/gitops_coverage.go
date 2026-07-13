package audit

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/subject"
)

// GitOps coverage check IDs. Two IDs (not one) so an operator can toggle the
// stricter "nothing manages this" finding independently from the softer
// "Helm but not GitOps" one.
const (
	checkGitOpsUnmanaged = "gitops-unmanaged-workload"
	checkGitOpsHelmOnly  = "gitops-helm-only-workload"
)

// Both IDs are evaluated against the same subject, so each eligible workload is
// recorded once per ID — passed = evaluated − failed then holds for each.
var gitopsCoverageCheckIDs = []string{checkGitOpsUnmanaged, checkGitOpsHelmOnly}

// checkGitOpsCoverage flags top-level workloads that are not tracked by a GitOps
// controller — the "which of my deployments live outside Git" coverage reading.
//
// It runs only when a GitOps controller is actually installed
// (input.GitOpsToolsPresent). Without that gate, a shop that deploys with plain
// kubectl would see every workload flagged, which is noise, not a finding.
//
// Management is classified with subject.ResolveOverlay (the same tier ladder the
// topology/overlay surfaces use) rather than hand-rolled label matching:
//   - tiers 1-4 (Flux / Argo) → GitOps-managed → no finding
//   - tier 5 (native Helm, meta.helm.sh/release-name) → helm-only finding
//   - tiers 6-9 (generic app labels) or no overlay → unmanaged finding
//
// Workloads with an ownerReference are skipped: they're created by a controller
// (an operator CR, a CronJob's Jobs) whose GitOps tracking labels don't
// propagate to the children, so the coverage question belongs to the owner, not
// the child — flagging the child would be a false positive.
func checkGitOpsCoverage(tr *evalTracker, input *CheckInput) []Finding {
	if input == nil || !input.GitOpsToolsPresent {
		return nil
	}

	var findings []Finding
	emit := func(kind string, obj metav1.Object) {
		if len(obj.GetOwnerReferences()) > 0 {
			return
		}
		tr.recordAll(gitopsCoverageCheckIDs, obj.GetNamespace())

		var checkID, message string
		overlay := subject.ResolveOverlay(obj, false)
		switch {
		case overlay != nil && overlay.Winner.Tier <= subject.TierArgoInstance:
			return // GitOps-managed (Flux / Argo) — the good case, no finding.
		case overlay != nil && overlay.Winner.Tier == subject.TierInstance && argoAppTracksByLabel(input, obj):
			// Argo label-tracking mode: app.kubernetes.io/instance naming a real
			// Application IS Argo management, not a generic app label.
			return
		case overlay != nil && overlay.Winner.Tier == subject.TierHelmRelease:
			// An Argo app that deploys a Helm chart carries BOTH the Helm release
			// label and app.kubernetes.io/instance; the Helm tier wins the overlay,
			// but if the instance label names a real Application it IS GitOps-managed
			// (Argo drives the Helm release from Git), not a stray helm-CLI install.
			if argoAppTracksByLabel(input, obj) {
				return
			}
			checkID = checkGitOpsHelmOnly
			message = "Managed by Helm but not GitOps — helm-CLI upgrades bypass Git review and the declarative rollback path."
		default:
			// No overlay, or only generic app labels (tiers 6-9): nothing manages
			// this workload's desired state.
			checkID = checkGitOpsUnmanaged
			message = "Not managed by GitOps or Helm — applied imperatively, so there is no Git source of truth, drift detection, or declarative rollback."
		}

		findings = append(findings, Finding{
			Kind:      kind,
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
			CheckID:   checkID,
			Category:  CategoryReliability,
			Severity:  SeverityWarning,
			Message:   message,
		})
	}

	for _, d := range input.Deployments {
		emit("Deployment", d)
	}
	for _, s := range input.StatefulSets {
		emit("StatefulSet", s)
	}
	for _, ds := range input.DaemonSets {
		emit("DaemonSet", ds)
	}
	for _, cj := range input.CronJobs {
		emit("CronJob", cj)
	}
	return findings
}

// argoAppTracksByLabel reports whether the workload's app.kubernetes.io/instance
// value names an existing Argo CD Application — the signature of Argo's label
// tracking mode. Without the cross-reference, every label-tracked workload
// would be flagged as unmanaged; without requiring a real Application, every
// Helm/kustomize install carrying the standard instance label would silently
// pass as GitOps-managed.
func argoAppTracksByLabel(input *CheckInput, obj metav1.Object) bool {
	if len(input.ArgoAppNames) == 0 {
		return false
	}
	instance := obj.GetLabels()["app.kubernetes.io/instance"]
	if instance == "" {
		return false
	}
	_, ok := input.ArgoAppNames[instance]
	return ok
}
