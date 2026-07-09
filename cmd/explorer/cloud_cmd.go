package main

// `radar cloud <sub>` subcommands — the first subcommand family in Radar's
// otherwise flat-flag CLI. Dispatched from main() before flag.Parse (see the
// os.Args[1]=="cloud" check there).
//
//	radar cloud connect     device-flow connect this cluster to Radar Cloud
//	radar cloud status      show the current context's cloud connection
//	radar cloud disconnect  forget the current context's cloud connection
//
// `connect` performs the browser device flow, persists the minted token to
// ~/.radar/credentials.json (0600), then rewrites os.Args so the normal main()
// flow brings up the server + cloud dialer with the obtained token. status and
// disconnect are terminal (they exit).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/helm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// signalContext returns a context cancelled on Ctrl-C / SIGTERM so a long poll
// wait can be interrupted cleanly.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

const defaultHubBase = "https://api.radarhq.io"

// runCloudSubcommand handles `radar cloud …`. For `connect` it returns after
// rewriting os.Args so main() proceeds; status/disconnect/help exit directly.
func runCloudSubcommand() {
	if len(os.Args) < 3 {
		cloudUsage(os.Stderr)
		os.Exit(2)
	}
	sub := os.Args[2]
	rest := os.Args[3:]
	switch sub {
	case "connect":
		cloudConnect(rest)
	case "install":
		cloudInstall(rest)
		os.Exit(0)
	case "status":
		cloudStatus()
		os.Exit(0)
	case "disconnect":
		cloudDisconnect(rest)
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
	fmt.Fprint(w, `Connect this cluster to Radar Cloud.

Usage:
  radar cloud install [--namespace NS] [--hub-url URL] [--name NAME] [--dry-run]
  radar cloud connect [--hub-url URL] [--name NAME] [--no-browser]
  radar cloud status
  radar cloud disconnect

install  Install Radar INTO the current-context cluster, connected to Cloud
         (uses your kubeconfig; the in-cluster agent serves with full per-user
         RBAC). This is the recommended way to connect a cluster.
connect  Tunnel THIS local Radar process to Cloud (preview; does not install).

Flags (install):
  --namespace NS   Namespace to install into (default: radar)
  --hub-url URL    Radar Cloud hub API (default `+defaultHubBase+`; set for self-hosted)
  --name NAME      Cluster name shown in Cloud (default: current kubecontext)
  --chart-version  Chart version to install (default: latest published)
  --dry-run        Run the permission preflight + print the plan; install nothing
  --no-browser     Print the approval URL instead of opening a browser

Flags (connect):
  --hub-url URL   Radar Cloud hub API (default `+defaultHubBase+`; set for self-hosted)
  --name NAME     Cluster name shown in Cloud (default: current kubecontext)
  --no-browser    Print the approval URL instead of opening a browser
`)
}

func cloudConnect(args []string) {
	fs := flag.NewFlagSet("cloud connect", flag.ExitOnError)
	hubURL := fs.String("hub-url", defaultHubBase, "Radar Cloud hub API origin")
	name := fs.String("name", "", "Cluster name shown in Cloud (default: current kubecontext)")
	noBrowser := fs.Bool("no-browser", false, "Print the approval URL instead of opening a browser")
	browserPref := fs.String("browser", "", "Browser to open the approval URL with")
	_ = fs.Parse(args)

	// Honor a config.json kubeconfig so the metadata + saved ServerURL describe
	// the SAME cluster main() will serve (not the default context).
	fileCfg := config.Load()
	if len(fileCfg.KubeconfigDirs) > 0 {
		fmt.Fprintln(os.Stderr, "`radar cloud connect` doesn't support a kubeconfigDirs (multi-cluster) config — it can't pick one cluster.")
		fmt.Fprintln(os.Stderr, "Point it at a single kubeconfig instead — set KUBECONFIG or config.json's `kubeconfig`.")
		os.Exit(1)
	}
	kubeconfig := fileCfg.Kubeconfig
	ctxName := currentKubeContextName(kubeconfig)
	clusterName := *name
	if clusterName == "" {
		clusterName = ctxName
	}
	if clusterName == "" {
		clusterName = "my-cluster"
	}

	meta := gatherConnectMetadata(clusterName, kubeconfig)

	ctx, cancel := signalContext()
	defer cancel()

	client := cloud.NewConnectClient(*hubURL)
	var open func(string)
	if !*noBrowser {
		open = func(u string) { go app.OpenBrowser(u, *browserPref) }
	}

	fmt.Printf("Connecting %q to Radar Cloud (%s)…\n", clusterName, *hubURL)
	res, err := client.RunFlow(ctx, meta, os.Stdout, open)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nconnect failed: %v\n", err)
		os.Exit(1)
	}

	// Persist the token (0600) keyed by kubecontext so status/disconnect and a
	// later resume can find it.
	keyCtx := ctxName
	if keyCtx == "" {
		keyCtx = res.ClusterID
	}
	if err := cloud.SaveClusterCredential(keyCtx, cloud.ClusterCredential{
		HubBase:     *hubURL,
		ClusterID:   res.ClusterID,
		ClusterName: clusterName,
		Token:       res.Token,
		WSSURL:      res.WSSURL,
		ServerURL:   currentClusterServerURL(kubeconfig),
	}); err != nil {
		// Non-fatal: we can still serve this session; the user just won't get
		// auto-resume next time. Warn and continue.
		fmt.Fprintf(os.Stderr, "warning: couldn't save credentials: %v\n", err)
	}

	fmt.Printf("\n  ✓ Connected. Starting Radar and serving to Cloud — Ctrl-C to stop.\n\n")

	// Rewrite os.Args so the normal main() flow starts the server + dialer with
	// the obtained connection. --no-browser suppresses the LOCAL UI tab (the
	// user already has the Cloud page open).
	os.Args = []string{
		os.Args[0],
		"--cloud-url=" + res.WSSURL,
		"--cloud-token=" + res.Token,
		"--cluster-name=" + res.ClusterID,
		"--no-browser",
	}
}

