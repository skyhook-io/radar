package ai

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Outputs recorded from claude 2.1.312 (`claude auth status --json`) and
// codex-cli 0.164.0 (`codex login status`), identifying fields replaced.
const (
	claudeSignedIn = `{
  "loggedIn": true,
  "authMethod": "claude.ai",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "/home/user/.claude/projects",
  "configDirectory": "/home/user/.claude",
  "email": "user@example.com",
  "orgId": "00000000-0000-0000-0000-000000000000",
  "orgName": "user@example.com's Organization",
  "subscriptionType": "max"
}
`
	claudeSignedOut = `{
  "loggedIn": false,
  "authMethod": "none",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "/home/user/.claude/projects",
  "configDirectory": "/home/user/.claude"
}
`
	claudeAPIKey = `{
  "loggedIn": true,
  "authMethod": "api_key",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "/home/user/.claude/projects",
  "configDirectory": "/home/user/.claude",
  "apiKeySource": "ANTHROPIC_API_KEY"
}
`
	claudeBedrock = `{
  "loggedIn": true,
  "authMethod": "third_party",
  "apiProvider": "bedrock",
  "analyticsDisabled": true,
  "projectsDirectory": "/home/user/.claude/projects",
  "configDirectory": "/home/user/.claude"
}
`
	codexSignedIn       = "Logged in using ChatGPT\n"
	codexSignedInAPIKey = "Logged in using an API key - sk-proj-***ABCD\n"
	codexSignedOut      = "Not logged in\n"
)

