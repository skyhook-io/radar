package updater

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"
)

// Antivirus scanners open a freshly written executable for a moment, and a
// rename fails while they hold it.
var (
	renameAttempts = 10
	renameBackoff  = 300 * time.Millisecond
	rename         = os.Rename
)

// replaceExecutable puts newExe at exe while exe is running. Windows lets a
// running executable be renamed but not overwritten, so it moves aside to
// .old, which the next start deletes, and the new one takes its name. The new
// one is first copied beside it because a rename can't cross drives and TEMP
// is often on another. Any failure puts the running version back, so Radar
// keeps running from where it was.
func replaceExecutable(exe, newExe string) error {
	staged := exe + ".new"
	oldExe := exe + ".old"
	if err := copyFile(newExe, staged); err != nil {
		os.Remove(staged)
		return fmt.Errorf("copy the new version next to %s: %w", exe, err)
	}
	if err := retry(func() error { return removeIfExists(oldExe) }); err != nil {
		os.Remove(staged)
		return fmt.Errorf("remove the previous backup %s: %w", oldExe, err)
	}
	if err := retry(func() error { return rename(exe, oldExe) }); err != nil {
		os.Remove(staged)
		return fmt.Errorf("move the current version aside: %w", err)
	}
	if err := retry(func() error { return rename(staged, exe) }); err != nil {
		if restoreErr := retry(func() error { return rename(oldExe, exe) }); restoreErr != nil {
			log.Printf("[updater] CRITICAL: failed to restore %s from %s: %v", exe, oldExe, restoreErr)
			return fmt.Errorf("move the new version into place: %w (the current version is at %s)", err, oldExe)
		}
		os.Remove(staged)
		return fmt.Errorf("move the new version into place (current version kept): %w", err)
	}
	return nil
}

func retry(op func() error) error {
	var err error
	for i := range renameAttempts {
		if err = op(); err == nil {
			return nil
		}
		if i < renameAttempts-1 {
			time.Sleep(renameBackoff)
		}
	}
	return err
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
