package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	// env is the environment a run with the profile gets; nil inherits
	// Radar's, as a full-local run does.
	env   func(profile ExecutionProfile) []string
	parse func(exitCode int, stdout, stderr string) SigninState
	// otherCredentials reports whether a run with this profile can
	// authenticate in a way the status command doesn't see. "Signed out" is
	// then not trustworthy and "signed in" shows the same card as unknown, so
	// the command isn't run at all: a CLI too old to know the status
	// subcommand may take its arguments as a prompt, and these credentials
	// would let it send one.
	otherCredentials func(profile ExecutionProfile) bool
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
		// environment itself. Every profile passes those variables through.
		return &signinProbe{
			args:  []string{"auth", "status", "--json"},
			env:   safeguardedEnv(scrubbedEnv),
			parse: parseClaudeAuthStatus,
		}
	case "codex":
		return &signinProbe{
			args:             []string{"login", "status"},
			env:              safeguardedEnv(codexEnv),
			parse:            parseCodexLoginStatus,
			otherCredentials: codexOtherCredentials,
		}
	}
	return nil
}

// safeguardedEnv gives a safeguarded check the environment a safeguarded run
// of that agent gets, and a full-local one Radar's own.
func safeguardedEnv(env func() []string) func(ExecutionProfile) []string {
	return func(profile ExecutionProfile) []string {
		if profile == ExecutionProfileSafeguarded {
			return env()
		}
		return nil
	}
}

var codexModelProviderLine = regexp.MustCompile(`(?m)^\s*model_provider\s*=`)

// codexOtherCredentials: a safeguarded run uses neither the environment's API
// keys (codexEnv drops them) nor the user's config, so only the stored login
// counts. A full-local run can authenticate with CODEX_API_KEY or
// OPENAI_API_KEY, or through a custom model provider in config.toml, none of
// which `codex login status` reports.
func codexOtherCredentials(profile ExecutionProfile) bool {
	if profile != ExecutionProfileFullLocal {
		return false
	}
	for _, key := range []string{"CODEX_API_KEY", "OPENAI_API_KEY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	dir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		dir = filepath.Join(home, ".codex")
	}
	f, err := os.Open(filepath.Join(dir, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	cfg, err := io.ReadAll(io.LimitReader(f, 1<<20))
	return err != nil || codexModelProviderLine.Match(cfg)
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

type signinKey struct {
	agent   string
	profile ExecutionProfile
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
	entries map[signinKey]*signinEntry
	now     func() time.Time
	timeout time.Duration
	run     func(ctx context.Context, path string, args, env []string) (int, string, string, error)
}

func newSigninChecker() *signinChecker {
	return &signinChecker{
		entries: map[signinKey]*signinEntry{},
		now:     time.Now,
		timeout: signinCheckTimeout,
		run:     runSigninCommand,
	}
}

// check answers for one agent backend run with profile. refresh discards a
// cached answer.
func (c *signinChecker) check(ctx context.Context, agent Agent, profile ExecutionProfile, refresh bool) SigninStatus {
	name := agent.Name()
	key := signinKey{agent: name, profile: profile}
	status := SigninStatus{Agent: name, State: SigninUnknown}
	probe := signinProbeFor(name)
	if probe == nil {
		return status
	}
	path := agent.Path()
	for {
		c.mu.Lock()
		e := c.entries[key]
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
		c.entries[key] = e
		c.mu.Unlock()

		state := c.probe(ctx, path, probe, profile)

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

func (c *signinChecker) probe(ctx context.Context, path string, p *signinProbe, profile ExecutionProfile) SigninState {
	if p.otherCredentials != nil && p.otherCredentials(profile) {
		return SigninUnknown
	}
	// Detached from the request: a caller that gives up must not leave the
	// waiters behind it with a cancelled answer.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	var env []string
	if p.env != nil {
		env = p.env(profile)
	}
	code, stdout, stderr, err := c.run(cctx, path, p.args, env)
	if err != nil || cctx.Err() != nil {
		return SigninUnknown
	}
	return p.parse(code, stdout, stderr)
}

// runSigninCommand runs a status command with the environment of the run it
// stands for (env, or Radar's when nil), plus the agent's directory on PATH
// as every run gets. It starts in the home directory so a project config in
// Radar's working directory can't change the answer, and reads no input. The
// process group is killed on timeout, and after the command exits so a child
// it left holding the output pipes can't outlive it.
func runSigninCommand(ctx context.Context, path string, args, env []string) (int, string, string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	addAgentDirToPath(cmd)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	configureProcessLifecycle(cmd)
	cmd.WaitDelay = 500 * time.Millisecond
	stdout, stderr := &cappedBuffer{limit: 64 << 10}, &cappedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	// Windows has no process group to signal after the parent exits; its
	// taskkill tree walk only helps while pipes are still held.
	if cmd.Process != nil && (runtime.GOOS != "windows" || errors.Is(err, exec.ErrWaitDelay)) {
		_ = cmd.Cancel()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String(), nil
	}
	if err != nil {
		return -1, "", "", err
	}
	return 0, stdout.String(), stderr.String(), nil
}