func TestParseClaudeAuthStatus(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		stdout string
		want   SigninState
	}{
		{"subscription", 0, claudeSignedIn, SigninSignedIn},
		{"api key from env", 0, claudeAPIKey, SigninSignedIn},
		{"bedrock from env", 0, claudeBedrock, SigninSignedIn},
		{"signed out", 1, claudeSignedOut, SigninSignedOut},
		{"older CLI without auth status", 1, "", SigninUnknown},
		{"text instead of json", 0, "Login method: Claude Max account\n", SigninUnknown},
		{"json without loggedIn", 0, `{"authMethod":"none"}`, SigninUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseClaudeAuthStatus(tc.exit, tc.stdout, ""); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseCodexLoginStatus(t *testing.T) {
	cases := []struct {
		name           string
		exit           int
		stdout, stderr string
		want           SigninState
	}{
		{"chatgpt", 0, "", codexSignedIn, SigninSignedIn},
		{"api key", 0, "", codexSignedInAPIKey, SigninSignedIn},
		{"signed out", 1, "", codexSignedOut, SigninSignedOut},
		{"signed out text with exit 0", 0, "", codexSignedOut, SigninUnknown},
		{"signed in text with failure exit", 1, "", codexSignedIn, SigninUnknown},
		{"config error", 1, "", "Error loading config.toml: invalid type\n", SigninUnknown},
		{"older CLI", 2, "", "error: unrecognized subcommand 'status'\n", SigninUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCodexLoginStatus(tc.exit, tc.stdout, tc.stderr); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

type signinTestAgent struct{ name, path string }

func (a signinTestAgent) Name() string      { return a.name }
func (a signinTestAgent) Path() string      { return a.path }
func (a signinTestAgent) SigninCmd() string { return resolveAgent(a.name).SigninCmd() }
func (a signinTestAgent) command(context.Context, turnSpec) (*exec.Cmd, func(), error) {
	return nil, nil, errors.New("not runnable")
}
func (a signinTestAgent) parseStream(io.Reader, func(StreamEvent)) Diagnosis {
	return Diagnosis{}
}

type fakeRun struct {
	mu    sync.Mutex
	calls int
	code  int
	out   string
	err   error
	delay time.Duration
	env   []string
}

func (f *fakeRun) run(ctx context.Context, _ string, _, env []string) (int, string, string, error) {
	f.mu.Lock()
	f.calls++
	f.env = env
	code, out, err, delay := f.code, f.out, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return -1, "", "", ctx.Err()
		}
	}
	return code, out, "", err
}

func (f *fakeRun) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestChecker(f *fakeRun) *signinChecker {
	c := newSigninChecker()
	c.run = f.run
	return c
}

func TestSigninCheckStates(t *testing.T) {
	agent := signinTestAgent{name: "claude", path: "/bin/claude"}
	t.Run("signed out carries the sign-in command", func(t *testing.T) {
		got := newTestChecker(&fakeRun{code: 1, out: claudeSignedOut}).check(context.Background(), agent, ExecutionProfileSafeguarded, false)
		if got.State != SigninSignedOut || got.SigninCmd != "claude auth login" || got.Agent != "claude" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("signed in", func(t *testing.T) {
		got := newTestChecker(&fakeRun{out: claudeSignedIn}).check(context.Background(), agent, ExecutionProfileSafeguarded, false)
		if got.State != SigninSignedIn || got.SigninCmd != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("spawn failure is unknown", func(t *testing.T) {
		got := newTestChecker(&fakeRun{err: errors.New("exec format error")}).check(context.Background(), agent, ExecutionProfileSafeguarded, false)
		if got.State != SigninUnknown {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("timeout is unknown", func(t *testing.T) {
		f := &fakeRun{code: 1, out: claudeSignedOut, delay: time.Second}
		c := newTestChecker(f)
		c.timeout = 20 * time.Millisecond
		start := time.Now()
		got := c.check(context.Background(), agent, ExecutionProfileSafeguarded, false)
		if got.State != SigninUnknown {
			t.Fatalf("got %+v", got)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("check took %s, the timeout did not bound it", elapsed)
		}
	})
	t.Run("agents without a cheap status command are never run", func(t *testing.T) {
		for _, name := range []string{"cursor-agent", "opencode"} {
			f := &fakeRun{out: claudeSignedIn}
			got := newTestChecker(f).check(context.Background(), signinTestAgent{name: name, path: "/bin/" + name}, ExecutionProfileSafeguarded, false)
			if got.State != SigninUnknown || f.count() != 0 {
				t.Fatalf("%s: got %+v after %d runs", name, got, f.count())
			}
		}
	})
}

func TestSigninCheckCodexOtherCredentialsDependOnProfile(t *testing.T) {
	agent := signinTestAgent{name: "codex", path: "/bin/codex"}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	var last *fakeRun
	signedOut := func(profile ExecutionProfile) SigninStatus {
		last = &fakeRun{code: 1, out: codexSignedOut}
		return newTestChecker(last).check(context.Background(), agent, profile, false)
	}
	for _, profile := range []ExecutionProfile{ExecutionProfileSafeguarded, ExecutionProfileFullLocal} {
		if got := signedOut(profile); got.State != SigninSignedOut || got.SigninCmd != "codex login" {
			t.Fatalf("%s, no other credentials: got %+v", profile, got)
		}
	}

	t.Setenv("CODEX_API_KEY", "sk-test")
	if got := signedOut(ExecutionProfileFullLocal); got.State != SigninUnknown || last.count() != 0 {
		t.Fatalf("full-local with CODEX_API_KEY: got %+v after %d runs", got, last.count())
	}
	// Safeguarded runs drop the key, so the stored login is all that counts.
	if got := signedOut(ExecutionProfileSafeguarded); got.State != SigninSignedOut {
		t.Fatalf("safeguarded with CODEX_API_KEY: got %+v", got)
	}
	t.Setenv("CODEX_API_KEY", "")

	cfg := "model = \"llama\"\n\n[profiles.local]\nmodel_provider = \"ollama\"\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := signedOut(ExecutionProfileFullLocal); got.State != SigninUnknown || last.count() != 0 {
		t.Fatalf("full-local with a custom model provider: got %+v after %d runs", got, last.count())
	}
	if got := signedOut(ExecutionProfileSafeguarded); got.State != SigninSignedOut {
		t.Fatalf("safeguarded ignores the user's config: got %+v", got)
	}
}

func TestSigninCheckCachesPerProfile(t *testing.T) {
	agent := signinTestAgent{name: "claude", path: "/bin/claude"}
	f := &fakeRun{code: 1, out: claudeSignedOut}
	c := newTestChecker(f)
	c.check(context.Background(), agent, ExecutionProfileSafeguarded, false)
	c.check(context.Background(), agent, ExecutionProfileFullLocal, false)
	if f.count() != 2 {
		t.Fatalf("one profile's answer served the other: %d runs", f.count())
	}
}

func TestSigninCheckCachesAndRefreshes(t *testing.T) {
	agent := signinTestAgent{name: "claude", path: "/bin/claude"}
	f := &fakeRun{code: 1, out: claudeSignedOut}
	c := newTestChecker(f)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }

	c.check(context.Background(), agent, ExecutionProfileSafeguarded, false)
	c.check(context.Background(), agent, ExecutionProfileSafeguarded, false)
	if f.count() != 1 {
		t.Fatalf("a cached answer re-ran the CLI: %d runs", f.count())
	}

	f.mu.Lock()
	f.code, f.out = 0, claudeSignedIn
	f.mu.Unlock()
	if got := c.check(context.Background(), agent, ExecutionProfileSafeguarded, true); got.State != SigninSignedIn || f.count() != 2 {
		t.Fatalf("refresh: got %+v after %d runs", got, f.count())
	}

	now = now.Add(signinCacheTTL + time.Second)
	c.check(context.Background(), agent, ExecutionProfileSafeguarded, false)
	if f.count() != 3 {
		t.Fatalf("an expired answer was served: %d runs", f.count())
	}

	moved := signinTestAgent{name: "claude", path: "/opt/claude"}
	c.check(context.Background(), moved, ExecutionProfileSafeguarded, false)
	if f.count() != 4 {
		t.Fatalf("a moved CLI was answered from the old path's cache: %d runs", f.count())
	}
}

func TestSigninCheckRunsOneCommandPerAgentAtATime(t *testing.T) {
	agent := signinTestAgent{name: "claude", path: "/bin/claude"}
	f := &fakeRun{code: 1, out: claudeSignedOut, delay: 50 * time.Millisecond}
	c := newTestChecker(f)
	var wg sync.WaitGroup
	var signedOut atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.check(context.Background(), agent, ExecutionProfileSafeguarded, true).State == SigninSignedOut {
				signedOut.Add(1)
			}
		}()
	}
	wg.Wait()
	if f.count() != 1 || signedOut.Load() != 8 {
		t.Fatalf("%d concurrent re-checks ran %d commands, %d answered", 8, f.count(), signedOut.Load())
	}
}

func TestSigninCheckWaiterCancelDoesNotCancelTheCheck(t *testing.T) {
	agent := signinTestAgent{name: "claude", path: "/bin/claude"}
	f := &fakeRun{code: 1, out: claudeSignedOut, delay: 50 * time.Millisecond}
	c := newTestChecker(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The first caller's request is already gone; the command still finishes
	// and its answer is cached for the next caller.
	c.check(ctx, agent, ExecutionProfileSafeguarded, false)
	if got := c.check(context.Background(), agent, ExecutionProfileSafeguarded, false); got.State != SigninSignedOut || f.count() != 1 {
		t.Fatalf("got %+v after %d runs", got, f.count())
	}
}

// TestRunSigninCommandKillsAHungCLI runs a real process that never answers.
func TestRunSigninCommandKillsAHungCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newSigninChecker()
	c.timeout = 200 * time.Millisecond
	start := time.Now()
	got := c.check(context.Background(), signinTestAgent{name: "claude", path: bin}, ExecutionProfileSafeguarded, false)
	if got.State != SigninUnknown {
		t.Fatalf("got %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hung CLI held the check for %s", elapsed)
	}
}

func TestRunSigninCommandReadsExitCodeAndOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	bin := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"login status\" ] || exit 9\necho 'Not logged in' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got := newSigninChecker().check(context.Background(), signinTestAgent{name: "codex", path: bin}, ExecutionProfileSafeguarded, false)
	if got.State != SigninSignedOut {
		t.Fatalf("got %+v", got)
	}
}

// A CLI that exits while a child it started still holds the output pipes must
// not leave that child running.
func TestRunSigninCommandKillsAChildLeftHoldingThePipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nsleep 30 &\necho $! > " + pidFile + "\necho '{\"loggedIn\": false}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := newSigninChecker().check(context.Background(), signinTestAgent{name: "claude", path: bin}, ExecutionProfileSafeguarded, false)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the child held the check for %s", elapsed)
	}
	if got.State != SigninSignedOut {
		t.Fatalf("got %+v", got)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A check stands for a run, so it gets that run's environment: a safeguarded
// Codex check sees no API keys, a full-local one sees Radar's environment.
func TestSigninCheckUsesTheProfilesEnvironment(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "sk-test")
	t.Setenv("CODEX_HOME", t.TempDir())
	agent := signinTestAgent{name: "codex", path: "/bin/codex"}
	f := &fakeRun{code: 0, out: codexSignedIn}
	newTestChecker(f).check(context.Background(), agent, ExecutionProfileSafeguarded, false)
	if f.env == nil {
		t.Fatal("safeguarded check inherited Radar's environment")
	}
	for _, kv := range f.env {
		if strings.HasPrefix(kv, "CODEX_API_KEY=") {
			t.Fatalf("safeguarded check saw %s", kv)
		}
	}
	t.Setenv("CODEX_API_KEY", "")
	f = &fakeRun{code: 0, out: codexSignedIn}
	newTestChecker(f).check(context.Background(), agent, ExecutionProfileFullLocal, false)
	if f.count() != 1 || f.env != nil {
		t.Fatalf("full-local check got a narrowed environment: %v", f.env)
	}
}
