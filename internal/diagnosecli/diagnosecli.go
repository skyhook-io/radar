// Package diagnosecli implements `radar diagnose` — a terminal client for the
// AI-diagnosis engine of a RUNNING radar instance. It is deliberately a thin
// client over the same REST+SSE contract the web panel uses: the run it starts
// is the same durable server-side job, so it can be watched or continued from
// the UI (and vice versa).
package diagnosecli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/config"
)

// kindAliases maps kubectl-style short/plural names to the canonical Kind.
var kindAliases = map[string]string{
	"po": "Pod", "pod": "Pod", "pods": "Pod",
	"deploy": "Deployment", "deployment": "Deployment", "deployments": "Deployment",
	"sts": "StatefulSet", "statefulset": "StatefulSet", "statefulsets": "StatefulSet",
	"ds": "DaemonSet", "daemonset": "DaemonSet", "daemonsets": "DaemonSet",
	"rs": "ReplicaSet", "replicaset": "ReplicaSet", "replicasets": "ReplicaSet",
	"svc": "Service", "service": "Service", "services": "Service",
	"ing": "Ingress", "ingress": "Ingress", "ingresses": "Ingress",
	"no": "Node", "node": "Node", "nodes": "Node",
	"job": "Job", "jobs": "Job",
	"cj": "CronJob", "cronjob": "CronJob", "cronjobs": "CronJob",
	"cm": "ConfigMap", "configmap": "ConfigMap", "configmaps": "ConfigMap",
	"secret": "Secret", "secrets": "Secret",
	"ns": "Namespace", "namespace": "Namespace", "namespaces": "Namespace",
	"pvc": "PersistentVolumeClaim", "persistentvolumeclaim": "PersistentVolumeClaim",
	"pv": "PersistentVolume", "persistentvolume": "PersistentVolume",
	"hpa": "HorizontalPodAutoscaler", "horizontalpodautoscaler": "HorizontalPodAutoscaler",
}