// cloudInstall implements `radar cloud install` (#2): install Radar INTO the
// current-context cluster with Cloud mode enabled, using the operator's own
// kubeconfig — the only identity that can provision the impersonation RBAC.
// Unlike `connect`, it does NOT start a local dialer: the in-cluster agent it
// installs is what dials the tunnel. Terminal (exits after installing).
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

	// Honor a config.json kubeconfig so we install into (and describe) the SAME
	// cluster the operator's config points at, not the default context.
	fileCfg := config.Load()
	if len(fileCfg.KubeconfigDirs) > 0 {
		fmt.Fprintln(os.Stderr, "`radar cloud install` doesn't support a kubeconfigDirs (multi-cluster) config — it can't pick one cluster.")
		fmt.Fprintln(os.Stderr, "Point it at a single kubeconfig instead — set KUBECONFIG or config.json's `kubeconfig`.")
		os.Exit(1)
	}
	kubeconfig := fileCfg.Kubeconfig
	ctxName := currentKubeContextName(kubeconfig)
	clusterName := *name
	if clusterName == "" {
		clusterName = ctxName
	}
	if clusterName == "" {
		clusterName = "my-cluster"
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Build kube + helm clients against the resolved kubecontext — the driver runs
	// before Radar's normal boot, so we resolve these ourselves.
	kc, hc, err := buildLocalInstallClients(kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud install: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Installing Radar into cluster %q (namespace %q)…\n\n", clusterName, *namespace)

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
		fmt.Fprintln(os.Stderr, "Ask your platform team to run `radar cloud install`, or share the approval link with them.")
		os.Exit(1)
	}
	if len(pf.Advisory) > 0 {
		fmt.Println("Note: couldn't confirm these up front (the install will fail cleanly if they're truly missing):")
		for _, d := range pf.Advisory {
			fmt.Printf("  • %s\n", d)
		}
		fmt.Println()
	}

	// 2. Existing-release gate — BEFORE the token mint. A fresh install can't
	//    replace a deployed release (upgrading an existing Radar to Cloud mode
	//    from the CLI isn't supported yet), and minting past this point would
	//    leave a live token + Secret in-cluster with no connected agent.
	if blocked, berr := hc.ReleaseInstallBlocked(*namespace, *release); berr != nil {
		fmt.Fprintf(os.Stderr, "couldn't inspect the existing Radar release: %v\n", berr)
		os.Exit(1)
	} else if blocked {
		fmt.Fprintf(os.Stderr, "Radar is already installed in namespace %q (release %q).\n", *namespace, *release)
		fmt.Fprintln(os.Stderr, "Upgrading an existing install to Cloud mode from `radar cloud install` isn't supported yet —")
		fmt.Fprintf(os.Stderr, "uninstall it first (helm uninstall %s -n %s) and re-run.\n", *release, *namespace)
		os.Exit(1)
	}

	// 3. Dry-run stops here — before any token mint or browser.
	if *dryRun {
		fmt.Printf("Dry run — would install chart skyhook/radar (version %s) as release %q in namespace %q with cloud.enabled=true.\n",
			chartVersionOrLatest(*chartVersion), *release, *namespace)
		fmt.Println("Permission preflight passed. Re-run without --dry-run to install.")
		return
	}

	// 4. Device flow → approve → cluster token (deployment_mode=in-cluster, so the
	//    hub tags the cluster source=connect_incluster).
	meta := gatherConnectMetadata(clusterName, kubeconfig)
	meta.DeploymentMode = "in-cluster"
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
	cloudURL := pr.WSSURL
	if cloudURL == "" { // approved poll may omit it; fall back to create-time wss
		cloudURL = cr.WSSURL
	}

	// 4. Provision — install the chart with the token; the in-cluster agent dials.
	fmt.Printf("\n  Approved. Installing Radar into %q…\n", *namespace)
	perr := cloudinstall.Provision(ctx, hc, kc, cloudinstall.ProvisionConfig{
		Namespace:    *namespace,
		ReleaseName:  *release,
		ChartVersion: *chartVersion,
		CloudURL:     cloudURL,
		ClusterID:    pr.ClusterID,
		Token:        pr.Token,
	})
	if errors.Is(perr, cloudinstall.ErrReleaseExists) {
		// Rare: a release appeared after the pre-mint gate. If it landed before
		// Provision's block-check nothing was written; in the narrow window
		// between that check and helm install, the token Secret may have been.
		fmt.Fprintf(os.Stderr, "\nRadar was installed into namespace %q concurrently, so this install stopped.\n", *namespace)
		fmt.Fprintf(os.Stderr, "Reconcile the existing install (or `helm uninstall %s -n %s`); a %q Secret may have been written — verify it before re-running.\n", *release, *namespace, cloudinstall.CloudTokenSecretName)
		os.Exit(1)
	}
	if perr != nil {
		fmt.Fprintf(os.Stderr, "\ninstall failed: %v\n", perr)
		os.Exit(1)
	}

	fmt.Printf("\n  ✓ Installed. Radar is starting in your cluster and will connect to Cloud shortly.\n")
	fmt.Printf("    Track it: kubectl -n %s rollout status deploy/%s\n\n", *namespace, *release)
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

func chartVersionOrLatest(v string) string {
	if v == "" {
		return "latest"
	}
	return v
}

func cloudStatus() {
	ctxName := currentKubeContextName(config.Load().Kubeconfig)
	creds := cloud.LoadCredentials()
	if len(creds.Clusters) == 0 {
		fmt.Println("No clusters connected to Radar Cloud.")
		fmt.Println("Run `radar cloud connect` to connect this cluster.")
		return
	}
	fmt.Printf("Radar Cloud connections (%s):\n\n", cloud.CredentialsPath())
	for ctx, c := range creds.Clusters {
		marker := "  "
		if ctx == ctxName {
			marker = "* " // current kubecontext
		}
		fmt.Printf("%s%s\n      cluster: %s (%s)\n      hub:     %s\n",
			marker, ctx, c.ClusterName, c.ClusterID, c.HubBase)
	}
	if ctxName != "" {
		fmt.Printf("\n(* = current kubecontext)\n")
	}
}

func cloudDisconnect(args []string) {
	fs := flag.NewFlagSet("cloud disconnect", flag.ExitOnError)
	ctxFlag := fs.String("context", "", "Kubecontext to disconnect (default: current)")
	_ = fs.Parse(args)
	ctxName := *ctxFlag
	if ctxName == "" {
		ctxName = currentKubeContextName(config.Load().Kubeconfig)
	}
	if ctxName == "" {
		fmt.Fprintln(os.Stderr, "no current kubecontext; pass --context NAME")
		os.Exit(1)
	}
	removed, err := cloud.RemoveClusterCredential(ctxName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "disconnect failed: %v\n", err)
		os.Exit(1)
	}
	if !removed {
		fmt.Printf("No Radar Cloud connection stored for context %q.\n", ctxName)
		return
	}
	fmt.Printf("Disconnected %q from Radar Cloud (credentials removed).\n", ctxName)
	fmt.Println("The cluster still exists in Cloud until you remove it there.")
}

