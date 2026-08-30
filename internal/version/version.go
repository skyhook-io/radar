package version

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/skyhook-io/radar/internal/k8s"
)

var (
	releasesURL = "https://releases.skyhook.io/radar/latest"
	githubURL   = "https://api.github.com/repos/skyhook-io/radar/releases/latest"

	// Current is the current version of Radar, set at build time
	Current = "dev"

	// isDesktop is set to true when running as a desktop app (Wails).
	// Controls install method detection and enables in-app update flow.
	isDesktop bool

	mu          sync.Mutex
	updateCache = map[string]updateCacheEntry{}
	cacheTTL    = 1 * time.Hour
	errorTTL    = 5 * time.Minute
)

type updateCacheEntry struct {
	result    *UpdateInfo
	checkedAt time.Time
}

// InstallMethod represents how Radar was installed
type InstallMethod string

const (
	InstallHomebrew InstallMethod = "homebrew"
	InstallKrew     InstallMethod = "krew"
	InstallScoop    InstallMethod = "scoop"
	InstallDirect   InstallMethod = "direct"
	InstallDesktop  InstallMethod = "desktop"
)

// UpdateInfo contains version update information
type UpdateInfo struct {
	CurrentVersion string        `json:"currentVersion"`
	LatestVersion  string        `json:"latestVersion,omitempty"`
	UpdateAvail    bool          `json:"updateAvailable"`
	ReleaseURL     string        `json:"releaseUrl,omitempty"`
	ReleaseNotes   string        `json:"releaseNotes,omitempty"`
	InstallMethod  InstallMethod `json:"installMethod"`
	UpdateCommand  string        `json:"updateCommand,omitempty"`
	Error          string        `json:"error,omitempty"`
}

// githubRelease represents a GitHub release response
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

type checkOptions struct {
	source string
}

// SetCurrent sets the current version (called from main)
func SetCurrent(v string) {
	Current = v
}

// SetDesktop marks this instance as a desktop app. Must be called before
// any version checks. When set, detectInstallMethod returns InstallDesktop
// which triggers the in-app update flow instead of showing CLI commands.
func SetDesktop(v bool) {
	isDesktop = v
}

// IsDesktop returns whether the current instance is running as a desktop app.
func IsDesktop() bool {
	return isDesktop
}

// CheckForUpdate checks GitHub for the latest release
func CheckForUpdate(_ context.Context) *UpdateInfo {
	if buildChannel(Current) == buildChannelDevelopment {
		return checkForUpdateCached(checkOptions{source: "release-only"})
	}
	return checkForUpdateCached(checkOptions{})
}

// CheckForUpdateRelease returns release information without performing a
// browser-scoped update check.
func CheckForUpdateRelease(_ context.Context) *UpdateInfo {
	return checkForUpdateCached(checkOptions{source: "release-only"})
}

func ReportBrowserUpdateCheck(ctx context.Context, reportDay string) error {
	if buildChannel(Current) == buildChannelDevelopment {
		return nil
	}

	mode := "in-cluster"
	method := detectInstallMethod()
	params := url.Values{
		"v":       {Current},
		"os":      {runtime.GOOS},
		"arch":    {runtime.GOARCH},
		"method":  {string(method)},
		"mode":    {mode},
		"channel": {string(buildChannel(Current))},
		"source":  {"browser-proxy"},
		"report":  {"1"},
		"day":     {reportDay},
	}
	if installedAt := k8s.InstalledAtCached(); installedAt != 0 {
		params.Set("t", strconv.FormatInt(installedAt, 10))
	}

	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, fmt.Sprintf("%s?%s", releasesURL, params.Encode()), nil)
	if err != nil {
		return fmt.Errorf("create browser update check: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("send browser update check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("browser update check returned %d", resp.StatusCode)
	}
	return nil
}

func checkForUpdateCached(options checkOptions) *UpdateInfo {
	mu.Lock()
	entry := updateCache[options.source]

	// Use shorter TTL for cached errors so transient failures recover quickly
	ttl := cacheTTL
	if entry.result != nil && entry.result.Error != "" {
		ttl = errorTTL
	}

	if entry.result != nil && time.Since(entry.checkedAt) < ttl {
		result := *entry.result
		mu.Unlock()
		return &result
	}
	mu.Unlock()

	// Fetch outside the lock to avoid blocking concurrent callers during HTTP request.
	// Use a background context so request cancellation doesn't poison the cache.
	fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := fetchLatestRelease(fetchCtx, options)

	mu.Lock()
	updateCache[options.source] = updateCacheEntry{result: result, checkedAt: time.Now()}
	mu.Unlock()

	return result
}

