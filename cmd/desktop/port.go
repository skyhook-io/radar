package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The webview's origin includes the server's port, and browser storage is
// kept per origin. A new port on every launch would start each session with
// empty storage — theme, favorites, the pinned nav rail, recent resources,
// log viewer and AI panel settings — so Desktop reuses the port it had last
// time, falling back to an OS-assigned one when that port is taken.

const desktopPortFile = "desktop-port"

func desktopPortPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".radar", desktopPortFile)
}

// lastDesktopPort returns the port recorded by the previous launch, or 0.
func lastDesktopPort(path string) int {
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func recordDesktopPort(path string, port int) {
	if path == "" || port <= 0 || port == lastDesktopPort(path) {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, desktopPortFile+".*.tmp")
	if err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
		return
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(strconv.Itoa(port) + "\n")
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		log.Printf("[desktop] Failed to record port: %v", firstErr(werr, cerr))
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
