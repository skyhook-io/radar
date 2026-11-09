//go:build !windows

// AppImage launches are Linux-only; these cases use Unix paths and symlinks.

package desktopenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectAppImage(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "image")
	exe := filepath.Join(appDir, "usr", "bin", "radar-desktop")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling whose name starts with the image directory's name.
	otherExe := filepath.Join(root, "image-other", "radar-desktop")
	if err := os.MkdirAll(filepath.Dir(otherExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherExe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedAppDir := filepath.Join(root, "link")
	if err := os.Symlink(appDir, linkedAppDir); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(root, "work")

	tests := []struct {
		name string
		env  map[string]string
		exe  string
		want appImage
	}{
		{name: "running from the image", env: map[string]string{"APPDIR": appDir, "OWD": launch}, exe: exe,
			want: appImage{inImage: true, launchDir: launch}},
		{name: "image reached through a symlink", env: map[string]string{"APPDIR": linkedAppDir, "OWD": launch}, exe: exe,
			want: appImage{inImage: true, launchDir: launch}},
		{name: "variables inherited from another AppImage", env: map[string]string{"APPIMAGE": "/opt/Other.AppImage", "APPDIR": appDir, "OWD": launch}, exe: otherExe,
			want: appImage{}},
		{name: "no APPDIR", env: map[string]string{"APPIMAGE": "/opt/Radar.AppImage", "OWD": launch}, exe: exe,
			want: appImage{}},
		{name: "APPDIR that does not exist", env: map[string]string{"APPDIR": filepath.Join(root, "gone"), "OWD": launch}, exe: exe,
			want: appImage{}},
		{name: "in the image without OWD", env: map[string]string{"APPDIR": appDir}, exe: exe,
			want: appImage{inImage: true}},
		{name: "in the image with a relative OWD", env: map[string]string{"APPDIR": appDir, "OWD": "work"}, exe: exe,
			want: appImage{inImage: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectAppImage(lookupFrom(tt.env), tt.exe); got != tt.want {
				t.Errorf("detectAppImage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveAgainst(t *testing.T) {
	launch := filepath.Join(t.TempDir(), "work")
	abs := filepath.Join(t.TempDir(), "kubeconfig")

	tests := []struct {
		name string
		dir  string
		path string
		want string
	}{
		{name: "relative path", dir: launch, path: "kube/config", want: launch + "/kube/config"},
		{name: "dot path", dir: launch, path: "./config", want: launch + "/./config"},
		{name: "parent through a symlink stays unresolved", dir: launch, path: "current/../ai-runs.db", want: launch + "/current/../ai-runs.db"},
		{name: "absolute path", dir: launch, path: abs, want: abs},
		{name: "home path", dir: launch, path: "~/.kube/config", want: "~/.kube/config"},
		{name: "empty path", dir: launch, path: "", want: ""},
		{name: "outside an AppImage", dir: "", path: "kube/config", want: "kube/config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAgainst(tt.dir, tt.path); got != tt.want {
				t.Errorf("resolveAgainst(%q, %q) = %q, want %q", tt.dir, tt.path, got, tt.want)
			}
		})
	}
}

func TestResolvedLaunchEnv(t *testing.T) {
	launch := "/home/u/work"
	sep := string(os.PathListSeparator)
	env := map[string]string{
		"KUBECONFIG":       strings.Join([]string{"kc.yaml", "/etc/kube/admin", "~/.kube/config", "", "../shared/kc"}, sep),
		"RADAR_AI_CLI_BIN": "codex",
		"HELM_PLUGINS":     strings.Join([]string{"plugins", "/opt/helm/plugins"}, sep),
		"SSL_CERT_FILE":    "certs/ca.pem",
		"DOCKER_CONFIG":    "creds/docker",
		"AWS_CONFIG_FILE":  "/home/u/.aws/config",
		"TMPDIR":           "",
		"UNRELATED_PATH":   "relative/file",
	}
	got := resolvedLaunchEnv(launch, lookupFrom(env))
	want := map[string]string{
		"KUBECONFIG":    strings.Join([]string{"/home/u/work/kc.yaml", "/etc/kube/admin", "~/.kube/config", "", "/home/u/work/../shared/kc"}, sep),
		"HELM_PLUGINS":  strings.Join([]string{"/home/u/work/plugins", "/opt/helm/plugins"}, sep),
		"SSL_CERT_FILE": "/home/u/work/certs/ca.pem",
		"DOCKER_CONFIG": "/home/u/work/creds/docker",
	}
	if len(got) != len(want) {
		t.Fatalf("resolvedLaunchEnv() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	env["RADAR_AI_CLI_BIN"] = "bin/codex"
	if got := resolvedLaunchEnv(launch, lookupFrom(env))["RADAR_AI_CLI_BIN"]; got != "/home/u/work/bin/codex" {
		t.Errorf("RADAR_AI_CLI_BIN with a directory = %q, want it resolved", got)
	}
	if got := resolvedLaunchEnv("", lookupFrom(env)); len(got) != 0 {
		t.Errorf("outside an AppImage nothing changes, got %v", got)
	}
}

// A relative path is resolved by walking it from the launch folder, symlinks
// included, the way the shell that started the AppImage would.
func TestResolveAgainstFollowsSymlinksLikeTheShell(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launch")
	releases := filepath.Join(root, "data", "releases")
	for _, d := range []string{launch, filepath.Join(releases, "v1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(releases, "ai-runs.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(releases, "v1"), filepath.Join(launch, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resolveAgainst(launch, "current/../ai-runs.db")); err != nil {
		t.Errorf("current/../ai-runs.db from the launch folder should reach data/releases/ai-runs.db: %v", err)
	}
}

func TestChildDirFor(t *testing.T) {
	launch := t.TempDir()
	home := func() (string, error) { return "/home/u", nil }
	if got := childDirFor("", home); got != "" {
		t.Errorf("outside an AppImage: %q, want \"\" (inherit)", got)
	}
	if got := childDirFor(launch, home); got != launch {
		t.Errorf("existing launch folder: %q, want %q", got, launch)
	}
	if got := childDirFor(filepath.Join(launch, "removed"), home); got != "/home/u" {
		t.Errorf("removed launch folder: %q, want the home folder", got)
	}
}
