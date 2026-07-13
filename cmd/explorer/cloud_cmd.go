package main

// `radar cloud <sub>` subcommands — the first subcommand family in Radar's
// otherwise flat-flag CLI. Dispatched from main() before flag.Parse (see the
// os.Args[1]=="cloud" check there).
//
//	radar cloud install     install an in-cluster agent connected to Cloud
//
// Local-process preview connections are not available yet. The reserved
// `connect` command exits before contacting the hub and points users to the
// supported in-cluster paths.

import (
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
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/contextname"
	"github.com/skyhook-io/radar/internal/helm"
	"helm.sh/helm/v3/pkg/chartutil"
	k8svalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	fmt.Fprint(w, `Connect this cluster to Radar Cloud with an in-cluster agent.

Usage:
  radar cloud install [--namespace NS] [--release NAME] [--hub-url URL] [--name NAME] [--dry-run]

install  Install Radar INTO the current-context cluster, connected to Cloud
         (uses your kubeconfig; the in-cluster agent serves with full per-user
         RBAC).

Flags (install):
  --namespace NS   Namespace to install into (default: radar)
  --release NAME   Helm release name (default: radar)
  --hub-url URL    Radar Cloud hub API (default `+defaultHubBase+`; set for self-hosted)
  --name NAME      Cluster name shown in Cloud (default: current kubecontext)
  --chart-version  Chart version to install (default: latest published)
  --dry-run        Run the permission preflight + print the plan; install nothing
  --no-browser     Print the approval URL instead of opening a browser
  --browser NAME   Browser to use for approval (default: Radar config / OS default)
`)
}

func cloudConnect(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("cloud connect", flag.ContinueOnError)
	fs.SetOutput(w)
	hubURL := fs.String("hub-url", defaultHubBase, "Radar Cloud hub API origin")
	name := fs.String("name", "", "Cluster name shown in Cloud (default: current kubecontext)")
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
	fmt.Fprintln(w, "Radar Cloud currently accepts in-cluster agents only; no request was sent to the hub.")
	fmt.Fprintln(w, "\nInstall the supported agent into your current kubeconfig cluster:")
	fmt.Fprintf(w, "  %s\n", installCommand)
	fmt.Fprintln(w, "\nIf Radar is already installed, open your Hub's installation wizard, choose \"Existing installation\", and run the generated Helm upgrade.")
	return 1
}

