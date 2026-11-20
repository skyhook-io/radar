//go:build windows

package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// RelaunchAfterPIDEnv carries the updating process's PID to the new version,
// which waits for it to exit before starting: the old one still holds the
// single-instance lock and the port the webview's storage belongs to.
const RelaunchAfterPIDEnv = "RADAR_RELAUNCH_AFTER_PID"

// launchedExecutable is the path Radar started from, taken before the update
// renames the running file to .old.
var launchedExecutable = sync.OnceValues(func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable path: %w", err)
	}
	return exe, nil
})

// DetectMode turns the in-app updater off when Radar can't create files in its
// own folder (Program Files for a standard user), where the swap would fail.
func DetectMode() Mode {
	exe, err := launchedExecutable()
	if err != nil {
		log.Printf("[updater] In-app update off: %v", err)
		return ModeDownload
	}
	if err := canCreateIn(filepath.Dir(exe)); err != nil {
		log.Printf("[updater] In-app update off for %s: %v", exe, err)
		return ModeDownload
	}
	return ModeInPlace
}

func applyUpdate(_ context.Context, assetPath string) error {
	exe, err := launchedExecutable()
	if err != nil {
		return err
	}
	extractDir, err := os.MkdirTemp("", "radar-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extractZip(assetPath, extractDir); err != nil {
		return fmt.Errorf("extract zip: %w", err)
	}
	newExe, err := findExtractedExe(extractDir)
	if err != nil {
		return fmt.Errorf("find extracted exe: %w", err)
	}
	log.Printf("[updater] Found new exe: %s", newExe)

	if err := replaceExecutable(exe, newExe); err != nil {
		return err
	}
	os.Remove(assetPath)
	return nil
}

// Relaunch starts the new version and exits.
func Relaunch() error {
	exe, err := launchedExecutable()
	if err != nil {
		return err
	}
	log.Printf("[updater] Relaunching %s", exe)
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), RelaunchAfterPIDEnv+"="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}

func findExtractedExe(dir string) (string, error) {
	return findInExtracted(dir, func(e os.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".exe")
	})
}

// cleanupPlatform deletes what an update left beside the executable, and the
// batch files earlier versions updated through when one failed.
func cleanupPlatform() {
	exe, err := launchedExecutable()
	if err != nil {
		log.Printf("[updater] Cleanup: %v", err)
		return
	}
	for _, leftover := range []string{exe + ".old", exe + ".new"} {
		if err := removeIfExists(leftover); err != nil {
			log.Printf("[updater] Cleanup: failed to remove %s: %v", leftover, err)
		}
	}
	bats, _ := filepath.Glob(filepath.Join(os.TempDir(), "radar-update-*.bat"))
	for _, bat := range bats {
		os.Remove(bat)
	}
}
