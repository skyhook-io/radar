package ai

import (
	"runtime"
	"strings"
	"testing"
)

// TestFilterEnvKeepsPlatformBase pins that a minimized env still contains what the
// platform needs to start a process and find the agent's own credentials. Dropping
// USERPROFILE/APPDATA on Windows leaves the CLI unable to sign in, and dropping
// SYSTEMROOT can stop it from launching at all — a failure that never reproduces
// on the maintainers' machines.
func TestFilterEnvKeepsPlatformBase(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/home/u", "TMPDIR=/tmp",
		"USERPROFILE=C:\\Users\\u", "APPDATA=C:\\Users\\u\\AppData\\Roaming",
		"LOCALAPPDATA=C:\\Users\\u\\AppData\\Local", "SystemRoot=C:\\Windows",
		"TEMP=C:\\Temp", "COMSPEC=C:\\Windows\\system32\\cmd.exe",
		"AWS_SECRET_ACCESS_KEY=shhh", "KUBECONFIG=/home/u/.kube/config",
	}
	got := filterEnv(environ, []string{"KUBECONFIG"}, nil)

	want := []string{"PATH=/usr/bin", "KUBECONFIG=/home/u/.kube/config"}
	if runtime.GOOS == "windows" {
		want = append(want,
			"USERPROFILE=C:\\Users\\u",
			"APPDATA=C:\\Users\\u\\AppData\\Roaming",
			"LOCALAPPDATA=C:\\Users\\u\\AppData\\Local",
			// Windows env names are case-insensitive: SystemRoot IS SYSTEMROOT.
			"SystemRoot=C:\\Windows",
			"TEMP=C:\\Temp",
			"COMSPEC=C:\\Windows\\system32\\cmd.exe",
		)
	} else {
		want = append(want, "HOME=/home/u", "TMPDIR=/tmp")
	}
	for _, kv := range want {
		if !envHas(got, kv) {
			t.Errorf("filterEnv dropped %q; got %v", kv, got)
		}
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") {
			t.Errorf("filterEnv leaked an unlisted secret: %q", kv)
		}
	}
}

func TestFilterEnvPrefixes(t *testing.T) {
	environ := []string{"LC_ALL=C", "LC_TIME=C", "LANG=C", "NOPE=1"}
	got := filterEnv(environ, nil, []string{"LC_"})
	if len(got) != 2 || !envHas(got, "LC_ALL=C") || !envHas(got, "LC_TIME=C") {
		t.Errorf("expected only the LC_ prefix matches; got %v", got)
	}
}
