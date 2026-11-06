package desktopenv

import (
	"path/filepath"
	"testing"
)

func TestLaunchDir(t *testing.T) {
	launch := filepath.Join(t.TempDir(), "work")

	tests := []struct {
		name     string
		appImage string
		owd      string
		want     string
	}{
		{name: "outside an AppImage", appImage: "", owd: launch, want: ""},
		{name: "inside an AppImage", appImage: "/opt/Radar.AppImage", owd: launch, want: launch},
		{name: "OWD missing", appImage: "/opt/Radar.AppImage", owd: "", want: ""},
		{name: "OWD relative", appImage: "/opt/Radar.AppImage", owd: "work", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APPIMAGE", tt.appImage)
			t.Setenv("OWD", tt.owd)
			if got := LaunchDir(); got != tt.want {
				t.Errorf("LaunchDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveLaunchPath(t *testing.T) {
	launch := filepath.Join(t.TempDir(), "work")
	abs := filepath.Join(t.TempDir(), "kubeconfig")

	tests := []struct {
		name     string
		appImage string
		path     string
		want     string
	}{
		{name: "relative path in an AppImage", appImage: "/opt/Radar.AppImage", path: "kube/config", want: filepath.Join(launch, "kube", "config")},
		{name: "dot path in an AppImage", appImage: "/opt/Radar.AppImage", path: "./config", want: filepath.Join(launch, "config")},
		{name: "absolute path in an AppImage", appImage: "/opt/Radar.AppImage", path: abs, want: abs},
		{name: "home path in an AppImage", appImage: "/opt/Radar.AppImage", path: "~/.kube/config", want: "~/.kube/config"},
		{name: "empty path in an AppImage", appImage: "/opt/Radar.AppImage", path: "", want: ""},
		{name: "relative path outside an AppImage", appImage: "", path: "kube/config", want: "kube/config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APPIMAGE", tt.appImage)
			t.Setenv("OWD", launch)
			if got := ResolveLaunchPath(tt.path); got != tt.want {
				t.Errorf("ResolveLaunchPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
