package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/pkg/investigation"
)

func TestResolveAgentOpencode(t *testing.T) {
	for _, bin := range []string{"opencode", "opencode.exe", "opencode.cmd", "OpenCode.EXE", "opencode-capture"} {
		path := filepath.Join("tools", bin)
		agent := resolveAgent(path)
		if agent.Name() != "opencode" || agent.Path() != path {
			t.Errorf("resolveAgent(%q) = %s at %s", path, agent.Name(), agent.Path())
		}
	}
}

func TestOpencodeConfigAndCommand(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENCODE_CONFIG", "/test/opencode.json")
	a := &opencodeAgent{bin: "opencode"}
	dir := t.TempDir()
	const url = "http://localhost:9280/mcp-readonly"
	const prompt = "Investigate this resource.\nReturn JSON: {\"healthy\":true}"

	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: prompt, systemPrompt: "System instructions", workdir: dir, model: "amazon-bedrock/us.anthropic.claude-sonnet-4-6", profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, entry := range []string{"OPENAI_API_KEY=test-key", "OPENCODE_CONFIG=/test/opencode.json"} {
		if !slices.Contains(cmd.Environ(), entry) {
			t.Errorf("full-local command lost %s", entry)
		}
	}

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--format json") || !strings.Contains(args, "--auto") {
		t.Errorf("expected --format json and --auto in args; got %q", args)
	}
	if !strings.Contains(args, "--model amazon-bedrock/us.anthropic.claude-sonnet-4-6") {
		t.Errorf("expected --model flag in args; got %q", args)
	}
	stdin, err := io.ReadAll(cmd.Stdin)
	if err != nil || string(stdin) != "System instructions\n\n"+prompt || strings.Contains(args, prompt) {
		t.Fatalf("prompt must reach stdin unchanged, got %q, err=%v", stdin, err)
	}

	cfgPath := filepath.Join(dir, "opencode.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected opencode.json at %s: %v", cfgPath, err)
	}

	var cfg struct {
		ToolOutput struct {
			MaxBytes int `json:"max_bytes"`
			MaxLines int `json:"max_lines"`
		} `json:"tool_output"`
		MCP map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("opencode.json not valid JSON: %v", err)
	}
	srv, ok := cfg.MCP["radar"]
	if !ok {
		t.Fatalf("opencode.json missing radar server entry: %s", string(b))
	}
	if srv.Type != "remote" || srv.URL != url {
		t.Errorf("opencode.json radar entry = %+v, want type=remote, url=%s", srv, url)
	}
	marker := investigation.RefMarker("ev_"+strings.Repeat("a", 128)+"_"+strings.Repeat("b", 128)) + "\n\n"
	for _, payload := range []string{strings.Repeat("\U0010ffff", maxToolPayload), strings.Repeat("\n", maxToolPayload)} {
		output := marker + payload
		if len(output) > cfg.ToolOutput.MaxBytes || strings.Count(output, "\n")+1 > cfg.ToolOutput.MaxLines {
			t.Fatal("OpenCode would truncate a result within Radar's retention limit")
		}
	}
	cmd, cleanup, err = a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "follow up", workdir: dir, sessionID: "ses_existing", profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if i := slices.Index(cmd.Args, "--session"); i < 0 || cmd.Args[i+1] != "ses_existing" {
		t.Fatalf("follow-up lost its session: %v", cmd.Args)
	}
	if _, _, err := a.command(context.Background(), turnSpec{profile: ExecutionProfileSafeguarded}); err == nil {
		t.Fatal("OpenCode must not claim support for safeguarded execution")
	}
}

func TestOpencodeLiveDiagnosisAndFollowup(t *testing.T) {
	var session string
	for _, name := range []string{"diagnosis", "followup"} {
		stream, err := os.ReadFile("testdata/opencode/" + name + ".jsonl")
		if err != nil {
			t.Fatal(err)
		}
		var steps []*StepInfo
		d := (&opencodeAgent{}).parseStream(strings.NewReader(string(stream)), func(ev StreamEvent) {
			if ev.Step != nil {
				steps = append(steps, ev.Step)
			}
		})
		if d.SessionID == "" || (session != "" && d.SessionID != session) {
			t.Fatalf("%s did not retain the original session: %q", name, d.SessionID)
		}
		session = d.SessionID
		if d.cliErrored || d.CostUSD == nil || *d.CostUSD <= 0 || d.Turns == 0 {
			t.Fatalf("%s lost completion/usage: %+v", name, d)
		}
		want := 1
		if name == "diagnosis" {
			want = 2
			investigation.Bind(&d.Verdict, d.citations, func(string) bool { return true })
			if !strings.Contains(d.RootCause, "GREETING") || len(d.Evidence) != 4 {
				t.Fatalf("lost structured diagnosis: %+v", d)
			}
		}
		if len(steps) != want {
			t.Fatalf("%s: got %d tool results, want %d", name, len(steps), want)
		}
		ids := map[string]bool{}
		for _, step := range steps {
			if step.ID == "" || ids[step.ID] || step.Status != "done" || !investigation.IsReadOnlyTool(step.Tool) {
				t.Fatalf("tool identity/completion lost: %+v", step)
			}
			ids[step.ID] = true
			if step.IsError == nil || *step.IsError || step.Truncated || step.EvidenceRef == "" ||
				step.producerResult == nil || *step.producerResult != step.Result || !json.Valid([]byte(step.Summary)) ||
				!strings.HasPrefix(step.Result, "{") {
				t.Fatalf("tool evidence/input lost: %+v", step)
			}
		}
	}
}