func fetchLatestRelease(ctx context.Context, options checkOptions) *UpdateInfo {
	method := detectInstallMethod()
	result := &UpdateInfo{
		CurrentVersion: Current,
		InstallMethod:  method,
		UpdateCommand:  getUpdateCommand(method),
	}

	if Current == "dev" {
		return result
	}

	client := &http.Client{Timeout: 10 * time.Second}

	mode := "local"
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		mode = "in-cluster"
	}
	params := url.Values{
		"v":       {Current},
		"os":      {runtime.GOOS},
		"arch":    {runtime.GOARCH},
		"method":  {string(method)},
		"mode":    {mode},
		"channel": {string(buildChannel(Current))},
	}
	if installedAt := installTimestamp(ctx, mode); installedAt != 0 {
		params.Set("t", strconv.FormatInt(installedAt, 10))
	}
	if options.source != "" {
		params.Set("source", options.source)
		params.Set("report", "0")
	}
	proxyURL := fmt.Sprintf("%s?%s", releasesURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", proxyURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		log.Printf("[version] %s", result.Error)
		return result
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		// Fallback to GitHub directly
		if resp != nil {
			resp.Body.Close()
		}
		log.Printf("[version] Proxy failed, falling back to GitHub directly")
		req2, err2 := http.NewRequestWithContext(ctx, "GET", githubURL, nil)
		if err2 != nil {
			result.Error = fmt.Sprintf("failed to create fallback request: %v", err2)
			log.Printf("[version] %s", result.Error)
			return result
		}
		req2.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))
		resp, err = client.Do(req2)
		if err != nil {
			result.Error = fmt.Sprintf("failed to check for updates: %v", err)
			log.Printf("[version] %s", result.Error)
			return result
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("update check returned %d", resp.StatusCode)
		log.Printf("[version] %s", result.Error)
		return result
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		result.Error = fmt.Sprintf("failed to parse response: %v", err)
		log.Printf("[version] %s", result.Error)
		return result
	}

	result.LatestVersion = strings.TrimPrefix(release.TagName, "v")
	result.ReleaseURL = release.HTMLURL
	result.ReleaseNotes = truncateNotes(release.Body, 500)

	newer, err := isNewerVersion(result.LatestVersion, Current)
	if err != nil {
		result.Error = fmt.Sprintf("version comparison failed: %v", err)
		log.Printf("[version] %s", result.Error)
	}
	result.UpdateAvail = newer

	return result
}

// IsReleaseVersion accepts only stable x.y.z builds.
func IsReleaseVersion(value string) bool {
	v, err := semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
	return err == nil && v.Prerelease() == "" && v.Metadata() == ""
}

type buildChannelName string

const (
	buildChannelStable      buildChannelName = "stable"
	buildChannelPrerelease  buildChannelName = "prerelease"
	buildChannelCustom      buildChannelName = "custom"
	buildChannelDevelopment buildChannelName = "development"
)

func buildChannel(value string) buildChannelName {
	normalized := strings.TrimPrefix(strings.ToLower(value), "v")
	if normalized == "dev" || strings.HasPrefix(normalized, "dev-") ||
		strings.HasSuffix(normalized, "-dirty") || isCommitVersion(normalized) ||
		isGitDescribeVersion(normalized) || isPackageReleaseVersion(normalized) {
		return buildChannelDevelopment
	}
	if IsReleaseVersion(value) {
		return buildChannelStable
	}
	if v, err := semver.StrictNewVersion(normalized); err == nil {
		label := strings.ToLower(v.Prerelease())
		if isNamedPrerelease(label) {
			return buildChannelPrerelease
		}
	}
	return buildChannelCustom
}

func isPackageReleaseVersion(value string) bool {
	for _, prefix := range []string{"k8s-ui-v", "radar-app-v", "pkg/v"} {
		if strings.HasPrefix(value, prefix) && IsReleaseVersion(strings.TrimPrefix(value, prefix)) {
			return true
		}
	}
	return false
}

