//go:build linux

package main

import (
	"log"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

// applySystemTheme detects the system color scheme via the xdg-desktop-portal
// D-Bus interface and sets GTK_THEME accordingly so that WebKitGTK's
// prefers-color-scheme CSS media query works on modern GNOME (42+).
//
// WebKitGTK does not query org.freedesktop.appearance.color-scheme directly —
// it only checks gtk-application-prefer-dark-theme, gtk-theme-name ending in
// "-dark", or the GTK_THEME env var containing ":dark". On GNOME 42+ the first
// two are unset, so we bridge the gap by reading the portal and setting
// GTK_THEME ourselves.
func applySystemTheme() {
	if !detectSystemDarkMode() {
		return
	}

	current := os.Getenv("GTK_THEME")
	if strings.Contains(strings.ToLower(current), "dark") {
		return // already dark
	}

	if current == "" {
		current = "Adwaita"
	}
	if err := os.Setenv("GTK_THEME", current+":dark"); err != nil {
		log.Printf("[theme] Failed to set GTK_THEME: %v", err)
	}
}

// detectSystemDarkMode queries the xdg-desktop-portal for the system color
// scheme preference. Returns true if the user prefers dark mode.
//
// Portal values: 0 = no preference, 1 = prefer dark, 2 = prefer light.
func detectSystemDarkMode() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("[theme] D-Bus session bus not available: %v", err)
		return false
	}
	defer conn.Close()

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	variant, err := obj.GetProperty("org.freedesktop.appearance.color-scheme")
	if err != nil {
		log.Printf("[theme] Could not read color-scheme from xdg-desktop-portal: %v", err)
		return false
	}

	// The variant wraps a uint32 inside another variant.
	val, ok := variant.Value().(dbus.Variant)
	if ok {
		v, ok := val.Value().(uint32)
		return ok && v == 1
	}

	v, ok := variant.Value().(uint32)
	return ok && v == 1
}
