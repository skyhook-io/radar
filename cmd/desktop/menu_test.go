package main

import (
	"reflect"
	goruntime "runtime"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

func TestCreateMenuFileMenuExposesSupportedActions(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", "linux")
	fileMenu := findSubmenu(t, appMenu, "File")

	got := menuLabels(fileMenu)
	want := []string{"Settings...", "Quit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("File menu labels = %v, want %v", got, want)
	}
}

// Close Window hides the whole app rather than closing anything, so it only
// belongs where the Dock can bring the app back. Its absence off macOS is
// pinned by TestCreateMenuFileMenuExposesSupportedActions' exact label match.
func TestCreateMenuAddsCloseWindowOnMacOnly(t *testing.T) {
	fileMenu := findSubmenu(t, createMenu(&DesktopApp{}, "test", "darwin"), "File")

	got := menuLabels(fileMenu)
	want := []string{"Settings...", "Close Window"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("File menu labels = %v, want %v", got, want)
	}
	if findMenuItem(t, fileMenu, "Close Window").Click == nil {
		t.Fatal("Close Window has no callback")
	}
}

// macOS puts Quit in the application menu, and binding Cmd+Q on a File item too
// would leave two menu entries racing for the same accelerator. The native
// AppMenu role also carries Hide/Show All, which is how a hidden app is found
// again.
func TestCreateMenuMacDelegatesQuitToTheApplicationMenu(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", "darwin")

	roles := topLevelRoles(appMenu)
	if len(roles) != 1 || roles[0] != menu.AppMenuRole {
		t.Fatalf("top-level roles = %v, want [AppMenuRole]", roles)
	}
	if appMenu.Items[0].Role != menu.AppMenuRole {
		t.Fatalf("first menu item role = %v, want the application menu", appMenu.Items[0].Role)
	}
	if containsLabel(findSubmenu(t, appMenu, "File"), "Quit") {
		t.Fatal("File menu still carries Quit on macOS — Cmd+Q would be bound twice")
	}
	if containsLabel(findSubmenu(t, appMenu, "Help"), "About Radar") {
		t.Fatal("Help menu still carries About on macOS — the application menu already shows it")
	}
}

// Roles are a macOS-only concept in Wails: off macOS a role item falls through
// to an ordinary item with an empty label, so it would render as a blank entry.
func TestCreateMenuOffMacUsesNoRolesAndKeepsFileQuit(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			appMenu := createMenu(&DesktopApp{}, "test", goos)

			if roles := topLevelRoles(appMenu); len(roles) != 0 {
				t.Fatalf("top-level roles on %s = %v, want none", goos, roles)
			}
			if !containsLabel(findSubmenu(t, appMenu, "File"), "Quit") {
				t.Fatalf("File menu on %s has no Quit — the app would have no exit", goos)
			}
			if !containsLabel(findSubmenu(t, appMenu, "Help"), "About Radar") {
				t.Fatalf("Help menu on %s has no About — there is no application menu to carry it", goos)
			}
		})
	}
}

func TestCreateMenuHelpMenuKeepsUpdateAction(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", "linux")
	helpMenu := findSubmenu(t, appMenu, "Help")

	if !containsLabel(helpMenu, "Check for Updates...") {
		t.Fatalf("Help menu is missing Check for Updates action")
	}
}

func TestCreateMenuNativeActionsHaveCallbacks(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", "linux")

	cases := []struct {
		menu string
		item string
	}{
		{"File", "Settings..."},
		{"File", "Quit"},
		{"Help", "Check for Updates..."},
	}
	for _, tc := range cases {
		t.Run(tc.menu+"/"+tc.item, func(t *testing.T) {
			item := findMenuItem(t, findSubmenu(t, appMenu, tc.menu), tc.item)
			if item.Click == nil {
				t.Fatalf("%s -> %s has no callback", tc.menu, tc.item)
			}
		})
	}
}

func TestReloadAcceleratorAvoidsCtrlROffMac(t *testing.T) {
	cases := []struct {
		goos string
		want *keys.Accelerator
	}{
		{"darwin", keys.CmdOrCtrl("r")},
		{"windows", keys.Combo("r", keys.ControlKey, keys.ShiftKey)},
		{"linux", keys.Combo("r", keys.ControlKey, keys.ShiftKey)},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got := reloadAccelerator(tc.goos)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("reloadAccelerator(%q) = %+v, want %+v", tc.goos, got, tc.want)
			}
		})
	}
}

// TestCreateMenuReloadIsWiredToPlatformAccelerator asserts the Reload item uses
// the platform-picked accelerator. On Linux CI (goruntime.GOOS == "linux") this
// executes the non-mac branch for real, proving Ctrl+R is not bound to Reload.
func TestCreateMenuReloadIsWiredToPlatformAccelerator(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", goruntime.GOOS)
	reload := findMenuItem(t, findSubmenu(t, appMenu, "View"), "Reload")

	if reload.Click == nil {
		t.Fatal("Reload item has no callback")
	}
	if !reflect.DeepEqual(reload.Accelerator, reloadAccelerator(goruntime.GOOS)) {
		t.Fatalf("Reload accelerator = %+v, want %+v", reload.Accelerator, reloadAccelerator(goruntime.GOOS))
	}
	if goruntime.GOOS != "darwin" && reflect.DeepEqual(reload.Accelerator, keys.CmdOrCtrl("r")) {
		t.Fatalf("Reload is bound to Ctrl+R on %s — collides with terminal reverse-i-search", goruntime.GOOS)
	}
}

