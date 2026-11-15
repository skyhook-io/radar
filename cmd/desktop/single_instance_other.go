//go:build !windows

package main

// handOffToRunningInstance: macOS brings the running app forward on its own,
// and Linux desktops start what they're asked to.
func handOffToRunningInstance() bool { return false }
