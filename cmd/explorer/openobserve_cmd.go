package main

// `radar openobserve connect|status|disconnect` manage the one OpenObserve
// connection that local Radar shares across every kubeconfig context.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/openobserve"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

const openobserveRequestTimeout = 30 * time.Second

// openobserveKube resolves a kubeconfig context (empty = current) to its name
// and a client.
type openobserveKube func(contextName string) (string, kubernetes.Interface, error)

type openobserveCmd struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
	kube   openobserveKube
}

func runOpenObserveSubcommand() {
	cmd := openobserveCmd{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, kube: localOpenObserveKube}
	os.Exit(cmd.run(os.Args[2:]))
}

func localOpenObserveKube(contextName string) (string, kubernetes.Interface, error) {
	sel, err := selectCloudCommandKubeconfig(config.Load())
	if err != nil {
		return "", nil, err
	}
	name, err := resolveCloudInstallContext(sel.path, contextName)
	if err != nil {
		return "", nil, err
	}
	clients, err := buildLocalKubernetesClients(sel.path, name)
	if err != nil {
		return "", nil, err
	}
	return name, clients.Kubernetes, nil
}

const openobserveUsage = `Share one OpenObserve connection across every kubeconfig context of local Radar.

Usage:
  radar openobserve connect --url URL --user USER (--token TOKEN | --token-env VAR) [flags]
  radar openobserve status [--context NAME]
  radar openobserve disconnect

connect     Save the connection in ~/.radar/config.json, test it, and match one
            context (default: the current one) to its cluster in OpenObserve.
            Run it again with --context to match another context; the saved
            connection is reused.
status      Show the connection (never the token) and the current context's match.
disconnect  Remove the connection and every saved match.

Flags (connect):
  --url URL             OpenObserve base URL, e.g. https://openobserve.example.com
  --org ORG             Organization (default "default")
  --user USER           User or service account email
  --token TOKEN         Password or token (stored in plain text, file mode 0600)
  --token-env VAR       Read the token from this environment variable instead
  --ca-cert FILE        Extra CA bundle (PEM) to trust
  --cluster-label NAME  Label that names clusters, if not one Radar knows
  --context NAME        Kubernetes context to match (default: current context)
  --yes                 Accept the suggested cluster name without asking

Local Radar only: Radar running inside a cluster ignores this connection.
`

func (c openobserveCmd) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.errOut, openobserveUsage)
		return 2
	}
	switch args[0] {
	case "connect":
		return c.connect(args[1:])
	case "status":
		return c.status(args[1:])
	case "disconnect":
		return c.disconnect(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(c.out, openobserveUsage)
		return 0
	default:
		fmt.Fprintf(c.errOut, "radar openobserve: unknown subcommand %q\n\n", args[0])
		fmt.Fprint(c.errOut, openobserveUsage)
		return 2
	}
}

func (c openobserveCmd) parse(fs *flag.FlagSet, args []string) (int, bool) {
	fs.SetOutput(c.errOut)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(c.errOut, "openobserve %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return 2, false
	}
	return 0, true
}

