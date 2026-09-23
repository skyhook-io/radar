//go:build windows

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

// applyUpdate on Windows writes a .bat trampoline that waits for the current
// process to exit, replaces the executable, and relaunches.
func applyUpdate(_ context.Context, assetPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// Extract zip to temp dir
	extractDir, err := os.MkdirTemp("", "radar-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	// Don't defer RemoveAll — the trampoline handles cleanup

	if err := extractZip(assetPath, extractDir); err != nil {
		os.RemoveAll(extractDir)
		return fmt.Errorf("extract zip: %w", err)
	}

	// Find the new exe inside extracted contents
	newExe, err := findExtractedExe(extractDir)
	if err != nil {
		os.RemoveAll(extractDir)
		return fmt.Errorf("find extracted exe: %w", err)
	}
	log.Printf("[updater] Found new exe: %s", newExe)

	// Write trampoline .bat that will:
	// 1. Wait for our PID to exit
	// 2. Copy new exe over old (with error check)
	// 3. Launch new exe
	// 4. Clean up temp dir and self
	pid := os.Getpid()
	bat := filepath.Join(os.TempDir(), fmt.Sprintf("radar-update-%d.bat", pid))
	script := fmt.Sprintf(`@echo off
:wait
tasklist /FI "PID eq %d" 2>NUL | find /I "%d" >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    goto wait
)
copy /Y "%s" "%s"
if errorlevel 1 (
    echo [%%date%% %%time%%] Update failed: could not replace binary. >> "%%USERPROFILE%%\.radar\update-error.log"
    echo The new version is at: %s >> "%%USERPROFILE%%\.radar\update-error.log"
    exit /b 1
)
start "" "%s"
rd /S /Q "%s"
del "%%~f0"
`, pid, pid, newExe, exe, newExe, exe, extractDir)

	if err := os.WriteFile(bat, []byte(script), 0o755); err != nil {
		os.RemoveAll(extractDir)
		return fmt.Errorf("write trampoline: %w", err)
	}

	// Clean up the downloaded archive
	os.Remove(assetPath)

	return nil
}

// Relaunch on Windows starts the trampoline and exits.
func Relaunch() error {
	pid := os.Getpid()
	bat := filepath.Join(os.TempDir(), fmt.Sprintf("radar-update-%d.bat", pid))
	if _, err := os.Stat(bat); err != nil {
		return fmt.Errorf("trampoline not found: %w", err)
	}

	log.Printf("[updater] Executing trampoline: %s", bat)
	cmd := exec.Command("cmd", "/C", "start", "/MIN", bat)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start trampoline: %w", err)
	}

	runBeforeExit()
	os.Exit(0)
	return nil // unreachable
}

func findExtractedExe(dir string) (string, error) {
	return findInExtracted(dir, func(e os.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".exe")
	})
}

func cleanupPlatform() {
	// On Windows, the trampoline handles cleanup.
	// Remove any leftover trampoline .bat files from previous runs.
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "radar-update-*.bat"))
	if err != nil {
		return
	}
	for _, bat := range matches {
		log.Printf("[updater] Cleaning up leftover trampoline: %s", bat)
		os.Remove(bat)
	}
}
