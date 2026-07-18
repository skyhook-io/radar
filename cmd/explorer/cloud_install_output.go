package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/skyhook-io/radar/internal/cliui"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
)

// cloudCommandTarget keeps copy-paste follow-up commands on the same cluster
// the installer inspected, even when --context or config.json's kubeconfig
// override differs from the user's later current context.
type cloudCommandTarget struct {
	Context    string
	Kubeconfig string
}

func (t cloudCommandTarget) kubectl() string {
	command := "kubectl"
	if t.Kubeconfig != "" {
		command += " --kubeconfig " + shellArgument(t.Kubeconfig)
	}
	if t.Context != "" {
		command += " --context " + shellArgument(t.Context)
	}
	return command
}

func (t cloudCommandTarget) helm() string {
	command := "helm"
	if t.Kubeconfig != "" {
		command += " --kubeconfig " + shellArgument(t.Kubeconfig)
	}
	if t.Context != "" {
		command += " --kube-context " + shellArgument(t.Context)
	}
	return command
}

func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func printPreparedInstallPlan(w io.Writer, prepared *cloudinstall.PreparedProvision, enableCloudFeatures, noSelfUpgrade bool) {
	fmt.Fprintln(w, cliui.New(w).Bold("Plan:"))
	fmt.Fprintf(w, "  Kubernetes target: namespace %q, Helm release %q\n", prepared.Namespace(), prepared.ReleaseName())
	if prepared.Mode() == cloudinstall.ProvisionFresh {
		fmt.Fprintln(w, "  Action: install a new connected Radar release")
		fmt.Fprintf(w, "  Stable target: chart %s, Radar %s\n", prepared.ChartVersion(), prepared.AppVersion())
		fmt.Fprintln(w, "  Cloud feature RBAC: enabled for Helm, Secrets, terminal, port-forward, and metrics")
	} else {
		current := prepared.CurrentValues()
		fmt.Fprintln(w, "  Action: atomically upgrade and connect the existing native Helm release")
		fmt.Fprintf(w, "  Chart: %s -> %s\n", prepared.CurrentChartVersion(), prepared.ChartVersion())
		fmt.Fprintf(w, "  Radar image: %s -> %s\n", current.EffectiveImageTag, prepared.AppVersion())
		if current.ImageTag != "" {
			fmt.Fprintf(w, "  Pinned image.tag %q will be cleared so the selected chart's stable Radar %s runs. Use --chart-version to choose a different stable target.\n",
				current.ImageTag, prepared.AppVersion())
		}
		if current.ImageRepository != "" {
			fmt.Fprintf(w, "  Image repository %q is preserved.\n", current.ImageRepository)
		}
		if enableCloudFeatures {
			fmt.Fprintln(w, "  Cloud feature RBAC: explicitly enable Helm, Secrets, terminal, port-forward, and metrics")
		} else {
			fmt.Fprintln(w, "  Cloud feature RBAC: preserve the existing release's Helm/Secrets/terminal/port-forward/metrics settings")
		}
		fmt.Fprintf(w, "  Failure rollback: Helm atomic rollback to the pre-adoption release (currently revision %d)\n", prepared.CurrentRevision())
	}
	if noSelfUpgrade {
		fmt.Fprintln(w, "  Future one-click agent upgrades: disabled by --no-self-upgrade")
	} else {
		fmt.Fprintln(w, "  Future one-click agent upgrades: enabled for organization owners; no upgrade runs automatically (opt out with --no-self-upgrade)")
	}
	fmt.Fprintln(w)
}

func printGitOpsInstallPlan(w io.Writer, plan cloudInstallPlan, target helm.PreparedChartSummary, enableCloudFeatures bool) {
	fmt.Fprintln(w, cliui.New(w).Bold("Plan:"))
	fmt.Fprintf(w, "  Kubernetes target: namespace %q, Helm release %q, Deployment %q\n",
		plan.Namespace, plan.Release, plan.Target.DeploymentName)
	fmt.Fprintln(w, "  Action: generate a source-of-truth handoff; do not mutate the live controller or workload")
	fmt.Fprintf(w, "  Current chart signal: %s\n", unknownCloudValue(plan.Target.Chart))
	fmt.Fprintf(w, "  Current image: %s\n", unknownCloudValue(plan.Target.Runtime.Image))
	fmt.Fprintf(w, "  Stable target: chart %s, Radar %s\n", target.ChartVersion, target.AppVersion)
	if enableCloudFeatures {
		fmt.Fprintln(w, "  Cloud feature RBAC: the merge fragment will explicitly enable Helm, Secrets, terminal, port-forward, and metrics")
	} else {
		fmt.Fprintln(w, "  Cloud feature RBAC: omitted from the merge fragment so existing settings stay unchanged")
	}
	fmt.Fprintln(w, "  Future one-click agent upgrades: disabled because live image patches would drift from Git")
	fmt.Fprintln(w)
}

func unknownCloudValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func printCloudPermissionFailure(
	w io.Writer,
	pf cloudinstall.PreflightResult,
	contextName, hubURL string,
	prepared *cloudinstall.PreparedProvision,
	clusterName string,
) {
	fmt.Fprintf(w, "%s Your current Kubernetes identity cannot perform the exact planned Radar operation.\n", cliui.New(w).Marker(cliui.Failure))
	fmt.Fprintln(w, "Blocked while trying to:")
	for _, detail := range pf.Blocking {
		fmt.Fprintf(w, "  • %s\n", detail)
	}
	fmt.Fprintln(w, "\nRadar's connected mode provisions Kubernetes impersonation RBAC, so a sufficiently privileged platform operator must run this step.")
	fmt.Fprintf(w, "Ask them to run `radar cloud install` against this Kubernetes cluster (your context %q; theirs may be named differently).\n", contextName)
	fmt.Fprintf(w, "Preserve Hub %q, namespace %q, Helm release %q, Radar cluster name %q, and chart target %q.\n",
		hubURL, prepared.Namespace(), prepared.ReleaseName(), clusterName, prepared.ChartVersion())
}