func (c openobserveCmd) connect(args []string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(c.errOut, openobserveUsage) }
	baseURL := fs.String("url", "", "")
	org := fs.String("org", "default", "")
	user := fs.String("user", "", "")
	token := fs.String("token", "", "")
	tokenEnv := fs.String("token-env", "", "")
	caCert := fs.String("ca-cert", "", "")
	clusterLabel := fs.String("cluster-label", "", "")
	contextName := fs.String("context", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	if code, ok := c.parse(fs, args); !ok {
		return code
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	existing := config.Load().OpenObserve
	entry := config.OpenObserveConfig{}
	if existing != nil {
		entry = existing.Clone()
	}
	if set["url"] {
		entry.BaseURL = strings.TrimRight(strings.TrimSpace(*baseURL), "/")
	}
	if set["org"] || entry.Org == "" {
		entry.Org = strings.TrimSpace(*org)
	}
	if set["user"] {
		entry.User = strings.TrimSpace(*user)
	}
	if set["ca-cert"] {
		entry.CACertFile = strings.TrimSpace(*caCert)
	}
	if set["cluster-label"] {
		entry.ClusterLabel = strings.ToLower(strings.TrimSpace(*clusterLabel))
	}
	sameTarget := existing != nil && existing.BaseURL == entry.BaseURL && existing.Org == entry.Org && existing.User == entry.User
	switch {
	case set["token"] && set["token-env"]:
		fmt.Fprintln(c.errOut, "openobserve connect: pass --token or --token-env, not both")
		return 2
	case set["token"]:
		entry.Token, entry.TokenEnv = *token, ""
	case set["token-env"]:
		entry.Token, entry.TokenEnv = "", strings.TrimSpace(*tokenEnv)
	case !sameTarget:
		// Never send a saved secret to a different server or account.
		entry.Token, entry.TokenEnv = "", ""
	}
	if entry.BaseURL == "" || entry.User == "" || (entry.Token == "" && entry.TokenEnv == "") {
		fmt.Fprintln(c.errOut, "openobserve connect: --url, --user and --token (or --token-env) are required")
		return 2
	}
	if existing == nil || existing.BaseURL != entry.BaseURL || existing.Org != entry.Org {
		// Matches belong to the data of one organization.
		entry.Matches = nil
	}
	conn, err := openobserve.Connection(entry)
	if err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: %v\n", err)
		return 1
	}
	if err := saveOpenObserve(&entry); err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: save %s: %v\n", config.Path(), err)
		return 1
	}
	fmt.Fprintf(c.out, "Saved the OpenObserve connection to %s (%s, organization %s, user %s).\n", config.Path(), entry.BaseURL, entry.Org, entry.User)

	ctx, cancel := signalContext()
	defer cancel()
	raw, err := conn.NewHTTPTransport(openobserveRequestTimeout)
	if err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: %v\n", err)
		return 1
	}
	if _, err := prom.NewClient(raw).Query(ctx, "vector(1)"); err != nil {
		fmt.Fprintf(c.errOut, "Connection test failed: %s\nThe connection is saved; fix it and run connect again.\n", prom.RedactURLs(err.Error()))
		return 1
	}
	fmt.Fprintln(c.out, "Connection test passed.")
	if legacy, err := oo.LegacyFilterBehaviour(ctx, raw); err == nil && legacy {
		fmt.Fprintf(c.out, "Warning: %s\n", oo.VersionWarning)
	} else if errors.Is(err, oo.ErrNoProbeData) {
		fmt.Fprintln(c.out, "OpenObserve has no Kubernetes metrics from the last 15 minutes yet.")
	}

	ctxName, client, err := c.kube(strings.TrimSpace(*contextName))
	if err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: %v\n", err)
		return 1
	}
	uid, err := openobserve.ReadClusterUID(ctx, client)
	if err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: context %q: %v\n", ctxName, err)
		return 1
	}
	return c.matchContext(ctx, raw, &entry, ctxName, uid, client, *yes)
}

