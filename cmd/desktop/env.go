package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	} else {
		log.Printf("PATH enrichment: no additional paths found; auth plugins like gke-gcloud-auth-plugin may not be found")
	}
}

// getShellPath runs the user's login shell to capture their full PATH.
// It uses -i (interactive) so that zsh reads ~/.zshrc, where tools like
// Homebrew's google-cloud-sdk add their PATH entries. Without -i, a
// non-interactive login shell skips ~/.zshrc, so PATH entries added
// there (like gke-gcloud-auth-plugin) are missing.
// Output markers safely extract PATH even if the interactive shell
// prints extra text (e.g. Oh My Zsh banners, motd).
func getShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}

	const startMarker = "__RADAR_PATH_START__"
	const endMarker = "__RADAR_PATH_END__"
	echoCmd := "echo " + startMarker + "$PATH" + endMarker

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", echoCmd)
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + shell,
	}
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		log.Printf("Shell PATH detection failed (%s -l -i -c): %v", shell, err)
		return ""
	}

	output := string(out)
	startIdx := strings.Index(output, startMarker)
	endIdx := strings.Index(output, endMarker)
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		log.Printf("Shell PATH detection: markers not found in output")
		return ""
	}
	path := output[startIdx+len(startMarker) : endIdx]
	path = strings.TrimSpace(path)

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
		"/opt/homebrew/share/google-cloud-sdk/bin", // Homebrew gcloud (Apple Silicon)
		"/usr/local/bin",
		"/usr/local/share/google-cloud-sdk/bin", // Homebrew gcloud (Intel)
		"/usr/local/go/bin",
		"/snap/bin", // Snap packages on Linux (kubectl, gcloud, aws-cli)
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
