package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
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
		{"homebrew tap", InstallHomebrew, "darwin", "brew upgrade skyhook-io/tap/radar"},
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
			got := getUpdateCommandForOS(tt.method, tt.goos, "/opt/homebrew/Cellar/radar/1.0/bin/kubectl-radar")
			if got != tt.want {
				t.Errorf("getUpdateCommandForOS(%q, %q, tap path) = %q, want %q", tt.method, tt.goos, got, tt.want)
			}
		})
	}
}

func TestHomebrewUpgradeCommand(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		want string
	}{
		{"core on Apple Silicon", "/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar", "brew upgrade kubectl-radar"},
		{"core on Intel", "/usr/local/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar", "brew upgrade kubectl-radar"},
		{"core on Linux", "/home/linuxbrew/.linuxbrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar", "brew upgrade kubectl-radar"},
		{"tap", "/opt/homebrew/Cellar/radar/1.16.0/bin/kubectl-radar", "brew upgrade skyhook-io/tap/radar"},
		{"unresolved link falls back to tap", "/opt/homebrew/bin/radar", "brew upgrade skyhook-io/tap/radar"},
		{"unknown path falls back to tap", "", "brew upgrade skyhook-io/tap/radar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := homebrewUpgradeCommand(tt.exe); got != tt.want {
				t.Errorf("homebrewUpgradeCommand(%q) = %q, want %q", tt.exe, got, tt.want)
			}
		})
	}
}

