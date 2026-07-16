package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
)

type cloudInstallPlanMode string

const (
	cloudInstallFresh  cloudInstallPlanMode = "fresh"
	cloudInstallAdopt  cloudInstallPlanMode = "adopt"
	cloudInstallGitOps cloudInstallPlanMode = "gitops"
)

type cloudInstallPlan struct {
	Mode                 cloudInstallPlanMode
	Namespace            string
	Release              string
	Target               *cloudinstall.RadarTarget
	ClusterWideScanError error
}

type cloudReleaseInspector interface {
	InspectCloudRelease(namespace, name string) (helm.CloudReleaseInspection, error)
}

// inspectCloudInstallPlan combines workload discovery with Helm storage. It
// never mutates Kubernetes and deliberately refuses to guess when ownership is
// ambiguous or an unmanaged workload collides with the intended target.
func inspectCloudInstallPlan(
	ctx context.Context,
	clients localInstallClients,
	namespace, release string,
	explicitTarget bool,
) (cloudInstallPlan, error) {
	result, err := cloudinstall.DiscoverRadarTargets(ctx, clients.Kubernetes, clients.Dynamic, cloudinstall.DiscoveryOptions{
		Namespace: namespace, ReleaseName: release, ClusterWide: !explicitTarget,
	})
	if err != nil {
		return cloudInstallPlan{}, err
	}
	return classifyCloudInstallPlan(result, clients.Releases, namespace, release, explicitTarget)
}

func classifyCloudInstallPlan(
	result cloudinstall.DiscoveryResult,
	releases cloudReleaseInspector,
	namespace, release string,
	explicitTarget bool,
) (cloudInstallPlan, error) {
	candidates := discoveredRadarTargets(result, explicitTarget)
	if len(candidates) > 1 {
		return cloudInstallPlan{}, fmt.Errorf(
			"found multiple Radar installations; choose one explicitly with --namespace and --release:\n%s",
			formatRadarTargets(candidates),
		)
	}

	plan := cloudInstallPlan{Namespace: namespace, Release: release}
	if !explicitTarget {
		plan.ClusterWideScanError = result.ClusterWideError
	}
	if len(candidates) == 1 {
		target := candidates[0]
		if strings.TrimSpace(target.ReleaseName) == "" {
			return cloudInstallPlan{}, fmt.Errorf(
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
			return cloudInstallPlan{}, fmt.Errorf(
				"Radar Deployment %q in namespace %q already has Cloud connection settings; recover that pairing instead of creating another",
				target.DeploymentName, target.Namespace,
			)
		}
		switch target.Ownership.Classification {
		case cloudinstall.OwnershipGitOpsVerified:
			plan.Mode = cloudInstallGitOps
			return plan, nil
		case cloudinstall.OwnershipGitOpsSuspected,
			cloudinstall.OwnershipGitOpsUnreadable,
			cloudinstall.OwnershipGitOpsStale,
			cloudinstall.OwnershipAmbiguous:
			return cloudInstallPlan{}, fmt.Errorf(
				"Radar Deployment %q in namespace %q has %s ownership evidence; refusing an imperative upgrade until its GitOps ownership is unambiguous and readable",
				target.DeploymentName, target.Namespace, target.Ownership.Classification,
			)
		}
	}

	if releases == nil {
		return cloudInstallPlan{}, fmt.Errorf("inspect Helm release: nil release inspector")
	}
	inspection, err := releases.InspectCloudRelease(plan.Namespace, plan.Release)
	if err != nil {
		return cloudInstallPlan{}, fmt.Errorf("inspect Helm release %q in namespace %q: %w", plan.Release, plan.Namespace, err)
	}
	switch inspection.State {
	case helm.CloudReleaseNone:
		if plan.Target != nil {
			return cloudInstallPlan{}, fmt.Errorf(
				"Radar Deployment %q already occupies release target %q/%q but no adoptable Helm release exists; refusing to overwrite an unmanaged installation",
				plan.Target.DeploymentName, plan.Namespace, plan.Release,
			)
		}
		plan.Mode = cloudInstallFresh
		return plan, nil
	case helm.CloudReleaseDeployed:
		if plan.Target != nil && (plan.Target.Ownership.Classification != cloudinstall.OwnershipNativeHelm || !plan.Target.Ownership.NativeHelmMatchesTarget) {
			return cloudInstallPlan{}, fmt.Errorf(
				"Helm release %q/%q is deployed, but workload ownership is %s; refusing to mutate a release with conflicting management metadata",
				plan.Namespace, plan.Release, plan.Target.Ownership.Classification,
			)
		}
		plan.Mode = cloudInstallAdopt
		return plan, nil
	case helm.CloudReleasePending:
		return cloudInstallPlan{}, fmt.Errorf(
			"Helm release %q in namespace %q is %q at revision %d; wait for or resolve that operation before connecting it to Radar",
			plan.Release, plan.Namespace, inspection.Status, inspection.Revision,
		)
	case helm.CloudReleaseHistory:
		return cloudInstallPlan{}, fmt.Errorf(
			"Helm release %q in namespace %q has retained %q history at revision %d but is not deployed; resolve or remove that history before connecting it to Radar",
			plan.Release, plan.Namespace, inspection.Status, inspection.Revision,
		)
	default:
		return cloudInstallPlan{}, fmt.Errorf("unrecognized Helm release state %q", inspection.State)
	}
}

func discoveredRadarTargets(result cloudinstall.DiscoveryResult, exactTarget bool) []cloudinstall.RadarTarget {
	if exactTarget {
		return result.Selected
	}
	targets := append([]cloudinstall.RadarTarget{}, result.Namespace...)
	return append(targets, result.ClusterWide...)
}

func formatRadarTargets(targets []cloudinstall.RadarTarget) string {
	var out strings.Builder
	for _, target := range targets {
		fmt.Fprintf(&out, "  - namespace %q, release %q, Deployment %q, ownership %s\n",
			target.Namespace, target.ReleaseName, target.DeploymentName, target.Ownership.Classification)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func confirmDiscoveryUncertainty(in io.Reader, out io.Writer, scanErr error, interactive bool, namespace, release string) bool {
	if scanErr == nil {
		return true
	}
	fmt.Fprintf(out, "Radar could not inspect all visible namespaces for an existing installation: %v\n", scanErr)
	fmt.Fprintf(out, "Selected target: namespace %q, Helm release %q. Another installation outside this target could not be ruled out.\n", namespace, release)
	if !interactive {
		fmt.Fprintln(out, "Pass --namespace and --release to make the intended target explicit; -y does not bypass this safety check.")
		return false
	}
	return confirmPrompt(in, out, "Continue with this selected target? [y/N] ")
}

func confirmExistingInstall(in io.Reader, out io.Writer, plan cloudInstallPlan, adoptExisting, interactive bool) bool {
	if adoptExisting {
		return true
	}
	if !interactive {
		fmt.Fprintln(out, "An existing Radar installation was detected. Pass --adopt-existing to approve this action in a non-interactive run; -y only confirms the kube context.")
		return false
	}
	if plan.Mode == cloudInstallGitOps {
		return confirmPrompt(in, out, "Connect this existing GitOps-managed Radar and generate source-of-truth guidance? [y/N] ")
	}
	return confirmPrompt(in, out, "Upgrade and connect this existing Helm-managed Radar installation? [y/N] ")
}

func confirmPrompt(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