// cloudInstall implements `radar cloud install`: install Radar INTO the
// current-context cluster with Cloud mode enabled, using the operator's own
// kubeconfig — the only identity that can provision the impersonation RBAC.
// It does not start a local dialer: the in-cluster agent it installs is what
// dials the tunnel. Terminal (exits after installing).
func cloudInstall(args []string) {
	fs := flag.NewFlagSet("cloud install", flag.ExitOnError)
	hubURL := fs.String("hub-url", defaultHubBase, "Radar Cloud hub API origin")
	namespace := fs.String("namespace", cloudinstall.DefaultInstallNamespace, "Namespace to install into")
	release := fs.String("release", cloudinstall.DefaultReleaseName, "Helm release name")
	chartVersion := fs.String("chart-version", "", "Chart version (default: latest published)")
	name := fs.String("name", "", "Cluster name shown in Cloud (default: current kubecontext)")
	noBrowser := fs.Bool("no-browser", false, "Print the approval URL instead of opening a browser")
	browserPref := fs.String("browser", "", "Browser to open the approval URL with")
	dryRun := fs.Bool("dry-run", false, "Preflight + print the plan; install nothing")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "cloud install: unexpected argument %q\n", fs.Arg(0))
		os.Exit(2)
	}
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
	ctxName := currentKubeContextName(kubeconfig)
	clusterName := resolveCloudInstallClusterName(*name, ctxName)

	ctx, cancel := signalContext()
	defer cancel()

	// Build kube + helm clients against the resolved kubecontext — the driver runs
	// before Radar's normal boot, so we resolve these ourselves.
	kc, hc, err := buildLocalInstallClients(kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Preparing a Radar Cloud installation for cluster %q (namespace %q)…\n\n", clusterName, *namespace)

	// 1. Preflight BEFORE minting a token — a permission failure after approval
	//    would orphan a Cloud cluster + a live token.
	pf, err := cloudinstall.InstallPreflight(ctx, kc, *namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "permission preflight failed: %v\n", err)
		os.Exit(1)
	}
	if !pf.OK() {
		fmt.Fprintln(os.Stderr, "You don't have the permissions to install cloud-enabled Radar into this cluster.")
		fmt.Fprintln(os.Stderr, "Missing:")
		for _, d := range pf.Blocking {
			fmt.Fprintf(os.Stderr, "  • %s\n", d)
		}
		fmt.Fprintln(os.Stderr, "\nEnabling Cloud mode provisions per-user RBAC (impersonation), which needs a cluster admin.")
		fmt.Fprintln(os.Stderr, "Ask your platform team to run `radar cloud install` against this kubecontext with the same `--hub-url`, `--namespace`, and `--name` options.")
		os.Exit(1)
	}
	if len(pf.Advisory) > 0 {
		fmt.Println("Note: couldn't confirm these up front (the server dry-run or install will report a denial if they're truly missing):")
		for _, d := range pf.Advisory {
			fmt.Printf("  • %s\n", d)
		}
		fmt.Println()
	}

	// 2. Resolve and server-dry-run one exact chart after checking that both the
	//    release name and fixed token Secret are unused. No Hub request exists yet.
	prepared, err := cloudinstall.Prepare(ctx, hc, kc, cloudinstall.PrepareConfig{
		Namespace: *namespace, ReleaseName: *release, ChartVersion: *chartVersion,
	})
	if err != nil {
		if !printFreshInstallConflict(os.Stderr, err) && !printTokenSecretConflict(os.Stderr, err) {
			fmt.Fprintf(os.Stderr, "installation preflight failed: %v\n", err)
		}
		fmt.Fprintln(os.Stderr, "No Hub request or cluster was created.")
		os.Exit(1)
	}

	// 3. Dry-run stops here — before any token mint or browser.
	if *dryRun {
		deployment := prepared.Deployment()
		fmt.Printf("Dry run — chart skyhook/radar version %s rendered against the target API server without conflicts as release %q in namespace %q (Deployment %q).\n",
			prepared.ChartVersion(), prepared.ReleaseName(), prepared.Namespace(), deployment.Name)
		fmt.Println("Blocking permission checks and installation preflight passed. Re-run without --dry-run to install.")
		return
	}

	// 4. Device flow → approve → cluster token (deployment_mode=in-cluster, so the
	//    hub tags the cluster source=connect_incluster).
	meta := gatherConnectMetadata(clusterName, kubeconfig)
	client := cloud.NewConnectClient(*hubURL)
	cr, err := client.Create(ctx, meta)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\ncouldn't start the connect flow: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Approve this connection in your browser:\n\n    %s\n\n", cr.ConnectURL)
	if !*noBrowser {
		go app.OpenBrowser(cr.ConnectURL, *browserPref)
	}
	fmt.Println("  Waiting for approval… (Ctrl-C to cancel)")

	pr, err := client.PollUntilApproved(ctx, cr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nconnect failed: %v\n", err)
		os.Exit(1)
	}
	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "\nThe Hub approved cluster %q, but this command was canceled before Kubernetes provisioning began.\n", pr.ClusterID)
		fmt.Fprintln(os.Stderr, "No token Secret was written, so this attempt cannot be resumed by rerunning the installer. Inspect and delete that Hub cluster before starting a fresh flow.")
		os.Exit(1)
	}

	// 5. Provision the exact prepared chart with the approved runtime values.
	fmt.Printf("\n  Approved. Installing Radar into %q…\n", *namespace)
	perr := cloudinstall.ProvisionPrepared(ctx, kc, prepared, cloudinstall.ProvisionConfig{
		Namespace:    *namespace,
		ReleaseName:  *release,
		ChartVersion: prepared.ChartVersion(),
		CloudURL:     pr.WSSURL,
		ClusterID:    pr.ClusterID,
		Token:        pr.Token,
	})
	if perr != nil {
		fmt.Fprintf(os.Stderr, "\ninstall failed: %v\n", perr)
		printPostApprovalRecoveryGuidance(os.Stderr, pr.ClusterID, prepared.ReleaseName(), prepared.Namespace(), prepared.Deployment())
		os.Exit(1)
	}

	fmt.Printf("\n  Installed. Waiting up to %s for the in-cluster agent to connect…\n", cloudTunnelConfirmationTimeout)
	if err := client.WaitUntilConsumed(ctx, cr, cloudTunnelConfirmationTimeout); err != nil {
		printTunnelConfirmationFailure(os.Stderr, err, pr.ClusterID, pr.WSSURL, prepared.Deployment())
		os.Exit(1)
	}

	printInstallSuccess(os.Stdout, clusterName, cloudClusterURL(cr.ConnectURL, pr.ClusterID), prepared.Deployment())
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