func printCloudPermissionAdvisories(w io.Writer, pf cloudinstall.PreflightResult) {
	if len(pf.Advisory) == 0 {
		return
	}
	style := cliui.New(w)
	fmt.Fprintf(w, "%s %s\n", style.Marker(cliui.Attention), style.Tone(cliui.Attention, "Preflight notes:"))
	for _, detail := range pf.Advisory {
		fmt.Fprintf(w, "  • %s\n", detail)
	}
	fmt.Fprintln(w)
}

func printApprovedGitOpsHandoff(
	w io.Writer,
	plan cloudInstallPlan,
	target helm.PreparedChartSummary,
	cloudURL, clusterID, token string,
	enableCloudFeatures bool,
) error {
	handoff, err := buildGitOpsHandoff(plan, target, cloudURL, clusterID, enableCloudFeatures)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n  %s Approved. No live Kubernetes resource was changed.\n", cliui.New(w).Marker(cliui.Success))
	fmt.Fprintln(w)
	fmt.Fprintln(w, handoff.Guidance)
	fmt.Fprintln(w, "\nOne-time connection token (store it through your existing secret-management workflow; never place it in Helm values or Git):")
	fmt.Fprintf(w, "  %s\n", token)
	fmt.Fprintln(w, "Set RADAR_CLOUD_TOKEN in your shell without putting it in shell history, then run the manifest-generation command shown above.")
	return nil
}

func buildGitOpsHandoff(
	plan cloudInstallPlan,
	target helm.PreparedChartSummary,
	cloudURL, clusterID string,
	enableCloudFeatures bool,
) (cloudinstall.GitOpsHandoff, error) {
	if plan.Target == nil {
		return cloudinstall.GitOpsHandoff{}, errors.New("verified GitOps target is missing")
	}
	handoff, err := cloudinstall.BuildGitOpsHandoff(cloudinstall.GitOpsHandoffConfig{
		Target:    *plan.Target,
		CloudURL:  cloudURL,
		ClusterID: clusterID,
		Current: cloudinstall.GitOpsVersionSummary{
			Chart: plan.Target.Chart,
			App:   plan.Target.Runtime.Image,
		},
		TargetVersion: cloudinstall.GitOpsVersionSummary{
			Chart: target.ChartVersion,
			App:   target.AppVersion,
		},
		EnableCloudFeatures: enableCloudFeatures,
	})
	if err != nil {
		return cloudinstall.GitOpsHandoff{}, err
	}
	return handoff, nil
}

func printGitOpsHandoffFailure(w io.Writer, err error, clusterID, clusterURL string) {
	fmt.Fprintf(w, "\n%s Could not generate the source-of-truth handoff for Hub cluster %q: %v.\n", cliui.New(w).Marker(cliui.Failure), clusterID, err)
	fmt.Fprintln(w, "The Hub approval already created this cluster, but no live Kubernetes resource was changed and no GitOps instructions or token Secret were generated.")
	fmt.Fprintln(w, "Do not rerun `radar cloud install`, because that would create another pending cluster.")
	fmt.Fprintln(w, "The credentials from this attempt were not handed off and cannot be recovered after this command exits.")
	fmt.Fprintln(w, "An organization owner can open this cluster and choose Resume install to rotate credentials and generate a fresh command, or delete it before deliberately starting over:")
	fmt.Fprintf(w, "  %s\n", clusterURL)
}

func printGitOpsPendingHandoff(w io.Writer, err error, clusterID, clusterURL string) {
	reason := err.Error()
	switch {
	case errors.Is(err, cloud.ErrConnectConsumptionTimeout):
		reason = "the five-minute convenience wait elapsed"
	case errors.Is(err, cloud.ErrConnectPickupExpired):
		reason = "the approval pickup window ended before the reconciled agent connected"
	case errors.Is(err, context.Canceled):
		reason = "the local wait was canceled"
	}
	fmt.Fprintf(w, "\n%s GitOps handoff generated for Hub cluster %q, but its in-cluster connection was not confirmed: %s.\n", cliui.New(w).Marker(cliui.Attention), clusterID, reason)
	fmt.Fprintln(w, "The configuration handoff is ready. Commit the generated configuration and token Secret through the source of truth; the existing Hub cluster remains the one to connect.")
	fmt.Fprintln(w, "Do not rerun `radar cloud install`, because that would create another pending cluster.")
	fmt.Fprintf(w, "Open or recover this cluster in Radar: %s\n", clusterURL)
}

func printAdoptionRollbackGuidance(w io.Writer, recovery cloudProvisionRecovery, clusterURL string, target cloudCommandTarget) {
	fmt.Fprintln(w, "  To deliberately undo this adoption later:")
	fmt.Fprintf(w, "    1. %s rollback %s %d -n %s\n", target.helm(), recovery.ReleaseName, recovery.CurrentRevision, recovery.Namespace)
	fmt.Fprintf(w, "    2. %s -n %s delete secret/%s\n", target.kubectl(), recovery.Namespace, cloudinstall.CloudTokenSecretName)
	fmt.Fprintf(w, "    3. Have an organization owner delete the connected cluster at %s\n", clusterURL)
	fmt.Fprintln(w, "  The Helm rollback restores the exact pre-adoption chart, image pin, values, and RBAC. Delete the Secret and connected cluster only after that rollback succeeds; otherwise the fleet row would remain disconnected.")
}
