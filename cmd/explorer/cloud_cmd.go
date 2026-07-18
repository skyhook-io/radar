package main

// `radar cloud <sub>` subcommands — the first subcommand family in Radar's
// otherwise flat-flag CLI. Dispatched from main() before flag.Parse (see the
// os.Args[1]=="cloud" check there).
//
//	radar cloud install     install an in-cluster agent connected to Cloud
//	radar cloud status      inspect an in-cluster Cloud installation
//
// Local-process preview connections are not available yet. The reserved
// `connect` command exits before contacting the hub and points users to the
// supported in-cluster paths.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/cliui"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/contextname"
	"github.com/skyhook-io/radar/internal/helm"
	"golang.org/x/term"
	"helm.sh/helm/v3/pkg/chartutil"
	k8svalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// signalContext returns a context cancelled on Ctrl-C / SIGTERM so a long poll
// wait can be interrupted cleanly.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

const (
	defaultHubBase                 = "https://api.radarhq.io"
	cloudTunnelConfirmationTimeout = 5 * time.Minute
	cloudKubernetesRequestTimeout  = 30 * time.Second
)

// runCloudSubcommand handles `radar cloud …` before the flat flag set is parsed.
func runCloudSubcommand() {
	if len(os.Args) < 3 {
		cloudUsage(os.Stderr)
		os.Exit(2)
	}
	sub := os.Args[2]
	rest := os.Args[3:]
	switch sub {
	case "connect":
		os.Exit(cloudConnect(rest, os.Stderr))
	case "install":
		cloudInstall(rest)
		os.Exit(0)
	case "status":
		os.Exit(cloudStatus(rest, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		cloudUsage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "radar cloud: unknown subcommand %q\n\n", sub)
		cloudUsage(os.Stderr)
		os.Exit(2)
	}
}

func cloudUsage(w *os.File) {
	fmt.Fprint(w, `Connect this cluster to Radar with an in-cluster agent.

Usage:
  radar cloud install [--context NAME] [-y|--yes] [--namespace NS] [--release NAME] [--adopt-existing] [--hub-url URL] [--name NAME] [--dry-run]
  radar cloud status [--context NAME] [--namespace NS --release NAME]

install  Connect one kubeconfig cluster to Radar. Installs Radar when absent,
         or offers a safe native-Helm adoption / GitOps handoff when detected.
         An explicit --context is used directly; otherwise the current context
         must be confirmed unless -y/--yes is set.

status   Inspect the Radar installation in one kubeconfig cluster. Reports
         ownership, Cloud configuration, agent readiness, and Hub-reported
         tunnel health without changing Kubernetes.

Flags (install):
  --context NAME   Kubernetes context to install into (default: current context)
  -y, --yes        Skip current-context confirmation (never adoption consent)
  --namespace NS   Preferred fresh-install namespace / discovery seed (default: radar)
  --release NAME   Preferred fresh-install Helm release / discovery seed (default: radar)
                   Pass both to select an exact target instead of auto-discovery
  --adopt-existing Confirm automation may connect a detected existing installation
  --enable-cloud-features
                   During adoption, also enable Helm/Secrets/exec/forward/metrics RBAC
  --no-self-upgrade
                   Do not install Radar's in-app self-upgrade Role/RoleBinding
  --hub-url URL    Radar Hub API (default `+defaultHubBase+`; set for self-hosted)
  --name NAME      Cluster name shown in Radar (default: selected Kubernetes context)
  --chart-version  Stable chart target (default: latest published, including adoption)
  --dry-run        Run the permission preflight + print the plan; install nothing
  --no-browser     Print the approval URL instead of opening a browser
  --browser NAME   Browser to use for approval (default: Radar config / OS default)

Flags (status):
  --context NAME   Kubernetes context to inspect (default: current context)
  --namespace NS   Exact namespace (requires --release)
  --release NAME   Exact Helm release (requires --namespace)
`)
}

func cloudConnect(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("cloud connect", flag.ContinueOnError)
	fs.SetOutput(w)
	hubURL := fs.String("hub-url", defaultHubBase, "Radar Hub API origin")
	name := fs.String("name", "", "Cluster name shown in Radar (default: current kubecontext)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(w, "\nLocal-process preview mode is not available yet; use `radar cloud install` for the supported in-cluster path.")
			return 0
		}
		fmt.Fprintln(w, "\nLocal-process preview mode is not available yet; use `radar cloud install` for the supported in-cluster path.")
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(w, "cloud connect: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	normalizedHubURL, err := normalizeHubOrigin(*hubURL)
	if err != nil {
		fmt.Fprintf(w, "cloud connect: %v\n", err)
		return 2
	}
	*hubURL = normalizedHubURL
	*name = strings.TrimSpace(*name)

	installCommand := "radar cloud install"
	if *hubURL != defaultHubBase {
		installCommand += fmt.Sprintf(" --hub-url=%q", *hubURL)
	}
	if *name != "" {
		installCommand += fmt.Sprintf(" --name=%q", *name)
	}
	fmt.Fprintln(w, "`radar cloud connect` local preview mode is not available yet.")
	fmt.Fprintln(w, "Radar currently accepts in-cluster agents only; no request was sent to the hub.")
	fmt.Fprintln(w, "\nInstall the supported agent into your current kubeconfig cluster:")
	fmt.Fprintf(w, "  %s\n", installCommand)
	fmt.Fprintln(w, "\nIf Radar is already installed, the same command detects it and offers a native Helm adoption or GitOps handoff. Non-interactive adoption also requires --adopt-existing.")
	return 1
}

// cloudInstall implements `radar cloud install`: install Radar INTO one
// kubeconfig context with Cloud mode enabled, using the operator's own
// kubeconfig — the only identity that can provision the impersonation RBAC.
// It does not start a local dialer: the in-cluster agent it installs is what
// dials the tunnel. Terminal (exits after installing).
func cloudInstall(args []string) {
	fs := flag.NewFlagSet("cloud install", flag.ExitOnError)
	hubURL := fs.String("hub-url", defaultHubBase, "Radar Hub API origin")
	namespace := fs.String("namespace", cloudinstall.DefaultInstallNamespace, "Preferred fresh-install namespace (with --release, selects an exact target)")
	release := fs.String("release", cloudinstall.DefaultReleaseName, "Preferred fresh-install Helm release (with --namespace, selects an exact target)")
	chartVersion := fs.String("chart-version", "", "Chart version (default: latest published)")
	name := fs.String("name", "", "Cluster name shown in Radar (default: selected Kubernetes context)")
	contextName := fs.String("context", "", "Kubernetes context to install into (default: current context)")
	adoptExisting := fs.Bool("adopt-existing", false, "Confirm automation may connect a detected existing installation")
	enableCloudFeatures := fs.Bool("enable-cloud-features", false, "Enable optional Cloud feature RBAC while adopting")
	noSelfUpgrade := fs.Bool("no-self-upgrade", false, "Disable Radar's in-app self-upgrade capability")
	yes := false
	fs.BoolVar(&yes, "y", false, "Skip confirmation when using the current context")
	fs.BoolVar(&yes, "yes", false, "Skip confirmation when using the current context")
	noBrowser := fs.Bool("no-browser", false, "Print the approval URL instead of opening a browser")
	browserPref := fs.String("browser", "", "Browser to open the approval URL with")
	dryRun := fs.Bool("dry-run", false, "Preflight + print the plan; install nothing")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "cloud install: unexpected argument %q\n", fs.Arg(0))
		os.Exit(2)
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
	*chartVersion = strings.TrimSpace(*chartVersion)

	normalizedNamespace, normalizedRelease, err := normalizeCloudInstallNames(*namespace, *release)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(2)
	}
	*namespace = normalizedNamespace
	*release = normalizedRelease
	normalizedHubURL, err := normalizeHubOrigin(*hubURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(2)
	}
	*hubURL = normalizedHubURL

	// Honor a config.json kubeconfig so we install into (and describe) the SAME
	// cluster the operator's config points at, not the default context.
	fileCfg := config.Load()
	if *browserPref == "" {
		*browserPref = fileCfg.Browser
	}
	if len(fileCfg.KubeconfigDirs) > 0 {
		fmt.Fprintln(os.Stderr, "`radar cloud install` cannot choose one cluster while config.json's `kubeconfigDirs` setting is enabled.")
		fmt.Fprintln(os.Stderr, "Clear `kubeconfigDirs` in Radar Settings (or ~/.radar/config.json), then select one current context with KUBECONFIG or config.json's `kubeconfig`.")
		os.Exit(1)
	}
	kubeconfig := fileCfg.Kubeconfig
	requestedContext := strings.TrimSpace(*contextName)
	ctxName, err := resolveCloudInstallContext(kubeconfig, requestedContext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(1)
	}
	commandTarget := cloudCommandTarget{Context: ctxName, Kubeconfig: kubeconfig}
	if !confirmCloudInstallContext(os.Stdin, os.Stderr, ctxName, requestedContext != "", yes, term.IsTerminal(int(os.Stdin.Fd()))) {
		fmt.Fprintln(os.Stderr, "\nRadar installation canceled. Pass --context NAME or -y/--yes to run without this prompt.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr)
	clusterName := resolveCloudInstallClusterName(*name, ctxName)

	ctx, cancel := signalContext()
	defer cancel()
	stdoutStyle := cliui.New(os.Stdout)
	stderrStyle := cliui.New(os.Stderr)

	// Build kube + helm clients against the resolved kubecontext — the driver runs
	// before Radar's normal boot, so we resolve these ourselves.
	clients, err := buildLocalInstallClients(kubeconfig, ctxName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s Inspecting Kubernetes context %q for an existing Radar installation…\n\n", stdoutStyle.Marker(cliui.Progress), ctxName)
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	plan, err := inspectCloudInstallPlan(ctx, clients, *namespace, *release, cloudInstallUsesExactTarget(explicitNamespace, explicitRelease))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
		os.Exit(1)
	}
	*namespace, *release = plan.Namespace, plan.Release
	if !confirmDiscoveryUncertainty(os.Stdin, os.Stderr, plan.ClusterWideScanError, interactive, plan.Namespace, plan.Release) {
		fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
		os.Exit(1)
	}
	if *adoptExisting && plan.Mode == cloudInstallFresh {
		fmt.Fprintln(os.Stderr, "--adopt-existing was set, but no existing Radar release was found at the selected target; refusing to turn an adoption assertion into a fresh install.")
		os.Exit(1)
	}
	if *enableCloudFeatures && plan.Mode == cloudInstallFresh {
		fmt.Fprintln(os.Stderr, "--enable-cloud-features applies only when connecting an existing installation; fresh installs already enable those capabilities.")
		os.Exit(2)
	}

	// Resolve one exact stable target before approval. Native Helm adoption pins
	// both the current release identity and target chart bytes; GitOps only
	// resolves the target version and never prepares an imperative mutation.
	var prepared *cloudinstall.PreparedProvision
	var gitOpsTarget helm.PreparedChartSummary
	if plan.Mode == cloudInstallGitOps {
		gitOpsTarget, err = cloudinstall.ResolveCloudChartSummary(ctx, clients.Helm, *chartVersion)
		if err == nil {
			_, err = buildGitOpsHandoff(plan, gitOpsTarget, "wss://preflight.invalid/agent", "preflight-cluster-id", *enableCloudFeatures)
		}
		if err == nil {
			printGitOpsInstallPlan(os.Stdout, plan, gitOpsTarget, *enableCloudFeatures)
		}
	} else {
		prepared, err = cloudinstall.Prepare(ctx, clients.Helm, clients.Kubernetes, cloudinstall.PrepareConfig{
			Namespace:           plan.Namespace,
			ReleaseName:         plan.Release,
			ChartVersion:        *chartVersion,
			AdoptExisting:       plan.Mode == cloudInstallAdopt,
			EnableCloudFeatures: *enableCloudFeatures,
			DisableSelfUpgrade:  *noSelfUpgrade,
		})
		if err == nil {
			printPreparedInstallPlan(os.Stdout, prepared, *enableCloudFeatures, *noSelfUpgrade)
		}
	}
	if err != nil {
		if !printFreshInstallConflict(os.Stderr, err, commandTarget) && !printTokenSecretConflict(os.Stderr, err) {
			fmt.Fprintf(os.Stderr, "installation preparation failed: %v\n", err)
		}
		fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
		os.Exit(1)
	}

	if plan.Mode != cloudInstallFresh && !*dryRun && !confirmExistingInstall(os.Stdin, os.Stderr, plan, *adoptExisting, interactive) {
		fmt.Fprintln(os.Stderr, "Radar connection canceled. No Hub request or Kubernetes change was made.")
		os.Exit(1)
	}

	// Prove the caller can perform the planned Kubernetes mutations before the
	// Hub creates a cluster or mints a token. GitOps handoff performs no live
	// mutation, so discovery/read access is its complete local gate.
	if plan.Mode != cloudInstallGitOps {
		var pf cloudinstall.PreflightResult
		if plan.Mode == cloudInstallAdopt {
			pf, err = cloudinstall.AdoptionPreflight(ctx, clients.Kubernetes, clients.Dynamic, clients.Discovery, cloudinstall.AdoptionPreflightOptions{
				Namespace:       prepared.Namespace(),
				ReleaseName:     prepared.ReleaseName(),
				CurrentRevision: prepared.CurrentRevision(),
				CurrentManifest: prepared.CurrentManifest(),
				TargetManifest:  prepared.TargetManifest(),
			})
		} else {
			pf, err = cloudinstall.FreshInstallPreflight(ctx, clients.Kubernetes, clients.Dynamic, clients.Discovery, cloudinstall.FreshInstallPreflightOptions{
				Namespace:      prepared.Namespace(),
				ReleaseName:    prepared.ReleaseName(),
				TargetManifest: prepared.TargetManifest(),
			})
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s permission preflight failed: %v\n", stderrStyle.Marker(cliui.Failure), err)
			fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
			os.Exit(1)
		}
		if !pf.OK() {
			printCloudPermissionFailure(os.Stderr, pf, ctxName, *hubURL, prepared, clusterName)
			fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
			os.Exit(1)
		}
		fmt.Printf("%s Permission preflight passed.\n", stdoutStyle.Marker(cliui.Success))
		printCloudPermissionAdvisories(os.Stdout, pf)
	}

	// Dry-run stops before device approval and token minting.
	if *dryRun {
		if plan.Mode == cloudInstallGitOps {
			fmt.Printf("%s Dry run complete. The verified GitOps controller and exact stable target are shown above; no Hub request or live Kubernetes change was made.\n", stdoutStyle.Marker(cliui.Success))
		} else {
			fmt.Printf("%s Dry run complete. Blocking permission checks and chart preparation passed; no Hub request or Kubernetes change was made.\n", stdoutStyle.Marker(cliui.Success))
		}
		return
	}

	// Device flow → approve → cluster token (deployment_mode=in-cluster, so the
	// Hub tags the cluster source=connect_incluster).
	meta := gatherConnectMetadata(clusterName, kubeconfig, ctxName)
	client := cloud.NewConnectClient(*hubURL)
	cr, err := client.Create(ctx, meta)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s couldn't start the connect flow: %v\n", stderrStyle.Marker(cliui.Failure), err)
		os.Exit(1)
	}
	fmt.Printf("  %s Approve this connection in your browser:\n\n    %s\n\n", stdoutStyle.Marker(cliui.Progress), cr.ConnectURL)
	if !*noBrowser {
		go app.OpenBrowser(cr.ConnectURL, *browserPref)
	}
	fmt.Printf("  %s Waiting for approval… (Ctrl-C to cancel)\n", stdoutStyle.Marker(cliui.Progress))

	pr, err := client.PollUntilApproved(ctx, cr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s connect failed: %v\n", stderrStyle.Marker(cliui.Failure), err)
		os.Exit(1)
	}
	if ctx.Err() != nil && plan.Mode != cloudInstallGitOps {
		printCanceledAfterApproval(os.Stderr, pr.ClusterID, cloudClusterURL(cr.ConnectURL, pr.ClusterID))
		os.Exit(1)
	}

	if plan.Mode == cloudInstallGitOps {
		if err := printApprovedGitOpsHandoff(os.Stdout, plan, gitOpsTarget, pr.WSSURL, pr.ClusterID, pr.Token, *enableCloudFeatures); err != nil {
			printGitOpsHandoffFailure(os.Stderr, err, pr.ClusterID, cloudClusterURL(cr.ConnectURL, pr.ClusterID))
			os.Exit(1)
		}
		fmt.Printf("\n  %s Waiting up to %s for your GitOps-managed agent to connect (you can safely leave this running)…\n", stdoutStyle.Marker(cliui.Progress), cloudTunnelConfirmationTimeout)
		if err := client.WaitUntilConsumed(ctx, cr, cloudTunnelConfirmationTimeout); err != nil {
			printGitOpsPendingHandoff(os.Stderr, err, pr.ClusterID, cloudClusterURL(cr.ConnectURL, pr.ClusterID))
			os.Exit(1)
		}
		printInstallSuccess(os.Stdout, clusterName, cloudClusterURL(cr.ConnectURL, pr.ClusterID), helm.DeploymentRef{
			Name: plan.Target.DeploymentName, Namespace: plan.Target.Namespace,
		}, commandTarget)
		return
	}

	action := "Installing"
	if plan.Mode == cloudInstallAdopt {
		action = "Upgrading and connecting"
	}
	fmt.Printf("\n  %s Approved.\n", stdoutStyle.Marker(cliui.Success))
	fmt.Printf("  %s %s Radar in namespace %q…\n", stdoutStyle.Marker(cliui.Progress), action, prepared.Namespace())
	perr := cloudinstall.ProvisionPrepared(ctx, clients.Kubernetes, prepared, cloudinstall.ProvisionConfig{
		Namespace:    prepared.Namespace(),
		ReleaseName:  prepared.ReleaseName(),
		ChartVersion: prepared.ChartVersion(),
		CloudURL:     pr.WSSURL,
		ClusterID:    pr.ClusterID,
		Token:        pr.Token,
	})
	if perr != nil {
		fmt.Fprintf(os.Stderr, "\n%s provisioning failed: %v\n", stderrStyle.Marker(cliui.Failure), perr)
		printPostApprovalRecoveryGuidance(os.Stderr, pr.ClusterID, cloudClusterURL(cr.ConnectURL, pr.ClusterID), provisionRecoveryFrom(prepared), perr, commandTarget)
		os.Exit(1)
	}

	fmt.Printf("\n  %s Kubernetes provisioning complete.\n", stdoutStyle.Marker(cliui.Success))
	fmt.Printf("  %s Waiting up to %s for the in-cluster agent to connect…\n", stdoutStyle.Marker(cliui.Progress), cloudTunnelConfirmationTimeout)
	if err := client.WaitUntilConsumed(ctx, cr, cloudTunnelConfirmationTimeout); err != nil {
		printTunnelConfirmationFailure(os.Stderr, err, pr.ClusterID, pr.WSSURL, prepared.Deployment(), commandTarget)
		os.Exit(1)
	}

	printInstallSuccess(os.Stdout, clusterName, cloudClusterURL(cr.ConnectURL, pr.ClusterID), prepared.Deployment(), commandTarget)
	if plan.Mode == cloudInstallAdopt {
		printAdoptionRollbackGuidance(os.Stdout, provisionRecoveryFrom(prepared), cloudClusterURL(cr.ConnectURL, pr.ClusterID), commandTarget)
	}
}

func normalizeCloudInstallNames(namespace, release string) (string, string, error) {
	namespace = strings.TrimSpace(namespace)
	release = strings.TrimSpace(release)
	if errs := k8svalidation.ValidateNamespaceName(namespace, false); len(errs) > 0 {
		return "", "", fmt.Errorf("invalid --namespace %q: %s", namespace, strings.Join(errs, "; "))
	}
	if err := chartutil.ValidateReleaseName(release); err != nil {
		return "", "", fmt.Errorf("invalid --release %q: %w", release, err)
	}
	return namespace, release, nil
}

func normalizeHubOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if err := cloud.ValidateHubOrigin(raw); err != nil {
		return "", fmt.Errorf("invalid --hub-url %q: %w", raw, err)
	}
	u, _ := url.Parse(raw) // ValidateHubOrigin already parsed and validated it.
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}

