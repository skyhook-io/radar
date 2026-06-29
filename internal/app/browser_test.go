package app

import (
	"slices"
	"testing"
)

func TestBrowserCommandUsesPreferredBrowser(t *testing.T) {
	cmd := browserCommand("http://localhost:9280", "firefox", "linux")
	assertCommandArgs(t, cmd.Args, []string{"firefox", "http://localhost:9280"})
}

func TestBrowserCommandUsesMacAppLauncherForAppNames(t *testing.T) {
	cmd := browserCommand("http://localhost:9280", "Google Chrome", "darwin")
	assertCommandArgs(t, cmd.Args, []string{"open", "-a", "Google Chrome", "http://localhost:9280"})
}

func TestBrowserCommandUsesMacAppLauncherForAppBundles(t *testing.T) {
	cmd := browserCommand("http://localhost:9280", "/Applications/Firefox.app", "darwin")
	assertCommandArgs(t, cmd.Args, []string{"open", "-a", "/Applications/Firefox.app", "http://localhost:9280"})
}

func TestBrowserCommandUsesMacExecutablePathDirectly(t *testing.T) {
	cmd := browserCommand("http://localhost:9280", "/Applications/Firefox.app/Contents/MacOS/firefox", "darwin")
	assertCommandArgs(t, cmd.Args, []string{"/Applications/Firefox.app/Contents/MacOS/firefox", "http://localhost:9280"})
}

func TestBrowserCommandUsesOSDefaults(t *testing.T) {
	tests := []struct {
		name string
		goos string
		args []string
	}{
		{name: "darwin", goos: "darwin", args: []string{"open", "http://localhost:9280"}},
		{name: "linux", goos: "linux", args: []string{"xdg-open", "http://localhost:9280"}},
		{name: "windows", goos: "windows", args: []string{"rundll32", "url.dll,FileProtocolHandler", "http://localhost:9280"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := browserCommand("http://localhost:9280", "", tt.goos)
			assertCommandArgs(t, cmd.Args, tt.args)
		})
	}
}

func TestBrowserCommandReturnsNilForUnsupportedOS(t *testing.T) {
	if cmd := browserCommand("http://localhost:9280", "", "plan9"); cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
}

func assertCommandArgs(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("Args = %v, want %v", got, want)
	}
}
