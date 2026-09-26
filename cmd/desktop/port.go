package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// rememberDesktopPort runs once the window is up, so a launch that fails
// before then records nothing. A launch that fell back because another Radar
// holds the remembered port (a second Desktop window) keeps the remembered
// one; a launch that fell back because something else holds it moves on, or
// every later launch would fall back too.
func rememberDesktopPort(path string, remembered, actual int, radarServing func(port int) bool) {
	if remembered != 0 && actual != remembered && radarServing(remembered) {
		return
	}
	recordDesktopPort(path, actual)
}

func radarServingOn(port int) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/capabilities", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var caps struct {
		Deployment *struct {
			Mode string `json:"mode"`
		} `json:"deployment"`
	}
	return resp.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&caps) == nil &&
		caps.Deployment != nil
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
