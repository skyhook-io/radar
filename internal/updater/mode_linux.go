//go:build linux

package updater

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/skyhook-io/radar/internal/desktopenv"
)

// packagedMarker is shipped only in the .deb and .rpm (deploy/linux/nfpm.yaml),
// at <prefix>/share/radar-desktop/ next to <prefix>/bin/radar-desktop. It is
// looked up relative to the running binary, so a tarball copied to
// /usr/local/bin on a machine that also has the package is not mistaken for it.
// Firefox's deb/rpm builds ship an is-packaged-app marker for the same purpose.
const packagedMarker = "is-packaged-app"

// DetectMode decides once, at startup, whether the in-app updater can work for
// this install. It checks what applyUpdate needs: the AppImage is read-only, a
// package install belongs to the package manager (even for root), and the
// swap creates files in the binary's folder.
func DetectMode() Mode {
	if desktopenv.InAppImage() {
		return ModeAppImage
	}
	exe, err := launchedExecutable()
	if err != nil {
		log.Printf("[updater] In-app update off: %v", err)
		return ModeDownload
	}
	mode, reason := modeFor(exe)
	if mode != ModeInPlace {
		log.Printf("[updater] In-app update off for %s: %s", exe, reason)
	}
	return mode
}

// modeFor decides for a resolved executable path, and says why when the
// in-app updater is off.
func modeFor(exe string) (Mode, string) {
	if packaged(exe) {
		return ModePackage, "installed from a package"
	}
	if err := canCreateIn(filepath.Dir(exe)); err != nil {
		return ModeDownload, err.Error()
	}
	return ModeInPlace, ""
}

func packaged(exe string) bool {
	marker := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "radar-desktop", packagedMarker)
	_, err := os.Lstat(marker)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("[updater] Package marker check: %v", err)
	}
	return err == nil
}

// canCreateIn creates and removes a file in dir with this process's own
// credentials, the first thing applyUpdate's swap needs (rename the binary,
// create the new one beside it). Firefox and minio/selfupdate check
// writability the same way. It does not prove the swap will succeed: an
// immutable binary or a file mounted over it still fails at apply time.
func canCreateIn(dir string) error {
	f, err := os.CreateTemp(dir, ".radar-update-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closeErr := f.Close()
	if err := os.Remove(name); err != nil {
		return err
	}
	return closeErr
}