// maybeResumeCloud auto-injects cloud flags when a plain `radar` runs on a
// kubecontext that has a saved Cloud connection, so `radar cloud connect` once
// makes future `radar` runs resume the tunnel. Explicit cloud config (a
// --cloud-url flag or RADAR_CLOUD_URL env — the in-cluster/Helm path) always
// wins and short-circuits this. `radar cloud disconnect` removes the saved
// credential, turning resume off.
func maybeResumeCloud(fileCfg config.Config) {
	if os.Getenv("RADAR_CLOUD_URL") != "" {
		return
	}
	for _, a := range os.Args[1:] {
		if a == "--cloud-url" || strings.HasPrefix(a, "--cloud-url=") {
			return
		}
	}
	// Multi-cluster mode (config.json kubeconfigDirs) serves an ambiguous set of
	// contexts, not one — the credential's single context name can't identify
	// which cluster is being served, so require an explicit `radar cloud connect`.
	if len(fileCfg.KubeconfigDirs) > 0 {
		return
	}
	// A CLI --kubeconfig / --kubeconfig-dir override selects a kubeconfig we can't
	// cheaply resolve here (flags aren't parsed yet), and the saved cred wasn't
	// keyed to it — decline. A single-file config.json `kubeconfig` is fine: main
	// serves that same file (the --kubeconfig flag defaults to it) and connect
	// keyed the cred + server_url against it via currentKubeContextName/
	// currentClusterServerURL below, so the server-URL guard makes resume safe.
	for _, a := range os.Args[1:] {
		if isKubeconfigOverrideArg(a) {
			return
		}
	}
	ctxName := currentKubeContextName(fileCfg.Kubeconfig)
	if ctxName == "" {
		return
	}
	cred, ok := cloud.GetClusterCredential(ctxName)
	if !ok || cred.Token == "" || cred.WSSURL == "" {
		return
	}
	// Bind the credential to the cluster: if the saved server URL differs from
	// the current context's, this is a same-named context in a different
	// kubeconfig pointing at a different cluster — don't resume (would serve
	// the wrong cluster under this cred's Cloud identity). Empty saved URL
	// (legacy cred) falls back to name-only matching.
	if cred.ServerURL != "" {
		if cur := currentClusterServerURL(fileCfg.Kubeconfig); cur != "" && cur != cred.ServerURL {
			log.Printf("[cloud] not resuming context %q: kubeconfig now points at a different cluster than the saved connection", ctxName)
			return
		}
	}
	log.Printf("[cloud] resuming saved Radar Cloud connection for context %q (cluster %s) — `radar cloud disconnect` to stop", ctxName, cred.ClusterID)
	os.Args = append(os.Args,
		"--cloud-url="+cred.WSSURL,
		"--cloud-token="+cred.Token,
		"--cluster-name="+cred.ClusterID,
	)
}

