package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/skyhook-io/radar/internal/cloudinstall"
)

type cloudInstallPlanMode = cloudinstall.InstallPlanMode

const (
	cloudInstallFresh  = cloudinstall.InstallModeFresh
	cloudInstallAdopt  = cloudinstall.InstallModeAdopt
	cloudInstallGitOps = cloudinstall.InstallModeGitOps
)

type cloudInstallPlan = cloudinstall.InstallPlan

type cloudReleaseInspector = cloudinstall.ReleaseInspector

// inspectCloudInstallPlan wraps the shared classifier with the CLI's flag
// vocabulary: a multiple-installation result resolves via --namespace/--release.
func inspectCloudInstallPlan(
	ctx context.Context,
	clients localInstallClients,
	namespace, release string,
	explicitTarget bool,
) (cloudInstallPlan, error) {
	plan, err := cloudinstall.InspectInstallPlan(ctx, clients.Kubernetes, clients.Dynamic, clients.Releases, namespace, release, explicitTarget)
	var multiple *cloudinstall.MultipleTargetsError
	if errors.As(err, &multiple) {
		return cloudInstallPlan{}, renderMultipleTargetsHint(multiple)
	}
	return plan, err
}

func renderMultipleTargetsHint(err *cloudinstall.MultipleTargetsError) error {
	return fmt.Errorf(
		"found multiple Radar installations; choose one explicitly with --namespace and --release:\n%s",
		cloudinstall.FormatTargets(err.Targets),
	)
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
