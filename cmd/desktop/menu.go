package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// reloadAccelerator picks the Reload shortcut per platform. Off macOS,
// CmdOrCtrl+R resolves to Ctrl+R — the terminal's reverse-i-search binding — and
// a native menu accelerator fires regardless of webview focus, so it would
// hijack Ctrl+R inside a pod/local shell. Ctrl+Shift+R keeps a reload hotkey
// without shadowing a key terminals actually send (plain function keys like F5
// are consumed by TUIs and cmd.exe history). macOS keeps Cmd+R, which doesn't
// collide (terminals use Ctrl, not Cmd).
func reloadAccelerator(goos string) *keys.Accelerator {
	if goos == "darwin" {
		return keys.CmdOrCtrl("r")
	}
	return keys.Combo("r", keys.ControlKey, keys.ShiftKey)
}

// pasteAccelerator drops Ctrl+V on Windows only. Paste needs an explicit
// callback (see the Edit menu below), and on Windows the accelerator does not
// consume the keypress — winc fires the menu action from WM_KEYDOWN and still
// lets the event reach the webview, which pastes natively, so binding both
// inserts the clipboard twice. macOS and Linux register accelerators through
// their own toolkits, which do consume the key, so the accelerator there is the
// single paste path and must stay. The cost on Windows is only the menu-item
// hint; Ctrl+V still pastes through the webview.
func pasteAccelerator(goos string) *keys.Accelerator {
	if goos == "windows" {
		return nil
	}
	return keys.CmdOrCtrl("v")
}

// clipboardAccelerator drops Ctrl+C/Ctrl+X on Linux only. GTK menubar
// accelerators fire before the key reaches the focused webview, so binding
// Cut/Copy there swallows Ctrl+C inside the pod terminal (xterm): it triggers a
// menu Copy instead of sending SIGINT to the running process. Without the
// accelerator the key reaches the webview — xterm gets its interrupt, and
// Monaco copy/cut still works through the keydown handler in main.tsx. Cut/Copy
// stay clickable via clipboardDelegate. macOS uses Cmd (terminals use Ctrl, no
// collision) and Windows' winc forwards the key to the webview anyway, so both
// keep the accelerator and its menu-item hint.
func clipboardAccelerator(goos, key string) *keys.Accelerator {
	if goos == "linux" {
		return nil
	}
	return keys.CmdOrCtrl(key)
}

// clipboardDelegate returns nil on macOS, where Cut/Copy rely on the native
// responder chain. Off macOS a nil callback binds no handler at all, so the menu
// entries do nothing when clicked; routing through execCommand reaches the
// copy/cut interception in main.tsx, which also covers Monaco's virtual
// selection. Both handlers re-read the selection, so the keyboard path staying
// on the webview is harmless.
func clipboardDelegate(goos string, desktopApp *DesktopApp, command string) func(*menu.CallbackData) {
	if goos == "darwin" {
		return nil
	}
	return func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "document.execCommand('"+command+"')")
	}
}

