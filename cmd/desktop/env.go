package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
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
	kubeconfigVal, kubeconfigFound := captured["KUBECONFIG"]
	switch {
	case originalKubeconfig != "" && kubeconfigFound && kubeconfigVal != "":
		pathCount := len(filepath.SplitList(originalKubeconfig))
		log.Printf("KUBECONFIG enrichment skipped: already set in process env (%d path(s))", pathCount)
		errorlog.Record("env-enrich", "warning",
			"KUBECONFIG enrichment skipped: already set in process env with %d path(s); "+
				"login shell value ignored", pathCount)
	case !kubeconfigFound || kubeconfigVal == "":
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

// envRecordStart matches the start of a KEY=VALUE record in `env` output,
// used only to discover candidate variable *names* — never to extract
// values. `env` output isn't safe to split into records by newline: a
// multiline value has continuation lines that don't match, and bash's
// exported-function entries (`BASH_FUNC_name%%=() { ... }`) don't match
// either, so treating non-matching lines as continuations would glom a
// function body onto whatever real variable preceded it.
var envRecordStart = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// getShellEnv runs the user's login shell to capture environment variables.
// It uses -i (interactive) so that zsh reads ~/.zshrc, where tools like
// Homebrew's google-cloud-sdk add their PATH/KUBECONFIG entries. Without -i,
// a non-interactive login shell skips ~/.zshrc.
//
// Values are never read off the raw `env` dump: an exact key or a
// prefix-matched name discovered there is instead fetched in a second pass
// by asking the shell to print it directly, framed with control-byte
// separators a real path/URL/API-key value won't contain, so a value that
// spans multiple lines can't be mis-split or bleed into another variable.
func getShellEnv(keys []string, prefixes []string) map[string]string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}

	raw := runLoginShell(shell, "env")
	if raw == "" {
		return nil
	}

	names := make(map[string]bool, len(keys))
	for _, k := range keys {
		names[k] = true
	}
	for _, line := range strings.Split(raw, "\n") {
		if !envRecordStart.MatchString(line) {
			continue
		}
		key := line[:strings.IndexByte(line, '=')]
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				names[key] = true
				break
			}
		}
	}
	if len(names) == 0 {
		return nil
	}

	const nameValueSep = "\x01"
	const recordSep = "\x02"

	var script strings.Builder
	for name := range names {
		script.WriteString("printf '%s\\001%s\\002' " + name + " \"$" + name + "\"\n")
	}

	payload := runLoginShell(shell, script.String())
	if payload == "" {
		return nil
	}
	// runLoginShell's markers are separated from the payload by the
	// newlines `echo` itself prints, which would otherwise land inside the
	// first record's name — trim them before splitting on the byte-level
	// record separator.
	payload = strings.Trim(payload, "\n")

	result := make(map[string]string, len(names))
	for _, record := range strings.Split(payload, recordSep) {
		idx := strings.IndexByte(record, nameValueSep[0])
		if idx < 0 {
			continue
		}
		result[record[:idx]] = record[idx+1:]
	}
	return result
}

// runLoginShell runs script in an interactive login shell and returns the
// output it printed between two unique markers, so any prompt/motd/rc-file
// chatter around it is discarded.
func runLoginShell(shell, script string) string {
	const startMarker = "__RADAR_ENV_START__"
	const endMarker = "__RADAR_ENV_END__"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", "echo "+startMarker+"\n"+script+"\necho "+endMarker)
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
