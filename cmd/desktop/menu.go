package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func createMenu(desktopApp *DesktopApp) *menu.Menu {
	appMenu := menu.NewMenu()

	// File menu
	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("New Window", keys.CmdOrCtrl("n"), func(_ *menu.CallbackData) {
		// Future: open a new window with a different context
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("Settings...", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		runtime.EventsEmit(desktopApp.ctx, "open-settings")
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("Quit", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
		runtime.Quit(desktopApp.ctx)
	})

	// Edit menu — clipboard handling strategy:
	//
	// Copy/Cut: Use nil callbacks to delegate to macOS's native responder chain.
	// WKWebView does NOT dispatch DOM copy/cut events from the native selectors.
	// Instead, the JS keydown handler in main.tsx intercepts Cmd+C/X before macOS
	// consumes the event, reads the selection (including Monaco virtual selection),
	// and writes to the clipboard via navigator.clipboard.writeText().
	//
	// Paste: Must use explicit WindowExecJS because WKWebView's native paste:
	// doesn't work for complex editors like Monaco. We read from the clipboard
	// API and dispatch a synthetic ClipboardEvent.
	//
	// Undo/Redo/SelectAll: Use WindowExecJS (these work fine via execCommand).
	editMenu := appMenu.AddSubmenu("Edit")
	editMenu.AddText("Undo", keys.CmdOrCtrl("z"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "document.execCommand('undo')")
	})
	editMenu.AddText("Redo", keys.Combo("z", keys.ShiftKey, keys.CmdOrCtrlKey), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "document.execCommand('redo')")
	})
	editMenu.AddSeparator()
	editMenu.AddText("Cut", keys.CmdOrCtrl("x"), nil)
	editMenu.AddText("Copy", keys.CmdOrCtrl("c"), nil)
	editMenu.AddText("Paste", keys.CmdOrCtrl("v"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, `
			navigator.clipboard.readText().then(function(text) {
				if (!text) return;
				var el = document.activeElement || document.body;
				try {
					var dt = new DataTransfer();
					dt.setData('text/plain', text);
					var ev = new ClipboardEvent('paste', {clipboardData: dt, bubbles: true, cancelable: true});
					if (!el.dispatchEvent(ev)) return;
				} catch(e) { /* ClipboardEvent dispatch failed, fall back to insertText */ }
				document.execCommand('insertText', false, text);
			}).catch(function(err) { console.warn('[Radar] Paste failed:', err); });
		`)
	})
	editMenu.AddText("Select All", keys.CmdOrCtrl("a"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "document.execCommand('selectAll')")
	})

	// View menu
	viewMenu := appMenu.AddSubmenu("View")
	viewMenu.AddText("Back", keys.CmdOrCtrl("["), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "window.history.back()")
	})
	viewMenu.AddText("Forward", keys.CmdOrCtrl("]"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "window.history.forward()")
	})
	viewMenu.AddSeparator()
	viewMenu.AddText("Reload", keys.CmdOrCtrl("r"), func(_ *menu.CallbackData) {
		runtime.WindowReloadApp(desktopApp.ctx)
	})
	viewMenu.AddSeparator()
	viewMenu.AddText("Zoom In", keys.CmdOrCtrl("="), nil)
	viewMenu.AddText("Zoom Out", keys.CmdOrCtrl("-"), nil)
	viewMenu.AddText("Reset Zoom", keys.CmdOrCtrl("0"), nil)

	// Help menu
	helpMenu := appMenu.AddSubmenu("Help")
	helpMenu.AddText("Check for Updates...", nil, func(_ *menu.CallbackData) {
		// Emit an event that the frontend listens for to trigger a version check
		runtime.EventsEmit(desktopApp.ctx, "check-for-updates")
	})
	helpMenu.AddSeparator()
	helpMenu.AddText("About Radar", nil, func(_ *menu.CallbackData) {
		runtime.MessageDialog(desktopApp.ctx, runtime.MessageDialogOptions{
			Type:    runtime.InfoDialog,
			Title:   "About Radar",
			Message: "Radar — Kubernetes Visibility Tool\nBuilt by Skyhook\n\nhttps://github.com/skyhook-io/radar",
		})
	})
	helpMenu.AddText("Documentation", nil, func(_ *menu.CallbackData) {
		runtime.BrowserOpenURL(desktopApp.ctx, "https://github.com/skyhook-io/radar#readme")
	})
	helpMenu.AddText("GitHub Repository", nil, func(_ *menu.CallbackData) {
		runtime.BrowserOpenURL(desktopApp.ctx, "https://github.com/skyhook-io/radar")
	})

	return appMenu
}