// normalizeKind resolves kubectl-style aliases; anything unknown is passed
// through title-cased — the server and the agent's own tools resolve kinds
// loosely, so this only needs to be friendly, not exhaustive.
func normalizeKind(k string) string {
	if canonical, ok := kindAliases[strings.ToLower(k)]; ok {
		return canonical
	}
	if k == "" {
		return k
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

type options struct {
	namespace  string
	agent      string
	server     string
	kubeconfig string
	standalone bool
	jsonOut    bool
	open       bool
	yes        bool
}

func newFlagSet() (*flag.FlagSet, *options) {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	o := &options{}
	fs.StringVar(&o.namespace, "n", "", "Namespace of the resource")
	fs.StringVar(&o.namespace, "namespace", "", "Namespace of the resource")
	fs.StringVar(&o.agent, "agent", "", "Agent backend to use (claude|codex|cursor-agent; default = server's pick)")
	fs.StringVar(&o.server, "server", "", "Radar server URL (default: discover the running instance via ~/.radar/mcp-port)")
	fs.BoolVar(&o.jsonOut, "json", false, "Print the final verdict as JSON on stdout (progress goes to stderr)")
	fs.BoolVar(&o.open, "open", false, "Also open the investigation in the Radar UI")
	fs.BoolVar(&o.yes, "yes", false, "Skip the first-run consent prompt")
	fs.BoolVar(&o.standalone, "standalone", false, "Run against a temporary in-process Radar instead of a running instance (slower: connects to the cluster first)")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "Kubeconfig for --standalone (default: ~/.kube/config)")
	return fs, o
}

// Run executes `radar diagnose <kind>/<name>` and returns the process exit code.
func Run(args []string, openBrowser func(url string)) int {
	fs, o := newFlagSet()
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: radar diagnose <kind>/<name> [-n namespace] [flags]

Runs an AI investigation of a Kubernetes resource using your own local agent
CLI (Claude Code, Codex, or Cursor) against the running Radar instance — no
API key, no cloud. The investigation is a durable Radar job: watch it here,
in the Radar UI, or continue it in your own agent afterwards.

Examples:
  radar diagnose pod/checkout-6f4d -n prod
  radar diagnose deploy/api --json > verdict.json
  radar diagnose node/ip-10-0-3-36 --open

Flags:
`)
		fs.PrintDefaults()
	}
	// Interleaved parse: Go's flag package stops at the first positional, but
	// kubectl users write `radar diagnose pod/web -n prod` — collect positionals
	// and keep parsing the remainder so flags may appear on either side.
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positionals) != 1 && len(positionals) != 2 {
		fs.Usage()
		return 2
	}
	var kind, name string
	if len(positionals) == 2 {
		kind, name = positionals[0], positionals[1]
	} else {
		var ok bool
		kind, name, ok = strings.Cut(positionals[0], "/")
		if !ok {
			fmt.Fprintf(os.Stderr, "target must be <kind>/<name> (e.g. pod/web), got %q\n", positionals[0])
			return 2
		}
	}
	kind = normalizeKind(kind)

	out := newRenderer(o.jsonOut)

	// Resolve the engine: an explicit --server, else the running instance from
	// ~/.radar/mcp-port, else fall back to a temporary in-process Radar (what
	// --standalone forces). Cold start pays a cluster connect up front — the
	// running-instance path stays the fast default.
	standalone := o.standalone
	var base string
	if !standalone {
		var err error
		base, err = resolveServer(o.server)
		if err != nil || !probeListening(base) {
			if o.server != "" {
				// An explicit --server that isn't answering is an error, not a
				// cue to boot something else.
				fmt.Fprintf(os.Stderr, "nothing is listening at %s\n", o.server)
				return 1
			}
			fmt.Fprintln(os.Stderr, "no running Radar found — starting a temporary one for this investigation (use --server to target a running instance)")
			standalone = true
		}
	}

	if standalone {
		// Consent BEFORE the boot: nobody wants to answer a prompt after
		// watching a cluster connect for 30 seconds. No server exists yet, so
		// read/write the shared machine-scoped store (~/.radar/config.json)
		// directly — the ephemeral server then sees it as already given.
		effective := ai.EffectiveAgent(o.agent, ai.DetectAgents(context.Background(), false))
		surface := ai.ConsentSurfaceFor(effective)
		if !consentGivenLocal(surface) {
			if o.yes {
				if err := recordConsentLocal(surface); err != nil {
					fmt.Fprintf(os.Stderr, "couldn't record consent: %v\n", err)
					return 1
				}
			} else if !promptConsent(consentLabel(effective), surface, recordConsentLocal) {
				fmt.Fprintln(os.Stderr, "aborted")
				return 1
			}
		}
		b, shutdown, err := bootEphemeral(o.kubeconfig)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer shutdown()
		base = b
	}

	agents, err := fetchAgents(base)
	if err != nil {
		// A 404 here means SOMETHING answered but has no /api/agents — almost
		// always an older Radar (or a stale ~/.radar/mcp-port pointing at one
		// when several instances ran). Say that, not just "404".
		if strings.Contains(err.Error(), "404") {
			fmt.Fprintf(os.Stderr, "the Radar at %s doesn't support AI diagnosis — it's likely an older version (or a stale ~/.radar/mcp-port from another instance). Upgrade/restart it, pass --server for the right instance, or use --standalone.\n", base)
		} else {
			fmt.Fprintf(os.Stderr, "found Radar at %s but couldn't query it: %v\n", base, err)
		}
		return 1
	}
	if !agents.Enabled {
		fmt.Fprintln(os.Stderr, "AI diagnosis is disabled on this Radar instance — install Claude Code, Codex, or Cursor and restart radar.")
		return 1
	}

	effective := ai.EffectiveAgent(o.agent, agents.Agents)
	surface := ai.ConsentSurfaceFor(effective)
	if !agents.Consented[surface] {
		if o.yes {
			// --yes acknowledges the disclosure; the server enforces consent at
			// start, so it must be recorded, not just skipped.
			if err := recordConsentHTTP(base, surface); err != nil {
				fmt.Fprintf(os.Stderr, "couldn't record consent: %v\n", err)
				return 1
			}
		} else if !promptConsent(consentLabel(effective), surface, func(sf string) error { return recordConsentHTTP(base, sf) }) {
			fmt.Fprintln(os.Stderr, "aborted")
			return 1
		}
	}

	run, err := startRun(base, kind, o.namespace, name, o.agent)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out.header(run, base)
	if o.open {
		openBrowser(base + "/?ai-run=" + run.ID)
	}

	diag, ok := streamRun(base, run.ID, out)
	if !ok {
		return 1
	}
	if o.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"run": run.ID, "kind": run.Kind, "namespace": run.Namespace, "name": run.Name,
			"agent": run.Agent, "diagnosis": diag,
		})
	}
	return 0
}

// --- server discovery -------------------------------------------------------

func resolveServer(explicit string) (string, error) {
	if explicit != "" {
		return strings.TrimRight(explicit, "/"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".radar", "mcp-port"))
	if err != nil {
		return "", fmt.Errorf("no running Radar found (%s missing) — start radar first, or pass --server http://localhost:<port>",
			filepath.Join(home, ".radar", "mcp-port"))
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || port <= 0 {
		return "", fmt.Errorf("~/.radar/mcp-port is unreadable — is radar running? (or pass --server)")
	}
	return fmt.Sprintf("http://localhost:%d", port), nil
}

type agentsResponse struct {
	Enabled   bool            `json:"enabled"`
	Consented map[string]bool `json:"consented"`
	Agents    []ai.AgentInfo  `json:"agents"`
}

func fetchAgents(base string) (agentsResponse, error) {
	var out agentsResponse
	err := getJSON(base+"/api/agents", &out)
	return out, err
}

// --- consent ----------------------------------------------------------------

// The standalone path reads/writes the shared store directly, pre-boot —
// versions live in internal/config (one source of truth with the server).
func consentGivenLocal(surface string) bool {
	return config.AIConsentGiven(surface)
}

func recordConsentLocal(surface string) error {
	return config.RecordAIConsent(surface)
}

// recordConsentHTTP persists consent via the running instance (same store).
// Failures must surface: the server enforces consent at start, so a swallowed
// write here turns into a baffling 403 one request later.
func recordConsentHTTP(base, surface string) error {
	body, _ := json.Marshal(map[string]string{"surface": surface})
	resp, err := http.Post(base+"/api/diagnose/consent", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", apiError(resp))
	}
	return nil
}

// consentLabel names the agent in the disclosure. effective is "" only when no
// supported CLI is detected — the run fails later with its own clearer error.
func consentLabel(effective string) string {
	if effective == "" {
		return "your agent CLI"
	}
	return ai.AgentLabel(effective)
}

// promptConsent mirrors the UI's one-time consent card. Interactive terminals
// get a real y/N gate; non-interactive callers (CI) get the disclosure on
// stderr and proceed — an explicit `radar diagnose` invocation in a script is
// already an informed act, and a blocking prompt there would just break CI.
// record persists the acknowledgment to the shared machine-scoped store.
func promptConsent(agentLabel, surface string, record func(surface string) error) bool {
	notice := fmt.Sprintf(`This runs your own %s on your machine — no Radar cloud, no API key.
Radar sends the resource's spec, recent events, and pod logs to it (and on to
its model provider under your account). Through Radar the agent can only READ
your cluster. Transcripts are kept in your local Radar history until cleared.
`, agentLabel)
	if surface == "cursor" {
		notice += `Note: Cursor also loads your own global MCP servers (Radar can't exclude
them) — if any of those can make changes, Cursor could use them.
`
	}
	fmt.Fprint(os.Stderr, notice)
	// A real ioctl-backed check — os.ModeCharDevice would misread /dev/null
	// (and daemon-inherited stdin) as an interactive terminal.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Non-interactive callers proceed after the disclosure (an explicit
		// invocation in a script is an informed act) — and must RECORD it,
		// because the server enforces consent at start.
		return recordOrReport(record, surface)
	}
	fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
		return false
	}
	return recordOrReport(record, surface)
}

func recordOrReport(record func(string) error, surface string) bool {
	if err := record(surface); err != nil {
		fmt.Fprintf(os.Stderr, "couldn't record consent: %v\n", err)
		return false
	}
	return true
}

// --- run start + stream ------------------------------------------------------

type runSummary struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	SessionID string `json:"sessionId"`
	ManagedBy string `json:"managedBy"`
	Health    *struct {
		IssueCount int `json:"issueCount"`
		AuditCount int `json:"auditCount"`
		Issues     []struct {
			Severity string `json:"severity"`
			Reason   string `json:"reason"`
			Message  string `json:"message"`
		} `json:"issues"`
		AuditFindings []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"auditFindings"`
	} `json:"health"`
}

