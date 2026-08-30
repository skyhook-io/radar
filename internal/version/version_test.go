package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

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

func TestInstallTimestampDoesNotUseLocalDirectoryInCluster(t *testing.T) {
	if got := installTimestamp(context.Background(), "in-cluster"); got != 0 {
		t.Fatalf("in-cluster timestamp = %d, want no local-directory fallback", got)
	}
}

func TestIsReleaseVersion(t *testing.T) {
	tests := map[string]bool{
		"1.2.3":       true,
		"v1.2.3":      true,
		"dev":         false,
		"1.2.3-dirty": false,
		"1.2.3-rc.1":  false,
		"1.2":         false,
		"":            false,
	}
	for value, want := range tests {
		if got := IsReleaseVersion(value); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestCheckForUpdateExcludesOnlyDevelopmentBuilds(t *testing.T) {
	var queries []url.Values
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.3.0"})
	}))
	defer proxy.Close()

	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	for _, current := range []string{
		"dev-a1b2c3d",
		"1.2.3-dirty",
		"k8s-ui-v1.13.3-27-g1197bab6",
		"1197bab6043c723e557714620758ace2dad36354",
	} {
		SetCurrent(current)
		resetUpdateCache()
		CheckForUpdate(context.Background())
		query := queries[len(queries)-1]
		if query.Get("source") != "release-only" || query.Get("report") != "0" {
			t.Errorf("CheckForUpdate with %q sent metered query %v", current, query)
		}
	}

	for _, current := range []string{"1.2.3", "1.2.3-rc.1", "1.8.6-pg.2"} {
		SetCurrent(current)
		resetUpdateCache()
		CheckForUpdate(context.Background())
		query := queries[len(queries)-1]
		if query.Has("source") || query.Has("report") {
			t.Errorf("measurable build %q sent unmetered query %v", current, query)
		}
	}

	beforeDev := len(queries)
	SetCurrent("dev")
	resetUpdateCache()
	CheckForUpdate(context.Background())
	if len(queries) != beforeDev {
		t.Errorf("dev build made %d update requests, want none", len(queries)-beforeDev)
	}
}

func TestReportBrowserUpdateCheckIsBestEffortAndIdentityFree(t *testing.T) {
	var queries []url.Values
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	SetCurrent("1.2.3-rc1")
	resetUpdateCache()
	cachedResult = &UpdateInfo{LatestVersion: "cached"}
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	if err := ReportBrowserUpdateCheck(context.Background(), "2026-08-29"); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("requests = %d, want 1", len(queries))
	}
	query := queries[0]
	for key, want := range map[string]string{
		"v": "1.2.3-rc1", "mode": "in-cluster", "source": "browser-proxy", "report": "1", "day": "2026-08-29",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query[%q] = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"rid", "iid", "auth"} {
		if query.Has(key) {
			t.Errorf("identity field %q present in query %v", key, query)
		}
	}
	if cachedResult == nil || cachedResult.LatestVersion != "cached" {
		t.Fatalf("browser check changed release cache: %+v", cachedResult)
	}

	SetCurrent("dev-a1b2c3d")
	if err := ReportBrowserUpdateCheck(context.Background(), "2026-08-29"); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("development build made %d browser requests, want none", len(queries)-1)
	}
}

func TestBuildChannel(t *testing.T) {
	tests := map[string]buildChannelName{
		"1.2.3":                       buildChannelStable,
		"v1.2.3":                      buildChannelStable,
		"1.2.3-rc.1":                  buildChannelPrerelease,
		"1.2.3-rc1":                   buildChannelPrerelease,
		"1.2.3-beta.2":                buildChannelPrerelease,
		"1.2.3-alphabet":              buildChannelCustom,
		"1.2.3-alpha.foo":             buildChannelCustom,
		"1.8.6-pg.2":                  buildChannelCustom,
		"acme-radar":                  buildChannelCustom,
		"1.2.3+vendor.1":              buildChannelCustom,
		"1.2.3-rc.01":                 buildChannelCustom,
		"dev":                         buildChannelDevelopment,
		"dev-a1b2c3d":                 buildChannelDevelopment,
		"1.2.3-dirty":                 buildChannelDevelopment,
		"k8s-ui-v1.13.3-27-g1197bab6": buildChannelDevelopment,
		"1197bab6043c723e557714620758ace2dad36354": buildChannelDevelopment,
	}
	for value, want := range tests {
		if got := buildChannel(value); got != want {
			t.Errorf("buildChannel(%q) = %q, want %q", value, got, want)
		}
	}
}

func resetUpdateCache() {
	mu.Lock()
	defer mu.Unlock()
	cachedResult = nil
	lastCheck = time.Time{}
}