func printInstallSuccess(w io.Writer, clusterName, clusterURL string, deployment helm.DeploymentRef) {
	fmt.Fprintf(w, "\n  ✓ Cluster %q installed and connected to Radar Cloud.\n", clusterName)
	fmt.Fprintf(w, "    Open: %s\n", clusterURL)
	fmt.Fprintf(w, "    Track it: kubectl -n %s rollout status deployment/%s\n\n", deployment.Namespace, deployment.Name)
}

func printFreshInstallConflict(w io.Writer, err error) bool {
	var exists *helm.ReleaseExistsError
	if errors.As(err, &exists) {
		fmt.Fprintf(w, "Radar is already deployed as Helm release %q in namespace %q (revision %d).\n", exists.Name, exists.Namespace, exists.Revision)
		fmt.Fprintln(w, "`radar cloud install` only creates fresh installations. In the Hub installation wizard, choose \"Existing installation\" and apply its generated Helm upgrade instead.")
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
		fmt.Fprintf(w, "  helm status %s -n %s\n", pending.Name, pending.Namespace)
		fmt.Fprintln(w, "Resolve that release before retrying; Cloud installation will not overwrite it.")
		return true
	}

	var history *helm.ReleaseHistoryError
	if errors.As(err, &history) {
		fmt.Fprintf(w, "Helm release %q in namespace %q has retained %q history (revision %d).\n", history.Name, history.Namespace, history.Status, history.Revision)
		fmt.Fprintln(w, "Cloud installation will not adopt or replace prior Helm history. Inspect it with:")
		fmt.Fprintf(w, "  helm history %s -n %s\n", history.Name, history.Namespace)
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

func printPostApprovalRecoveryGuidance(w io.Writer, clusterID, releaseName, namespace string, deployment helm.DeploymentRef) {
	fmt.Fprintf(w, "Hub cluster %q already exists. Do not rerun the installer or delete it by default; first inspect the existing attempt.\n", clusterID)
	fmt.Fprintln(w, "Inspect:")
	fmt.Fprintf(w, "  helm status %s -n %s\n", releaseName, namespace)
	fmt.Fprintf(w, "  kubectl -n %s get secret/%s\n", namespace, cloudinstall.CloudTokenSecretName)
	fmt.Fprintf(w, "  kubectl -n %s get deployment/%s\n", deployment.Namespace, deployment.Name)
	fmt.Fprintln(w, "The installer removes only the unchanged token Secret it created when a Helm failure can be cleaned up safely; verify the actual release and Secret state.")
	fmt.Fprintln(w, "If the token Secret remains, recover the partial install with this Hub cluster. If the Secret was cleaned up, the token is no longer recoverable: clean up any partial Helm release, then delete this Hub cluster before starting a fresh flow.")
}

func printTunnelConfirmationFailure(w io.Writer, err error, clusterID, cloudURL string, deployment helm.DeploymentRef) {
	reason := err.Error()
	switch {
	case errors.Is(err, cloud.ErrConnectConsumptionTimeout):
		reason = "the five-minute confirmation window elapsed"
	case errors.Is(err, cloud.ErrConnectPickupExpired):
		reason = "the Hub stopped reporting the approved request before the agent connected"
	case errors.Is(err, context.Canceled):
		reason = "confirmation was canceled"
	}

	fmt.Fprintf(w, "\nRadar was installed and Hub cluster %q already exists, but its tunnel could not be confirmed: %s.\n", clusterID, reason)
	fmt.Fprintln(w, "Do not rerun the installer or delete the cluster by default; the existing agent can still connect after you resolve its startup or egress issue.")
	fmt.Fprintln(w, "Inspect:")
	fmt.Fprintf(w, "  kubectl -n %s rollout status deployment/%s\n", deployment.Namespace, deployment.Name)
	fmt.Fprintf(w, "  kubectl -n %s logs deployment/%s --all-containers=true --tail=200\n", deployment.Namespace, deployment.Name)
	fmt.Fprintf(w, "Verify cluster DNS and outbound WSS/HTTPS access to %s.\n", cloudURL)
	fmt.Fprintln(w, "Keep using this Hub cluster and token Secret for recovery. Only if you deliberately abandon the installation should you clean up Helm and the Secret, then delete the Hub cluster before starting a fresh flow.")
}

// buildLocalInstallClients resolves a kube clientset + Helm client from the
// resolved kubecontext (honoring a config.json kubeconfig override), so the
// install targets the operator's configured cluster, not the default context.
func buildLocalInstallClients(kubeconfig string) (kubernetes.Interface, *helm.Client, error) {
	rules := connectLoadingRules(kubeconfig)
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("no reachable kubeconfig context: %w", err)
	}
	// Helm's non-cancelable mutation avoids returning while its background apply
	// is still running. Bound each Kubernetes request so that critical section
	// cannot hang forever on a dead apiserver connection.
	if restCfg.Timeout <= 0 || restCfg.Timeout > cloudKubernetesRequestTimeout {
		restCfg.Timeout = cloudKubernetesRequestTimeout
	}
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("kube client: %w", err)
	}
	// Hand Helm the SAME resolved rest.Config, not a kubeconfig path — otherwise
	// a multi-file KUBECONFIG could leave Helm on a different current-context
	// (cluster B) than the preflight/Secret client (cluster A).
	if err := helm.InitializeWithRESTConfig(restCfg); err != nil {
		return nil, nil, fmt.Errorf("helm init: %w", err)
	}
	return kc, helm.GetClient(), nil
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

// currentKubeContextName reads the current kubecontext directly from kubeconfig,
// without initializing Radar's full client (this runs before that setup).
// Empty string on any failure (e.g. in-cluster with no kubeconfig). kubeconfig
// is the config.json override (or "" for default resolution).
func currentKubeContextName(kubeconfig string) string {
	cfg, err := connectLoadingRules(kubeconfig).Load()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.CurrentContext
}

// gatherConnectMetadata assembles best-effort display context for the consent
// page. k8s version + node count are looked up under a short timeout and simply
// omitted on any failure (RBAC, unreachable) — the consent page renders what's
// present. kubeconfig is the config.json override (or "").
func gatherConnectMetadata(clusterName, kubeconfig string) cloud.ConnectMetadata {
	meta := cloud.ConnectMetadata{
		DeploymentMode: "in-cluster",
		ClusterName:    clusterName,
		RadarVersion:   version,
		Scope:          "cluster",
	}

	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		connectLoadingRules(kubeconfig),
		&clientcmd.ConfigOverrides{},
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
