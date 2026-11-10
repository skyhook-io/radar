//go:build linux

package desktopenv

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMaps(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "maps")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestWebKitLibrary(t *testing.T) {
	tests := []struct {
		name string
		maps string
		want string
	}{
		{
			name: "finds the mapped webkit library",
			maps: `55a1b2c00000-55a1b2c21000 r--p 00000000 08:01 1000  /usr/bin/radar-desktop
7f8c1c000000-7f8c1c021000 rw-p 00000000 00:00 0  [heap]
7f8c1d000000-7f8c1f000000 r--p 00000000 08:01 2000  /usr/lib/x86_64-linux-gnu/libwebkit2gtk-4.1.so.0.13.6
7f8c20000000-7f8c20100000 r--p 00000000 08:01 3000  /usr/lib/x86_64-linux-gnu/libgtk-3.so.0.2405.32
`,
			want: "libwebkit2gtk-4.1.so.0.13.6",
		},
		{
			name: "strips the deleted marker left by a package upgrade",
			maps: "7f8c1d000000-7f8c1f000000 r--p 00000000 08:01 2000  /usr/lib/libwebkit2gtk-4.1.so.0.13.6 (deleted)\n",
			want: "libwebkit2gtk-4.1.so.0.13.6",
		},
		{
			name: "finds the GTK4-generation library name",
			maps: "7f8c1d000000-7f8c1f000000 r--p 00000000 08:01 2000  /usr/lib/x86_64-linux-gnu/libwebkitgtk-6.0.so.4.8.0\n",
			want: "libwebkitgtk-6.0.so.4.8.0",
		},
		{
			name: "reports nothing when no webview is loaded",
			maps: `55a1b2c00000-55a1b2c21000 r--p 00000000 08:01 1000  /usr/bin/radar
7f8c1c000000-7f8c1c021000 rw-p 00000000 00:00 0  [stack]
`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webKitLibrary(writeMaps(t, tt.maps)); got != tt.want {
				t.Errorf("webKitLibrary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebKitLibraryMissingFile(t *testing.T) {
	if got := webKitLibrary(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Errorf("webKitLibrary() = %q, want empty", got)
	}
}

func TestDisplayServer(t *testing.T) {
	tests := []struct {
		name    string
		wayland string
		x11     string
		want    string
	}{
		{"wayland only", "wayland-0", "", "wayland"},
		{"x11 only", "", ":0", "x11"},
		{"xwayland reports both", "wayland-0", ":0", "wayland+x11"},
		{"headless", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WAYLAND_DISPLAY", tt.wayland)
			t.Setenv("DISPLAY", tt.x11)
			if got := displayServer(); got != tt.want {
				t.Errorf("displayServer() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Absent overrides must still be reported — "the compositing override was not
// applied" is what distinguishes a fixed host from an unfixed one.
func TestReadAllKeepsAbsentKeys(t *testing.T) {
	t.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	os.Unsetenv("WEBKIT_DISABLE_COMPOSITING_MODE")

	got := readAll(OverrideKeys)
	if len(got) != len(OverrideKeys) {
		t.Fatalf("readAll() returned %d vars, want %d", len(got), len(OverrideKeys))
	}

	byKey := map[string]EnvVar{}
	for i, v := range got {
		if v.Key != OverrideKeys[i] {
			t.Errorf("readAll()[%d].Key = %q, want %q (declaration order)", i, v.Key, OverrideKeys[i])
		}
		byKey[v.Key] = v
	}
	if dmabuf := byKey["WEBKIT_DISABLE_DMABUF_RENDERER"]; dmabuf.Value != "1" || !dmabuf.Set {
		t.Errorf("DMABUF override = %+v, want value \"1\" and Set", dmabuf)
	}
	if comp := byKey["WEBKIT_DISABLE_COMPOSITING_MODE"]; comp.Value != "" || comp.Set {
		t.Errorf("compositing override = %+v, want empty and not Set", comp)
	}
}

// An override set to the empty string is NOT the same as an absent one: the
// desktop app skips its own WebKit defaults for any variable that is merely
// present, and WebKit reads an empty value as "disable". Collapsing the two
// would hide exactly the cause this section exists to surface.
func TestReadAllDistinguishesEmptyFromAbsent(t *testing.T) {
	t.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "")
	os.Unsetenv("WEBKIT_DISABLE_COMPOSITING_MODE")

	byKey := map[string]EnvVar{}
	for _, v := range readAll(OverrideKeys) {
		byKey[v.Key] = v
	}

	empty := byKey["WEBKIT_DISABLE_DMABUF_RENDERER"]
	if !empty.Set || empty.Value != "" {
		t.Errorf("empty-but-set override = %+v, want Set with empty value", empty)
	}
	absent := byKey["WEBKIT_DISABLE_COMPOSITING_MODE"]
	if absent.Set {
		t.Errorf("absent override = %+v, want not Set", absent)
	}
}

func TestReadSetDropsUnsetKeys(t *testing.T) {
	t.Setenv("SNAP", "")
	t.Setenv("FLATPAK_ID", "io.skyhook.Radar")
	t.Setenv("container", "")

	got := readSet(SandboxKeys)
	if len(got) != 1 {
		t.Fatalf("readSet() = %v, want exactly one entry", got)
	}
	if got[0].Key != "FLATPAK_ID" || got[0].Value != "io.skyhook.Radar" || !got[0].Set {
		t.Errorf("readSet()[0] = %+v, want FLATPAK_ID=io.skyhook.Radar and Set", got[0])
	}
}

func TestCollectReportsGPUPolicy(t *testing.T) {
	SetGPUPolicy("on-demand")
	t.Cleanup(func() { SetGPUPolicy("") })

	snap := Collect()
	if snap == nil {
		t.Fatal("Collect() = nil on linux")
	}
	if snap.GPUPolicy != "on-demand" {
		t.Errorf("GPUPolicy = %q, want \"on-demand\"", snap.GPUPolicy)
	}
	if len(snap.RenderOverrides) != len(OverrideKeys) {
		t.Errorf("RenderOverrides has %d entries, want %d", len(snap.RenderOverrides), len(OverrideKeys))
	}
}
