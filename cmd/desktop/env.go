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

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

// shellEnvVars lists environment variables to capture from the user's
// login shell. GUI apps (macOS .app, Linux .desktop) inherit a minimal
// environment that lacks these, causing silent failures when tools or
// configs set in .zshrc/.bashrc are not available.
var shellEnvVars = []string{
	"PATH",
	"KUBECONFIG",
	"AWS_PROFILE",
	"AWS_DEFAULT_REGION",
	"GOOGLE_APPLICATION_CREDENTIALS",
	"CLOUDSDK_CONFIG",
	"AZURE_CONFIG_DIR",
	// Self-hosted Hub overrides — a Finder/Dock launch strips the shell env,
	// and without these Cloud Connect would silently target the hosted Hub.
	"RADAR_HUB_URL",
	"RADAR_HUB_APP_URL",
}

// enrichEnv captures key environment variables from the user's login
// shell so the desktop app can find CLI tools and config files that are
// set in .zshrc/.bashrc but not available to macOS .app bundles or
// Linux desktop applications.
func enrichEnv() {
	captured := getShellEnv(shellEnvVars)

	if path, ok := captured["PATH"]; ok && path != "" {
		os.Setenv("PATH", path)
		log.Printf("PATH enriched from login shell (%d entries)", len(strings.Split(path, ":")))
	} else {
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

	// Apply non-PATH vars that were found in the shell but not in our env
	for _, key := range shellEnvVars {
		if key == "PATH" {
			continue
		}
		val, found := captured[key]
		if found && val != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
			log.Printf("Env enriched: %s from login shell", key)
			if key == "KUBECONFIG" {
				k8s.SetEnrichedKubeconfigFromShell(true)
			}
			continue
		}
		// Explain KUBECONFIG skip reasons — the GUI app starts with a stripped
		// env on macOS/Linux, and if enrichment doesn't fire the user may see
		// fewer clusters than they expect in the switcher. We surface this via
		// the errorlog so it shows up in bug report diagnostics.
		if key != "KUBECONFIG" {
			continue
		}
		switch {
		case os.Getenv(key) != "":
			// Pre-existing KUBECONFIG in the process env blocks enrichment.
			// Most likely cause: launchctl setenv or a parent shell that
			// already exported a (possibly shorter) value.
			existing := os.Getenv(key)
			pathCount := len(filepath.SplitList(existing))
			log.Printf("KUBECONFIG enrichment skipped: already set in process env (%d path(s))", pathCount)
			errorlog.Record("env-enrich", "warning",
				"KUBECONFIG enrichment skipped: already set in process env with %d path(s); "+
					"login shell value ignored", pathCount)
		case !found || val == "":
			// Use Base() so a user with a custom shell binary under $HOME
			// (e.g. nix-profile) doesn't leak their username into a public
			// bug report. Fall back to a placeholder when $SHELL is unset
			// — filepath.Base("") returns "." which would be a confusing
			// diagnostic message.
			shellName := "unknown"
			if s := os.Getenv("SHELL"); s != "" {
				shellName = filepath.Base(s)
			}
			log.Printf("KUBECONFIG enrichment skipped: not found in login shell")
			errorlog.Record("env-enrich", "warning",
				"KUBECONFIG not found in login shell (%s -l -i); "+
					"multi-file configs from .zshrc/.bashrc will not be visible", shellName)
		}
	}
}

// getShellEnv runs the user's login shell to capture environment variables.
// It uses -i (interactive) so that zsh reads ~/.zshrc, where tools like
// Homebrew's google-cloud-sdk add their PATH/KUBECONFIG entries. Without -i,
// a non-interactive login shell skips ~/.zshrc.
// Output markers safely extract values even if the interactive shell
// prints extra text (e.g. Oh My Zsh banners, motd).
func getShellEnv(keys []string) map[string]string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}

	// Print each var on its own line between markers so values containing
	// special strings don't break parsing. Using printf '%s\n' per var.
	const startMarker = "__RADAR_ENV_START__"
	const endMarker = "__RADAR_ENV_END__"

	var printCmds []string
	for _, key := range keys {
		printCmds = append(printCmds, "printf '%s\\n' \"$"+key+"\"")
	}
	echoCmd := "echo " + startMarker + "; " + strings.Join(printCmds, "; ") + "; echo " + endMarker

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
		log.Printf("Shell env detection failed (%s -l -i -c): %v", shell, err)
		return nil
	}

	output := string(out)
	startIdx := strings.Index(output, startMarker)
	endIdx := strings.Index(output, endMarker)
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		log.Printf("Shell env detection: markers not found in output")
		return nil
	}
	// Payload is: \nVAL1\nVAL2\n...\nVALn\n (empty vars produce empty lines).
	// Split on \n gives ["", val1, val2, ..., valn, ""] — trim first and last.
	payload := output[startIdx+len(startMarker) : endIdx]
	lines := strings.Split(payload, "\n")
	if len(lines) >= 2 {
		lines = lines[1 : len(lines)-1] // drop leading "" from echo newline and trailing ""
	}
	if len(lines) != len(keys) {
		log.Printf("Shell env detection: expected %d values, got %d", len(keys), len(lines))
		return nil
	}

	result := make(map[string]string, len(keys))
	for i, key := range keys {
		result[key] = lines[i]
	}
	return result
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