func cloudInstallUsesExactTarget(explicitNamespace, explicitRelease bool) bool {
	return explicitNamespace && explicitRelease
}

func resolveCloudInstallClusterName(explicit, contextName string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	if name := strings.TrimSpace(contextname.ShortName(contextName)); name != "" {
		return name
	}
	return "my-cluster"
}

func cloudClusterURL(connectURL, clusterID string) string {
	u, _ := url.Parse(connectURL)
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	return origin + "/c/" + url.PathEscape(clusterID)
}

func printInstallSuccess(w io.Writer, clusterName, clusterURL string, deployment helm.DeploymentRef, target cloudCommandTarget) {
	fmt.Fprintf(w, "\n  %s Cluster %q is connected to Radar.\n", cliui.New(w).Marker(cliui.Success), clusterName)
	fmt.Fprintf(w, "    Open: %s\n", clusterURL)
	fmt.Fprintf(w, "    Track it: %s -n %s rollout status deployment/%s\n\n", target.kubectl(), deployment.Namespace, deployment.Name)
}

func printFreshInstallConflict(w io.Writer, err error, target cloudCommandTarget) bool {
	var exists *helm.ReleaseExistsError
	if errors.As(err, &exists) {
		fmt.Fprintf(w, "Radar is already deployed as Helm release %q in namespace %q (revision %d).\n", exists.Name, exists.Namespace, exists.Revision)
		fmt.Fprintln(w, "The release appeared after this command inspected the cluster, so the prepared fresh-install plan is stale. Rerun the command to classify it; approve the offered adoption interactively or pass --adopt-existing in automation.")
		return true
	}

	var pending *helm.ReleasePendingError
	if errors.As(err, &pending) {
		fmt.Fprintf(w, "Helm release %q in namespace %q is in status %q (revision %d).\n", pending.Name, pending.Namespace, pending.Status, pending.Revision)
		if strings.HasPrefix(pending.Status, "pending-") || pending.Status == "uninstalling" {
			fmt.Fprintln(w, "Wait for the current Helm operation to finish. If it is stale, inspect it with:")
		} else {
			fmt.Fprintln(w, "Radar cannot safely determine how to continue this Helm release. Inspect its state with:")
		}
		fmt.Fprintf(w, "  %s status %s -n %s\n", target.helm(), pending.Name, pending.Namespace)
		fmt.Fprintln(w, "Resolve that release before retrying; Radar installation will not overwrite it.")
		return true
	}

	var history *helm.ReleaseHistoryError
	if errors.As(err, &history) {
		fmt.Fprintf(w, "Helm release %q in namespace %q has retained %q history (revision %d).\n", history.Name, history.Namespace, history.Status, history.Revision)
		fmt.Fprintln(w, "Radar installation will not adopt or replace prior Helm history. Inspect it with:")
		fmt.Fprintf(w, "  %s history %s -n %s\n", target.helm(), history.Name, history.Namespace)
		fmt.Fprintln(w, "Then choose a new --release name, or deliberately remove the old release history before retrying.")
		return true
	}

	return false
}