// createMenu builds the native menubar. macOS diverges throughout: the
// application-menu role owns Quit and About there, and Close Window hides the
// whole app rather than closing anything — which is only safe on the one
// platform where the close button does the same and the Dock can bring it back.
func createMenu(desktopApp *DesktopApp, version, goos string) *menu.Menu {
	mac := goos == "darwin"

	appMenu := menu.NewMenu()

	// macOS keeps Quit, Hide and Show All in the application menu — the native
	// role also supplies the Hide/Show All pair that brings a hidden app back.
	// Roles are darwin-only in Wails; elsewhere they degrade to a blank item.
	if mac {
		appMenu.Append(menu.AppMenu())
	}

	// File menu
	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("Settings...", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, `window.dispatchEvent(new Event('radar:open-settings'))`)
	})
	if mac {
		fileMenu.AddText("Close Window", keys.CmdOrCtrl("w"), func(_ *menu.CallbackData) {
			runtime.Hide(desktopApp.ctx)
		})
	}
	// On macOS the application menu owns Quit; a second Cmd+Q here would leave
	// two menu items bound to the same accelerator.
	if !mac {
		fileMenu.AddSeparator()
		fileMenu.AddText("Quit", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
			runtime.Quit(desktopApp.ctx)
		})
	}

	// Edit menu — clipboard handling strategy:
	//
	// Copy/Cut: On macOS a nil callback delegates to the native responder chain.
	// WKWebView does NOT dispatch DOM copy/cut events from the native selectors.
	// Instead, the JS keydown handler in main.tsx intercepts Cmd+C/X before macOS
	// consumes the event, reads the selection (including Monaco virtual selection),
	// and writes to the clipboard via navigator.clipboard.writeText(). Elsewhere
	// there is no responder chain to fall back on — see clipboardDelegate. The
	// keyboard accelerator is dropped on Linux so Ctrl+C reaches the terminal —
	// see clipboardAccelerator.
	//
	// Paste: Must use explicit WindowExecJS because WKWebView's native paste:
	// doesn't work for complex editors like Monaco. We read from the clipboard
	// API and dispatch a synthetic ClipboardEvent. The accelerator is dropped on
	// Windows — see pasteAccelerator.
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
	editMenu.AddText("Cut", clipboardAccelerator(goos, "x"), clipboardDelegate(goos, desktopApp, "cut"))
	editMenu.AddText("Copy", clipboardAccelerator(goos, "c"), clipboardDelegate(goos, desktopApp, "copy"))
	editMenu.AddText("Paste", pasteAccelerator(goos), func(_ *menu.CallbackData) {
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
	viewMenu.AddText("Reload", reloadAccelerator(goos), func(_ *menu.CallbackData) {
		runtime.WindowReloadApp(desktopApp.ctx)
	})
	viewMenu.AddSeparator()
	viewMenu.AddText("Zoom In", keys.CmdOrCtrl("="), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, `
			var z = parseFloat(document.body.style.zoom || '1');
			document.body.style.zoom = String(Math.min(2.0, z + 0.1));
		`)
	})
	viewMenu.AddText("Zoom Out", keys.CmdOrCtrl("-"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, `
			var z = parseFloat(document.body.style.zoom || '1');
			document.body.style.zoom = String(Math.max(0.5, z - 0.1));
		`)
	})
	viewMenu.AddText("Reset Zoom", keys.CmdOrCtrl("0"), func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, "document.body.style.zoom = '1';")
	})

	// Help menu
	helpMenu := appMenu.AddSubmenu("Help")
	helpMenu.AddText("Check for Updates...", nil, func(_ *menu.CallbackData) {
		runtime.WindowExecJS(desktopApp.ctx, `window.dispatchEvent(new Event('radar:check-for-updates'))`)
	})
	helpMenu.AddSeparator()
	// The macOS application menu already carries About, drawn from mac.AboutInfo.
	if !mac {
		helpMenu.AddText("About Radar", nil, func(_ *menu.CallbackData) {
			runtime.MessageDialog(desktopApp.ctx, runtime.MessageDialogOptions{
				Type:    runtime.InfoDialog,
				Title:   "About Radar",
				Message: "Radar — Kubernetes Visibility Tool\nBuilt by Skyhook\n\nVersion: " + version + "\n\nhttps://github.com/skyhook-io/radar",
			})
		})
	}
	helpMenu.AddText("Documentation", nil, func(_ *menu.CallbackData) {
		runtime.BrowserOpenURL(desktopApp.ctx, "https://github.com/skyhook-io/radar#readme")
	})
	helpMenu.AddText("GitHub Repository", nil, func(_ *menu.CallbackData) {
		runtime.BrowserOpenURL(desktopApp.ctx, "https://github.com/skyhook-io/radar")
	})

	return appMenu
}
