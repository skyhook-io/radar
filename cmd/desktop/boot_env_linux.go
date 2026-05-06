//go:build linux

package main

import (
	"log"
	"os"
	"strings"
)

// logBootEnv prints Linux desktop environment context at startup so bug
// reports include the info needed to diagnose Wayland/X11, compositor, and
// WebKit rendering issues without the reporter having to run extra commands.
func logBootEnv() {
	session := []string{"XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "WAYLAND_DISPLAY", "DISPLAY"}
	overrides := []string{"GDK_BACKEND", "GSK_RENDERER", "WEBKIT_DISABLE_DMABUF_RENDERER", "WEBKIT_DISABLE_COMPOSITING_MODE", "GTK_THEME"}
	sandbox := []string{"SNAP", "FLATPAK_ID", "container"}

	log.Printf("[desktop] session: %s", joinEnv(session, true))
	if s := joinEnv(overrides, false); s != "" {
		log.Printf("[desktop] render overrides: %s", s)
	}
	if s := joinEnv(sandbox, false); s != "" {
		log.Printf("[desktop] sandbox: %s", s)
	}
}

// applyWebKitDefaults sets WEBKIT_DISABLE_DMABUF_RENDERER=1 unless the user
// has set it explicitly. WebKitGTK's DMABUF renderer produces blank windows
// on a range of Wayland setups (NVIDIA, KDE KWin, some Mesa stacks) and
// upstream considers this an application-level concern, not a WebKit fix.
// The legacy renderer is stable everywhere.
//
// Must run before Wails initializes WebKit. LookupEnv (not an empty-string
// check) lets users opt back in to DMABUF with WEBKIT_DISABLE_DMABUF_RENDERER=0.
func applyWebKitDefaults() {
	const key = "WEBKIT_DISABLE_DMABUF_RENDERER"
	if _, set := os.LookupEnv(key); set {
		return
	}
	if err := os.Setenv(key, "1"); err != nil {
		log.Printf("[desktop] failed to set %s: %v", key, err)
		return
	}
	log.Printf("[desktop] applied %s=1 (default; set %s=0 to opt out)", key, key)
}

// joinEnv formats env vars as "KEY=value" pairs. When includeUnset is true,
// unset vars are rendered as "KEY=" so the reader can tell they were checked.
// When false, unset vars are omitted (noise reduction for overrides).
func joinEnv(keys []string, includeUnset bool) string {
	var parts []string
	for _, k := range keys {
		v := os.Getenv(k)
		if v == "" && !includeUnset {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}