func printTokenSecretConflict(w io.Writer, err error) bool {
	var secret *cloudinstall.TokenSecretExistsError
	if !errors.As(err, &secret) {
		return false
	}
	fmt.Fprintf(w, "Cloud token Secret %q already exists in namespace %q; Radar will not overwrite it.\n", secret.Name, secret.Namespace)
	fmt.Fprintln(w, "Inspect the existing Helm release and Secret, and recover that installation if it belongs to an earlier approval.")
	fmt.Fprintln(w, "If it was abandoned, clean up its Helm release and Secret and delete the corresponding Hub cluster before starting a fresh flow.")
	return true
}

func printCanceledAfterApproval(w io.Writer, clusterID, clusterURL string) {
	fmt.Fprintf(w, "\n%s The Hub approved cluster %q, but this command was canceled before Kubernetes provisioning began.\n", cliui.New(w).Marker(cliui.Attention), clusterID)
	fmt.Fprintln(w, "No token Secret or Helm release was written. Rerun `radar cloud install` to try again.")
	fmt.Fprintln(w, "The previous approval may remain as a pending cluster in Radar. An organization owner can delete it later:")
	fmt.Fprintf(w, "  %s\n", clusterURL)
}

type cloudProvisionRecovery struct {
	Mode            cloudinstall.ProvisionMode
	ReleaseName     string
	Namespace       string
	Deployment      helm.DeploymentRef
	CurrentRevision int
}

