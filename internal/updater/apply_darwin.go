//go:build darwin

package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// applyUpdate replaces the current .app bundle with the downloaded one.
// Strategy:
//  1. Find current .app bundle root by walking up from the executable
//  2. Extract the downloaded .zip to a temp directory alongside the .app
//  3. Rename old .app → .app.old, new .app → target (atomic on same FS)
//  4. Remove quarantine attribute
func applyUpdate(_ context.Context, assetPath string) error {
	// Find the .app bundle root
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	appBundle := findAppBundle(exe)
	if appBundle == "" {
		return fmt.Errorf("could not find .app bundle for executable %s", exe)
	}
	log.Printf("[updater] Current app bundle: %s", appBundle)

	// Extract zip to a temp dir next to the .app bundle
	parentDir := filepath.Dir(appBundle)
	extractDir, err := os.MkdirTemp(parentDir, "radar-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(extractDir) // cleanup on failure; on success the .app has been moved out

	if err := extractZip(assetPath, extractDir); err != nil {
		return fmt.Errorf("extract zip: %w", err)
	}

	// Find the .app inside the extracted contents
	newApp, err := findExtractedApp(extractDir)
	if err != nil {
		return fmt.Errorf("find extracted app: %w", err)
	}
	log.Printf("[updater] Extracted new app: %s", newApp)

	// Atomic swap: old → .old, new → target
	oldBundle := appBundle + ".old"
	if err := os.RemoveAll(oldBundle); err != nil {
		return fmt.Errorf("remove previous backup %s: %w", oldBundle, err)
	}

	if err := os.Rename(appBundle, oldBundle); err != nil {
		return fmt.Errorf("rename current bundle: %w", err)
	}

	if err := os.Rename(newApp, appBundle); err != nil {
		// Try to restore old bundle
		if restoreErr := os.Rename(oldBundle, appBundle); restoreErr != nil {
			log.Printf("[updater] CRITICAL: failed to restore old bundle from %s: %v", oldBundle, restoreErr)
			return fmt.Errorf("move new bundle into place: %w (previous version may be at %s)", err, oldBundle)
		}
		return fmt.Errorf("move new bundle into place (previous version restored): %w", err)
	}

	// Remove quarantine attribute (best effort)
	cmd := exec.Command("xattr", "-cr", appBundle)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[updater] xattr -cr warning: %s (%v)", string(out), err)
	}

	// Clean up the downloaded archive
	os.Remove(assetPath)

	return nil
}

// Relaunch opens the .app bundle and exits the current process.
func Relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	appBundle := findAppBundle(exe)
	if appBundle == "" {
		return fmt.Errorf("could not find .app bundle")
	}

	log.Printf("[updater] Relaunching %s", appBundle)
	cmd := exec.Command("open", "-n", appBundle)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}

	runBeforeExit()
	os.Exit(0)
	return nil // unreachable
}

// findAppBundle walks up the path from the executable to find the .app directory.
// e.g., /Applications/Radar.app/Contents/MacOS/radar-desktop → /Applications/Radar.app
func findAppBundle(exe string) string {
	path := exe
	for {
		if strings.HasSuffix(path, ".app") {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "" // reached root without finding .app
		}
		path = parent
	}
}

// findExtractedApp locates the .app directory inside the extracted directory.
func findExtractedApp(dir string) (string, error) {
	return findInExtracted(dir, func(e os.DirEntry) bool {
		return e.IsDir() && strings.HasSuffix(e.Name(), ".app")
	})
}

// cleanupPlatform removes .app.old from a previous update.
func cleanupPlatform() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[updater] Cleanup: could not determine executable path: %v", err)
		return
	}
	appBundle := findAppBundle(exe)
	if appBundle == "" {
		return
	}
	oldBundle := appBundle + ".old"
	if _, err := os.Stat(oldBundle); err == nil {
		log.Printf("[updater] Cleaning up previous version: %s", oldBundle)
		if err := os.RemoveAll(oldBundle); err != nil {
			log.Printf("[updater] Cleanup: failed to remove %s: %v", oldBundle, err)
		}
	}
}