func (c openobserveCmd) matchContext(ctx context.Context, raw prom.Transport, entry *config.OpenObserveConfig, ctxName, uid string, client kubernetes.Interface, yes bool) int {
	ids, err := oo.DiscoverIDs(ctx, raw, []string{uid})
	if err != nil {
		fmt.Fprintf(c.errOut, "Looking for this cluster in OpenObserve failed: %s\n", prom.RedactURLs(err.Error()))
		return 1
	}
	if m, ok := ids[uid]; ok {
		if missing, err := oo.DetectFamilies(ctx, raw, m.Options); err == nil {
			m.Missing = missing
		}
		if stored, ok := entry.Matches[uid]; !ok || stored.By != config.OpenObserveMatchByName {
			setMatch(entry, uid, config.OpenObserveMatch{Label: m.Label, Value: m.Value, By: config.OpenObserveMatchByID})
			if err := saveOpenObserve(entry); err != nil {
				fmt.Fprintf(c.errOut, "openobserve connect: save: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(c.out, "Context %q is matched by cluster ID (%s=%s).\n", ctxName, m.Label, m.Value)
		c.printMatchDetails(m)
		return 0
	}

	sample, err := openobserve.BuildPodSample(ctx, client)
	if err != nil {
		fmt.Fprintf(c.errOut, "Context %q has no cluster ID label in OpenObserve, and Radar couldn't sample its Pods: %v\n", ctxName, err)
		c.printIDLines(uid)
		return 1
	}
	sug, err := oo.SuggestName(ctx, raw, sample, entry.ClusterLabel)
	if err != nil {
		fmt.Fprintf(c.errOut, "Looking for this cluster's Pods in OpenObserve failed: %s\n", prom.RedactURLs(err.Error()))
		return 1
	}
	fmt.Fprintf(c.out, "Context %q has no cluster ID label in OpenObserve. %s\n", ctxName, sug.Reason)
	if sug.Value == "" {
		c.printIDLines(uid)
		return 1
	}
	if sug.UnsafeDefault || sug.Value == oo.UnsafeDefaultName {
		fmt.Fprintf(c.out, "Radar won't use %s=%q: it is a default name other clusters share.\n", sug.Label, sug.Value)
		c.printIDLines(uid)
		return 1
	}
	if sug.Conflict {
		fmt.Fprintf(c.out, "Radar won't use %s=%q: it doesn't identify this cluster alone.\n", sug.Label, sug.Value)
		c.printIDLines(uid)
		return 1
	}
	if !yes {
		fmt.Fprintf(c.out, "Use %s=%s for context %s? [y/N] ", sug.Label, sug.Value, ctxName)
		answer, _ := bufio.NewReader(c.in).ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(c.out, "Not matched. The connection is saved; this context keeps using its own Prometheus.")
			return 1
		}
	}
	m, err := oo.ConfirmedNameMatch(ctx, raw, sug.Label, sug.Value, uid)
	if err != nil {
		fmt.Fprintf(c.errOut, "Couldn't use %s=%q: %s\n", sug.Label, sug.Value, prom.RedactURLs(err.Error()))
		return 1
	}
	setMatch(entry, uid, config.OpenObserveMatch{Label: m.Label, Value: m.Value, By: config.OpenObserveMatchByName, ConfirmedAt: time.Now().UTC().Format(time.RFC3339)})
	if err := saveOpenObserve(entry); err != nil {
		fmt.Fprintf(c.errOut, "openobserve connect: save: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out, "Context %q is matched by confirmed name (%s=%s).\n", ctxName, m.Label, m.Value)
	c.printMatchDetails(m)
	c.printIDLines(uid)
	return 0
}

func (c openobserveCmd) printMatchDetails(m oo.ClusterMatch) {
	if m.Dialect == oo.DialectCollector {
		fmt.Fprintln(c.out, "Data comes from the OpenObserve collector.")
	}
	if len(m.Missing) > 0 {
		names := make([]string, len(m.Missing))
		for i, f := range m.Missing {
			names[i] = string(f)
		}
		fmt.Fprintf(c.out, "No data for: %s\n", strings.Join(names, ", "))
	}
	if m.Warning != "" {
		fmt.Fprintf(c.out, "Warning: %s\n", m.Warning)
	}
	fmt.Fprintln(c.out, "Radar uses this match the next time it starts or switches to this context.")
}

func (c openobserveCmd) printIDLines(uid string) {
	fmt.Fprintf(c.out, `To match this cluster by ID, add its kube-system namespace UID to its metrics:
  Prometheus remote write (prometheus.yml, global section):
    external_labels: { radar_cluster_uid: "%s" }
  kube-prometheus-stack: prometheus.prometheusSpec.externalLabels.radar_cluster_uid: "%s"
  OpenObserve collector: give the cluster a unique name instead of "cluster1"
    (the k8s.cluster.name resource attribute); its agent already sends the ID.
`, uid, uid)
}

func (c openobserveCmd) status(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(c.errOut, openobserveUsage) }
	contextName := fs.String("context", "", "")
	if code, ok := c.parse(fs, args); !ok {
		return code
	}
	entry := config.Load().OpenObserve
	if entry == nil {
		fmt.Fprintln(c.out, "No OpenObserve connection. Add one with: radar openobserve connect --url URL --user USER --token TOKEN")
		return 0
	}
	fmt.Fprintf(c.out, "OpenObserve: %s\nOrganization: %s\nUser: %s\n", entry.BaseURL, entry.Org, entry.User)
	switch {
	case entry.TokenEnv != "":
		fmt.Fprintf(c.out, "Token: from $%s\n", entry.TokenEnv)
	case entry.Token != "":
		fmt.Fprintln(c.out, "Token: saved")
	}
	if entry.CACertFile != "" {
		fmt.Fprintf(c.out, "CA certificate: %s\n", entry.CACertFile)
	}
	if entry.ClusterLabel != "" {
		fmt.Fprintf(c.out, "Cluster label: %s\n", entry.ClusterLabel)
	}
	conn, err := openobserve.Connection(*entry)
	if err != nil {
		fmt.Fprintf(c.out, "Connection unusable: %v\n", err)
		return 1
	}
	ctxName, client, err := c.kube(strings.TrimSpace(*contextName))
	if err != nil {
		fmt.Fprintf(c.out, "Context: unknown (%v)\n", err)
		return 1
	}
	ctx, cancel := signalContext()
	defer cancel()
	uid, err := openobserve.ReadClusterUID(ctx, client)
	if err != nil {
		fmt.Fprintf(c.out, "Context %q: couldn't read the cluster ID: %v\n", ctxName, err)
		return 1
	}
	raw, err := conn.NewHTTPTransport(openobserveRequestTimeout)
	if err != nil {
		fmt.Fprintf(c.out, "Connection unusable: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out, "Context %q (cluster ID %s): ", ctxName, uid)
	ids, err := oo.DiscoverIDs(ctx, raw, []string{uid})
	if err != nil {
		fmt.Fprintf(c.out, "OpenObserve unavailable: %s\n", prom.RedactURLs(err.Error()))
		return 1
	}
	m, ok := ids[uid]
	stored, hasStored := entry.Matches[uid]
	switch {
	case ok:
		fmt.Fprintf(c.out, "matched by cluster ID (%s=%s)\n", m.Label, m.Value)
	case hasStored && stored.By == config.OpenObserveMatchByName:
		m, err = oo.ConfirmedNameMatch(ctx, raw, stored.Label, stored.Value, uid)
		if err != nil {
			fmt.Fprintf(c.out, "confirmed name %s=%q no longer matches: %s\n", stored.Label, stored.Value, prom.RedactURLs(err.Error()))
			return 1
		}
		fmt.Fprintf(c.out, "matched by confirmed name (%s=%s)\n", m.Label, m.Value)
	default:
		fmt.Fprintln(c.out, "not matched")
		fmt.Fprintln(c.out, openobserve.UnmatchedFix(ctxName))
		return 0
	}
	if ok {
		if missing, err := oo.DetectFamilies(ctx, raw, m.Options); err == nil {
			m.Missing = missing
		}
	}
	if m.Dialect == oo.DialectCollector {
		fmt.Fprintln(c.out, "Data comes from the OpenObserve collector.")
	}
	if len(m.Missing) > 0 {
		names := make([]string, len(m.Missing))
		for i, f := range m.Missing {
			names[i] = string(f)
		}
		fmt.Fprintf(c.out, "No data for: %s\n", strings.Join(names, ", "))
	} else {
		fmt.Fprintln(c.out, "Every metric family has data.")
	}
	if m.Warning != "" {
		fmt.Fprintf(c.out, "Warning: %s\n", m.Warning)
	}
	if legacy, err := oo.LegacyFilterBehaviour(ctx, raw); err == nil && legacy {
		fmt.Fprintf(c.out, "Warning: %s\n", oo.VersionWarning)
	}
	return 0
}

func (c openobserveCmd) disconnect(args []string) int {
	fs := flag.NewFlagSet("disconnect", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(c.errOut, openobserveUsage) }
	if code, ok := c.parse(fs, args); !ok {
		return code
	}
	removed := false
	if _, err := config.Update(func(cfg *config.Config) {
		removed = cfg.OpenObserve != nil
		cfg.OpenObserve = nil
	}); err != nil {
		fmt.Fprintf(c.errOut, "openobserve disconnect: %v\n", err)
		return 1
	}
	if removed {
		fmt.Fprintln(c.out, "Removed the OpenObserve connection. Radar uses each context's Prometheus the next time it starts or switches context.")
	} else {
		fmt.Fprintln(c.out, "No OpenObserve connection was saved.")
	}
	return 0
}

func setMatch(entry *config.OpenObserveConfig, uid string, m config.OpenObserveMatch) {
	if entry.Matches == nil {
		entry.Matches = map[string]config.OpenObserveMatch{}
	}
	entry.Matches[uid] = m
}

func saveOpenObserve(entry *config.OpenObserveConfig) error {
	cp := entry.Clone()
	_, err := config.Update(func(cfg *config.Config) { cfg.OpenObserve = &cp })
	return err
}