func provisionRecoveryFrom(prepared *cloudinstall.PreparedProvision) cloudProvisionRecovery {
	return cloudProvisionRecovery{
		Mode:            prepared.Mode(),
		ReleaseName:     prepared.ReleaseName(),
		Namespace:       prepared.Namespace(),
		Deployment:      prepared.Deployment(),
		CurrentRevision: prepared.CurrentRevision(),
	}
}

func printPostApprovalRecoveryGuidance(w io.Writer, clusterID, clusterURL string, recovery cloudProvisionRecovery, provisionErr error, target cloudCommandTarget) {
	fmt.Fprintf(w, "Hub cluster %q already exists. Do not rerun the installer; first inspect the existing attempt.\n", clusterID)
	fmt.Fprintf(w, "Open: %s\n", clusterURL)
	if recovery.Mode == cloudinstall.ProvisionAdopt {
		var adoptionErr *cloudinstall.AdoptionUpgradeError
		var preMutationErr *cloudinstall.ProvisionPreMutationError
		switch {
		case errors.As(provisionErr, &preMutationErr):
			fmt.Fprintln(w, "The Helm upgrade did not start, so there was no rollback to verify.")
			if preMutationErr.TokenSecretMayExist {
				fmt.Fprintln(w, "Kubernetes did not confirm whether the Cloud token Secret was created. Inspect the fixed Secret name before deciding how to recover this Hub cluster.")
			} else {
				fmt.Fprintln(w, "This attempt created no Cloud token Secret and made no change to the existing Helm release.")
				fmt.Fprintln(w, "Use this Hub cluster's owner-only Resume install flow to generate fresh credentials, or delete it before deliberately starting over.")
			}
		case errors.As(provisionErr, &adoptionErr) && adoptionErr.RollbackVerified:
			fmt.Fprintf(w, "Helm's atomic rollback restored the original release after the failed adoption (the pre-adoption revision was %d).\n", recovery.CurrentRevision)
			if adoptionErr.TokenSecretPreserved {
				fmt.Fprintln(w, "The Cloud token Secret could not be safely removed; keep it while you inspect the release and recover this same Hub cluster.")
			} else {
				fmt.Fprintln(w, "The unchanged Cloud token Secret created by this attempt was removed after rollback verification.")
			}
		default:
			fmt.Fprintln(w, "Radar could not prove that the original release was fully restored, so it preserved the Cloud token Secret. Inspect and recover this same Hub cluster before any cleanup.")
		}
		fmt.Fprintln(w, "Inspect:")
		fmt.Fprintf(w, "  %s status %s -n %s\n", target.helm(), recovery.ReleaseName, recovery.Namespace)
		fmt.Fprintf(w, "  %s history %s -n %s\n", target.helm(), recovery.ReleaseName, recovery.Namespace)
		fmt.Fprintf(w, "  %s -n %s get secret/%s\n", target.kubectl(), recovery.Namespace, cloudinstall.CloudTokenSecretName)
		fmt.Fprintf(w, "  %s -n %s get deployment/%s\n", target.kubectl(), recovery.Deployment.Namespace, recovery.Deployment.Name)
		return
	}

	releaseName, namespace, deployment := recovery.ReleaseName, recovery.Namespace, recovery.Deployment
	fmt.Fprintln(w, "Inspect:")
	fmt.Fprintf(w, "  %s status %s -n %s\n", target.helm(), releaseName, namespace)
	fmt.Fprintf(w, "  %s -n %s get secret/%s\n", target.kubectl(), namespace, cloudinstall.CloudTokenSecretName)
	fmt.Fprintf(w, "  %s -n %s get deployment/%s\n", target.kubectl(), deployment.Namespace, deployment.Name)
	fmt.Fprintln(w, "The installer removes only the unchanged token Secret it created when a Helm failure can be cleaned up safely; verify the actual release and Secret state.")
	fmt.Fprintln(w, "If the token Secret remains, recover the partial install with this Hub cluster. If the Secret was cleaned up, the token is no longer recoverable: clean up any partial Helm release, then delete this Hub cluster before starting a fresh flow.")
}

