package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// Current is the current version of Radar, set at build time
	Current = "dev"

	// cached update check result
	mu           sync.RWMutex
	cachedResult *UpdateInfo
	lastCheck    time.Time
	cacheTTL     = 1 * time.Hour
)

// UpdateInfo contains version update information
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	UpdateAvail    bool   `json:"updateAvailable"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	ReleaseNotes   string `json:"releaseNotes,omitempty"`
	Error          string `json:"error,omitempty"`
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
func CheckForUpdate() *UpdateInfo {
	mu.RLock()
	if cachedResult != nil && time.Since(lastCheck) < cacheTTL {
		result := *cachedResult
		mu.RUnlock()
		return &result
	}
	mu.RUnlock()

	// Fetch from GitHub
	result := fetchLatestRelease()

	// Cache the result
	mu.Lock()
	cachedResult = result
	lastCheck = time.Now()
	mu.Unlock()

	return result
}

func fetchLatestRelease() *UpdateInfo {
	result := &UpdateInfo{
		CurrentVersion: Current,
	}

	// Don't check for dev builds
	if Current == "dev" {
		result.Error = "development build"
		return result
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/skyhook-io/radar/releases/latest")
	if err != nil {
		result.Error = fmt.Sprintf("failed to check for updates: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("GitHub API returned %d", resp.StatusCode)
		return result
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		result.Error = fmt.Sprintf("failed to parse response: %v", err)
		return result
	}

	result.LatestVersion = strings.TrimPrefix(release.TagName, "v")
	result.ReleaseURL = release.HTMLURL
	result.ReleaseNotes = truncateNotes(release.Body, 500)
	result.UpdateAvail = isNewerVersion(result.LatestVersion, Current)

	return result
}

// isNewerVersion compares semver-like versions
func isNewerVersion(latest, current string) bool {
	// Strip v prefix if present
	latest = strings.TrimPrefix(latest, "v")
	current = strings.TrimPrefix(current, "v")

	// Simple comparison - split by dots and compare
	latestParts := strings.Split(latest, ".")
	currentParts := strings.Split(current, ".")

	for i := 0; i < len(latestParts) && i < len(currentParts); i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}

	// If latest has more parts (e.g., 1.2.3 vs 1.2), it's newer
	return len(latestParts) > len(currentParts)
}

func truncateNotes(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
