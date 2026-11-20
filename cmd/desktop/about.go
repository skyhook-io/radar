package main

import (
	"encoding/json"
	"strings"
)

const aboutTagline = "The open-source Kubernetes UI, by Skyhook"

// aboutText is the native About text (macOS panel, Linux dialog).
func aboutText(version string) string {
	return aboutTagline + "\n\nVersion " + strings.TrimPrefix(version, "v") + "\n\nradarhq.io"
}

func platformLabel(goos, goarch string) string {
	name := map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}[goos]
	if name == "" {
		name = goos
	}
	return name + " · " + goarch
}

// openAboutScript opens the page's About dialog (web/src/desktop/AboutDialog.tsx)
// with the exact build version and platform.
func openAboutScript(version, goos, goarch string) string {
	detail, _ := json.Marshal(map[string]string{"version": version, "platform": platformLabel(goos, goarch)})
	return "window.dispatchEvent(new CustomEvent('radar:open-about',{detail:" + string(detail) + "}))"
}