// TestInstallLayouts builds each installer's on-disk layout with real symlinks
// and checks what a binary launched through the user-facing path reports.
func TestInstallLayouts(t *testing.T) {
	link := func(t *testing.T, target, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, name); err != nil {
			// Windows needs Developer Mode or elevation to create symlinks.
			t.Skipf("cannot create symlinks here: %v", err)
		}
	}
	binary := func(t *testing.T, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T, root string) string // returns the path the user runs
		wantMethod  InstallMethod
		wantCommand string
	}{
		{
			name: "homebrew-core on Apple Silicon",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar")
				link(t, "kubectl-radar", root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/radar")
				link(t, "../Cellar/kubectl-radar/1.16.0/bin/radar", root+"/opt/homebrew/bin/radar")
				return root + "/opt/homebrew/bin/radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade kubectl-radar",
		},
		{
			name: "homebrew-core on Intel, previously reported as a direct install",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/usr/local/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar")
				link(t, "kubectl-radar", root+"/usr/local/Cellar/kubectl-radar/1.16.0/bin/radar")
				link(t, "../Cellar/kubectl-radar/1.16.0/bin/radar", root+"/usr/local/bin/radar")
				return root + "/usr/local/bin/radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade kubectl-radar",
		},
		{
			name: "homebrew-core via kubectl plugin name",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar")
				link(t, "../Cellar/kubectl-radar/1.16.0/bin/kubectl-radar", root+"/opt/homebrew/bin/kubectl-radar")
				return root + "/opt/homebrew/bin/kubectl-radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade kubectl-radar",
		},
		{
			name: "homebrew-core via opt link, as brew services runs it",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar")
				link(t, "../Cellar/kubectl-radar/1.16.0", root+"/opt/homebrew/opt/kubectl-radar")
				return root + "/opt/homebrew/opt/kubectl-radar/bin/kubectl-radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade kubectl-radar",
		},
		{
			name: "skyhook-io tap",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/opt/homebrew/Cellar/radar/1.16.0/bin/kubectl-radar")
				link(t, "kubectl-radar", root+"/opt/homebrew/Cellar/radar/1.16.0/bin/radar")
				link(t, "../Cellar/radar/1.16.0/bin/radar", root+"/opt/homebrew/bin/radar")
				return root + "/opt/homebrew/bin/radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade skyhook-io/tap/radar",
		},
		{
			name: "own symlink to a Homebrew install",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar")
				link(t, root+"/opt/homebrew/Cellar/kubectl-radar/1.16.0/bin/kubectl-radar", root+"/home/user/bin/radar")
				return root + "/home/user/bin/radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade kubectl-radar",
		},
		{
			name: "install script shorthand link",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/usr/local/bin/kubectl-radar")
				link(t, "kubectl-radar", root+"/usr/local/bin/radar")
				return root + "/usr/local/bin/radar"
			},
			wantMethod:  InstallDirect,
			wantCommand: "curl -fsSL https://get.radarhq.io | sh",
		},
		{
			name: "krew",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/home/user/.krew/store/radar/v1.16.0/kubectl-radar")
				link(t, "../store/radar/v1.16.0/kubectl-radar", root+"/home/user/.krew/bin/kubectl-radar")
				return root + "/home/user/.krew/bin/kubectl-radar"
			},
			wantMethod:  InstallKrew,
			wantCommand: "kubectl krew upgrade radar",
		},
		{
			name: "scoop current link",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/home/user/scoop/apps/radar/1.16.0/radar")
				link(t, "1.16.0", root+"/home/user/scoop/apps/radar/current")
				return root + "/home/user/scoop/apps/radar/current/radar"
			},
			wantMethod:  InstallScoop,
			wantCommand: "scoop update radar",
		},
		{
			name: "plain binary, no links",
			setup: func(t *testing.T, root string) string {
				binary(t, root+"/home/user/bin/radar")
				return root + "/home/user/bin/radar"
			},
			wantMethod:  InstallDirect,
			wantCommand: "curl -fsSL https://get.radarhq.io | sh",
		},
		{
			name: "dangling link keeps the original path",
			setup: func(t *testing.T, root string) string {
				link(t, "missing", root+"/opt/homebrew/bin/radar")
				return root + "/opt/homebrew/bin/radar"
			},
			wantMethod:  InstallHomebrew,
			wantCommand: "brew upgrade skyhook-io/tap/radar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not t.TempDir: its path includes the test name, which can contain
			// "homebrew" and mask a detection that only worked by accident.
			root, err := os.MkdirTemp("", "layout")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			exe := resolveSymlinks(tt.setup(t, root))
			if got := detectInstallMethodFromPath(exe); got != tt.wantMethod {
				t.Errorf("method = %q, want %q (resolved %s)", got, tt.wantMethod, exe)
			}
			if got := getUpdateCommandForOS(detectInstallMethodFromPath(exe), "darwin", exe); got != tt.wantCommand {
				t.Errorf("command = %q, want %q (resolved %s)", got, tt.wantCommand, exe)
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

func TestInstallTimestampFallsBackInCluster(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".radar"), 0o755); err != nil {
		t.Fatalf("seed ~/.radar: %v", err)
	}

	ctx := context.Background()
	local := installTimestamp(ctx, "local")
	if got := installTimestamp(ctx, "in-cluster"); got != local {
		t.Fatalf("in-cluster timestamp %d did not fall back to the local timestamp %d", got, local)
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

func TestCheckForUpdateSelectsSourceByBuildChannel(t *testing.T) {
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
		if query.Get("source") != "release-only" {
			t.Errorf("CheckForUpdate with %q used source %q, want release-only", current, query.Get("source"))
		}
		if got := query.Get("channel"); got != string(buildChannel(current)) {
			t.Errorf("CheckForUpdate with %q sent channel %q", current, got)
		}
	}

	for _, current := range []string{"1.2.3", "1.2.3-rc.1", "1.8.6-pg.2"} {
		SetCurrent(current)
		resetUpdateCache()
		CheckForUpdate(context.Background())
		query := queries[len(queries)-1]
		if query.Has("source") {
			t.Errorf("CheckForUpdate with %q used source %q, want default", current, query.Get("source"))
		}
		if got := query.Get("channel"); got != string(buildChannel(current)) {
			t.Errorf("CheckForUpdate with %q sent channel %q", current, got)
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

func TestRelayUpdateCheck(t *testing.T) {
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
	updateCache[""] = updateCacheEntry{result: &UpdateInfo{LatestVersion: "cached"}}
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	if err := RelayUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("requests = %d, want 1", len(queries))
	}
	query := queries[0]
	for key, want := range map[string]string{
		"v": "1.2.3-rc1", "mode": "in-cluster", "channel": "prerelease", "source": "browser-proxy",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query[%q] = %q, want %q", key, got, want)
		}
	}
	if cached := updateCache[""]; cached.result == nil || cached.result.LatestVersion != "cached" {
		t.Fatalf("relayed check changed release cache: %+v", cached)
	}

	SetCurrent("dev-a1b2c3d")
	if err := RelayUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("development build made %d relayed requests, want none", len(queries)-1)
	}
}

func TestReleaseOnlyCheckDoesNotSatisfyDefaultCheck(t *testing.T) {
	var queries []url.Values
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.3.0"})
	}))
	defer proxy.Close()

	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	SetCurrent("1.2.3")
	resetUpdateCache()
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	CheckForUpdateRelease(context.Background())
	CheckForUpdate(context.Background())

	if len(queries) != 2 {
		t.Fatalf("requests = %d, want separate release-only and default requests", len(queries))
	}
	if got := queries[0].Get("source"); got != "release-only" {
		t.Fatalf("first source = %q, want release-only", got)
	}
	if queries[1].Has("source") {
		t.Fatalf("default request used source %q", queries[1].Get("source"))
	}
}