func startRun(base, kind, namespace, name, agent string) (runSummary, error) {
	body, _ := json.Marshal(map[string]any{
		"kind": kind, "namespace": namespace, "name": name, "agent": agent,
	})
	resp, err := http.Post(base+"/api/diagnose/runs", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return runSummary{}, fmt.Errorf("couldn't start the investigation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runSummary{}, fmt.Errorf("couldn't start the investigation: %s", apiError(resp))
	}
	var run runSummary
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return runSummary{}, fmt.Errorf("unexpected response: %w", err)
	}
	return run, nil
}

type streamEvent struct {
	Type  string          `json:"type"`
	Token string          `json:"token"`
	Error string          `json:"error"`
	Step  *stepInfo       `json:"step"`
	Diag  json.RawMessage `json:"diagnosis"`
}

type stepInfo struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	Ms      *int64 `json:"ms"`
	Summary string `json:"summary"`
}

// diagnosis mirrors the verdict fields the terminal renders.
type diagnosis struct {
	Healthy           bool     `json:"healthy"`
	Inconclusive      bool     `json:"inconclusive"`
	RootCause         string   `json:"rootCause"`
	Report            string   `json:"report"`
	Remediation       []string `json:"remediation"`
	RecommendedIndex  *int     `json:"recommendedIndex"`
	RecommendedReason string   `json:"recommendedReason"`
	Confidence        *float64 `json:"confidence"`
	SessionID         string   `json:"sessionId"`
}

