package ai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SigninState is what an agent CLI's own status command says about its sign-in.
type SigninState string

const (
	SigninSignedIn  SigninState = "signed-in"
	SigninSignedOut SigninState = "signed-out"
	// SigninUnknown covers every answer that is not a definite one: an agent
	// with no cheap status command, a timeout, a failed spawn, output Radar
	// doesn't recognize, or credentials the status command can't see.
	SigninUnknown SigninState = "unknown"
)

// SigninStatus is the pre-consent sign-in answer for one agent.
type SigninStatus struct {
	Agent string      `json:"agent"`
	State SigninState `json:"state"`
	// SigninCmd is the command that signs the agent in; set only when signed out.
	SigninCmd string `json:"signinCmd,omitempty"`
}

// signinCheckTimeout bounds one status command. Measured warm on macOS:
// `claude auth status` 0.11–0.38s (one cold 1.07s), `codex login status`
// 0.10–0.15s. Two seconds covers a cold start with room to spare while keeping
// a hung CLI from holding the answer; the consent card never waits on it.
const signinCheckTimeout = 2 * time.Second

// signinCacheTTL keeps reopening the consent card from spawning the CLI again.
// A re-check bypasses it.
const signinCacheTTL = time.Minute

// signinProbe is one agent's status command and how to read it.
type signinProbe struct {
	args []string
	// envCredential names environment variables that authenticate a real run
	// but that the status command ignores. With one set, "signed out" from the
	// status command is not trustworthy, so the answer becomes unknown.
	envCredential []string
	parse         func(exitCode int, stdout, stderr string) SigninState
}

// signinProbeFor returns the status check for agents whose CLI has a cheap,
// local, non-interactive one. The others return nil and always answer
// unknown:
//   - cursor-agent: `cursor-agent status` contacts Cursor's servers when
//     signed in (1.0–1.6s measured), rewrites its config file while it runs,
//     and ignores CURSOR_API_KEY.
//   - opencode: it signs in per model provider, and `opencode auth list`
//     can't say whether the provider of the configured model is usable.
func signinProbeFor(agent string) *signinProbe {
	switch agent {
	case "claude":
		// Reports API keys, OAuth tokens and Bedrock/Vertex from the
		// environment itself, so no envCredential is needed.
		return &signinProbe{args: []string{"auth", "status", "--json"}, parse: parseClaudeAuthStatus}
	case "codex":
		return &signinProbe{
			args:          []string{"login", "status"},
			envCredential: []string{"CODEX_API_KEY", "OPENAI_API_KEY"},
			parse:         parseCodexLoginStatus,
		}
	}
	return nil
}

// parseClaudeAuthStatus reads `claude auth status --json`: {"loggedIn": bool, ...}.
func parseClaudeAuthStatus(_ int, stdout, _ string) SigninState {
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &status); err != nil || status.LoggedIn == nil {
		return SigninUnknown
	}
	if *status.LoggedIn {
		return SigninSignedIn
	}
	return SigninSignedOut
}

// parseCodexLoginStatus reads `codex login status`, which prints one line on
// stderr: "Logged in using ChatGPT" (or "... an API key") with exit 0, or
// "Not logged in" with exit 1.
func parseCodexLoginStatus(exitCode int, stdout, stderr string) SigninState {
	text := strings.TrimSpace(stdout + "\n" + stderr)
	switch {
	case exitCode == 0 && strings.HasPrefix(text, "Logged in"):
		return SigninSignedIn
	case exitCode != 0 && text == "Not logged in":
		return SigninSignedOut
	}
	return SigninUnknown
}

type signinEntry struct {
	path   string
	state  SigninState
	at     time.Time
	inProg chan struct{} // closed when the in-flight check finishes
}

// signinChecker caches status answers per agent and runs at most one status
// command per agent at a time, whoever asks.
type signinChecker struct {
	mu      sync.Mutex
	entries map[string]*signinEntry
	now     func() time.Time
	timeout time.Duration
	run     func(ctx context.Context, path string, args []string) (int, string, string, error)
}

func newSigninChecker() *signinChecker {
	return &signinChecker{
		entries: map[string]*signinEntry{},
		now:     time.Now,
		timeout: signinCheckTimeout,
		run:     runSigninCommand,
	}
}

// check answers for one agent backend. refresh discards a cached answer.
func (c *signinChecker) check(ctx context.Context, agent Agent, refresh bool) SigninStatus {
	name := agent.Name()
	status := SigninStatus{Agent: name, State: SigninUnknown}
	probe := signinProbeFor(name)
	if probe == nil {
		return status
	}
	path := agent.Path()
	for {
		c.mu.Lock()
		e := c.entries[name]
		if e != nil && e.path == path && e.inProg != nil {
			wait := e.inProg
			c.mu.Unlock()
			select {
			case <-wait:
				// A check that was already running when a re-check was asked
				// for is still a fresh answer.
				refresh = false
				continue
			case <-ctx.Done():
				return status
			}
		}
		if e != nil && e.path == path && !refresh && c.now().Sub(e.at) < signinCacheTTL {
			c.mu.Unlock()
			status.State = e.state
			break
		}
		e = &signinEntry{path: path, inProg: make(chan struct{})}
		c.entries[name] = e
		c.mu.Unlock()

		state := c.probe(ctx, path, probe)

		c.mu.Lock()
		e.state, e.at = state, c.now()
		close(e.inProg)
		e.inProg = nil
		c.mu.Unlock()
		status.State = state
		break
	}
	if status.State == SigninSignedOut {
		status.SigninCmd = agent.SigninCmd()
	}
	return status
}

func (c *signinChecker) probe(ctx context.Context, path string, p *signinProbe) SigninState {
	// Detached from the request: a caller that gives up must not leave the
	// waiters behind it with a cancelled answer.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	code, stdout, stderr, err := c.run(cctx, path, p.args)
	if err != nil || cctx.Err() != nil {
		return SigninUnknown
	}
	state := p.parse(code, stdout, stderr)
	if state == SigninSignedOut {
		for _, key := range p.envCredential {
			if strings.TrimSpace(os.Getenv(key)) != "" {
				return SigninUnknown
			}
		}
	}
	return state
}

// runSigninCommand runs a status command the way a full-local run spawns the
// agent: Radar's environment, which every profile's environment is a subset
// of, so "signed out" here holds for every profile. It starts in the home
// directory so a project config in Radar's working directory can't change the
// answer, and reads no input. The process group is killed on timeout.
func runSigninCommand(ctx context.Context, path string, args []string) (int, string, string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	addAgentDirToPath(cmd)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	configureProcessLifecycle(cmd)
	cmd.WaitDelay = 500 * time.Millisecond
	stdout, stderr := &cappedBuffer{limit: 64 << 10}, &cappedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String(), nil
	}
	if err != nil {
		return -1, "", "", err
	}
	return 0, stdout.String(), stderr.String(), nil
}
