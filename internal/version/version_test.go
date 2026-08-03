package version

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFetchLatestReleaseOnlySendsAggregateFields(t *testing.T) {
	originalCurrent := Current
	Current = "1.8.7"
	t.Cleanup(func() { Current = originalCurrent })
	originalOptOut, optOutWasSet := os.LookupEnv(noUpdateCheckEnv)
	if err := os.Unsetenv(noUpdateCheckEnv); err != nil {
		t.Fatalf("unset %s: %v", noUpdateCheckEnv, err)
	}
	t.Cleanup(func() {
		if optOutWasSet {
			_ = os.Setenv(noUpdateCheckEnv, originalOptOut)
		} else {
			_ = os.Unsetenv(noUpdateCheckEnv)
		}
	})

	var captured *http.Request
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"tag_name":"v1.8.8",
				"html_url":"https://github.com/skyhook-io/radar/releases/tag/v1.8.8",
				"body":"notes"
			}`)),
			Header: make(http.Header),
		}, nil
	})}

	info := fetchLatestReleaseWithClient(context.Background(), client)
	if info.Error != "" || !info.UpdateAvail {
		t.Fatalf("update result = %+v, want an available update", info)
	}
	if captured == nil {
		t.Fatal("update check did not send a request")
	}

	parsed, err := url.Parse(captured.URL.String())
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", captured.URL.String(), err)
	}
	if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != releasesURL {
		t.Fatalf("update check endpoint = %q, want %q", got, releasesURL)
	}

	query := parsed.Query()
	if len(query) != 3 {
		t.Fatalf("update check query = %v, want exactly version, OS, and architecture", query)
	}
	for key, want := range map[string]string{
		"v": "1.8.7", "os": runtime.GOOS, "arch": runtime.GOARCH,
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query parameter %q = %q, want %q", key, got, want)
		}
	}
	if got, want := captured.Header.Get("User-Agent"), "radar/1.8.7"; got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
	if len(captured.Header) != 1 {
		t.Errorf("application request headers = %v, want only User-Agent", captured.Header)
	}
}

func TestFetchLatestReleaseFallbackOnlySendsVersionHeader(t *testing.T) {
	originalCurrent := Current
	Current = "1.8.7"
	t.Cleanup(func() { Current = originalCurrent })
	originalOptOut, optOutWasSet := os.LookupEnv(noUpdateCheckEnv)
	if err := os.Unsetenv(noUpdateCheckEnv); err != nil {
		t.Fatalf("unset %s: %v", noUpdateCheckEnv, err)
	}
	t.Cleanup(func() {
		if optOutWasSet {
			_ = os.Setenv(noUpdateCheckEnv, originalOptOut)
		} else {
			_ = os.Unsetenv(noUpdateCheckEnv)
		}
	})

	var captured []*http.Request
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = append(captured, request.Clone(request.Context()))
		if len(captured) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("unavailable")),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"tag_name":"v1.8.8",
				"html_url":"https://github.com/skyhook-io/radar/releases/tag/v1.8.8",
				"body":"notes"
			}`)),
			Header: make(http.Header),
		}, nil
	})}

	info := fetchLatestReleaseWithClient(context.Background(), client)
	if info.Error != "" || !info.UpdateAvail {
		t.Fatalf("fallback update result = %+v, want an available update", info)
	}
	if len(captured) != 2 {
		t.Fatalf("requests = %d, want primary plus fallback", len(captured))
	}
	fallback := captured[1]
	if got := fallback.URL.String(); got != githubURL {
		t.Fatalf("fallback URL = %q, want %q", got, githubURL)
	}
	if got, want := fallback.Header.Get("User-Agent"), "radar/1.8.7"; got != want {
		t.Errorf("fallback User-Agent = %q, want %q", got, want)
	}
	if len(fallback.Header) != 1 {
		t.Errorf("fallback application request headers = %v, want only User-Agent", fallback.Header)
	}
}

func TestFetchLatestReleaseSkipsAutomaticCheck(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })

	t.Run("development build", func(t *testing.T) {
		Current = "dev"
		original, wasSet := os.LookupEnv(noUpdateCheckEnv)
		if err := os.Unsetenv(noUpdateCheckEnv); err != nil {
			t.Fatalf("unset %s: %v", noUpdateCheckEnv, err)
		}
		t.Cleanup(func() {
			if wasSet {
				_ = os.Setenv(noUpdateCheckEnv, original)
			} else {
				_ = os.Unsetenv(noUpdateCheckEnv)
			}
		})
		assertSkippedUpdateCheck(t)
	})

	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "one", value: "1"},
		{name: "empty", value: ""},
		{name: "zero", value: "0"},
		{name: "false", value: "false"},
	} {
		t.Run("operator opt out "+tt.name, func(t *testing.T) {
			Current = "1.8.7"
			t.Setenv(noUpdateCheckEnv, tt.value)
			assertSkippedUpdateCheck(t)
		})
	}
}

