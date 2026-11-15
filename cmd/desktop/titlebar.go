package main

import (
	"encoding/json"
	"log"
	goruntime "runtime"
	"slices"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// On Windows the window is frameless and the page draws the title bar: icon,
// menus, title and window buttons in one row, as VS Code does. A native menu
// bar can only sit on its own row under the system title bar.
//
// The native menu stays attached. The frameless window hides it, but its
// accelerators still fire, and the page's menus run the same items, so there
// is one definition of every action.
//
// The page loads from Radar's local server rather than the Wails origin, so the
// Wails JS runtime is not injected; the page posts raw Wails messages instead.
// That needs the local origin in BindingsAllowedOrigins (see main.go), which
// also lets that origin use the rest of the Wails runtime (clipboard, opening
// URLs, window state). DesktopApp binds no methods.
var customTitleBar = goruntime.GOOS == "windows"

type titleBarMenu struct {
	Label string         `json:"label"`
	Items []titleBarItem `json:"items"`
}

type titleBarItem struct {
	Label     string `json:"label,omitempty"`
	Shortcut  string `json:"shortcut,omitempty"`
	Separator bool   `json:"separator,omitempty"`
}

// shortcutHints labels keys that work without a registered accelerator. Paste
// has none on Windows (see pasteAccelerator) but Ctrl+V still pastes through
// the webview.
var shortcutHints = map[string]string{
	"Edit/Paste": "Ctrl+V",
}

func titleBarMenus(m *menu.Menu) []titleBarMenu {
	var menus []titleBarMenu
	for _, top := range m.Items {
		if top.SubMenu == nil || top.Hidden {
			continue
		}
		tm := titleBarMenu{Label: top.Label}
		for _, item := range top.SubMenu.Items {
			switch {
			case item.Hidden:
			case item.Type == menu.SeparatorType:
				tm.Items = append(tm.Items, titleBarItem{Separator: true})
			case item.Type == menu.TextType && item.Click != nil:
				shortcut := formatAccelerator(item.Accelerator)
				if shortcut == "" {
					shortcut = shortcutHints[top.Label+"/"+item.Label]
				}
				tm.Items = append(tm.Items, titleBarItem{Label: item.Label, Shortcut: shortcut})
			}
		}
		menus = append(menus, tm)
	}
	return menus
}

// menuActions indexes every clickable item as "Menu/Label", the path the page
// sends back when one of its entries is chosen.
func menuActions(m *menu.Menu) map[string]*menu.MenuItem {
	actions := map[string]*menu.MenuItem{}
	for _, top := range m.Items {
		if top.SubMenu == nil {
			continue
		}
		for _, item := range top.SubMenu.Items {
			if item.Type == menu.TextType && item.Click != nil && !item.Hidden {
				actions[top.Label+"/"+item.Label] = item
			}
		}
	}
	return actions
}

// formatAccelerator renders an accelerator the way Windows menus show it.
func formatAccelerator(a *keys.Accelerator) string {
	if a == nil {
		return ""
	}
	var parts []string
	add := func(s string) {
		if !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	for _, m := range a.Modifiers {
		if m == keys.CmdOrCtrlKey || m == keys.ControlKey {
			add("Ctrl")
		}
	}
	if slices.Contains(a.Modifiers, keys.OptionOrAltKey) {
		add("Alt")
	}
	if slices.Contains(a.Modifiers, keys.ShiftKey) {
		add("Shift")
	}
	return strings.Join(append(parts, strings.ToUpper(a.Key)), "+")
}

func (a *DesktopApp) installTitleBar(m *menu.Menu) {
	if !customTitleBar {
		return
	}
	a.titleBarMenus = titleBarMenus(m)
	actions := menuActions(m)
	runtime.EventsOn(a.ctx, "radar:titlebar:hello", func(...any) {
		a.pushTitleBar()
	})
	runtime.EventsOn(a.ctx, "radar:titlebar:invoke", func(data ...any) {
		if len(data) == 0 {
			return
		}
		path, _ := data[0].(string)
		item := actions[path]
		if item == nil {
			log.Printf("[titlebar] unknown menu action %q", path)
			return
		}
		item.Click(&menu.CallbackData{MenuItem: item})
	})
}

func titleBarScript(title string, menus []titleBarMenu) string {
	detail, _ := json.Marshal(map[string]any{"title": title, "menus": menus})
	return "window.dispatchEvent(new CustomEvent('radar:titlebar',{detail:" + string(detail) + "}))"
}

func (a *DesktopApp) pushTitleBar() {
	if !customTitleBar || a.ctx == nil {
		return
	}
	a.titleMu.Lock()
	title := a.currentTitle
	a.titleMu.Unlock()
	runtime.WindowExecJS(a.ctx, titleBarScript(title, a.titleBarMenus))
}
