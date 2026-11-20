//go:build !linux && !windows

package updater

// DetectMode: macOS installs always use the in-app updater.
func DetectMode() Mode { return ModeInPlace }
