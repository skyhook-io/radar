//go:build !linux || !cgo

package main

// separateChildEnv is a no-op: only the Linux AppImage changes Radar's
// environment for the bundled GTK.
func separateChildEnv() {}
