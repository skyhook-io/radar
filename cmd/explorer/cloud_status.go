package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/cliui"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type hubTunnelVerdict uint8

const (
	hubTunnelUnavailable hubTunnelVerdict = iota
	hubTunnelHealthy
	hubTunnelUnhealthy

	hubTunnelNeverConnectedSummary = "never connected"
)

type hubTunnelResult struct {
	verdict         hubTunnelVerdict
	summary         string
	hubClusterID    string
	lastConnectedAt *time.Time
	detail          string
	next            string
}

type agentStatusGetter interface {
	Get(context.Context, string) (*cloud.AgentStatusResponse, error)
}

func cloudStatus(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("cloud status", flag.ContinueOnError)
	var flagOutput bytes.Buffer
	fs.SetOutput(&flagOutput)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: radar cloud status [--context NAME] [--namespace NS --release NAME]")
		fmt.Fprintln(fs.Output(), "Without both --namespace and --release, Radar discovers installations across visible namespaces.")
		fs.PrintDefaults()
	}
	namespace := fs.String("namespace", cloudinstall.DefaultInstallNamespace, "Exact namespace (requires --release)")
	release := fs.String("release", cloudinstall.DefaultReleaseName, "Exact Helm release (requires --namespace)")
	contextName := fs.String("context", "", "Kubernetes context to inspect (default: current context)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(out, &flagOutput)
			return 0
		}
		_, _ = io.Copy(errOut, &flagOutput)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(errOut, "cloud status: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	explicitNamespace, explicitRelease := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "namespace":
			explicitNamespace = true
		case "release":
			explicitRelease = true
		}
	})
	if explicitNamespace != explicitRelease {
		fmt.Fprintln(errOut, "cloud status: --namespace and --release must be passed together to select one installation")
		return 2
	}
	normalizedNamespace, normalizedRelease, err := normalizeCloudInstallNames(*namespace, *release)
	if err != nil {
		fmt.Fprintf(errOut, "cloud status: %v\n", err)
		return 2
	}

	fileCfg := config.Load()
	if len(fileCfg.KubeconfigDirs) > 0 {
		fmt.Fprintln(errOut, "`radar cloud status` cannot choose one cluster while config.json's `kubeconfigDirs` setting is enabled.")
		fmt.Fprintln(errOut, "Clear `kubeconfigDirs` in Radar Settings (or ~/.radar/config.json), then select one current context with KUBECONFIG or config.json's `kubeconfig`.")
		return 1
	}
	ctxName, err := resolveCloudInstallContext(fileCfg.Kubeconfig, strings.TrimSpace(*contextName))
	if err != nil {
		fmt.Fprintf(errOut, "cloud status: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Kubernetes context: %q\n", ctxName)
	clients, err := buildLocalKubernetesClients(fileCfg.Kubeconfig, ctxName)
	if err != nil {
		fmt.Fprintf(errOut, "cloud status: %v\n", err)
		return 1
	}

	ctx, cancel := signalContext()
	defer cancel()
	exactTarget := cloudInstallUsesExactTarget(explicitNamespace, explicitRelease)
	result, err := cloudinstall.DiscoverRadarTargets(ctx, clients.Kubernetes, clients.Dynamic, cloudinstall.DiscoveryOptions{
		Namespace: normalizedNamespace, ReleaseName: normalizedRelease, ClusterWide: !exactTarget,
	})
	if err != nil {
		fmt.Fprintln(errOut, "cloud status: could not inspect Radar in this Kubernetes context")
		fmt.Fprintf(errOut, "Details: %v\n", err)
		if apierrors.IsForbidden(err) {
			if exactTarget {
				fmt.Fprintf(errOut, "Next: confirm this account can list Deployments in namespace %q.\n", normalizedNamespace)
			} else {
				fmt.Fprintln(errOut, "Next: pass both --namespace NS and --release NAME to inspect an installation this account can read.")
			}
		}
		return 1
	}

	targets := discoveredRadarTargets(result, exactTarget)
	var tunnel hubTunnelResult
	if len(targets) == 1 {
		tunnel = checkHubTunnelStatus(ctx, clients.Kubernetes, targets[0])
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "cloud status: interrupted")
			return 1
		}
	}
	fmt.Fprintf(out, "\n%s\n\n", formatCloudStatusHeadline(cliui.New(out), targets, result.ClusterWideError, tunnel))
	code := printCloudDiscoveryStatus(out, errOut, targets, result.ClusterWideError, exactTarget, normalizedNamespace, normalizedRelease)
	if len(targets) == 1 {
		if targets[0].Runtime.AlreadyCloud {
			printHubTunnelStatus(out, targets[0], tunnel)
		} else if tunnel.next != "" {
			fmt.Fprintf(out, "\n%s %s\n", cliui.New(out).Bold("Next:"), tunnel.next)
		}
		if tunnel.verdict == hubTunnelUnhealthy {
			return 1
		}
	}
	return code
}

