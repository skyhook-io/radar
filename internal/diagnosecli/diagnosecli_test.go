package diagnosecli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/ai"
)

func TestCursorConsentNoticeDisclosesAutoApprovedTools(t *testing.T) {
	notice := strings.Join(strings.Fields(consentNotice("cursor-agent", ai.ExecutionProfileFullLocal)), " ")
	for _, want := range []string{
		"passes Cursor --force",
		"auto-approves Cursor's built-in tools",
		"every MCP server it loads",
		"including your global servers",
		"does not reliably confine those tools",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("Cursor consent notice missing %q:\n%s", want, notice)
		}
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := map[string]string{
		"pod": "Pod", "pods": "Pod", "po": "Pod",
		"deploy": "Deployment", "deployments": "Deployment",
		"sts": "StatefulSet", "svc": "Service", "ns": "Namespace",
		"Pod": "Pod", "CronJob": "CronJob",
		// Unknown kinds pass through title-cased (CRDs, etc.).
		"kafkacluster": "Kafkacluster", "HelmRelease": "HelmRelease",
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTargetGroup(t *testing.T) {
	cases := []struct {
		name, kind, explicit, want string
	}{
		{name: "built-in apps kind", kind: "Deployment", want: "apps"},
		{name: "core kind", kind: "Service", want: ""},
		{name: "custom kind requires explicit group", kind: "Rollout", want: ""},
		{name: "explicit custom group", kind: "Rollout", explicit: " argoproj.io ", want: "argoproj.io"},
		{name: "explicit group is canonicalized", kind: "Deployment", explicit: " Apps ", want: "apps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := targetGroup(tc.kind, tc.explicit); got != tc.want {
				t.Fatalf("targetGroup(%q, %q) = %q, want %q", tc.kind, tc.explicit, got, tc.want)
			}
		})
	}
}

func TestStartRunSendsExactAPIGroup(t *testing.T) {
	var request struct {
		Kind      string `json:"kind"`
		Group     string `json:"group"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnose/runs" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run-1","kind":"Rollout","group":"argoproj.io","namespace":"prod","name":"checkout","agent":"codex"}`))
	}))
	defer server.Close()

	run, err := startRun(server.URL, "Rollout", "argoproj.io", "prod", "checkout", "codex", ai.ExecutionProfileSafeguarded)
	if err != nil {
		t.Fatalf("startRun: %v", err)
	}
	if request.Kind != "Rollout" || request.Group != "argoproj.io" || request.Namespace != "prod" || request.Name != "checkout" {
		t.Fatalf("request target = %#v", request)
	}
	if run.Group != "argoproj.io" {
		t.Fatalf("run group = %q, want argoproj.io", run.Group)
	}
}

func TestRendererConclusionShapes(t *testing.T) {
	// Smoke: every conclusion shape renders without panicking and mentions its
	// anchor word (plain-text path, no TTY).
	r := &renderer{w: nil, color: false}
	_ = r
	conf := 0.9
	rec := 1
	shapes := []struct {
		d    diagnosis
		want string
	}{
		{diagnosis{Healthy: true, Report: "All good."}, "No problems found"},
		{diagnosis{Inconclusive: true, Report: "RBAC blocked reads."}, "Couldn't determine"},
		{diagnosis{RootCause: "bad `image` tag", Remediation: []string{"fix the **tag**"},
			RecommendedIndex: &rec, RecommendedReason: "targeted", Confidence: &conf}, "Root cause"},
		{diagnosis{Report: "narration only"}, "narration only"},
	}
	for _, c := range shapes {
		out := captureConclusion(t, c.d)
		if !strings.Contains(out, c.want) {
			t.Errorf("conclusion output missing %q:\n%s", c.want, out)
		}
	}
}

func TestRendererConclusionRepeatsWatchURL(t *testing.T) {
	tmp, err := createTempFile(t)
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{w: tmp, color: false}
	r.header(runSummary{ID: "run-123", Kind: "Pod", Name: "checkout", Agent: "codex"}, "http://localhost:9280")
	r.conclusion(diagnosis{Healthy: true})
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, _ := tmp.Read(buf)
	got := string(buf[:n])
	if url := "http://localhost:9280/?ai-run=run-123"; strings.Count(got, url) != 2 {
		t.Fatalf("watch URL should appear in the header and final footer:\n%s", got)
	}
}

