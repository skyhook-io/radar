package updater

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