func printCloudDiscoveryStatus(out, errOut io.Writer, targets []cloudinstall.RadarTarget, clusterWideErr error, exactTarget bool, namespace, release string) int {
	if clusterWideErr != nil {
		style := cliui.New(errOut)
		fmt.Fprintf(errOut, "%s Radar could not inspect all visible namespaces: %v\n", style.Tone(cliui.Attention, "Warning:"), clusterWideErr)
	}
	switch len(targets) {
	case 0:
		if exactTarget {
			fmt.Fprintln(out, "Radar installation: not found")
			fmt.Fprintf(out, "Checked for a Radar Deployment with release label %q in namespace %q.\n", release, namespace)
		} else if clusterWideErr != nil {
			fmt.Fprintln(out, "Radar installation: none found in namespaces this account can inspect")
			fmt.Fprintln(out, "Other installations could not be ruled out. Pass --namespace and --release to inspect one directly.")
		} else {
			fmt.Fprintln(out, "Radar installation: not found")
			fmt.Fprintln(out, "Run `radar cloud install` to connect this cluster.")
		}
		return 1
	case 1:
		healthy := printCloudStatus(out, targets[0])
		if clusterWideErr != nil {
			style := cliui.New(out)
			fmt.Fprintf(out, "Discovery: %s incomplete; other Radar installations could not be ruled out\n", style.Marker(cliui.Attention))
			fmt.Fprintln(out, "Pass --namespace and --release to make the target explicit.")
			return 1
		}
		if healthy {
			return 0
		}
		return 1
	default:
		fmt.Fprintf(errOut, "cloud status: found multiple Radar installations; choose one explicitly with --namespace and --release:\n%s\n", formatRadarTargets(targets))
		return 1
	}
}

func printCloudStatus(out io.Writer, target cloudinstall.RadarTarget) bool {
	return printCloudStatusWithStyle(out, target, cliui.New(out))
}