func printTunnelConfirmationFailure(w io.Writer, err error, clusterID, cloudURL string, deployment helm.DeploymentRef, target cloudCommandTarget) {
	reason := err.Error()
	switch {
	case errors.Is(err, cloud.ErrConnectConsumptionTimeout):
		reason = "the five-minute confirmation window elapsed"
	case errors.Is(err, cloud.ErrConnectPickupExpired):
		reason = "the Hub stopped reporting the approved request before the agent connected"
	case errors.Is(err, context.Canceled):
		reason = "confirmation was canceled"
	}

	fmt.Fprintf(w, "\n%s Radar was provisioned and Hub cluster %q already exists, but its tunnel could not be confirmed: %s.\n", cliui.New(w).Marker(cliui.Attention), clusterID, reason)
	fmt.Fprintln(w, "Do not rerun the installer or delete the cluster by default; the existing agent can still connect after you resolve its startup or egress issue.")
	fmt.Fprintln(w, "Inspect:")
	fmt.Fprintf(w, "  %s -n %s rollout status deployment/%s\n", target.kubectl(), deployment.Namespace, deployment.Name)
	fmt.Fprintf(w, "  %s -n %s logs deployment/%s --all-containers=true --tail=200\n", target.kubectl(), deployment.Namespace, deployment.Name)
	fmt.Fprintf(w, "Verify cluster DNS and outbound WSS/HTTPS access to %s.\n", cloudURL)
	fmt.Fprintln(w, "Keep using this Hub cluster and token Secret for recovery. Only if you deliberately abandon the installation should you clean up Helm and the Secret, then delete the Hub cluster before starting a fresh flow.")
}