func TestOpencodeLiveApplyRetainsMutationButDoesNotAuthenticateFullLocalWrites(t *testing.T) {
	stream, err := os.ReadFile("testdata/opencode/apply.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var tracker applyMutationTracker
	d := (&opencodeAgent{}).parseStream(strings.NewReader(string(stream)), tracker.observe)
	if d.SessionID == "ses_f39d16efeffe6kTEXGHijdVqs2" || d.SessionID == "" {
		t.Fatal("Apply must use a separate session")
	}
	if len(tracker.steps) != 1 {
		t.Fatalf("expected one captured write, got %+v", tracker.steps)
	}
	for _, step := range tracker.steps {
		if step.tool != "patch_resource" || step.evidence() != mutationEvidenceConfirmed {
			t.Fatalf("successful patch result was lost: %+v", step)
		}
	}
	if tracker.outcome(ExecutionProfileFullLocal) != ApplyMutationUnknown {
		t.Fatal("full-local writes must still require current-state verification")
	}
}

func TestOpencodeToolEvidenceRequiresPrivateIssuance(t *testing.T) {
	for _, status := range []string{"completed", "error"} {
		for _, private := range []bool{true, false} {
			scope := strings.Repeat("a", 26)
			refs := investigationrefs.NewRegistry()
			lease, err := refs.Begin(scope)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			payload := "\n" + `{"kind":"Pod","metadata":{"name":"demo"}}` + "\n"
			ref := testEvidenceRef('a', 'b')
			if private {
				ref, _ = refs.Issue(scope, payload)
			}
			output, _ := json.Marshal(investigation.RefMarker(ref) + "\n\n" + payload)
			stream := `{"type":"tool_use","part":{"id":"call1","tool":"radar_get_resource","state":{"status":"` + status + `","input":{"kind":"Pod"},"output":` + string(output) + `,"error":` + string(output) + `}}}`
			validator := investigationEvidenceValidator{registry: refs, scope: scope, claimed: map[string]struct{}{}}
			events := []RunEvent{{Event: StreamEvent{Type: "turn"}}}
			(&opencodeAgent{}).parseStream(strings.NewReader(stream), func(ev StreamEvent) {
				events = append(events, RunEvent{Event: validator.validate(ev)})
			})
			got := bindEvidenceWithIssued(events, rootCauseCitations([]string{ref}), scope, lease.Close())
			want := investigation.Invalid
			if private && status == "completed" {
				want = investigation.Linked
			}
			if got == nil || got.Status != want {
				t.Fatalf("status=%s private=%v: evidence=%+v, want %s", status, private, got, want)
			}
		}
	}
}

func TestOpencodeToolResultsPreserveTruncationAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, status, output                    string
		hostTruncated, wantError, wantTruncated bool
	}{
		{name: "tool failure", status: "error", output: "ConfigMap not found", wantError: true},
		{name: "local cap", status: "completed", output: strings.Repeat("界", maxToolPayload+1), wantTruncated: true},
		{name: "host cap", status: "completed", output: "partial data", hostTruncated: true, wantTruncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, _ := json.Marshal(tc.output)
			truncated, _ := json.Marshal(tc.hostTruncated)
			stream := `{"type":"tool_use","part":{"id":"call","tool":"radar_get_resource","state":{"status":"` + tc.status + `","output":` + string(output) + `,"error":` + string(output) + `,"metadata":{"truncated":` + string(truncated) + `}}}}`
			var step *StepInfo
			(&opencodeAgent{}).parseStream(strings.NewReader(stream), func(ev StreamEvent) { step = ev.Step })
			if step == nil || step.Status != "done" || step.IsError == nil || *step.IsError != tc.wantError || step.Truncated != tc.wantTruncated {
				t.Fatalf("result state lost: %+v", step)
			}
			if step.producerResult == nil || *step.producerResult != tc.output {
				t.Fatal("uncapped producer result lost")
			}
		})
	}
}

func TestOpencodeProviderAndStreamErrors(t *testing.T) {
	stream, err := os.ReadFile("testdata/opencode/provider-error.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	a := &opencodeAgent{}
	d := a.parseStream(strings.NewReader(string(stream)), func(StreamEvent) {})
	if !d.cliErrored || !strings.Contains(d.cliErrText, "free tier") || d.Structured() {
		t.Fatalf("provider error was lost: %+v", d)
	}
	if err := agentExitError(a.Name(), a.SigninCmd(), d.cliErrText, ""); !strings.Contains(err.Error(), "free tier") {
		t.Fatalf("provider reason did not reach the user: %v", err)
	}
	d = a.parseStream(iotest.ErrReader(errors.New("stream interrupted")), func(StreamEvent) {})
	if !d.cliErrored || !strings.Contains(d.cliErrText, "stream interrupted") {
		t.Fatal("stream read failure was swallowed")
	}
	d = a.parseStream(strings.NewReader("session=fake\n{\"type\":\"text\",\"part\":{\"text\":\"session=also-fake\"}}"), func(StreamEvent) {})
	if d.SessionID != "" {
		t.Fatal("prose must not select the resumed session")
	}
}
