package main

import (
	"reflect"
	goruntime "runtime"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

func TestCreateMenuFileMenuExposesSupportedActions(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test")
	fileMenu := findSubmenu(t, appMenu, "File")

	got := menuLabels(fileMenu)
	want := []string{"Settings...", "Quit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("File menu labels = %v, want %v", got, want)
	}
}

func TestCreateMenuHelpMenuKeepsUpdateAction(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test")
	helpMenu := findSubmenu(t, appMenu, "Help")

	if !containsLabel(helpMenu, "Check for Updates...") {
		t.Fatalf("Help menu is missing Check for Updates action")
	}
}

func TestCreateMenuNativeActionsHaveCallbacks(t *testing.T) {
	appMenu := createMenu(&DesktopApp{}, "test")

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
	appMenu := createMenu(&DesktopApp{}, "test")
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