// streamRun consumes the run's SSE stream until the FIRST turn terminates.
// Returns the raw diagnosis JSON (for --json) and whether the turn succeeded.
func streamRun(base, id string, out *renderer) (json.RawMessage, bool) {
	resp, err := http.Get(base + "/api/diagnose/runs/" + id + "/stream")
	if err != nil {
		fmt.Fprintf(os.Stderr, "stream failed: %v\n", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "stream failed: %s\n", apiError(resp))
		return nil, false
	}

	out.startSpinner()
	defer out.stopSpinner()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	steps := map[string]stepInfo{}
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev streamEvent
		if json.Unmarshal([]byte(line[len("data: "):]), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thinking":
			out.thinking(ev.Token)
		case "step":
			if ev.Step == nil {
				continue
			}
			// The done event omits tool/summary; merge with the running one.
			cur := steps[ev.Step.ID]
			if ev.Step.Tool != "" {
				cur.Tool = ev.Step.Tool
			}
			if ev.Step.Summary != "" {
				cur.Summary = ev.Step.Summary
			}
			cur.Ms = ev.Step.Ms
			cur.Status = ev.Step.Status
			steps[ev.Step.ID] = cur
			if ev.Step.Status == "running" {
				out.toolStarted(cur.Tool)
			} else if ev.Step.Status == "done" {
				out.step(cur)
			}
		case "done":
			var d diagnosis
			_ = json.Unmarshal(ev.Diag, &d)
			out.verdict(d)
			return ev.Diag, true
		case "error":
			out.errorLine(ev.Error)
			return nil, false
		case "closed":
			out.errorLine("The investigation is no longer available.")
			return nil, false
		}
	}
	fmt.Fprintln(os.Stderr, "stream ended unexpectedly — the run keeps going; watch it in the Radar UI")
	return nil, false
}

// --- helpers ------------------------------------------------------------------

func getJSON(url string, into any) error {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", apiError(resp))
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func apiError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return resp.Status
}
