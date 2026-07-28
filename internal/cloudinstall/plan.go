package cloudinstall

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/helm"
)

// InstallPlanMode is the classified way a cluster can be connected to Cloud.
type InstallPlanMode string

const (
	InstallModeFresh  InstallPlanMode = "fresh"
	InstallModeAdopt  InstallPlanMode = "adopt"
	InstallModeGitOps InstallPlanMode = "gitops"
)

// InstallPlan is the outcome of discovery + classification: which mode applies,
// at which namespace/release target, and whether cluster-wide discovery was
// complete enough to trust the selection.
type InstallPlan struct {
	Mode                 InstallPlanMode
	Namespace            string
	Release              string
	Target               *RadarTarget
	ClusterWideScanError error
}

// ReleaseInspector reports the Helm-storage state of a candidate release.
type ReleaseInspector interface {
	InspectCloudRelease(namespace, name string) (helm.CloudReleaseInspection, error)
}

// MultipleTargetsError is returned when discovery finds more than one Radar
// installation and no explicit target selects between them. Presenters render
// their own resolution hint (CLI: pass --namespace/--release; UI: use the CLI).
type MultipleTargetsError struct {
	Targets []RadarTarget
}

func (e *MultipleTargetsError) Error() string {
	return fmt.Sprintf("found multiple Radar installations:\n%s", FormatTargets(e.Targets))
}

// InspectInstallPlan combines workload discovery with Helm storage. It never
// mutates Kubernetes and deliberately refuses to guess when ownership is
// ambiguous or an unmanaged workload collides with the intended target.
func InspectInstallPlan(
	ctx context.Context,
	kc kubernetes.Interface,
	dc dynamic.Interface,
	releases ReleaseInspector,
	namespace, release string,
	explicitTarget bool,
) (InstallPlan, error) {
	result, err := DiscoverRadarTargets(ctx, kc, dc, DiscoveryOptions{
		Namespace: namespace, ReleaseName: release, ClusterWide: !explicitTarget,
	})
	if err != nil {
		return InstallPlan{}, err
	}
	return ClassifyInstallPlan(result, releases, namespace, release, explicitTarget)
}

// ClassifyInstallPlan turns a discovery result + Helm storage state into an
// InstallPlan, failing closed on every ambiguous or conflicting arrangement.
func ClassifyInstallPlan(
	result DiscoveryResult,
	releases ReleaseInspector,
	namespace, release string,
	explicitTarget bool,
) (InstallPlan, error) {
	candidates := DiscoveredTargets(result, explicitTarget)
	if len(candidates) > 1 {
		return InstallPlan{}, &MultipleTargetsError{Targets: candidates}
	}

	plan := InstallPlan{Namespace: namespace, Release: release}
	if !explicitTarget {
		plan.ClusterWideScanError = result.ClusterWideError
	}
	if len(candidates) == 1 {
		target := candidates[0]
		if strings.TrimSpace(target.ReleaseName) == "" {
			return InstallPlan{}, fmt.Errorf(
				"Radar Deployment %q in namespace %q has no Helm release identity; refusing to guess how it is managed",
				target.DeploymentName, target.Namespace,
			)
		}
		plan.Target = &target
		if !explicitTarget {
			plan.Namespace = target.Namespace
			plan.Release = target.ReleaseName
		}
		if target.Runtime.AlreadyCloud {
			return InstallPlan{}, fmt.Errorf(
				"Radar Deployment %q in namespace %q already has Cloud connection settings; recover that pairing instead of creating another",
				target.DeploymentName, target.Namespace,
			)
		}
		switch target.Ownership.Classification {
		case OwnershipGitOpsVerified:
			plan.Mode = InstallModeGitOps
			return plan, nil
		case OwnershipGitOpsSuspected,
			OwnershipGitOpsUnreadable,
			OwnershipGitOpsStale,
			OwnershipAmbiguous:
			return InstallPlan{}, fmt.Errorf(
				"Radar Deployment %q in namespace %q has %s ownership evidence; refusing an imperative upgrade until its GitOps ownership is unambiguous and readable",
				target.DeploymentName, target.Namespace, target.Ownership.Classification,
			)
		}
	}

	if releases == nil {
		return InstallPlan{}, fmt.Errorf("inspect Helm release: nil release inspector")
	}
	inspection, err := releases.InspectCloudRelease(plan.Namespace, plan.Release)
	if err != nil {
		return InstallPlan{}, fmt.Errorf("inspect Helm release %q in namespace %q: %w", plan.Release, plan.Namespace, err)
	}
	switch inspection.State {
	case helm.CloudReleaseNone:
		if plan.Target != nil {
			return InstallPlan{}, fmt.Errorf(
				"Radar Deployment %q already occupies release target %q/%q but no adoptable Helm release exists; refusing to overwrite an unmanaged installation",
				plan.Target.DeploymentName, plan.Namespace, plan.Release,
			)
		}
		plan.Mode = InstallModeFresh
		return plan, nil
	case helm.CloudReleaseDeployed:
		if plan.Target != nil && (plan.Target.Ownership.Classification != OwnershipNativeHelm || !plan.Target.Ownership.NativeHelmMatchesTarget) {
			return InstallPlan{}, fmt.Errorf(
				"Helm release %q/%q is deployed, but workload ownership is %s; refusing to mutate a release with conflicting management metadata",
				plan.Namespace, plan.Release, plan.Target.Ownership.Classification,
			)
		}
		plan.Mode = InstallModeAdopt
		return plan, nil
	case helm.CloudReleasePending:
		return InstallPlan{}, fmt.Errorf(
			"Helm release %q in namespace %q is %q at revision %d; wait for or resolve that operation before connecting it to Radar",
			plan.Release, plan.Namespace, inspection.Status, inspection.Revision,
		)
	case helm.CloudReleaseHistory:
		return InstallPlan{}, fmt.Errorf(
			"Helm release %q in namespace %q has retained %q history at revision %d but is not deployed; resolve or remove that history before connecting it to Radar",
			plan.Release, plan.Namespace, inspection.Status, inspection.Revision,
		)
	default:
		return InstallPlan{}, fmt.Errorf("unrecognized Helm release state %q", inspection.State)
	}
}

// DiscoveredTargets flattens a DiscoveryResult into the candidate list the
// classifier arbitrates over.
func DiscoveredTargets(result DiscoveryResult, exactTarget bool) []RadarTarget {
	if exactTarget {
		return result.Selected
	}
	targets := append([]RadarTarget{}, result.Namespace...)
	return append(targets, result.ClusterWide...)
}

// FormatTargets renders candidate installations one per line for error and
// selection messages.
func FormatTargets(targets []RadarTarget) string {
	var out strings.Builder
	for _, target := range targets {
		fmt.Fprintf(&out, "  - namespace %q, release %q, Deployment %q, ownership %s\n",
			target.Namespace, target.ReleaseName, target.DeploymentName, target.Ownership.Classification)
	}
	return strings.TrimSuffix(out.String(), "\n")
}
