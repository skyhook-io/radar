// Package desktopenv reports the host rendering environment the desktop app
// is running in. On Linux the display server, compositor and WebKit render
// overrides decide whether the webview works at all, and none of it is
// derivable from anything else Radar already collects.
package desktopenv

import "sync/atomic"

// Env var groups. Shared so startup logging and the diagnostics snapshot can
// never report different sets.
var (
	SessionKeys  = []string{"XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "WAYLAND_DISPLAY", "DISPLAY"}
	OverrideKeys = []string{"GDK_BACKEND", "GSK_RENDERER", "WEBKIT_DISABLE_DMABUF_RENDERER", "WEBKIT_DISABLE_COMPOSITING_MODE", "GTK_THEME"}
	SandboxKeys  = []string{"SNAP", "FLATPAK_ID", "container"}
)

// EnvVar is one environment variable. A slice preserves declaration order, so
// two snapshots of the same host always read the same way.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Set separates an explicitly empty value from an absent one. The
	// difference decides behavior: the desktop app's WebKit defaults are
	// skipped whenever a variable is merely present, so an empty
	// WEBKIT_DISABLE_DMABUF_RENDERER leaves the DMABUF renderer on while
	// looking, to os.Getenv, exactly like an untouched host.
	Set bool `json:"set"`
}

// Snapshot describes the desktop rendering environment.
type Snapshot struct {
	SessionType        string `json:"sessionType,omitempty"`
	DesktopEnvironment string `json:"desktopEnvironment,omitempty"`
	// DisplayServer is derived from WAYLAND_DISPLAY and DISPLAY rather than
	// XDG_SESSION_TYPE, which login managers frequently leave unset.
	DisplayServer string `json:"displayServer,omitempty"`
	// RenderOverrides lists every key in OverrideKeys, including unset ones —
	// "the compositing override was not applied" is as diagnostic as its value.
	RenderOverrides []EnvVar `json:"renderOverrides,omitempty"`
	Sandbox         []EnvVar `json:"sandbox,omitempty"`
	// WebKitLibrary is the webview library actually mapped into this process,
	// which is the version that matters and not necessarily the one the
	// package manager reports.
	WebKitLibrary string `json:"webkitLibrary,omitempty"`
	GPUPolicy     string `json:"gpuPolicy,omitempty"`
}

var gpuPolicy atomic.Value

// SetGPUPolicy records the webview hardware-acceleration policy the app asked
// for. Called by the desktop binary; the CLI leaves it unset.
func SetGPUPolicy(p string) {
	gpuPolicy.Store(p)
}

// GPUPolicy returns the recorded policy, or "" if none was set.
func GPUPolicy() string {
	p, _ := gpuPolicy.Load().(string)
	return p
}

// Collect returns the current environment, or nil on platforms where none of
// this applies.
func Collect() *Snapshot { return collect() }

// WebviewLibrary returns the webview library mapped into this process, or ""
// where that cannot be determined. Exposed separately from Collect so startup
// logging can name the build before anything else has run.
func WebviewLibrary() string { return webviewLibrary() }