func TestCheckForUpdateSingleFlightsConcurrentCacheMisses(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.3.0"})
	}))
	defer proxy.Close()

	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	SetCurrent("1.2.3")
	resetUpdateCache()
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	const callers = 12
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			CheckForUpdate(context.Background())
		}()
	}
	<-started
	close(release)
	wg.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("release requests = %d, want 1", got)
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
		"k8s-ui-v1.13.3":              buildChannelDevelopment,
		"radar-app-v1.2.3":            buildChannelDevelopment,
		"pkg/v1.12.1":                 buildChannelDevelopment,
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
	updateCache = map[string]updateCacheEntry{}
	disabledByFlag = false
	lastManualResult = nil
}

func TestCheckForUpdateDisabledByFlag(t *testing.T) {
	var requestCount int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.3.0"})
	}))
	defer proxy.Close()

	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	SetCurrent("1.2.0")
	resetUpdateCache()
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	SetDisabled(true)
	if !IsDisabled() {
		t.Fatal("expected IsDisabled() to be true when disabled by flag")
	}

	info := CheckForUpdate(context.Background())
	if info == nil {
		t.Fatal("expected non-nil UpdateInfo when disabled")
	}
	if info.UpdateAvail {
		t.Errorf("expected UpdateAvail to be false when disabled, got true")
	}
	if info.LatestVersion != "" {
		t.Errorf("expected LatestVersion to be empty when disabled, got %q", info.LatestVersion)
	}
	if info.ReleaseURL != defaultReleasesURL {
		t.Errorf("expected ReleaseURL to be %q when disabled, got %q", defaultReleasesURL, info.ReleaseURL)
	}
	if requestCount != 0 {
		t.Errorf("CheckForUpdate made %d HTTP requests when disabled, want 0", requestCount)
	}

	infoRelease := CheckForUpdateRelease(context.Background())
	if infoRelease == nil || infoRelease.UpdateAvail {
		t.Errorf("expected CheckForUpdateRelease to return clean UpdateInfo with UpdateAvail=false")
	}
	if requestCount != 0 {
		t.Errorf("CheckForUpdateRelease made %d HTTP requests when disabled, want 0", requestCount)
	}

	if err := RelayUpdateCheck(context.Background()); err != nil {
		t.Errorf("RelayUpdateCheck returned error when disabled: %v", err)
	}
	if requestCount != 0 {
		t.Errorf("RelayUpdateCheck made %d HTTP requests when disabled, want 0", requestCount)
	}
}

func TestCheckForUpdateEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envVar   string
		value    string
		disabled bool
	}{
		{"explicit opt-out", "RADAR_DISABLE_VERSION_CHECK", "1", true},
		{"true opt-out", "RADAR_DISABLE_VERSION_CHECK", "true", true},
		{"case-insensitive opt-out", "RADAR_DISABLE_VERSION_CHECK", "TRUE", true},
		{"zero leaves checks enabled", "RADAR_DISABLE_VERSION_CHECK", "0", false},
		{"false leaves checks enabled", "RADAR_DISABLE_VERSION_CHECK", "false", false},
		{"unset leaves checks enabled", "RADAR_DISABLE_VERSION_CHECK", "", false},
		{"DNT leaves checks enabled", "DO_NOT_TRACK", "1", false},
		{"unused alias leaves checks enabled", "RADAR_NO_VERSION_CHECK", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requestCount int
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.3.0"})
			}))
			defer proxy.Close()

			previousURL, previousVersion := releasesURL, Current
			releasesURL = proxy.URL
			SetCurrent("1.2.0")
			resetUpdateCache()
			t.Cleanup(func() {
				releasesURL = previousURL
				SetCurrent(previousVersion)
				resetUpdateCache()
			})

			t.Setenv("RADAR_DISABLE_VERSION_CHECK", "")
			t.Setenv(tc.envVar, tc.value)
			if got := IsDisabled(); got != tc.disabled {
				t.Fatalf("IsDisabled() = %v for %s=%q, want %v", got, tc.envVar, tc.value, tc.disabled)
			}

			info := CheckForUpdate(context.Background())
			if info == nil {
				t.Fatal("expected non-nil UpdateInfo")
			}
			if tc.disabled {
				if info.UpdateAvail || info.LatestVersion != "" {
					t.Errorf("expected no release lookup when disabled, got %+v", info)
				}
				if requestCount != 0 {
					t.Errorf("made %d requests when disabled, want 0", requestCount)
				}
			} else {
				if !info.UpdateAvail || info.LatestVersion != "1.3.0" {
					t.Errorf("expected available update from release lookup, got %+v", info)
				}
				if requestCount != 1 {
					t.Errorf("made %d requests when enabled, want 1", requestCount)
				}
			}
		})
	}
}

func TestManualUpdateCheckPreservesAutomaticOptOut(t *testing.T) {
	for _, byEnv := range []bool{false, true} {
		name := "flag"
		if byEnv {
			name = "environment"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("RADAR_DISABLE_VERSION_CHECK", "")
			var requests atomic.Int32
			var status atomic.Int32
			status.Store(http.StatusOK)
			var tag atomic.Value
			tag.Store("v1.3.0")
			queries := make(chan url.Values, 16)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				queries <- r.URL.Query()
				if code := int(status.Load()); code != http.StatusOK {
					w.WriteHeader(code)
					return
				}
				_ = json.NewEncoder(w).Encode(githubRelease{TagName: tag.Load().(string)})
			}))
			t.Cleanup(proxy.Close)
			previousURL, previousGitHub, previousVersion := releasesURL, githubURL, Current
			releasesURL, githubURL = proxy.URL, proxy.URL
			SetCurrent("1.2.0")
			resetUpdateCache()
			t.Cleanup(func() {
				releasesURL, githubURL = previousURL, previousGitHub
				SetCurrent(previousVersion)
				resetUpdateCache()
			})

			if info := CheckForUpdate(context.Background()); info.LatestVersion != "1.3.0" {
				t.Fatalf("warm cache: %+v", info)
			}
			<-queries
			if byEnv {
				t.Setenv("RADAR_DISABLE_VERSION_CHECK", "1")
			} else {
				SetDisabled(true)
			}
			for _, latest := range []string{"1.4.0", "1.5.0"} {
				tag.Store("v" + latest)
				info := CheckForUpdateManual(context.Background())
				if info.Error != "" || !info.UpdateAvail || info.LatestVersion != latest {
					t.Fatalf("fresh manual lookup: %+v", info)
				}
				if source := (<-queries).Get("source"); source != "release-only" {
					t.Fatalf("manual source = %q, want release-only", source)
				}
			}
			if got := requests.Load(); got != 3 {
				t.Fatalf("requests = %d, want warm cache plus two fresh checks", got)
			}
			if !IsDisabled() {
				t.Fatal("manual check changed the opt-out")
			}
			if info := CheckForUpdate(context.Background()); info.LatestVersion != "1.5.0" || !info.UpdateAvail {
				t.Fatalf("automatic refresh lost the manual result: %+v", info)
			}
			if info := CheckForUpdateRelease(context.Background()); info.LatestVersion != "1.5.0" {
				t.Fatalf("release-only refresh lost the manual result: %+v", info)
			}
			if err := RelayUpdateCheck(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 3 {
				t.Fatalf("automatic checks made requests after manual check: %d", got)
			}

			status.Store(http.StatusBadGateway)
			if info := CheckForUpdateManual(context.Background()); info.Error == "" {
				t.Fatalf("failed manual lookup reported success: %+v", info)
			}
			if !IsDisabled() {
				t.Fatal("failed manual check changed the opt-out")
			}
			status.Store(http.StatusOK)
			for _, current := range []string{"dev", "1.6.0-37-g8b325b372", "abc1234", "radar-app-v0.3.1"} {
				SetCurrent(current)
				if info := CheckForUpdateManual(context.Background()); info.Error != "" || info.LatestVersion != "1.5.0" || info.UpdateAvail || info.BuildChannel != "development" {
					t.Fatalf("development build manual result: %+v", info)
				}
			}
			tag.Store("invalid")
			if info := CheckForUpdateManual(context.Background()); info.Error == "" {
				t.Fatalf("invalid release version reported success: %+v", info)
			}
		})
	}
}

