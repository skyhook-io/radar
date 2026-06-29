package main

import (
	"reflect"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
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
