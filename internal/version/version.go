package version

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

var (
	// Current is the current version of Radar, set at build time
	Current = "dev"

	// cached update check result
	mu           sync.Mutex
	cachedResult *UpdateInfo
	lastCheck    time.Time
	cacheTTL     = 1 * time.Hour
	errorTTL     = 5 * time.Minute
)

// InstallMethod represents how Radar was installed
type InstallMethod string

const (
	InstallHomebrew InstallMethod = "homebrew"
	InstallKrew     InstallMethod = "krew"
	InstallScoop    InstallMethod = "scoop"
	InstallDirect   InstallMethod = "direct"
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

// SetCurrent sets the current version (called from main)
func SetCurrent(v string) {
	Current = v
}

// CheckForUpdate checks GitHub for the latest release
func CheckForUpdate(_ context.Context) *UpdateInfo {
	mu.Lock()

	// Use shorter TTL for cached errors so transient failures recover quickly
	ttl := cacheTTL
	if cachedResult != nil && cachedResult.Error != "" {
		ttl = errorTTL
	}

	if cachedResult != nil && time.Since(lastCheck) < ttl {
		result := *cachedResult
		mu.Unlock()
		return &result
	}
	mu.Unlock()

	// Fetch outside the lock to avoid blocking concurrent callers during HTTP request.
	// Use a background context so request cancellation doesn't poison the cache.
	fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := fetchLatestRelease(fetchCtx)

	mu.Lock()
	cachedResult = result
	lastCheck = time.Now()
	mu.Unlock()

	return result
}

func fetchLatestRelease(ctx context.Context) *UpdateInfo {
	method := detectInstallMethod()
	result := &UpdateInfo{
		CurrentVersion: Current,
		InstallMethod:  method,
		UpdateCommand:  getUpdateCommand(method),
	}

	// Don't compare dev builds
	if Current == "dev" {
		return result
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/skyhook-io/radar/releases/latest", nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		log.Printf("[version] %s", result.Error)
		return result
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))

	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("failed to check for updates: %v", err)
		log.Printf("[version] %s", result.Error)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("GitHub API returned %d", resp.StatusCode)
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

// detectInstallMethod determines how Radar was installed based on binary path
func detectInstallMethod() InstallMethod {
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

// getUpdateCommand returns the command to update based on install method
func getUpdateCommand(method InstallMethod) string {
	switch method {
	case InstallHomebrew:
		return "brew upgrade skyhook-io/tap/radar"
	case InstallKrew:
		return "kubectl krew upgrade radar"
	case InstallScoop:
		return "scoop update radar"
	default:
		return ""
	}
}
