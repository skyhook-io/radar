//go:build !linux

package updater

// DetectMode: macOS and Windows installs always use the in-app updater.
func DetectMode() Mode { return ModeInPlace }