// buildLocalInstallClients resolves a kube clientset + Helm client from the
// resolved kubecontext (honoring a config.json kubeconfig override), so the
// install targets the operator's configured cluster, not the default context.
type localInstallClients struct {
	Kubernetes kubernetes.Interface
	Dynamic    dynamic.Interface
	Discovery  discovery.DiscoveryInterface
	Helm       *helm.Client
	Releases   cloudReleaseInspector
}

type localKubernetesClients struct {
	Kubernetes kubernetes.Interface
	Dynamic    dynamic.Interface
	Discovery  discovery.DiscoveryInterface
	RESTConfig *rest.Config
}

func buildLocalKubernetesClients(kubeconfig, contextName string) (localKubernetesClients, error) {
	rules := connectLoadingRules(kubeconfig)
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: contextName}).ClientConfig()
	if err != nil {
		return localKubernetesClients{}, fmt.Errorf("no reachable kubeconfig context: %w", err)
	}
	// Bound each Kubernetes request so status and install cannot hang forever on
	// a dead apiserver connection. This also bounds Helm's non-cancelable apply
	// critical section when the caller initializes Helm with this config.
	if restCfg.Timeout <= 0 || restCfg.Timeout > cloudKubernetesRequestTimeout {
		restCfg.Timeout = cloudKubernetesRequestTimeout
	}
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return localKubernetesClients{}, fmt.Errorf("kube client: %w", err)
	}
	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return localKubernetesClients{}, fmt.Errorf("dynamic kube client: %w", err)
	}
	return localKubernetesClients{Kubernetes: kc, Dynamic: dc, Discovery: kc.Discovery(), RESTConfig: restCfg}, nil
}

