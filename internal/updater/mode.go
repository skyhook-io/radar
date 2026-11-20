package updater

import "os"

// Mode says whether this install can use the in-app updater, and if not, how
// the user gets a new version instead.
type Mode string

const (
	// ModeInPlace: the in-app updater replaces Radar where it is.
	ModeInPlace Mode = ""
	// ModeAppImage: an AppImage is a read-only image; the user downloads the
	// new AppImage.
	ModeAppImage Mode = "appimage"
	// ModePackage: installed from our .deb or .rpm; the user installs the new
	// package with their package manager, which keeps its records right.
	ModePackage Mode = "package"
	// ModeDownload: Radar cannot write to its own folder; the user downloads
	// the new version.
	ModeDownload Mode = "download"
)

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