func printCloudStatusWithStyle(out io.Writer, target cloudinstall.RadarTarget, style cliui.Styler) bool {
	runtime := target.Runtime
	fmt.Fprintf(out, "Radar installation: %s/%s\n", target.Namespace, target.DeploymentName)
	fmt.Fprintf(out, "Managed by: %s\n", cloudOwnershipLabel(target))
	if target.Chart != "" {
		fmt.Fprintf(out, "Chart: %s\n", target.Chart)
	}
	if runtime.Image != "" {
		fmt.Fprintf(out, "Image: %s\n", runtime.Image)
	}

	ready := cloudAgentHealthy(runtime)
	agentTone := cloudAgentTone(runtime)
	if runtime.DesiredReplicas == 0 {
		fmt.Fprintf(out, "Agent: %s scaled to zero\n", style.Marker(agentTone))
	} else if !runtime.StatusObserved {
		state := fmt.Sprintf("rollout status pending (%d/%d replicas ready)", runtime.ReadyReplicas, runtime.DesiredReplicas)
		fmt.Fprintf(out, "Agent: %s %s\n", style.Marker(agentTone), state)
	} else if !ready {
		oldReplicas := runtime.TotalReplicas - runtime.UpdatedReplicas
		if oldReplicas > 0 {
			noun := "replica"
			if oldReplicas != 1 {
				noun = "replicas"
			}
			state := fmt.Sprintf(
				"rollout incomplete (%d/%d updated, %d/%d ready, %d/%d available; %d old %s still running; readiness includes old replicas)",
				runtime.UpdatedReplicas,
				runtime.DesiredReplicas,
				runtime.ReadyReplicas,
				runtime.DesiredReplicas,
				runtime.AvailableReplicas,
				runtime.DesiredReplicas,
				oldReplicas,
				noun,
			)
			fmt.Fprintf(out, "Agent: %s %s\n", style.Marker(agentTone), state)
		} else {
			state := fmt.Sprintf(
				"rollout incomplete (%d/%d updated, %d/%d ready, %d/%d available)",
				runtime.UpdatedReplicas,
				runtime.DesiredReplicas,
				runtime.ReadyReplicas,
				runtime.DesiredReplicas,
				runtime.AvailableReplicas,
				runtime.DesiredReplicas,
			)
			fmt.Fprintf(out, "Agent: %s %s\n", style.Marker(agentTone), state)
		}
	} else {
		state := fmt.Sprintf("%d/%d replicas ready", runtime.ReadyReplicas, runtime.DesiredReplicas)
		fmt.Fprintf(out, "Agent: %s %s\n", style.Marker(agentTone), state)
	}

	if !runtime.AlreadyCloud {
		fmt.Fprintf(out, "Cloud configuration: %s not configured\n", style.Marker(cliui.Attention))
		return false
	}

	missing := missingCloudConfiguration(runtime)
	if len(missing) > 0 {
		state := fmt.Sprintf("incomplete (missing %s)", strings.Join(missing, ", "))
		fmt.Fprintf(out, "Cloud configuration: %s %s\n", style.Marker(cliui.Attention), state)
	} else {
		fmt.Fprintf(out, "Cloud configuration: %s present\n", style.Marker(cliui.Success))
	}
	if runtime.CloudModeUnresolved {
		fmt.Fprintln(out, "Cloud mode: configured from a referenced value (value not inspected)")
	}
	if runtime.CloudURL != "" {
		fmt.Fprintf(out, "Hub: %s\n", runtime.CloudURL)
	} else if runtime.CloudURLUnresolved {
		fmt.Fprintln(out, "Hub: configured from a referenced value (value not inspected)")
	}
	if runtime.ClusterName != "" {
		fmt.Fprintf(out, "Configured cluster reference: %s\n", runtime.ClusterName)
	} else if runtime.ClusterNameUnresolved {
		fmt.Fprintln(out, "Configured cluster reference: sourced from a referenced value (value not inspected)")
	}
	return ready && len(missing) == 0
}

func cloudAgentTone(runtime cloudinstall.DeploymentRuntime) cliui.Tone {
	if runtime.DesiredReplicas == 0 {
		return cliui.Failure
	}
	if cloudAgentHealthy(runtime) {
		return cliui.Success
	}
	return cliui.Attention
}

func cloudAgentHealthy(runtime cloudinstall.DeploymentRuntime) bool {
	return runtime.StatusObserved &&
		runtime.DesiredReplicas > 0 &&
		runtime.TotalReplicas == runtime.DesiredReplicas &&
		runtime.UpdatedReplicas == runtime.DesiredReplicas &&
		runtime.ReadyReplicas >= runtime.DesiredReplicas &&
		runtime.AvailableReplicas >= runtime.DesiredReplicas
}

func cloudStatusHeadline(targets []cloudinstall.RadarTarget, clusterWideErr error, tunnel hubTunnelResult) string {
	switch len(targets) {
	case 0:
		if clusterWideErr != nil {
			return "Radar installation status is inconclusive; some namespaces could not be inspected."
		}
		return "Radar is not installed in this cluster."
	case 1:
		headline := cloudTargetStatusHeadline(targets[0], tunnel)
		if clusterWideErr != nil {
			return headline + " Discovery was incomplete, so other installations may exist."
		}
		return headline
	default:
		return "Multiple Radar installations were found; choose one explicitly."
	}
}

