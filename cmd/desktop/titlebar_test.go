package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

func TestTitleBarMenusMirrorTheNativeMenu(t *testing.T) {
	m := createMenu(&DesktopApp{}, "1.2.3", "windows")
	menus := titleBarMenus(m)

	var labels []string
	for _, tm := range menus {
		labels = append(labels, tm.Label)
	}
	if got := strings.Join(labels, ","); got != "File,Edit,View,Help" {
		t.Fatalf("menus = %s", got)
	}

	actions := menuActions(m)
	for _, tm := range menus {
		for _, item := range tm.Items {
			if item.Separator {
				continue
			}
			if actions[tm.Label+"/"+item.Label] == nil {
				t.Errorf("%s/%s is shown but has no action", tm.Label, item.Label)
			}
		}
	}

	shortcuts := map[string]string{}
	for _, tm := range menus {
		for _, item := range tm.Items {
			shortcuts[tm.Label+"/"+item.Label] = item.Shortcut
		}
	}
	for path, want := range map[string]string{
		"File/Settings...": "Ctrl+,",
		"File/Quit":        "Ctrl+Q",
		"Edit/Redo":        "Ctrl+Shift+Z",
		"Edit/Paste":       "Ctrl+V",
		"View/Reload":      "Ctrl+Shift+R",
		"View/Zoom In":     "Ctrl+=",
		"Help/About Radar": "",
	} {
		if shortcuts[path] != want {
			t.Errorf("%s shortcut = %q, want %q", path, shortcuts[path], want)
		}
	}
}

func TestMenuActionsSkipHiddenItems(t *testing.T) {
	m := menu.NewMenu()
	sub := m.AddSubmenu("File")
	sub.AddText("Shown", nil, func(*menu.CallbackData) {})
	hidden := sub.AddText("Hidden", nil, func(*menu.CallbackData) {})
	hidden.Hidden = true

	actions := menuActions(m)
	if actions["File/Shown"] == nil || actions["File/Hidden"] != nil {
		t.Fatalf("actions = %v", actions)
	}
	if items := titleBarMenus(m)[0].Items; len(items) != 1 || items[0].Label != "Shown" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFormatAccelerator(t *testing.T) {
	for _, tc := range []struct {
		acc  *keys.Accelerator
		want string
	}{
		{nil, ""},
		{keys.CmdOrCtrl("q"), "Ctrl+Q"},
		{keys.Combo("r", keys.ControlKey, keys.ShiftKey), "Ctrl+Shift+R"},
		{keys.Combo("z", keys.ShiftKey, keys.CmdOrCtrlKey), "Ctrl+Shift+Z"},
		{keys.OptionOrAlt("f4"), "Alt+F4"},
	} {
		if got := formatAccelerator(tc.acc); got != tc.want {
			t.Errorf("formatAccelerator(%+v) = %q, want %q", tc.acc, got, tc.want)
		}
	}
}

func TestTitleBarScriptIsAnEventWithJSONDetail(t *testing.T) {
	script := titleBarScript(`Radar — "prod" </script>`, []titleBarMenu{{Label: "File", Items: []titleBarItem{{Label: "Quit", Shortcut: "Ctrl+Q"}}}})
	const prefix = "window.dispatchEvent(new CustomEvent('radar:titlebar',{detail:"
	if !strings.HasPrefix(script, prefix) || !strings.HasSuffix(script, "}))") {
		t.Fatalf("script = %s", script)
	}
	var detail struct {
		Title string         `json:"title"`
		Menus []titleBarMenu `json:"menus"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(script, prefix), "}))")), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Title != `Radar — "prod" </script>` || detail.Menus[0].Items[0].Shortcut != "Ctrl+Q" {
		t.Fatalf("detail = %+v", detail)
	}
}
