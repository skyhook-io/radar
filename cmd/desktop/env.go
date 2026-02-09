package main

import (
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// enrichPath ensures the desktop app can find CLI tools like
// gke-gcloud-auth-plugin, aws, gcloud, helm, etc. that are
// installed in the user's shell environment but not available
// to macOS .app bundles or Linux desktop applications.
func enrichPath() {
	shellPath := getShellPath()
	if shellPath != "" {
		os.Setenv("PATH", shellPath)
		log.Printf("PATH enriched from login shell (%d entries)", len(strings.Split(shellPath, ":")))
		return
	}

	// Fallback: append common tool locations
	current := os.Getenv("PATH")
	extras := commonPaths()
	if len(extras) > 0 {
		os.Setenv("PATH", current+":"+strings.Join(extras, ":"))
		log.Printf("PATH enriched with %d common paths (shell detection failed)", len(extras))
	}
}

// getShellPath runs the user's login shell to capture their full PATH.
func getShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}

	cmd := exec.Command(shell, "-l", "-c", "echo $PATH")
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + shell,
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(out))
	if path == "" || path == os.Getenv("PATH") {
		return ""
	}
	return path
}

// commonPaths returns well-known directories where CLI tools are typically installed.
func commonPaths() []string {
	home := os.Getenv("HOME")
	if home == "" {
		if u, err := user.Current(); err == nil {
			home = u.HomeDir
		}
	}

	candidates := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/go/bin",
	}

	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "google-cloud-sdk", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".krew", "bin"),
		)
	}

	var existing []string
	current := os.Getenv("PATH")
	for _, p := range candidates {
		if strings.Contains(current, p) {
			continue
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			existing = append(existing, p)
		}
	}
	return existing
}
