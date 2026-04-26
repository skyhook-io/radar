package server

import (
	"reflect"
	"strings"
	"testing"
)

// TestDefaultExecCommand covers the precedence rules for building the argv
// passed to the pod exec subresource:
//   - ?shell= override always wins and is passed verbatim as a single argv element
//   - --pod-shell-default fallback is used when no override is present
//   - the built-in defaultShellScript is used when neither is set
func TestDefaultExecCommand(t *testing.T) {
	tests := []struct {
		name     string
		override string
		fallback string
		want     []string
	}{
		{
			name:     "no override and no fallback uses built-in script",
			override: "",
			fallback: "",
			want:     []string{"sh", "-c", defaultShellScript},
		},
		{
			name:     "fallback configured, no override, wraps in sh -c",
			override: "",
			fallback: "exec zsh",
			want:     []string{"sh", "-c", "exec zsh"},
		},
		{
			name:     "explicit override without fallback is passed verbatim",
			override: "/bin/bash",
			fallback: "",
			want:     []string{"/bin/bash"},
		},
		{
			name:     "explicit override wins over configured fallback",
			override: "/bin/dash",
			fallback: "exec zsh",
			want:     []string{"/bin/dash"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultExecCommand(tc.override, tc.fallback)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("defaultExecCommand(%q, %q) = %v, want %v", tc.override, tc.fallback, got, tc.want)
			}
		})
	}
}

// TestDefaultShellScript is a tripwire that pins the exact script content.
// The script is load-bearing for skyhook-io/radar#452 — any edit should be
// manually re-verified against the scenarios documented in the PR that
// introduced it (bash present, ash-only, sh-only, --pod-shell-default
// override, multi-container pod). If you're here because this test failed,
// update `expected` AND re-run those scenarios before merging.
//
// Earlier drafts used `exec bash || exec ash || exec sh`, but POSIX requires
// non-interactive shells to exit immediately when `exec` fails to find the
// target — so that cascade died with exit 127 on the first missing shell.
// The current form detects each shell with `command -v` before execing, so
// exec only runs for commands that are known to exist.
func TestDefaultShellScript(t *testing.T) {
	const expected = "export TERM=xterm-256color; if command -v bash >/dev/null 2>&1; then exec bash -il; elif command -v ash >/dev/null 2>&1; then exec ash; else exec sh; fi"
	if defaultShellScript != expected {
		t.Errorf("defaultShellScript changed:\n  got:  %q\n  want: %q", defaultShellScript, expected)
	}

	// Behavioral asserts — pin the load-bearing properties so a future edit
	// that drops one of them fails with a specific message, not just a
	// string-diff. These would have caught the `exec bash || exec ash` draft
	// that broke live in alpine during this PR's development.
	if !strings.Contains(defaultShellScript, "command -v bash") {
		t.Error("defaultShellScript must detect bash with `command -v` before exec'ing — naive `exec bash || ...` fails under POSIX because non-interactive shells exit on exec-not-found")
	}
	if !strings.Contains(defaultShellScript, "bash -il") {
		t.Error("defaultShellScript must run bash as `-il` (interactive login) so it sources the image's startup files and picks up a PS1 with \\w — that's the PWD-in-prompt fix from skyhook-io/radar#452")
	}
}

// TestIsShellNotFoundError pins the substring patterns used to classify
// "shell missing" errors so the frontend renders the "Start debug container"
// CTA instead of a generic "Failed to connect". Patterns must stay broad
// enough to catch each container runtime's wording — see comments inline.
func TestIsShellNotFoundError(t *testing.T) {
	tests := []struct {
		name   string
		errMsg string
		want   bool
	}{
		{
			name:   "containerd: executable file not found",
			errMsg: `OCI runtime exec failed: exec failed: unable to start container process: exec: "sh": executable file not found in $PATH: unknown`,
			want:   true,
		},
		{
			name:   "no such file or directory",
			errMsg: `no such file or directory`,
			want:   true,
		},
		{
			name:   "exit code 127 from sh -c wrapper",
			errMsg: `command terminated with exit code 127`,
			want:   true,
		},
		{
			name:   "case-insensitive OCI runtime",
			errMsg: `oci runtime exec failed`,
			want:   true,
		},
		{
			name:   "non-127 exit codes are not classified as shell-missing",
			errMsg: `command terminated with exit code 1`,
			want:   false,
		},
		{
			name:   "unrelated transport error",
			errMsg: `unable to upgrade connection: timeout`,
			want:   false,
		},
		{
			name:   "permission denied is not shell-missing",
			errMsg: `forbidden: pods "foo" is forbidden: User cannot exec`,
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShellNotFoundError(tc.errMsg); got != tc.want {
				t.Errorf("isShellNotFoundError(%q) = %v, want %v", tc.errMsg, got, tc.want)
			}
		})
	}
}

// TestLooksLikeShellNotFound covers the drift canary. The function is a
// broader heuristic than isShellNotFoundError and intentionally tolerates
// some false positives — the goal is to log a breadcrumb when an error
// LOOKS shell-related but the precise substring matcher didn't recognise
// it, so maintainers notice kubelet error-text drift before users do.
func TestLooksLikeShellNotFound(t *testing.T) {
	tests := []struct {
		name    string
		errMsg  string
		want    bool
		comment string
	}{
		{
			name:    "current kubelet: executable file not found",
			errMsg:  `rpc error: code = Unknown desc = failed to exec in container: failed to start exec: cannot exec in a stopped container: executable file not found in $PATH: unknown`,
			want:    true,
			comment: "contains 'exec' and 'not found' — catches the error our isShellNotFoundError already handles, ensuring the canary overlap is intact",
		},
		{
			name:    "hypothetical kubelet reword: exec missing",
			errMsg:  `unable to start container process: exec: "bash": not found`,
			want:    true,
			comment: "new phrasing with 'exec' + 'not found' — the exact drift scenario the canary is designed to catch",
		},
		{
			name:    "exit code 127 from sh -c script",
			errMsg:  `command terminated with exit code 127`,
			want:    true,
			comment: "POSIX's reserved 'command not found' exit code — what kubelet surfaces when sh -c can't run",
		},
		{
			name:    "unrelated websocket error",
			errMsg:  `unable to upgrade connection: timeout`,
			want:    false,
			comment: "no exec/not-found/127 signals — must not fire",
		},
		{
			name:    "permission denied",
			errMsg:  `forbidden: pods "foo" is forbidden: User cannot exec`,
			want:    false,
			comment: "contains 'exec' but no 'not found' or exit 127 — must not fire",
		},
		{
			name:    "k8s not found (pod, not shell)",
			errMsg:  `pods "nonexistent-pod" not found`,
			want:    false,
			comment: "contains 'not found' but no 'exec' signal — must not fire",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeShellNotFound(tc.errMsg)
			if got != tc.want {
				t.Errorf("looksLikeShellNotFound(%q) = %v, want %v (%s)", tc.errMsg, got, tc.want, tc.comment)
			}
		})
	}
}
