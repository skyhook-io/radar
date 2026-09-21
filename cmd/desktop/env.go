package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

// shellEnvVars lists exact environment variables to capture from the user's
// login shell. GUI apps (macOS .app, Linux .desktop) inherit a minimal
// environment that lacks these, causing silent failures when tools or
// configs set in .zshrc/.bashrc are not available.
var shellEnvVars = []string{
	"PATH",
	"KUBECONFIG",
	"AWS_PROFILE",
	"AWS_DEFAULT_REGION",
	"AWS_REGION",
	"GOOGLE_APPLICATION_CREDENTIALS",
	"CLOUDSDK_CONFIG",
	"AZURE_CONFIG_DIR",
	// Self-hosted Hub overrides — a Finder/Dock launch strips the shell env,
	// and without these Cloud Connect would silently target the hosted Hub.
	"RADAR_HUB_URL",
	"RADAR_HUB_APP_URL",
}

var shellEnvPrefixes = []string{
	"ANTHROPIC_",
	"CLAUDE_CODE_",
}

// enrichEnv captures key environment variables from the user's login
// shell so the desktop app can find CLI tools and config files that are
// set in .zshrc/.bashrc but not available to macOS .app bundles or
// Linux desktop applications.
func enrichEnv() {
	originalKubeconfig := os.Getenv("KUBECONFIG")
	captured := getShellEnv(shellEnvVars, shellEnvPrefixes)

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

	for key, val := range captured {
		if key == "PATH" {
			continue
		}
		if val != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
			log.Printf("Env enriched: %s from login shell", key)
			if key == "KUBECONFIG" {
				k8s.SetEnrichedKubeconfigFromShell(true)
			}
		}
	}

	// Explain KUBECONFIG skip reasons — the GUI app starts with a stripped
	// env on macOS/Linux, and if enrichment doesn't fire the user may see
	// fewer clusters than they expect in the switcher. We surface this via
	// the errorlog so it shows up in bug report diagnostics.
	kubeconfigVal := captured["KUBECONFIG"]
	switch {
	case originalKubeconfig != "":
		pathCount := len(filepath.SplitList(originalKubeconfig))
		log.Printf("KUBECONFIG enrichment skipped: already set in process env (%d path(s))", pathCount)
		if kubeconfigVal != "" {
			errorlog.Record("env-enrich", "warning",
				"KUBECONFIG enrichment skipped: already set in process env with %d path(s); "+
					"login shell value ignored", pathCount)
		}
	case kubeconfigVal == "":
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

// getShellEnv runs the user's login shell to capture environment variables.
// It uses -i (interactive) so that zsh reads ~/.zshrc, where tools like
// Homebrew's google-cloud-sdk add their PATH/KUBECONFIG entries. Without -i,
// a non-interactive login shell skips ~/.zshrc.
func getShellEnv(keys []string, prefixes []string) map[string]string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}

	var script strings.Builder
	// Fixed keys can be unexported shell variables, which env alone omits.
	// NUL framing preserves every byte an env value can hold.
	for _, key := range keys {
		script.WriteString("printf '%s\\000' \"" + key + "=$" + key + "\"\n")
	}
	script.WriteString("command env -0 || exit 1\n")

	payload := runLoginShell(shell, script.String())
	if payload == "" {
		return nil
	}
	result := make(map[string]string, len(keys))
	for _, record := range strings.Split(payload, "\x00") {
		key, value, ok := strings.Cut(record, "=")
		if !ok {
			continue
		}
		if slices.Contains(keys, key) {
			result[key] = value
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				result[key] = value
				break
			}
		}
	}
	return result
}

// runLoginShell runs script in an interactive login shell and returns the
// output it printed between two unique markers, so any prompt/motd/rc-file
// chatter around it is discarded.
func runLoginShell(shell, script string) string {
	// Whole NUL-delimited markers cannot appear inside an environment value.
	const startMarker = "\x00__RADAR_ENV_START__\x00"
	const endMarker = "\x00__RADAR_ENV_END__\x00"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c",
		"printf '\\000__RADAR_ENV_START__\\000'\n"+script+"\nprintf '\\000__RADAR_ENV_END__\\000'")
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + shell,
	}
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		log.Printf("Shell env detection failed (%s -l -i -c): %v", shell, err)
		return ""
	}

	output := string(out)
	startIdx := strings.Index(output, startMarker)
	endIdx := strings.Index(output, endMarker)
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		log.Printf("Shell env detection: markers not found in output")
		return ""
	}
	return output[startIdx+len(startMarker) : endIdx]
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