func TestManualUpdateCheckSupersedesInFlightAutomaticLookup(t *testing.T) {
	for _, source := range []string{"", "release-only"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("RADAR_DISABLE_VERSION_CHECK", "")
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var requests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tag := "v1.4.0"
				if requests.Add(1) == 1 {
					close(started)
					<-release
					tag = "v1.3.0"
				}
				_ = json.NewEncoder(w).Encode(githubRelease{TagName: tag})
			}))
			t.Cleanup(proxy.Close)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			previousURL, previousVersion := releasesURL, Current
			releasesURL = proxy.URL
			SetCurrent("1.2.0")
			resetUpdateCache()
			t.Cleanup(func() {
				releasesURL = previousURL
				SetCurrent(previousVersion)
				resetUpdateCache()
			})

			automatic := make(chan *UpdateInfo, 1)
			go func() { automatic <- checkForUpdateCached(checkOptions{source: source}) }()
			<-started
			manual := CheckForUpdateManual(context.Background())
			once.Do(func() { close(release) })
			background := <-automatic
			if manual.LatestVersion != "1.4.0" || manual.Error != "" {
				t.Fatalf("manual result: %+v", manual)
			}
			if background.LatestVersion != "1.4.0" {
				t.Fatalf("in-flight lookup replaced manual result: %+v", background)
			}
			for _, info := range []*UpdateInfo{CheckForUpdate(context.Background()), CheckForUpdateRelease(context.Background())} {
				if info.LatestVersion != "1.4.0" || info.Error != "" {
					t.Fatalf("cached lookup lost manual result: %+v", info)
				}
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("requests = %d, want one automatic and one manual", got)
			}
		})
	}
}

// A build of a commit past a release tag parses as a prerelease of that tag,
// so the tag would always read as an update.
func TestAutomaticCheckOffersNoUpdateToDevelopmentBuilds(t *testing.T) {
	t.Setenv("RADAR_DISABLE_VERSION_CHECK", "")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.18.2"})
	}))
	defer proxy.Close()
	previousURL, previousVersion := releasesURL, Current
	releasesURL = proxy.URL
	t.Cleanup(func() {
		releasesURL = previousURL
		SetCurrent(previousVersion)
		resetUpdateCache()
	})

	for _, current := range []string{"1.18.2-22-ga0694c7fa", "1.18.2-22-ga0694c7fa-dirty", "1.18.2-dirty"} {
		SetCurrent(current)
		resetUpdateCache()
		info := CheckForUpdate(context.Background())
		if info.UpdateAvail || info.LatestVersion != "1.18.2" || info.Error != "" {
			t.Errorf("CheckForUpdate with %q = %+v, want latest 1.18.2 and no update", current, info)
		}
	}
	SetCurrent("1.18.1")
	resetUpdateCache()
	if info := CheckForUpdate(context.Background()); !info.UpdateAvail {
		t.Errorf("a release behind the latest got no update: %+v", info)
	}
}
