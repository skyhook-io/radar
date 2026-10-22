//go:build linux

package main

import (
	"log"
	"os"
	"strings"

	"github.com/skyhook-io/radar/internal/desktopenv"
)

// logBootEnv prints Linux desktop environment context at startup so bug
// reports include the info needed to diagnose Wayland/X11, compositor, and
// WebKit rendering issues without the reporter having to run extra commands.
//
// Users launching from the .desktop entry never see stdout, so the same values
// are also served in the diagnostics snapshot — see internal/desktopenv.
func logBootEnv() {
	log.Printf("[desktop] session: %s", joinEnv(desktopenv.SessionKeys, true))
	// The webview build decides which rendering bugs apply, and the package
	// manager's answer is not necessarily the library that got loaded.
	if lib := desktopenv.WebviewLibrary(); lib != "" {
		log.Printf("[desktop] webview: %s", lib)
	}
	if s := joinEnv(desktopenv.OverrideKeys, false); s != "" {
		log.Printf("[desktop] render overrides: %s", s)
	}
	if s := joinEnv(desktopenv.SandboxKeys, false); s != "" {
		log.Printf("[desktop] sandbox: %s", s)
	}
}

// applyWebKitDefaults sets WEBKIT_DMABUF_RENDERER_FORCE_SHM=1 unless the user
// has chosen a renderer mode. WebKitGTK's hardware (DMABUF) buffer transport
// produces blank windows on a range of Wayland setups (NVIDIA, KDE KWin, some
// Mesa stacks); shared-memory buffers avoid the DMABUF import entirely.
//
// Not WEBKIT_DISABLE_DMABUF_RENDERER: current WebKitGTK has no legacy renderer
// to fall back to, so that variable leaves no buffer transport at all, and the
// UI process segfaults on a null backing store as soon as a page enters
// accelerated compositing (document.startViewTransition does). Setting SHM
// alongside it does not help, so a user who set it keeps their choice as-is.
//
// Must run before Wails initializes WebKit.
func applyWebKitDefaults() {
	const (
		disableKey = "WEBKIT_DISABLE_DMABUF_RENDERER"
		shmKey     = "WEBKIT_DMABUF_RENDERER_FORCE_SHM"
	)
	for _, k := range []string{disableKey, shmKey} {
		if _, set := os.LookupEnv(k); set {
			return
		}
	}
	if err := os.Setenv(shmKey, "1"); err != nil {
		log.Printf("[desktop] failed to set %s: %v", shmKey, err)
		return
	}
	log.Printf("[desktop] applied %s=1 (default; set %s=0 to opt out)", shmKey, shmKey)
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