func TestPasteAcceleratorDroppedOnlyOnWindows(t *testing.T) {
	cases := []struct {
		goos string
		want *keys.Accelerator
	}{
		{"windows", nil},
		{"darwin", keys.CmdOrCtrl("v")},
		{"linux", keys.CmdOrCtrl("v")},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got := pasteAccelerator(tc.goos)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pasteAccelerator(%q) = %+v, want %+v", tc.goos, got, tc.want)
			}
		})
	}
}

func TestClipboardAcceleratorDroppedOnlyOnLinux(t *testing.T) {
	for _, key := range []string{"c", "x"} {
		cases := []struct {
			goos string
			want *keys.Accelerator
		}{
			{"linux", nil},
			{"darwin", keys.CmdOrCtrl(key)},
			{"windows", keys.CmdOrCtrl(key)},
		}
		for _, tc := range cases {
			t.Run(tc.goos+"/"+key, func(t *testing.T) {
				got := clipboardAccelerator(tc.goos, key)
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("clipboardAccelerator(%q, %q) = %+v, want %+v", tc.goos, key, got, tc.want)
				}
			})
		}
	}
}

// TestCreateMenuCutCopyAcceleratorMatchesPlatform guards the Linux terminal
// contract: on Linux the Cut/Copy accelerators must be unbound so Ctrl+C reaches
// xterm as SIGINT rather than firing a menu Copy, while the items keep their
// click callbacks (covered by TestCreateMenuCutCopyAreClickableOffMac).
func TestCreateMenuCutCopyAcceleratorMatchesPlatform(t *testing.T) {
	editMenu := findSubmenu(t, createMenu(&DesktopApp{}, "test", goruntime.GOOS), "Edit")

	for _, tc := range []struct{ label, key string }{{"Cut", "x"}, {"Copy", "c"}} {
		t.Run(tc.label, func(t *testing.T) {
			item := findMenuItem(t, editMenu, tc.label)
			want := clipboardAccelerator(goruntime.GOOS, tc.key)
			if !reflect.DeepEqual(item.Accelerator, want) {
				t.Fatalf("%s accelerator = %+v, want %+v", tc.label, item.Accelerator, want)
			}
			if goruntime.GOOS == "linux" && item.Accelerator != nil {
				t.Fatalf("%s is bound to %+v on linux — GTK would swallow Ctrl+C before the terminal sees it", tc.label, item.Accelerator)
			}
		})
	}
}

// TestCreateMenuPasteKeepsCallbackWithoutDoubleBinding pins both halves of the
// paste contract: the callback must stay (a nil one leaves the item inert on
// Windows), and on Windows no accelerator may be bound alongside it because
// winc does not consume the key.
func TestCreateMenuPasteKeepsCallbackWithoutDoubleBinding(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test", goruntime.GOOS)
	paste := findMenuItem(t, findSubmenu(t, appMenu, "Edit"), "Paste")

	if paste.Click == nil {
		t.Fatal("Paste item has no callback")
	}
	if !reflect.DeepEqual(paste.Accelerator, pasteAccelerator(goruntime.GOOS)) {
		t.Fatalf("Paste accelerator = %+v, want %+v", paste.Accelerator, pasteAccelerator(goruntime.GOOS))
	}
	if goruntime.GOOS == "windows" && paste.Accelerator != nil {
		t.Fatalf("Paste is bound to %+v on windows — the webview pastes on Ctrl+V too, so the clipboard lands twice", paste.Accelerator)
	}
}

func TestClipboardDelegateSkipsOnlyMac(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			if clipboardDelegate(goos, &DesktopApp{}, "copy") == nil {
				t.Fatalf("clipboardDelegate(%q) = nil, want a callback", goos)
			}
		})
	}
	if clipboardDelegate("darwin", &DesktopApp{}, "copy") != nil {
		t.Fatal("clipboardDelegate(\"darwin\") returned a callback, want nil for the responder chain")
	}
}

// TestCreateMenuCutCopyAreClickableOffMac guards the inert-menu-item case: off
// macOS a nil callback binds no handler, so Edit -> Cut/Copy would do nothing.
func TestCreateMenuCutCopyAreClickableOffMac(t *testing.T) {
	if goruntime.GOOS == "darwin" {
		t.Skip("macOS delegates Cut/Copy to the native responder chain")
	}
	editMenu := findSubmenu(t, createMenu(&DesktopApp{}, "test", goruntime.GOOS), "Edit")

	for _, label := range []string{"Cut", "Copy"} {
		t.Run(label, func(t *testing.T) {
			if findMenuItem(t, editMenu, label).Click == nil {
				t.Fatalf("%s has no callback on %s — the menu entry would be inert", label, goruntime.GOOS)
			}
		})
	}
}

func topLevelRoles(m *menu.Menu) []menu.Role {
	var roles []menu.Role
	for _, item := range m.Items {
		if item.Role != 0 {
			roles = append(roles, item.Role)
		}
	}
	return roles
}

func findSubmenu(t *testing.T, root *menu.Menu, label string) *menu.Menu {
	t.Helper()
	for _, item := range root.Items {
		if item.Label == label && item.SubMenu != nil {
			return item.SubMenu
		}
	}
	t.Fatalf("submenu %q not found", label)
	return nil
}

func findMenuItem(t *testing.T, m *menu.Menu, label string) *menu.MenuItem {
	t.Helper()
	for _, item := range m.Items {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("menu item %q not found", label)
	return nil
}

func menuLabels(m *menu.Menu) []string {
	var labels []string
	for _, item := range m.Items {
		if item.Type == menu.SeparatorType {
			continue
		}
		labels = append(labels, item.Label)
	}
	return labels
}

func containsLabel(m *menu.Menu, label string) bool {
	for _, item := range m.Items {
		if item.Label == label {
			return true
		}
	}
	return false
}