func assertSkippedUpdateCheck(t *testing.T) {
	t.Helper()
	roundTrips := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		roundTrips++
		return nil, nil
	})}
	info := fetchLatestReleaseWithClient(context.Background(), client)
	if roundTrips != 0 {
		t.Fatalf("skipped update check made %d HTTP requests", roundTrips)
	}
	if info.LatestVersion != "" || info.ReleaseURL != "" || info.Error != "" {
		t.Fatalf("skipped update check returned remote data or an error: %+v", info)
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
		wantErr bool
	}{
		{"major upgrade", "2.0.0", "1.0.0", true, false},
		{"minor upgrade", "1.1.0", "1.0.0", true, false},
		{"patch upgrade", "1.0.1", "1.0.0", true, false},
		{"same version", "1.0.0", "1.0.0", false, false},
		{"downgrade", "1.0.0", "2.0.0", false, false},
		{"prerelease newer than stable", "1.1.0-rc1", "1.0.0", true, false},
		{"with v prefix on latest", "v1.1.0", "1.0.0", true, false},
		{"with v prefix on current", "1.1.0", "v1.0.0", true, false},
		{"invalid latest", "not-a-version", "1.0.0", false, true},
		{"invalid current", "1.0.0", "not-a-version", false, true},
		{"empty latest", "", "1.0.0", false, true},
		{"empty current", "1.0.0", "", false, true},
		{"both empty", "", "", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isNewerVersion(tt.latest, tt.current)
			if (err != nil) != tt.wantErr {
				t.Errorf("isNewerVersion(%q, %q) error = %v, wantErr %v", tt.latest, tt.current, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestGetUpdateCommand(t *testing.T) {
	tests := []struct {
		name   string
		method InstallMethod
		goos   string
		want   string
	}{
		{"homebrew", InstallHomebrew, "darwin", "brew upgrade skyhook-io/tap/radar"},
		{"krew", InstallKrew, "linux", "kubectl krew upgrade radar"},
		{"scoop", InstallScoop, "windows", "scoop update radar"},
		{"direct linux", InstallDirect, "linux", "curl -fsSL https://get.radarhq.io | sh"},
		{"direct darwin", InstallDirect, "darwin", "curl -fsSL https://get.radarhq.io | sh"},
		{"direct windows", InstallDirect, "windows", "irm https://get.radarhq.io/install.ps1 | iex"},
		{"direct freebsd falls through", InstallDirect, "freebsd", ""},
		{"direct empty goos falls through", InstallDirect, "", ""},
		{"desktop", InstallDesktop, "darwin", ""},
		{"unknown", InstallMethod("unknown"), "linux", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getUpdateCommandForOS(tt.method, tt.goos)
			if got != tt.want {
				t.Errorf("getUpdateCommandForOS(%q, %q) = %q, want %q", tt.method, tt.goos, got, tt.want)
			}
		})
	}
}

func TestDetectInstallMethodFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want InstallMethod
	}{
		{"homebrew mac arm", "/opt/homebrew/bin/radar", InstallHomebrew},
		{"homebrew cellar", "/usr/local/Cellar/radar/1.0/bin/radar", InstallHomebrew},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/radar", InstallHomebrew},
		{"krew", "/home/user/.krew/store/radar/v1.0/radar", InstallKrew},
		{"scoop unix", "/home/user/scoop/apps/radar/current/radar", InstallScoop},
		{"scoop windows", `C:\Users\user\scoop\apps\radar\current\radar.exe`, InstallScoop},
		{"direct /usr/local/bin", "/usr/local/bin/radar", InstallDirect},
		{"direct home", "/home/user/bin/radar", InstallDirect},
		{"direct tmp", "/tmp/radar", InstallDirect},
		{"mixed case Homebrew", "/opt/Homebrew/bin/radar", InstallHomebrew},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstallMethodFromPath(tt.path)
			if got != tt.want {
				t.Errorf("detectInstallMethodFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTruncateNotes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exactly at max", "hello", 5, "hello"},
		{"longer than max", "hello world", 5, "hello..."},
		{"empty string", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateNotes(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateNotes(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
