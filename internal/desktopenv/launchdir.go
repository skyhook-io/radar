package desktopenv

import (
	"os"
	"path/filepath"
	"strings"
)

// LaunchDir returns the directory an AppImage was started from, or "" when
// Radar is not running from one. The AppImage's launcher changes into the
// image's library directory so WebKit can find its bundled helper processes,
// which leaves the process working directory pointing inside the read-only
// image. The launcher exports the original directory as OWD.
func LaunchDir() string {
	if os.Getenv("APPIMAGE") == "" {
		return ""
	}
	dir := os.Getenv("OWD")
	if !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// ResolveLaunchPath makes a relative path the user supplied absolute against
// LaunchDir, so it means what it meant in the shell that started the AppImage.
// Empty, absolute and "~"-prefixed paths, and every path outside an AppImage,
// are returned unchanged.
func ResolveLaunchPath(path string) string {
	dir := LaunchDir()
	if dir == "" || path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return path
	}
	return filepath.Join(dir, path)
}
