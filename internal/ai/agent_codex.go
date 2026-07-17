package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// codexAgent drives the Codex CLI (`codex exec`). Codex has no per-MCP-tool
// allowlist and its shell can't be disabled, so containment differs from Claude:
//   - cluster access is gated by the read-only MCP MOUNT (radar-side), not flags;
//   - --sandbox read-only blocks the model's shell from network + filesystem
//     writes (verified: no loopback, no kubectl), though it can still READ local
//     files into context — disclosed to the user;
//   - --ignore-user-config keeps the user's other MCP servers out of an isolated
//     run; cmd.Dir is an empty temp dir so it doesn't read the launch directory.
type codexAgent struct{ bin string }

func (a *codexAgent) Name() string { return "codex" }

func (a *codexAgent) SigninCmd() string { return "codex login" }

func (a *codexAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	// Codex has no system-prompt flag; the framing rides on the first turn's
	// prompt (the resumed session already carries it).
	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}
	mcpCfg := fmt.Sprintf("mcp_servers.radar.url=%q", s.mcpURL)

	// Always start from the base flags; isolation adds --ignore-user-config, an
	// empty cwd, and a minimal env. "My setup" keeps the user's config (their other
	// MCP servers, guidelines), their full env, and their home cwd. The shell stays
	// --sandbox read-only in BOTH modes — but the "cluster writes go only through
	// Radar's read-only MCP" containment holds ONLY when isolated. In "my setup"
	// the agent also gets the user's own MCP servers (possibly write/network/cloud
	// capable) + local file reads: a deliberate trusted mode, not a contained one.
	base := []string{"--json", "--skip-git-repo-check", "-c", mcpCfg}
	// Emit reasoning summaries so the UI can show the model's thinking between tool
	// calls (off by default — without this the stream is only tool calls + the final
	// message). The Codex parser maps these `reasoning` items to thinking events.
	base = append(base, "-c", `model_reasoning_summary="auto"`)
	// Disable Codex's built-in web_search tool: it reaches the public internet
	// (server-side, so --sandbox can't stop it), which breaks the "contained, reads
	// only this cluster" guarantee. The diagnosis needs cluster data via MCP, not the
	// web. (image_gen is also built in but is inert for k8s and can't be disabled.)
	base = append(base, "-c", `web_search="disabled"`)
	if s.isolated {
		base = append(base, "--ignore-user-config")
	}
	if s.model != "" {
		base = append(base, "-m", s.model)
	}
	// Default to medium reasoning effort (not Codex's bare default) — it gives a
	// solid investigation AND is the level at which Codex actually emits reasoning
	// summaries under --ignore-user-config, so the UI can show the model's thinking.
	effort := s.effort
	if effort == "" {
		effort = "medium"
	}
	// Codex's always-on image_gen tool is rejected by the API at "minimal" effort
	// (400), and it can't be disabled — so clamp minimal up to low.
	if effort == "minimal" {
		effort = "low"
	}
	base = append(base, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))

	var args []string
	if s.sessionID != "" {
		// resume lacks --sandbox; set sandbox via -c (cwd via cmd.Dir below).
		args = append([]string{"exec", "resume"}, base...)
		args = append(args, "-c", `sandbox_mode="read-only"`, s.sessionID, prompt)
	} else {
		args = append([]string{"exec"}, base...)
		args = append(args, "--sandbox", "read-only", prompt)
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)

	cleanup := func() {}
	if s.isolated {
		// Empty working dir so the model's shell can't read radar's source / cwd.
		dir, err := os.MkdirTemp("", "radar-codex-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: codex workdir: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		cmd.Dir = dir
		cmd.Env = codexEnv()
	}
	// "My setup": inherit radar's cwd + full env so the user's auth/config/MCPs work.

	return cmd, cleanup, nil
}

// codexEnv is the minimal environment Codex needs (auth via HOME/CODEX_HOME, PATH
// to exec, locale). Cloud-provider secrets are deliberately omitted — the shell
// can read env into context, and the agent reaches the cluster only via MCP.
func codexEnv() []string {
	keep := map[string]bool{
		"HOME": true, "PATH": true, "CODEX_HOME": true, "TMPDIR": true,
		"TERM": true, "LANG": true, "USER": true, "LOGNAME": true, "SHELL": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if keep[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// Codex JSONL event shapes (codex exec --json). Only the fields we consume.
type codexEvent struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Item     *codexItem `json:"item"`
}

type codexItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"` // mcp_tool_call | agent_message | reasoning | ...
	Text      string          `json:"text"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

func (a *codexAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var sessionID string
	var answer strings.Builder

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e codexEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			if e.ThreadID != "" {
				sessionID = e.ThreadID
			}
		case "item.started":
			if e.Item != nil && e.Item.Type == "mcp_tool_call" {
				onEvent(StreamEvent{Type: "step", Step: &StepInfo{
					ID: e.Item.ID, Tool: e.Item.Tool, Status: "running",
					Summary: codexArgsText(e.Item.Arguments),
				}})
			}
		case "item.completed":
			if e.Item == nil {
				continue
			}
			switch e.Item.Type {
			case "mcp_tool_call":
				res, trunc := capPayload(codexResultText(e.Item))
				onEvent(StreamEvent{Type: "step", Step: &StepInfo{
					ID: e.Item.ID, Tool: e.Item.Tool, Status: "done",
					Result: res, Truncated: trunc,
				}})
			case "reasoning":
				if e.Item.Text != "" {
					onEvent(StreamEvent{Type: "thinking", Token: e.Item.Text})
				}
			case "agent_message":
				if e.Item.Text != "" {
					answer.WriteString(e.Item.Text)
					answer.WriteString("\n")
				}
			}
		}
	}

	d := diagnosisFromText(answer.String())
	d.SessionID = sessionID
	return d
}

func codexArgsText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	args, _ := capPayload(s)
	return args
}

// codexResultText joins the text parts of a Codex mcp_tool_call result (already
// the unwrapped content[].text shape). Capping happens at the call site so the
// truncated flag can be surfaced.
func codexResultText(it *codexItem) string {
	if it.Result == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range it.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}
