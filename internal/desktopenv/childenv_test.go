package desktopenv

import (
	"reflect"
	"testing"
)

func TestPlanChildEnv(t *testing.T) {
	image := map[string]string{
		// Saved by AppRun before it set up GTK.
		"RADAR_APPIMAGE_KEYS":               "GDK_BACKEND XDG_DATA_DIRS GTK_PATH GTK_THEME WEBKIT_INJECTED_BUNDLE_PATH",
		"RADAR_APPIMAGE_ORIG_XDG_DATA_DIRS": "/usr/local/share:/usr/share",
		"RADAR_APPIMAGE_ORIG_GTK_THEME":     "",
		"RADAR_APPIMAGE_ORIG_GDK_BACKEND":   "wayland",
		// Set for the bundled GTK.
		"GDK_BACKEND":                 "x11",
		"XDG_DATA_DIRS":               "/tmp/.mount_x/usr/share:/usr/share:/usr/local/share:/usr/share",
		"GTK_PATH":                    "/tmp/.mount_x/usr/lib/gtk-3.0",
		"GTK_THEME":                   "Adwaita:dark",
		"WEBKIT_INJECTED_BUNDLE_PATH": "/tmp/.mount_x/usr/lib/webkit2gtk-4.1/injected-bundle/",
		// Launcher variables.
		"APPIMAGE": "/home/u/Radar.AppImage",
		"APPDIR":   "/tmp/.mount_x",
		"ARGV0":    "./Radar.AppImage",
		"OWD":      "/home/u/work",
		// Untouched.
		"HOME": "/home/u",
	}
	changes := planChildEnv(lookupFrom(image))
	got := map[string]EnvChange{}
	for _, c := range changes {
		got[c.Key] = c
	}

	want := map[string]EnvChange{
		"GDK_BACKEND":   {Key: "GDK_BACKEND", Child: "wayland", ChildSet: true, Image: "x11", ImageSet: true},
		"XDG_DATA_DIRS": {Key: "XDG_DATA_DIRS", Child: "/usr/local/share:/usr/share", ChildSet: true, Image: image["XDG_DATA_DIRS"], ImageSet: true},
		// Set to empty before launch: stays set to empty, not unset.
		"GTK_THEME": {Key: "GTK_THEME", Child: "", ChildSet: true, Image: "Adwaita:dark", ImageSet: true},
		// Not set before launch: unset for children.
		"GTK_PATH":                          {Key: "GTK_PATH", Image: image["GTK_PATH"], ImageSet: true},
		"WEBKIT_INJECTED_BUNDLE_PATH":       {Key: "WEBKIT_INJECTED_BUNDLE_PATH", Image: image["WEBKIT_INJECTED_BUNDLE_PATH"], ImageSet: true},
		"APPIMAGE":                          {Key: "APPIMAGE", Image: image["APPIMAGE"], ImageSet: true},
		"APPDIR":                            {Key: "APPDIR", Image: image["APPDIR"], ImageSet: true},
		"ARGV0":                             {Key: "ARGV0", Image: image["ARGV0"], ImageSet: true},
		"OWD":                               {Key: "OWD", Image: image["OWD"], ImageSet: true},
		"RADAR_APPIMAGE_KEYS":               {Key: "RADAR_APPIMAGE_KEYS", Image: image["RADAR_APPIMAGE_KEYS"], ImageSet: true},
		"RADAR_APPIMAGE_ORIG_XDG_DATA_DIRS": {Key: "RADAR_APPIMAGE_ORIG_XDG_DATA_DIRS", Image: image["RADAR_APPIMAGE_ORIG_XDG_DATA_DIRS"], ImageSet: true},
		"RADAR_APPIMAGE_ORIG_GTK_THEME":     {Key: "RADAR_APPIMAGE_ORIG_GTK_THEME", Image: "", ImageSet: true},
		"RADAR_APPIMAGE_ORIG_GDK_BACKEND":   {Key: "RADAR_APPIMAGE_ORIG_GDK_BACKEND", Image: "wayland", ImageSet: true},
	}
	if !reflect.DeepEqual(got, want) {
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s: got %+v, want %+v", k, got[k], w)
			}
		}
		for k, g := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected change %+v", g)
			}
		}
	}
}

func TestPlanChildEnvWithoutAppRunRecord(t *testing.T) {
	if got := planChildEnv(lookupFrom(map[string]string{"GDK_BACKEND": "x11", "APPDIR": "/tmp/.mount_x"})); got != nil {
		t.Errorf("without RADAR_APPIMAGE_KEYS nothing is known about the original environment; got %+v", got)
	}
}

func TestWebviewLookup(t *testing.T) {
	t.Cleanup(func() { RecordWebviewEnv(nil) })
	RecordWebviewEnv([]EnvChange{{Key: "GDK_BACKEND", Image: "x11", ImageSet: true}})
	process := lookupFrom(map[string]string{"GTK_THEME": "Adwaita"})
	if v, ok := webviewLookup("GDK_BACKEND", process); v != "x11" || !ok {
		t.Errorf("GDK_BACKEND = %q,%v, want the webview's x11", v, ok)
	}
	if v, ok := webviewLookup("GTK_THEME", process); v != "Adwaita" || !ok {
		t.Errorf("GTK_THEME = %q,%v, want the process value", v, ok)
	}
}
