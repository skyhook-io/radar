package helm

import "testing"

func TestFindBestUpgradeVersion(t *testing.T) {
	tests := []struct {
		name            string
		candidates      []repoVersionInfo
		wantVersion     string
		wantRepo        string
	}{
		{
			name:        "no candidates returns empty",
			candidates:  nil,
			wantVersion: "",
			wantRepo:    "",
		},
		{
			name: "single repo with current version",
			candidates: []repoVersionInfo{
				{repoName: "metallb", latestVersion: "0.15.3", hasCurrentVersion: true},
			},
			wantVersion: "0.15.3",
			wantRepo:    "metallb",
		},
		{
			name: "multiple repos only one has current version - picks source repo",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "6.4.22", hasCurrentVersion: false},
				{repoName: "metallb", latestVersion: "0.15.3", hasCurrentVersion: true},
			},
			wantVersion: "0.15.3",
			wantRepo:    "metallb",
		},
		{
			name: "multiple repos none has current version - falls back to highest",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "6.4.22", hasCurrentVersion: false},
				{repoName: "metallb", latestVersion: "0.15.3", hasCurrentVersion: false},
			},
			wantVersion: "6.4.22",
			wantRepo:    "bitnami",
		},
		{
			name: "multiple repos both have current version - picks highest among them",
			candidates: []repoVersionInfo{
				{repoName: "repo-a", latestVersion: "2.0.0", hasCurrentVersion: true},
				{repoName: "repo-b", latestVersion: "3.0.0", hasCurrentVersion: true},
			},
			wantVersion: "3.0.0",
			wantRepo:    "repo-b",
		},
		{
			name: "source repo has lower latest than non-source - still picks source",
			candidates: []repoVersionInfo{
				{repoName: "community", latestVersion: "10.0.0", hasCurrentVersion: false},
				{repoName: "official", latestVersion: "1.2.0", hasCurrentVersion: true},
			},
			wantVersion: "1.2.0",
			wantRepo:    "official",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVersion, gotRepo := findBestUpgradeVersion(tt.candidates)
			if gotVersion != tt.wantVersion {
				t.Errorf("findBestUpgradeVersion() version = %q, want %q", gotVersion, tt.wantVersion)
			}
			if gotRepo != tt.wantRepo {
				t.Errorf("findBestUpgradeVersion() repo = %q, want %q", gotRepo, tt.wantRepo)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"0.15.3", "6.4.22", -1},
		{"6.4.22", "0.15.3", 1},
		{"v1.0.0", "1.0.0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.v1+"_vs_"+tt.v2, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}