func buildLocalInstallClients(kubeconfig, contextName string) (localInstallClients, error) {
	clients, err := buildLocalKubernetesClients(kubeconfig, contextName)
	if err != nil {
		return localInstallClients{}, err
	}
	// Hand Helm the SAME resolved rest.Config, not a kubeconfig path — otherwise
	// a multi-file KUBECONFIG could leave Helm on a different current-context
	// (cluster B) than the preflight/Secret client (cluster A).
	if err := helm.InitializeWithRESTConfig(clients.RESTConfig); err != nil {
		return localInstallClients{}, fmt.Errorf("helm init: %w", err)
	}
	return localInstallClients{
		Kubernetes: clients.Kubernetes,
		Dynamic:    clients.Dynamic,
		Discovery:  clients.Discovery,
		Helm:       helm.GetClient(),
		Releases:   helm.GetClient(),
	}, nil
}

// connectLoadingRules builds kubeconfig loading rules that honor a config.json
// `kubeconfig` override. NewDefaultClientConfigLoadingRules reads the KUBECONFIG
// env + ~/.kube/config (which main() also honors), but NOT ~/.radar/config.json's
// `kubeconfig` — without this the install flow would resolve the default context
// while targeting the config.json-selected one.
func connectLoadingRules(kubeconfig string) *clientcmd.ClientConfigLoadingRules {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	return rules
}

