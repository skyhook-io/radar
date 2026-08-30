//go:build linux

package desktopenv

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

func collect() *Snapshot {
	s := &Snapshot{
		SessionType:        os.Getenv("XDG_SESSION_TYPE"),
		DesktopEnvironment: os.Getenv("XDG_CURRENT_DESKTOP"),
		DisplayServer:      displayServer(),
		RenderOverrides:    readAll(OverrideKeys),
		Sandbox:            readSet(SandboxKeys),
		WebKitLibrary:      webviewLibrary(),
		GPUPolicy:          GPUPolicy(),
	}
	return s
}

func displayServer() string {
	var parts []string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		parts = append(parts, "wayland")
	}
	if os.Getenv("DISPLAY") != "" {
		parts = append(parts, "x11")
	}
	return strings.Join(parts, "+")
}

// readAll returns every key, including absent ones, so a reader can tell an
// override that was never applied from one that was applied as empty.
func readAll(keys []string) []EnvVar {
	out := make([]EnvVar, 0, len(keys))
	for _, k := range keys {
		v, set := os.LookupEnv(k)
		out = append(out, EnvVar{Key: k, Value: v, Set: set})
	}
	return out
}

// readSet returns only the keys carrying a value. An empty sandbox marker says
// nothing, so unlike readAll there is no tri-state worth reporting here.
func readSet(keys []string) []EnvVar {
	var out []EnvVar
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			out = append(out, EnvVar{Key: k, Value: v, Set: true})
		}
	}
	return out
}

func webviewLibrary() string { return webKitLibrary("/proc/self/maps") }

// webKitLibrary returns the basename of the webview library mapped into this
// process. The soname's trailing version identifies the WebKitGTK build, which
// a bug report otherwise has no way to state. The prefix is matched loosely
// because the library is named per GTK generation (libwebkit2gtk-4.x on GTK3,
// libwebkitgtk-6.0 on GTK4) and reporting nothing is the worst outcome.
func webKitLibrary(mapsPath string) string {
	f, err := os.Open(mapsPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// A maps line ends with the mapped file's path, if it has one.
		idx := strings.IndexByte(line, '/')
		if idx < 0 {
			continue
		}
		path := strings.TrimSuffix(line[idx:], " (deleted)")
		base := filepath.Base(path)
		if strings.HasPrefix(base, "libwebkit") {
			return base
		}
	}
	return ""
}