func formatCloudStatusHeadline(style cliui.Styler, targets []cloudinstall.RadarTarget, clusterWideErr error, tunnel hubTunnelResult) string {
	return fmt.Sprintf("%s %s %s", style.Bold("Status:"), style.Marker(cloudStatusTone(targets, clusterWideErr, tunnel)), cloudStatusHeadline(targets, clusterWideErr, tunnel))
}

func cloudStatusTone(targets []cloudinstall.RadarTarget, clusterWideErr error, tunnel hubTunnelResult) cliui.Tone {
	if len(targets) != 1 {
		return cliui.Attention
	}
	tone := cloudTargetStatusTone(targets[0], tunnel)
	if clusterWideErr != nil && tone == cliui.Success {
		return cliui.Attention
	}
	return tone
}

func cloudTargetStatusTone(target cloudinstall.RadarTarget, tunnel hubTunnelResult) cliui.Tone {
	agentTone := cloudAgentTone(target.Runtime)
	if !target.Runtime.AlreadyCloud || len(missingCloudConfiguration(target.Runtime)) > 0 {
		if agentTone == cliui.Failure {
			return cliui.Failure
		}
		return cliui.Attention
	}
	if tunnel.verdict == hubTunnelHealthy {
		return agentTone
	}
	if tunnel.verdict == hubTunnelUnhealthy && tunnel.summary != hubTunnelNeverConnectedSummary {
		return cliui.Failure
	}
	if agentTone == cliui.Failure {
		return cliui.Failure
	}
	return cliui.Attention
}

func cloudTargetStatusHeadline(target cloudinstall.RadarTarget, tunnel hubTunnelResult) string {
	runtime := target.Runtime
	agentHealthy := cloudAgentHealthy(runtime)
	if !runtime.AlreadyCloud {
		if agentHealthy {
			return "Radar is healthy, but not configured for Cloud."
		}
		return "Radar needs attention: its agent is not healthy, and Cloud is not configured."
	}
	if len(missingCloudConfiguration(runtime)) > 0 {
		if agentHealthy {
			return "Radar is running, but its Cloud configuration is incomplete."
		}
		return "Radar needs attention: its agent is not healthy, and its Cloud configuration is incomplete."
	}

	switch tunnel.verdict {
	case hubTunnelHealthy:
		if agentHealthy {
			return "Radar is healthy and connected to the Hub."
		}
		return "Radar is connected to the Hub, but its agent is not healthy."
	case hubTunnelUnhealthy:
		connection := "its Cloud connection failed"
		switch tunnel.summary {
		case "disconnected":
			connection = "it is disconnected from the Hub"
		case hubTunnelNeverConnectedSummary:
			connection = "it has never connected to the Hub"
		case "failed — the Hub rejected the connection token":
			connection = "the Hub rejected its connection token"
		case "failed — the connection Secret was not found":
			connection = "its connection Secret is missing"
		case "failed — the connection Secret has no usable token":
			connection = "its connection Secret has no usable token"
		case "failed — the configured Hub URL is invalid":
			connection = "its configured Hub URL is invalid"
		}
		if agentHealthy {
			return "Radar is installed, but " + connection + "."
		}
		return "Radar needs attention: its agent is not healthy, and " + connection + "."
	default:
		if agentHealthy {
			return "Radar is healthy; live Cloud connection status could not be verified."
		}
		return "Radar needs attention: its agent is not healthy, and live Cloud connection status could not be verified."
	}
}

func printHubTunnelStatus(out io.Writer, target cloudinstall.RadarTarget, result hubTunnelResult) {
	printHubTunnelStatusWithStyle(out, target, result, cliui.New(out))
}

func printHubTunnelStatusWithStyle(out io.Writer, target cloudinstall.RadarTarget, result hubTunnelResult, style cliui.Styler) {
	tone := hubTunnelTone(result)
	fmt.Fprintf(out, "\nCloud connection: %s %s\n", style.Marker(tone), result.summary)
	if result.hubClusterID != "" && result.hubClusterID != target.Runtime.ClusterName {
		fmt.Fprintf(out, "Hub cluster ID: %s\n", result.hubClusterID)
	}
	if result.lastConnectedAt != nil {
		label := "Previous connection started"
		if result.verdict == hubTunnelHealthy {
			label = "Connected since"
		}
		fmt.Fprintf(out, "%s: %s\n", label, result.lastConnectedAt.UTC().Format(time.RFC3339))
	}
	if result.detail != "" {
		fmt.Fprintf(out, "%s %s\n", style.Dim("Details:"), result.detail)
	}
	if result.next != "" {
		fmt.Fprintf(out, "%s %s\n", style.Bold("Next:"), result.next)
	}
}