// isKubeconfigOverrideArg reports whether an arg selects a non-default
// kubeconfig, which would make the default-context resume unsafe.
func isKubeconfigOverrideArg(a string) bool {
	for _, p := range []string{"--kubeconfig", "-kubeconfig", "--kubeconfig-dir", "-kubeconfig-dir"} {
		if a == p || strings.HasPrefix(a, p+"=") {
			return true
		}
	}
	return false
}

// connectLoadingRules builds kubeconfig loading rules that honor a config.json
// `kubeconfig` override. NewDefaultClientConfigLoadingRules reads the KUBECONFIG
// env + ~/.kube/config (which main() also honors), but NOT ~/.radar/config.json's
// `kubeconfig` — without this the connect flow would resolve the DEFAULT context
// while main() serves the config.json one, registering/serving mismatched
// clusters under the minted token. (KubeconfigDirs multi-cluster mode is left to
// default resolution: the resume guard already declines there, and single-cluster
// connect metadata is inherently best-effort across many clusters.)
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

// currentClusterServerURL returns the kube-apiserver endpoint of the current
// context, resolved locally (no network). Empty on any failure. Used to bind a
// saved credential to its cluster so a same-named context in a different
// kubeconfig can't resume it.
func currentClusterServerURL(kubeconfig string) string {
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		connectLoadingRules(kubeconfig),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return ""
	}
	return restCfg.Host
}

// gatherConnectMetadata assembles best-effort display context for the consent
// page. k8s version + node count are looked up under a short timeout and simply
// omitted on any failure (RBAC, unreachable) — the consent page renders what's
// present. kubeconfig is the config.json override (or "").
func gatherConnectMetadata(clusterName, kubeconfig string) cloud.ConnectMetadata {
	meta := cloud.ConnectMetadata{
		DeploymentMode: "local",
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
	// Bound the whole best-effort probe so `radar cloud connect` never hangs on
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
