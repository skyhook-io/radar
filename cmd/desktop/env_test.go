package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

func setupEnvTestShell(t *testing.T, shell, profile string) string {
	t.Helper()
	if _, err := os.Stat(shell); err != nil {
		t.Skipf("shell unavailable: %v", err)
	}
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile")
	profile = "printf x >> \"$1.starts\"\nexport PATH=/usr/bin:/bin\n" + profile
	if err := os.WriteFile(profilePath, []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	args := "--noprofile --norc -i"
	if shell == "/bin/zsh" {
		args = "-f -i"
	}
	wrapper := filepath.Join(dir, "shell")
	// Source only the fixture, so developer profiles cannot affect the test.
	script := "#!/bin/sh\nexec " + shell + " " + args +
		" -c '. \"$1\"; eval \"$2\"' radar-env-test \"${0%/*}/profile\" \"$4\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", wrapper)
	return profilePath + ".starts"
}

func TestGetShellEnv(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/zsh"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			profile := `printf 'profile banner\n'
KUBECONFIG='/tmp/a:/tmp/b'
export AWS_REGION=eu-west-1
export RADAR_HUB_URL=https://hub.example.test
export ANTHROPIC_AUTH_TOKEN='fake=token=='
export CLAUDE_CODE_USE_BEDROCK=1
export ANTHROPIC_CUSTOM_HEADERS=$'  first=value\nKUBECONFIG=/tmp/wrong\nANTHROPIC_FAKE_NAME=not-a-variable\nlast=value  \n'
export ANTHROPIC_MARKERS='__RADAR_ENV_START____RADAR_ENV_END__'
export ANTHROPIC_CONTROL_BYTES=$'one\001two\002UNRELATED\001three'
ANTHROPIC_UNEXPORTED=not-captured
export UNRELATED=not-captured
`
			if shell == "/bin/bash" {
				profile += "ANTHROPIC_function() { printf hello; }\nexport -f ANTHROPIC_function\n"
			}
			startsPath := setupEnvTestShell(t, shell, profile)
			got := getShellEnv(shellEnvVars, shellEnvPrefixes)
			want := map[string]string{
				"PATH":                     "/usr/bin:/bin",
				"KUBECONFIG":               "/tmp/a:/tmp/b",
				"AWS_REGION":               "eu-west-1",
				"RADAR_HUB_URL":            "https://hub.example.test",
				"ANTHROPIC_AUTH_TOKEN":     "fake=token==",
				"CLAUDE_CODE_USE_BEDROCK":  "1",
				"ANTHROPIC_CUSTOM_HEADERS": "  first=value\nKUBECONFIG=/tmp/wrong\nANTHROPIC_FAKE_NAME=not-a-variable\nlast=value  \n",
				"ANTHROPIC_MARKERS":        "__RADAR_ENV_START____RADAR_ENV_END__",
				"ANTHROPIC_CONTROL_BYTES":  "one\x01two\x02UNRELATED\x01three",
			}
			for _, key := range shellEnvVars {
				if _, ok := want[key]; !ok {
					want[key] = ""
				}
			}
			for key, value := range want {
				if actual, ok := got[key]; !ok || actual != value {
					t.Errorf("%s: got %q (present=%t), want %q", key, actual, ok, value)
				}
			}
			for key := range got {
				if _, ok := want[key]; !ok {
					t.Errorf("unexpected captured key: %s", key)
				}
			}
			starts, err := os.ReadFile(startsPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(starts) != "x" {
				t.Errorf("shell initialized %d times, want once", len(starts))
			}
		})
	}
}

func TestGetShellEnvFailure(t *testing.T) {
	for _, profile := range []string{"exit 1\n", "export PATH=/nonexistent\n"} {
		t.Run(strings.TrimSpace(profile), func(t *testing.T) {
			setupEnvTestShell(t, "/bin/bash", profile)
			if got := getShellEnv(shellEnvVars, shellEnvPrefixes); got != nil {
				t.Errorf("capture should fail, got %v", got)
			}
		})
	}
}

func TestEnrichEnvPrecedenceAndDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, original, shellValue, warning string
	}{
		{"neither source", "", "", "not found in login shell"},
		{"shell only", "", "/tmp/shell-a:/tmp/shell-b", ""},
		{"both sources", "/tmp/process", "/tmp/shell-a:/tmp/shell-b", "login shell value ignored"},
		{"process only", "/tmp/process", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range shellEnvVars {
				t.Setenv(key, "")
			}
			t.Setenv("PATH", "/process/path")
			t.Setenv("KUBECONFIG", tc.original)
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-token")
			t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
			setupEnvTestShell(t, "/bin/bash", "export AWS_REGION=eu-west-1\n"+
				"export ANTHROPIC_AUTH_TOKEN=shell-token\nexport CLAUDE_CODE_USE_BEDROCK=1\n"+
				"KUBECONFIG='"+tc.shellValue+"'\n")
			errorlog.Reset()
			t.Cleanup(errorlog.Reset)
			t.Cleanup(func() { k8s.SetEnrichedKubeconfigFromShell(false) })
			enrichEnv()
			wantConfig := tc.original
			if wantConfig == "" {
				wantConfig = tc.shellValue
			}
			for key, value := range map[string]string{
				"PATH": "/usr/bin:/bin", "KUBECONFIG": wantConfig, "AWS_REGION": "eu-west-1",
				"ANTHROPIC_AUTH_TOKEN": "process-token", "CLAUDE_CODE_USE_BEDROCK": "1",
			} {
				if got := os.Getenv(key); got != value {
					t.Errorf("%s: got %q, want %q", key, got, value)
				}
			}
			entries := errorlog.GetEntries()
			if tc.warning == "" {
				if len(entries) != 0 {
					t.Errorf("unexpected diagnostics: %v", entries)
				}
			} else if len(entries) != 1 || entries[0].Source != "env-enrich" ||
				!strings.Contains(entries[0].Message, tc.warning) {
				t.Errorf("diagnostics=%v, want warning containing %q", entries, tc.warning)
			}
		})
	}
}