func hubTunnelTone(result hubTunnelResult) cliui.Tone {
	switch result.verdict {
	case hubTunnelHealthy:
		return cliui.Success
	case hubTunnelUnhealthy:
		if result.summary == hubTunnelNeverConnectedSummary {
			return cliui.Attention
		}
		return cliui.Failure
	default:
		return cliui.Attention
	}
}

func checkHubTunnelStatus(ctx context.Context, kc kubernetes.Interface, target cloudinstall.RadarTarget) hubTunnelResult {
	runtime := target.Runtime
	if !runtime.AlreadyCloud {
		return hubTunnelResult{
			summary: "not configured",
			next:    "run `radar cloud install` to connect this installation.",
		}
	}
	if missing := missingCloudConfiguration(runtime); len(missing) > 0 {
		return hubTunnelResult{
			summary: "not checked — the Cloud configuration is incomplete",
			detail:  "Missing " + strings.Join(missing, ", "),
			next:    "complete the Cloud settings above, then run this command again.",
		}
	}
	if runtime.CloudURL == "" {
		return hubTunnelResult{
			summary: "not checked — the Hub URL cannot be read from the Deployment",
			next:    "make the Hub URL directly inspectable, then run this command again.",
		}
	}
	client, err := cloud.NewAgentStatusClient(runtime.CloudURL)
	if err != nil {
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy,
			summary: "failed — the configured Hub URL is invalid",
			detail:  err.Error(),
			next:    "correct the Hub URL, then run this command again.",
		}
	}
	return checkHubTunnelStatusWithClient(ctx, kc, target, client)
}

func checkHubTunnelStatusWithClient(ctx context.Context, kc kubernetes.Interface, target cloudinstall.RadarTarget, client agentStatusGetter) hubTunnelResult {
	runtime := target.Runtime
	if runtime.CloudTokenSecretName == "" || runtime.CloudTokenSecretKey == "" {
		return hubTunnelResult{
			summary: "not checked — the connection token is not sourced from a Kubernetes Secret",
			next:    "migrate the token to cloud.existingSecret to enable this check.",
		}
	}
	secretRef := target.Namespace + "/" + runtime.CloudTokenSecretName
	secret, err := kc.CoreV1().Secrets(target.Namespace).Get(ctx, runtime.CloudTokenSecretName, metav1.GetOptions{})
	if apierrors.IsForbidden(err) {
		return hubTunnelResult{
			summary: "not checked — Kubernetes denied access to the connection Secret",
			detail:  fmt.Sprintf("Secret %q", secretRef),
			next:    "run this command with an account that can read the Secret, or verify the connection in the Hub.",
		}
	}
	if apierrors.IsNotFound(err) {
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy,
			summary: "failed — the connection Secret was not found",
			detail:  fmt.Sprintf("Secret %q", secretRef),
			next:    "restore the Secret or correct cloud.existingSecret, then run this command again.",
		}
	}
	if err != nil {
		return hubTunnelResult{
			summary: "not checked — the connection Secret could not be read",
			detail:  fmt.Sprintf("Secret %q: %v", secretRef, err),
			next:    "resolve the Kubernetes error, then run this command again.",
		}
	}
	token := string(secret.Data[runtime.CloudTokenSecretKey])
	if token == "" {
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy,
			summary: "failed — the connection Secret has no usable token",
			detail:  fmt.Sprintf("Secret %q, key %q", secretRef, runtime.CloudTokenSecretKey),
			next:    "restore the token key, then restart the Radar agent.",
		}
	}
	status, err := client.Get(ctx, token)
	result := evaluateHubTunnelStatus(status, err)
	if errors.Is(err, cloud.ErrAgentStatusUnauthorized) {
		result.detail = fmt.Sprintf("Kubernetes Secret %q, key %q", secretRef, runtime.CloudTokenSecretKey)
		result.next = "generate a new cluster token in the Hub, update this Secret, then restart the Radar agent."
	}
	return result
}