func isNamedPrerelease(label string) bool {
	for _, prefix := range []string{"alpha", "beta", "rc"} {
		if !strings.HasPrefix(label, prefix) {
			continue
		}
		for _, char := range strings.TrimPrefix(label, prefix) {
			if (char < '0' || char > '9') && char != '.' && char != '-' {
				return false
			}
		}
		return true
	}
	return false
}

func isCommitVersion(value string) bool {
	if len(value) < 7 || len(value) > 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isGitDescribeVersion(value string) bool {
	shaSeparator := strings.LastIndex(value, "-g")
	if shaSeparator < 0 || !isCommitVersion(value[shaSeparator+2:]) {
		return false
	}
	countSeparator := strings.LastIndex(value[:shaSeparator], "-")
	if countSeparator < 0 || countSeparator == shaSeparator-1 {
		return false
	}
	for _, char := range value[countSeparator+1 : shaSeparator] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// isNewerVersion compares semver versions using Masterminds/semver
func isNewerVersion(latest, current string) (bool, error) {
	latestV, err := semver.NewVersion(latest)
	if err != nil {
		return false, fmt.Errorf("failed to parse latest version %q: %w", latest, err)
	}
	currentV, err := semver.NewVersion(current)
	if err != nil {
		return false, fmt.Errorf("failed to parse current version %q: %w", current, err)
	}
	return latestV.GreaterThan(currentV), nil
}

func truncateNotes(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// installTimestamp reports when this install was set up. In-cluster that is the
// Deployment's creation time; ~/.radar is commonly an emptyDir whose birthtime
// resets with the pod and must not be mistaken for an installation identity.
func installTimestamp(ctx context.Context, mode string) int64 {
	if mode == "in-cluster" {
		return k8s.InstalledAt(ctx)
	}
	return radarDirBirthtime()
}

// radarDirBirthtime returns the creation timestamp of ~/.radar/ as Unix epoch
// seconds, or 0 if unavailable.
func radarDirBirthtime() int64 {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	return dirBirthtime(filepath.Join(homeDir, ".radar"))
}

// detectInstallMethod determines how Radar was installed based on binary path
func detectInstallMethod() InstallMethod {
	if isDesktop {
		return InstallDesktop
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("[version] Could not determine executable path: %v", err)
		return InstallDirect
	}

	return detectInstallMethodFromPath(exe)
}

// detectInstallMethodFromPath determines install method from a binary path.
// Extracted for testability.
func detectInstallMethodFromPath(exe string) InstallMethod {
	// Normalize path for comparison
	path := strings.ToLower(exe)

	// Homebrew: /opt/homebrew/..., /usr/local/Cellar/..., /home/linuxbrew/...
	if strings.Contains(path, "homebrew") || strings.Contains(path, "cellar") || strings.Contains(path, "linuxbrew") {
		return InstallHomebrew
	}

	// Krew: ~/.krew/store/...
	if strings.Contains(path, ".krew") {
		return InstallKrew
	}

	// Scoop: ~/scoop/apps/... or C:\Users\...\scoop\apps\...
	if strings.Contains(path, "scoop") {
		return InstallScoop
	}

	return InstallDirect
}

// getUpdateCommand returns the command to update based on install method.
// Desktop returns empty since updates are handled in-app.
func getUpdateCommand(method InstallMethod) string {
	return getUpdateCommandForOS(method, runtime.GOOS)
}

// getUpdateCommandForOS returns the update command for a given install method
// and OS. For InstallDirect, the install one-liner is idempotent — re-running
// it upgrades an existing binary in place. Returns "" for any GOOS that the
// public install script doesn't support, so the frontend falls through to the
// GitHub release-download link.
func getUpdateCommandForOS(method InstallMethod, goos string) string {
	switch method {
	case InstallHomebrew:
		return "brew upgrade skyhook-io/tap/radar"
	case InstallKrew:
		return "kubectl krew upgrade radar"
	case InstallScoop:
		return "scoop update radar"
	case InstallDirect:
		switch goos {
		case "darwin", "linux":
			return "curl -fsSL https://get.radarhq.io | sh"
		case "windows":
			return "irm https://get.radarhq.io/install.ps1 | iex"
		default:
			return ""
		}
	case InstallDesktop:
		return "" // in-app update, no CLI command
	default:
		return ""
	}
}