func resolveCloudInstallContext(kubeconfig, requested string) (string, error) {
	cfg, err := connectLoadingRules(kubeconfig).Load()
	if err != nil {
		return "", fmt.Errorf("load kubeconfig: %w", err)
	}
	if cfg == nil {
		return "", errors.New("kubeconfig is empty")
	}
	contextName := strings.TrimSpace(requested)
	if contextName == "" {
		contextName = cfg.CurrentContext
	}
	if strings.TrimSpace(contextName) == "" {
		return "", errors.New("kubeconfig has no current context; pass --context NAME")
	}
	if _, ok := cfg.Contexts[contextName]; !ok {
		return "", fmt.Errorf("Kubernetes context %q was not found in the resolved kubeconfig", contextName)
	}
	return contextName, nil
}

func confirmCloudInstallContext(in io.Reader, out io.Writer, contextName string, contextExplicit, yes, interactive bool) bool {
	fmt.Fprintf(out, "Kubernetes context: %q\n", contextName)
	if contextExplicit || yes {
		return true
	}
	if !interactive {
		fmt.Fprintln(out, "No interactive terminal is available for current-context confirmation.")
		return false
	}
	fmt.Fprint(out, "Use this current context? [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// gatherConnectMetadata assembles best-effort display context for the consent
// page. k8s version + node count are looked up under a short timeout and simply
// omitted on any failure (RBAC, unreachable) — the consent page renders what's
// present. kubeconfig is the config.json override (or "").
func gatherConnectMetadata(clusterName, kubeconfig, contextName string) cloud.ConnectMetadata {
	meta := cloud.ConnectMetadata{
		DeploymentMode: "in-cluster",
		ClusterName:    clusterName,
		RadarVersion:   version,
		Scope:          "cluster",
	}

	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		connectLoadingRules(kubeconfig),
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	).ClientConfig()
	if err != nil {
		return meta
	}
	// Bound the whole best-effort probe so `radar cloud install` never hangs on
	// an unreachable cluster — ServerVersion() has no context and would
	// otherwise inherit the rest config's (zero = infinite) timeout.
	restCfg.Timeout = 5 * time.Second
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return meta
	}
	if v, err := cs.Discovery().ServerVersion(); err == nil && v != nil {
		meta.K8sVersion = v.GitVersion
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500}); err == nil {
		n := len(nodes.Items)
		meta.NodeCount = &n
	}
	return meta
}