func TestRendererHeaderQualifiesKindWithAPIGroup(t *testing.T) {
	tmp, err := createTempFile(t)
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{w: tmp, color: false}
	r.header(runSummary{ID: "run-123", Kind: "Rollout", Group: "argoproj.io", Namespace: "prod", Name: "checkout", Agent: "codex"}, "http://localhost:9280")
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, _ := tmp.Read(buf)
	if got := string(buf[:n]); !strings.Contains(got, "Rollout.argoproj.io prod/checkout") {
		t.Fatalf("header did not show group-qualified target:\n%s", got)
	}
}

func captureConclusion(t *testing.T, d diagnosis) string {
	t.Helper()
	tmp, err := createTempFile(t)
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{w: tmp, color: false}
	r.conclusion(d)
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, _ := tmp.Read(buf)
	return string(buf[:n])
}

func createTempFile(t *testing.T) (*os.File, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "render")
	if err == nil {
		t.Cleanup(func() { _ = f.Close() })
	}
	return f, err
}

// TestInterleavedFlagParsing pins kubectl-style invocation: flags may follow
// the positional target (`radar diagnose pod/web -n prod --json`).
func TestInterleavedFlagParsing(t *testing.T) {
	fs, o := newFlagSet()
	var positionals []string
	rest := []string{"rollout/web", "-n", "prod", "--group", "argoproj.io", "--json"}
	for {
		if err := fs.Parse(rest); err != nil {
			t.Fatal(err)
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positionals) != 1 || positionals[0] != "rollout/web" {
		t.Fatalf("positionals = %v", positionals)
	}
	if o.namespace != "prod" || o.group != "argoproj.io" || !o.jsonOut {
		t.Fatalf("flags not parsed: ns=%q group=%q json=%v", o.namespace, o.group, o.jsonOut)
	}
}

func TestExecutionProfile(t *testing.T) {
	cases := []struct {
		agent, requested string
		want             string
		wantErr          bool
	}{
		{"codex", "", "safeguarded", false},
		{"codex", "full-local", "full-local", false},
		{"claude", "full-local", "full-local", false},
		{"cursor-agent", "", "full-local", false},
		{"", "", "", true},
	}
	for _, tc := range cases {
		got, err := executionProfile(tc.agent, tc.requested)
		if tc.wantErr {
			if err == nil {
				t.Errorf("executionProfile(%q, %q) succeeded, want error", tc.agent, tc.requested)
			}
			continue
		}
		if err != nil || string(got) != tc.want {
			t.Errorf("executionProfile(%q, %q) = %q, %v; want %q", tc.agent, tc.requested, got, err, tc.want)
		}
	}
}

func TestStandaloneEffectiveAgentHonorsCLIOverride(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cursor-agent-custom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_AI_CLI_BIN", bin)

	if got := standaloneEffectiveAgent(context.Background(), ""); got != "cursor-agent" {
		t.Fatalf("standaloneEffectiveAgent() = %q, want cursor-agent", got)
	}
	if got := standaloneEffectiveAgent(context.Background(), "codex"); got != "cursor-agent" {
		t.Fatalf("unsupported explicit pick should fall back to the override, got %q", got)
	}
}

func TestResolveServerReadsBasePathFromDiscoveryFile(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "port only (older instance)", contents: "9280\n", want: "http://localhost:9280"},
		{name: "port and base path", contents: "9280\n/radar\n", want: "http://localhost:9280/radar"},
		{name: "nested base path", contents: "9280\n/tools/radar\n", want: "http://localhost:9280/tools/radar"},
		{name: "trailing slash trimmed", contents: "9280\n/radar/\n", want: "http://localhost:9280/radar"},
		{name: "no trailing newline", contents: "9280\n/radar", want: "http://localhost:9280/radar"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.MkdirAll(filepath.Join(home, ".radar"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(home, ".radar", "mcp-port"), []byte(tc.contents), 0o644); err != nil {
				t.Fatalf("write port file: %v", err)
			}
			got, err := resolveServer("")
			if err != nil {
				t.Fatalf("resolveServer: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveServer() = %q, want %q", got, tc.want)
			}
		})
	}
}

// probeListening receives the discovered base URL, which carries the server's
// --base-path when it has one. A path suffix is not a dialable address, so
// leaving it in makes a healthy prefixed instance look dead and sends the CLI
// off to spawn a throwaway standalone server instead of attaching.
func TestProbeListeningIgnoresBasePathSuffix(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	for _, base := range []string{
		"http://" + addr,
		"http://" + addr + "/radar",
		"http://" + addr + "/tools/radar",
		addr + "/radar",
	} {
		if !probeListening(base) {
			t.Errorf("probeListening(%q) = false, want true", base)
		}
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := closed.Addr().String()
	closed.Close()
	if probeListening("http://" + deadAddr + "/radar") {
		t.Error("probeListening on a closed port = true, want false")
	}
}
