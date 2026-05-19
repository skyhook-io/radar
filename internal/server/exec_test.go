package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestDefaultExecCommand pins the argv precedence: ?shell= override wins
// over everything; Windows pods route to cmd.exe ahead of the POSIX-only
// --pod-shell-default fallback; built-in script is the floor.
func TestDefaultExecCommand(t *testing.T) {
	tests := []struct {
		name     string
		override string
		fallback string
		podOS    string
		want     []string
	}{
		{
			name:     "no override and no fallback uses built-in script",
			override: "",
			fallback: "",
			podOS:    "",
			want:     []string{"sh", "-c", defaultShellScript},
		},
		{
			name:     "fallback configured, no override, wraps in sh -c",
			override: "",
			fallback: "exec zsh",
			podOS:    "",
			want:     []string{"sh", "-c", "exec zsh"},
		},
		{
			name:     "explicit override without fallback is passed verbatim",
			override: "/bin/bash",
			fallback: "",
			podOS:    "",
			want:     []string{"/bin/bash"},
		},
		{
			name:     "explicit override wins over configured fallback",
			override: "/bin/dash",
			fallback: "exec zsh",
			podOS:    "",
			want:     []string{"/bin/dash"},
		},
		{
			name:     "windows pod with no override uses cmd.exe + windows script",
			override: "",
			fallback: "",
			podOS:    "windows",
			want:     []string{"cmd.exe", "/c", windowsDefaultShellScript},
		},
		{
			name:     "windows pod ignores POSIX-only --pod-shell-default fallback",
			override: "",
			fallback: "exec zsh",
			podOS:    "windows",
			want:     []string{"cmd.exe", "/c", windowsDefaultShellScript},
		},
		{
			name:     "windows pod still honours explicit override (operator escape hatch)",
			override: "powershell.exe",
			fallback: "",
			podOS:    "windows",
			want:     []string{"powershell.exe"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultExecCommand(tc.override, tc.fallback, tc.podOS)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("defaultExecCommand(%q, %q, %q) = %v, want %v", tc.override, tc.fallback, tc.podOS, got, tc.want)
			}
		})
	}
}

// TestWindowsDefaultShellScript is a tripwire — if you edit the script,
// manually verify against both Nano Server (no PowerShell) and Server Core
// (PowerShell present) before merging.
func TestWindowsDefaultShellScript(t *testing.T) {
	const expected = `where powershell >nul 2>&1 && powershell || cmd`
	if windowsDefaultShellScript != expected {
		t.Errorf("windowsDefaultShellScript changed:\n  got:  %q\n  want: %q", windowsDefaultShellScript, expected)
	}
	if !strings.Contains(windowsDefaultShellScript, "where powershell") {
		t.Error("windowsDefaultShellScript must probe for PowerShell with `where` before invoking it — Nano Server ships without PowerShell and would otherwise fail")
	}
	if !strings.Contains(windowsDefaultShellScript, "|| cmd") {
		t.Error("windowsDefaultShellScript must fall back to cmd.exe when PowerShell is missing — cmd.exe is the only shell guaranteed present on every Windows container image")
	}
}

// TestDetectPodOS pins the three-tier authority order and the
// degrade-to-empty contract for tier-3 lookup failures.
func TestDetectPodOS(t *testing.T) {
	tests := []struct {
		name       string
		pod        *corev1.Pod
		nodeLabels map[string]string
		nodeErr    error
		want       string
	}{
		{
			name: "tier 1: pod.spec.os.name=windows is authoritative",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				OS:           &corev1.PodOS{Name: corev1.Windows},
				NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
				NodeName:     "n1",
			}},
			nodeLabels: map[string]string{"kubernetes.io/os": "linux"},
			want:       "windows",
		},
		{
			name: "tier 1: pod.spec.os.name=linux is authoritative",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				OS: &corev1.PodOS{Name: corev1.Linux},
			}},
			want: "linux",
		},
		{
			name: "tier 2: nodeSelector kubernetes.io/os when spec.os is unset",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"kubernetes.io/os": "windows"},
				NodeName:     "n1",
			}},
			nodeLabels: map[string]string{"kubernetes.io/os": "linux"},
			want:       "windows",
		},
		{
			name: "tier 2: kubernetes.io/os wins over beta.kubernetes.io/os",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{
					"kubernetes.io/os":      "linux",
					"beta.kubernetes.io/os": "windows",
				},
			}},
			want: "linux",
		},
		{
			name: "tier 2: beta.kubernetes.io/os is read when kubernetes.io/os is missing",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"beta.kubernetes.io/os": "windows"},
			}},
			want: "windows",
		},
		{
			// Guards against a "simplify the `v != \"\"` check" refactor.
			// Some admission webhooks emit an empty-string label rather than
			// omitting the key; treating empty as absent lets the beta
			// fallback (or tier 3) take over instead of returning "" as a
			// valid OS and routing Windows pods to sh -c.
			name: "tier 2: empty kubernetes.io/os value falls through to beta label",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{
					"kubernetes.io/os":      "",
					"beta.kubernetes.io/os": "windows",
				},
			}},
			want: "windows",
		},
		{
			name: "tier 3: node label fetched when pod has no selector",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeName: "n1",
			}},
			nodeLabels: map[string]string{"kubernetes.io/os": "windows"},
			want:       "windows",
		},
		{
			name: "tier 3 skipped when nodeName is empty (unscheduled pod)",
			pod:  &corev1.Pod{Spec: corev1.PodSpec{}},
			want: "",
		},
		{
			name: "node fetch error degrades to unknown, not panic",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				NodeName: "n1",
			}},
			nodeErr: errors.New("forbidden: cannot get nodes"),
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var lookup osNodeLabelsLookup
			if tc.nodeLabels != nil || tc.nodeErr != nil {
				lookup = func(ctx context.Context, name string) (map[string]string, error) {
					if tc.nodeErr != nil {
						return nil, tc.nodeErr
					}
					return tc.nodeLabels, nil
				}
			}
			got := detectPodOS(context.Background(), tc.pod, lookup)
			if got != tc.want {
				t.Errorf("detectPodOS = %q, want %q", got, tc.want)
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
		{
			// Verbatim hcs error surface — full kubectl-style wrapping
			// around the inner CreateProcess failure.
			name:   "windows: hcs CreateProcess + 'the system cannot find the file'",
			errMsg: `Internal error occurred: error executing command in container: failed to exec in container: failed to start exec "abc": hcs::System::CreateProcess: sh -c "..." def: The system cannot find the file specified.: unknown`,
			want:   true,
		},
		{
			// Non-English Windows surfaces the same hcs prefix but localizes
			// the tail; prefix match alone must be enough.
			name:   "windows: hcs CreateProcess prefix alone (non-English locale)",
			errMsg: `hcs::System::CreateProcess: cmd.exe /c "..." ghi: <localized message>: unknown`,
			want:   true,
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