func evaluateHubTunnelStatus(status *cloud.AgentStatusResponse, err error) hubTunnelResult {
	if errors.Is(err, cloud.ErrAgentStatusUnauthorized) {
		return hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "failed — the Hub rejected the connection token"}
	}
	if errors.Is(err, cloud.ErrAgentStatusEndpointNotFound) {
		return hubTunnelResult{
			summary: "not checked — the Hub status endpoint was not found",
			detail:  "GET /api/agent/status returned HTTP 404",
			next:    "verify the connection in the Hub's cluster list; if this is a self-hosted Hub, upgrade it to a version that supports live connection status.",
		}
	}
	if err != nil {
		return hubTunnelResult{
			summary: "not checked — the Hub status request failed",
			detail:  err.Error(),
			next:    "check Hub availability and this machine's network access, then try again.",
		}
	}
	switch status.Status {
	case "connected":
		return hubTunnelResult{
			verdict: hubTunnelHealthy, summary: "connected", hubClusterID: status.ClusterID, lastConnectedAt: status.LastConnectedAt,
		}
	case "disconnected":
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy, summary: "disconnected", hubClusterID: status.ClusterID, lastConnectedAt: status.LastConnectedAt,
			next: "check the Radar agent state above and its Cloud connection logs.",
		}
	case "never_connected":
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy, summary: hubTunnelNeverConnectedSummary, hubClusterID: status.ClusterID,
			next: "check the Radar agent state above and its Cloud connection logs.",
		}
	default:
		return hubTunnelResult{
			verdict: hubTunnelUnhealthy,
			summary: "failed — the Hub returned an unknown connection state",
			detail:  fmt.Sprintf("Hub cluster %q reported status %q", status.ClusterID, status.Status),
			next:    "check the Hub version and logs.",
		}
	}
}

func missingCloudConfiguration(runtime cloudinstall.DeploymentRuntime) []string {
	var missing []string
	if !runtime.CloudURLConfigured || (runtime.CloudURL == "" && !runtime.CloudURLUnresolved) {
		missing = append(missing, "Hub URL")
	}
	if !runtime.CloudModeConfigured {
		missing = append(missing, "Cloud mode")
	} else if !runtime.CloudMode && !runtime.CloudModeUnresolved {
		missing = append(missing, "enabled Cloud mode")
	}
	if !runtime.ClusterNameConfigured || (runtime.ClusterName == "" && !runtime.ClusterNameUnresolved) {
		missing = append(missing, "cluster ID")
	}
	if !runtime.CloudTokenConfigured {
		missing = append(missing, "connection token")
	}
	return missing
}

func cloudOwnershipLabel(target cloudinstall.RadarTarget) string {
	switch target.Ownership.Classification {
	case cloudinstall.OwnershipNativeHelm:
		if target.ReleaseName != "" {
			return fmt.Sprintf("Helm release %q", target.ReleaseName)
		}
		return "Helm"
	case cloudinstall.OwnershipGitOpsVerified:
		for _, controller := range target.Ownership.Controllers {
			if controller.Verification != cloudinstall.ControllerVerified {
				continue
			}
			ref := controller.Ref
			name := ref.Name
			if ref.Namespace != "" {
				name = ref.Namespace + "/" + name
			}
			return fmt.Sprintf("GitOps (%s %s)", ref.Kind, name)
		}
		return "GitOps"
	case cloudinstall.OwnershipGitOpsSuspected:
		return "GitOps evidence (suspected; not verified)"
	case cloudinstall.OwnershipGitOpsUnreadable:
		return "GitOps controller (not readable)"
	case cloudinstall.OwnershipGitOpsStale:
		return "GitOps reference (stale)"
	case cloudinstall.OwnershipAmbiguous:
		return "ambiguous ownership"
	case cloudinstall.OwnershipGeneric:
		return "unmanaged"
	default:
		return "unknown"
	}
}
