package version

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// githubAsset represents a single file attached to a GitHub release.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// githubReleaseWithAssets extends the release with asset information.
type githubReleaseWithAssets struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

// FetchRelease fetches the latest release from GitHub including assets.
func FetchRelease(ctx context.Context) (*githubReleaseWithAssets, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/skyhook-io/radar/releases/latest", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubReleaseWithAssets
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &release, nil
}

// FindDesktopAsset finds the desktop asset for the current OS and architecture.
// Asset naming convention: radar-desktop_vX.Y.Z_{os}_{arch}.{ext}
func FindDesktopAsset(release *githubReleaseWithAssets, goos, goarch string) *githubAsset {
	// Normalize arch for asset matching
	arch := goarch
	if goos == "darwin" {
		arch = "universal"
	}

	// Build expected prefix: radar-desktop_ (version will vary)
	suffix := fmt.Sprintf("_%s_%s.", goos, arch)

	for i := range release.Assets {
		a := &release.Assets[i]
		if strings.HasPrefix(a.Name, "radar-desktop_") && strings.Contains(a.Name, suffix) {
			return a
		}
	}
	return nil
}

// ProgressFunc is called with bytes downloaded so far and total expected bytes.
type ProgressFunc func(downloaded, total int64)

// DownloadAsset downloads a release asset to the given destination path,
// reporting progress via the callback. The download goes to a temporary file
// first and is renamed on completion for atomicity.
func DownloadAsset(ctx context.Context, url string, dest string, progress ProgressFunc) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		f.Close()
		os.Remove(tmp) // clean up on failure; no-op if renamed
	}()

	total := resp.ContentLength
	var downloaded int64

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, total)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read: %w", readErr)
		}
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// checksumFor reads sha256sum output ("hash  name", or "hash *name" in binary
// mode, which is what sha256sum writes on Windows) and returns the hash for
// assetName.
func checksumFor(r io.Reader, assetName string) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 2 && strings.TrimPrefix(parts[1], "*") == assetName {
			return parts[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum found for %s", assetName)
}

// VerifyChecksum downloads the checksums file from the release and verifies
// that the given asset matches its expected SHA256 hash.
func VerifyChecksum(ctx context.Context, release *githubReleaseWithAssets, assetPath string, assetName string) error {
	// Find the checksums file
	var checksumURL string
	for _, a := range release.Assets {
		if a.Name == "checksums-desktop.txt" {
			checksumURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		return fmt.Errorf("release is missing checksums-desktop.txt; cannot verify download integrity")
	}

	// Download checksums
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return fmt.Errorf("create checksum request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", Current))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums download returned HTTP %d", resp.StatusCode)
	}

	expectedHash, err := checksumFor(resp.Body, assetName)
	if err != nil {
		return err
	}

	// Hash the downloaded file
	f, err := os.Open(assetPath)
	if err != nil {
		return fmt.Errorf("open asset for verification: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash asset: %w", err)
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	log.Printf("[updater] Checksum verified for %s", assetName)
	return nil
}

// UpdatesDir returns the path to the updates directory (~/.radar/updates/).
func UpdatesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".radar", "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create updates dir: %w", err)
	}
	return dir, nil
}

// DesktopAssetName returns the expected asset filename for the current platform.
func DesktopAssetName(version string) string {
	goos := runtime.GOOS
	arch := runtime.GOARCH
	if goos == "darwin" {
		arch = "universal"
	}

	ext := "tar.gz"
	if goos == "darwin" || goos == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("radar-desktop_%s_%s_%s.%s", version, goos, arch, ext)
}
