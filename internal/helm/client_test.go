package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/helmhistory"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFindBestUpgradeVersion(t *testing.T) {
	tests := []struct {
		name        string
		candidates  []repoVersionInfo
		sourceHosts []string
		wantVersion string
		wantRepo    string
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
			name: "multiple repos both have current version without affinity - bail out",
			candidates: []repoVersionInfo{
				{repoName: "repo-a", latestVersion: "2.0.0", hasCurrentVersion: true, repoURL: "https://charts.example-a.com"},
				{repoName: "repo-b", latestVersion: "3.0.0", hasCurrentVersion: true, repoURL: "https://charts.example-b.com"},
			},
			wantVersion: "",
			wantRepo:    "",
		},
		{
			name: "multiple repos both have current version with affinity - picks matching repo",
			candidates: []repoVersionInfo{
				{repoName: "repo-a", latestVersion: "2.0.0", hasCurrentVersion: true, repoURL: "https://charts.example-a.com"},
				{repoName: "repo-b", latestVersion: "3.0.0", hasCurrentVersion: true, repoURL: "https://charts.example-b.com"},
			},
			sourceHosts: []string{"example-b.com"},
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
		{
			name: "ambiguous chart-name collision without affinity - bail out",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "6.4.22", hasCurrentVersion: false, repoURL: "https://charts.bitnami.com/bitnami"},
				{repoName: "argo", latestVersion: "8.5.0", hasCurrentVersion: false, repoURL: "https://argoproj.github.io/argo-helm"},
			},
			wantVersion: "",
			wantRepo:    "",
		},
		{
			name: "single candidate without current version - accept (stale index case)",
			candidates: []repoVersionInfo{
				{repoName: "argo", latestVersion: "8.5.0", hasCurrentVersion: false, repoURL: "https://argoproj.github.io/argo-helm"},
			},
			wantVersion: "8.5.0",
			wantRepo:    "argo",
		},
		{
			name: "source-affinity host match picks correct repo",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "6.4.22", hasCurrentVersion: false, repoURL: "https://charts.bitnami.com/bitnami"},
				{repoName: "argo", latestVersion: "8.5.0", hasCurrentVersion: false, repoURL: "https://argoproj.github.io/argo-helm"},
			},
			sourceHosts: []string{"argoproj.github.io"},
			wantVersion: "8.5.0",
			wantRepo:    "argo",
		},
		{
			name: "source-affinity registered-domain match (charts.bitnami.com vs bitnami.com)",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "12.0.0", hasCurrentVersion: false, repoURL: "https://charts.bitnami.com/bitnami"},
				{repoName: "argo", latestVersion: "8.5.0", hasCurrentVersion: false, repoURL: "https://argoproj.github.io/argo-helm"},
			},
			sourceHosts: []string{"bitnami.com"},
			wantVersion: "12.0.0",
			wantRepo:    "bitnami",
		},
		{
			name: "source-affinity hosts present but none match - bail out",
			candidates: []repoVersionInfo{
				{repoName: "bitnami", latestVersion: "6.4.22", hasCurrentVersion: false, repoURL: "https://charts.bitnami.com/bitnami"},
				{repoName: "argo", latestVersion: "8.5.0", hasCurrentVersion: false, repoURL: "https://argoproj.github.io/argo-helm"},
			},
			sourceHosts: []string{"github.com"}, // chart-declared, but not the repo's host
			wantVersion: "",
			wantRepo:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVersion, gotRepo := findBestUpgradeVersion(tt.candidates, tt.sourceHosts)
			if gotVersion != tt.wantVersion {
				t.Errorf("findBestUpgradeVersion() version = %q, want %q", gotVersion, tt.wantVersion)
			}
			if gotRepo != tt.wantRepo {
				t.Errorf("findBestUpgradeVersion() repo = %q, want %q", gotRepo, tt.wantRepo)
			}
		})
	}
}

func TestChartSourceHosts(t *testing.T) {
	tests := []struct {
		name    string
		home    string
		sources []string
		want    []string
	}{
		{
			name: "empty inputs",
			want: nil,
		},
		{
			name: "bitnami home only",
			home: "https://bitnami.com",
			want: []string{"bitnami.com"},
		},
		{
			name: "subdomain expands to registered domain",
			home: "https://charts.bitnami.com",
			want: []string{"charts.bitnami.com", "bitnami.com"},
		},
		{
			name:    "deduplicates across home and sources",
			home:    "https://github.com/argoproj/argo-helm",
			sources: []string{"https://github.com/argoproj/argo-cd"},
			want:    []string{"github.com", "argoproj.github.io"},
		},
		{
			name: "argo-cd realistic chart metadata derives argoproj.github.io",
			home: "https://github.com/argoproj/argo-helm",
			want: []string{"github.com", "argoproj.github.io"},
		},
		{
			name: "github.io chart home does not seed bare github.io (multi-tenant)",
			home: "https://argoproj.github.io",
			want: []string{"argoproj.github.io"},
		},
		{
			name: "ipv4 host does not seed a bogus registered domain",
			home: "http://127.0.0.1:8080/charts",
			want: []string{"127.0.0.1"},
		},
		{
			name:    "skips invalid urls",
			sources: []string{"not a url", "ftp://", ""},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chartSourceHosts(tt.home, tt.sources)
			if !equalStringSlices(got, tt.want) {
				t.Errorf("chartSourceHosts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoURLMatchesAny(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		hosts   []string
		want    bool
	}{
		{name: "empty repo url", repoURL: "", hosts: []string{"bitnami.com"}, want: false},
		{name: "empty hosts", repoURL: "https://charts.bitnami.com", hosts: nil, want: false},
		{name: "exact host match", repoURL: "https://argoproj.github.io/argo-helm", hosts: []string{"argoproj.github.io"}, want: true},
		{name: "registered-domain match", repoURL: "https://charts.bitnami.com/bitnami", hosts: []string{"bitnami.com"}, want: true},
		{name: "no match", repoURL: "https://charts.bitnami.com", hosts: []string{"argoproj.github.io"}, want: false},
		{name: "github.io is multi-tenant: unrelated github.io repos do not match each other", repoURL: "https://kubernetes-sigs.github.io/external-dns", hosts: []string{"argoproj.github.io"}, want: false},
		{name: "oci registry host match", repoURL: "oci://registry-1.docker.io/bitnamicharts/argo-cd", hosts: []string{"docker.io"}, want: true},
		{name: "https with explicit port", repoURL: "https://charts.example.com:8443/charts", hosts: []string{"example.com"}, want: true},
		{name: "https with userinfo", repoURL: "https://user:pass@charts.bitnami.com/bitnami", hosts: []string{"bitnami.com"}, want: true},
		{name: "invalid url", repoURL: "://broken", hosts: []string{"bitnami.com"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repoURLMatchesAny(tt.repoURL, tt.hosts); got != tt.want {
				t.Errorf("repoURLMatchesAny(%q, %v) = %v, want %v", tt.repoURL, tt.hosts, got, tt.want)
			}
		})
	}
}

func TestMarkCurrentVersion_DoesNotMutateBaseOrLeakAcrossReleases(t *testing.T) {
	base := []repoVersionInfo{
		{repoName: "bitnami", latestVersion: "20.0.0"},
		{repoName: "argo", latestVersion: "8.5.0"},
	}
	versions := map[string][]string{
		"bitnami": {"19.0.0", "20.0.0"},
		"argo":    {"8.4.0", "8.5.0"},
	}

	a := markCurrentVersion(base, versions, "20.0.0")
	b := markCurrentVersion(base, versions, "8.5.0")

	if !a[0].hasCurrentVersion || a[1].hasCurrentVersion {
		t.Errorf("release A: bitnami should match, argo should not; got %+v", a)
	}
	if b[0].hasCurrentVersion || !b[1].hasCurrentVersion {
		t.Errorf("release B: argo should match, bitnami should not; got %+v", b)
	}
	if base[0].hasCurrentVersion || base[1].hasCurrentVersion {
		t.Errorf("base slice was mutated; per-release flags would leak across releases sharing a chart name: %+v", base)
	}
}

func TestToHelmRelease_StorageNamespace(t *testing.T) {
	rel := &release.Release{
		Name:      "podinfo",
		Namespace: "demo-flux-helm",
		Version:   1,
		Info: &release.Info{
			Status:       release.StatusDeployed,
			LastDeployed: helmtime.Unix(0, 0),
		},
		Chart: &chart.Chart{Metadata: &chart.Metadata{
			Name:       "podinfo",
			Version:    "6.11.2",
			AppVersion: "6.11.2",
		}},
	}

	same := toHelmRelease(rel, "demo-flux-helm")
	if same.StorageNamespace != "" {
		t.Fatalf("same storage namespace should be omitted, got %q", same.StorageNamespace)
	}

	different := toHelmRelease(rel, "flux-system")
	if different.Namespace != "demo-flux-helm" {
		t.Fatalf("target namespace changed: got %q", different.Namespace)
	}
	if different.StorageNamespace != "flux-system" {
		t.Fatalf("storage namespace = %q, want flux-system", different.StorageNamespace)
	}
}

func TestHelmReleaseStorageNamespacesWithClient(t *testing.T) {
	assertStorageNamespaceFromSecret(t, false)
}

func TestHelmReleaseStorageNamespacesWithClient_GzippedPayload(t *testing.T) {
	assertStorageNamespaceFromSecret(t, true)
}

func TestHelmReleaseRowsFromStorageSnapshot_AttachesLastOperation(t *testing.T) {
	revisions := []*release.Release{
		helmTestRelease("atomic", "demo", 1, release.StatusSuperseded, "Install complete"),
		helmTestRelease("atomic", "demo", 2, release.StatusFailed, `Upgrade "atomic" failed: timed out waiting for the condition`),
		helmTestRelease("atomic", "demo", 3, release.StatusDeployed, "Rollback to 1"),
	}
	secrets := make([]*corev1.Secret, 0, len(revisions))
	for _, rel := range revisions {
		secrets = append(secrets, helmReleaseSecret(t, "flux-system", rel, false))
	}

	client := fake.NewSimpleClientset(secretsToObjects(secrets)...)
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		t.Fatal(err)
	}
	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil)

	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Revision != 3 {
		t.Fatalf("revision = %d, want 3", row.Revision)
	}
	if row.StorageNamespace != "flux-system" {
		t.Fatalf("storage namespace = %q, want flux-system", row.StorageNamespace)
	}
	if row.LastOperation == nil {
		t.Fatal("LastOperation = nil")
	}
	if row.LastOperation.Kind != helmhistory.KindUpgradeRolledBack {
		t.Fatalf("kind = %q, want %q", row.LastOperation.Kind, helmhistory.KindUpgradeRolledBack)
	}
	if row.LastOperation.FailedRevision != 2 || row.LastOperation.RollbackRevision != 3 || row.LastOperation.TargetRevision != 1 {
		t.Fatalf("operation revisions = failed:%d rollback:%d target:%d", row.LastOperation.FailedRevision, row.LastOperation.RollbackRevision, row.LastOperation.TargetRevision)
	}
	if row.LastOperation.FailureDescription == "" {
		t.Fatal("FailureDescription is empty")
	}
	if len(row.Operations) != 1 {
		t.Fatalf("len(Operations) = %d, want 1", len(row.Operations))
	}
	if row.Operations[0].Kind != helmhistory.KindUpgradeRolledBack {
		t.Fatalf("operations[0].kind = %q, want %q", row.Operations[0].Kind, helmhistory.KindUpgradeRolledBack)
	}
}

func TestHelmReleaseRowsFromStorageSnapshot_KeepsHealthyRowsCompact(t *testing.T) {
	rel := helmTestRelease("healthy", "demo", 1, release.StatusDeployed, "Install complete")
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "demo", rel, false))
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		t.Fatal(err)
	}

	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil)

	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].LastOperation != nil {
		t.Fatalf("LastOperation = %#v, want nil", rows[0].LastOperation)
	}
	if len(rows[0].Operations) != 0 {
		t.Fatalf("Operations = %#v, want none", rows[0].Operations)
	}
}

func TestHelmReleaseRowsFromStorageSnapshot_SkipsMalformedReleaseSecret(t *testing.T) {
	malformed := &release.Release{
		Name:      "malformed",
		Namespace: "demo",
		Version:   1,
		Info: &release.Info{
			Status:       release.StatusDeployed,
			LastDeployed: helmtime.Unix(1, 0),
		},
	}
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "demo", malformed, false))
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		t.Fatal(err)
	}

	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil)

	if len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0 for malformed release secret", len(rows))
	}
}

func TestHelmReleaseRowsFromStorageSnapshot_CapsOperations(t *testing.T) {
	revisions := []*release.Release{
		helmTestRelease("repeat", "demo", 1, release.StatusSuperseded, "Install complete"),
		helmTestRelease("repeat", "demo", 2, release.StatusFailed, `Upgrade "repeat" failed: first`),
		helmTestRelease("repeat", "demo", 3, release.StatusSuperseded, "Rollback to 1"),
		helmTestRelease("repeat", "demo", 4, release.StatusFailed, `Upgrade "repeat" failed: second`),
		helmTestRelease("repeat", "demo", 5, release.StatusSuperseded, "Rollback to 3"),
		helmTestRelease("repeat", "demo", 6, release.StatusFailed, `Upgrade "repeat" failed: third`),
		helmTestRelease("repeat", "demo", 7, release.StatusSuperseded, "Rollback to 5"),
		helmTestRelease("repeat", "demo", 8, release.StatusFailed, `Upgrade "repeat" failed: fourth`),
		helmTestRelease("repeat", "demo", 9, release.StatusDeployed, "Rollback to 7"),
	}
	secrets := make([]*corev1.Secret, 0, len(revisions))
	for _, rel := range revisions {
		secrets = append(secrets, helmReleaseSecret(t, "demo", rel, false))
	}
	client := fake.NewSimpleClientset(secretsToObjects(secrets)...)
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		t.Fatal(err)
	}

	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil)

	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if len(rows[0].Operations) != 3 {
		t.Fatalf("len(Operations) = %d, want 3: %#v", len(rows[0].Operations), rows[0].Operations)
	}
	wantRollbackRevisions := []int{9, 7, 5}
	for i, want := range wantRollbackRevisions {
		if rows[0].Operations[i].RollbackRevision != want {
			t.Fatalf("Operations[%d].RollbackRevision = %d, want %d", i, rows[0].Operations[i].RollbackRevision, want)
		}
	}
}

func TestHelmReleaseRowsFromStorageSnapshot_UsesDetailHistoryWindow(t *testing.T) {
	revisions := []*release.Release{
		helmTestRelease("long-lived", "demo", 1, release.StatusSuperseded, "Install complete"),
		helmTestRelease("long-lived", "demo", 2, release.StatusFailed, `Upgrade "long-lived" failed: early failure`),
		helmTestRelease("long-lived", "demo", 3, release.StatusSuperseded, "Rollback to 1"),
	}
	for rev := 4; rev <= releaseHistoryMax+44; rev++ {
		status := release.StatusSuperseded
		if rev == releaseHistoryMax+44 {
			status = release.StatusDeployed
		}
		revisions = append(revisions, helmTestRelease("long-lived", "demo", rev, status, "Upgrade complete"))
	}
	secrets := make([]*corev1.Secret, 0, len(revisions))
	for _, rel := range revisions {
		secrets = append(secrets, helmReleaseSecret(t, "demo", rel, false))
	}
	client := fake.NewSimpleClientset(secretsToObjects(secrets)...)
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		t.Fatal(err)
	}

	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil)

	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Revision != releaseHistoryMax+44 {
		t.Fatalf("revision = %d, want %d", rows[0].Revision, releaseHistoryMax+44)
	}
	if rows[0].LastOperation != nil {
		t.Fatalf("LastOperation = %#v, want nil for operations outside detail history window", rows[0].LastOperation)
	}
	if len(rows[0].Operations) != 0 {
		t.Fatalf("Operations = %#v, want none for operations outside detail history window", rows[0].Operations)
	}
	if got := len(snapshot.histories["demo/long-lived"]); got != releaseHistoryMax {
		t.Fatalf("history window = %d, want %d", got, releaseHistoryMax)
	}
}

func assertStorageNamespaceFromSecret(t *testing.T, gzipped bool) {
	t.Helper()
	rel := helmTestRelease("podinfo", "demo-flux-helm", 1, release.StatusDeployed, "Install complete")
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "flux-system", rel, gzipped))

	storageNamespaces, err := helmReleaseStorageNamespacesWithClient(client)
	if err != nil {
		t.Fatal(err)
	}
	if got := storageNamespaces[releaseStorageKey(rel)]; got != "flux-system" {
		t.Fatalf("storage namespace = %q, want flux-system", got)
	}
}

func helmTestRelease(name, namespace string, version int, status release.Status, description string) *release.Release {
	return &release.Release{
		Name:      name,
		Namespace: namespace,
		Version:   version,
		Info: &release.Info{
			Status:       status,
			Description:  description,
			LastDeployed: helmtime.Unix(int64(version), 0),
		},
		Chart: &chart.Chart{Metadata: &chart.Metadata{
			Name:       name,
			Version:    "1.0.0",
			AppVersion: "1.0.0",
		}},
	}
}

func helmReleaseSecret(t *testing.T, storageNamespace string, rel *release.Release, gzipped bool) *corev1.Secret {
	t.Helper()
	payload, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	if gzipped {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		payload = b.Bytes()
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", rel.Name, rel.Version),
			Namespace: storageNamespace,
			Labels:    map[string]string{"owner": "helm"},
		},
		Data: map[string][]byte{
			"release": []byte(base64.StdEncoding.EncodeToString(payload)),
		},
	}
}

func secretsToObjects(secrets []*corev1.Secret) []runtime.Object {
	objects := make([]runtime.Object, 0, len(secrets))
	for _, secret := range secrets {
		objects = append(objects, secret)
	}
	return objects
}

func TestResolveUpgradeChartPath_UsesRepositoryHint(t *testing.T) {
	client := testHelmClientWithRepos(t)

	chartPath, repoName, err := client.resolveUpgradeChartPath("argo-cd", "9.5.11", "argo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if repoName != "argo" {
		t.Fatalf("repo = %q, want argo", repoName)
	}
	if !strings.Contains(chartPath, "argoproj.github.io") {
		t.Fatalf("chart path = %q, want argo repo URL", chartPath)
	}
}

func TestResolveUpgradeChartPath_AmbiguousWithoutHintOrAffinity(t *testing.T) {
	client := testHelmClientWithRepos(t)

	_, _, err := client.resolveUpgradeChartPath("argo-cd", "9.5.11", "", nil)
	if err == nil {
		t.Fatal("expected ambiguous chart error")
	}
	if !strings.Contains(err.Error(), "could not identify upstream chart repository") {
		t.Fatalf("error = %q", err)
	}
}

func TestResolveUpgradeChartPath_UsesSourceAffinity(t *testing.T) {
	client := testHelmClientWithRepos(t)

	chartPath, repoName, err := client.resolveUpgradeChartPath("argo-cd", "9.5.11", "", []string{"argoproj.github.io"})
	if err != nil {
		t.Fatal(err)
	}
	if repoName != "argo" {
		t.Fatalf("repo = %q, want argo", repoName)
	}
	if !strings.Contains(chartPath, "argoproj.github.io") {
		t.Fatalf("chart path = %q, want argo repo URL", chartPath)
	}
}

func TestResolveUpgradeChartPath_RepositoryHintDoesNotFallback(t *testing.T) {
	client := testHelmClientWithRepoVersions(t, map[string][]string{
		"bitnami": {"9.5.11"},
		"argo":    {"9.5.10"},
	})

	_, _, err := client.resolveUpgradeChartPath("argo-cd", "9.5.11", "argo", nil)
	if err == nil {
		t.Fatal("expected target version missing from hinted repo")
	}
	if !strings.Contains(err.Error(), "chart argo-cd version 9.5.11 not found in repository argo") {
		t.Fatalf("error = %q", err)
	}
}

func testHelmClientWithRepos(t *testing.T) *Client {
	return testHelmClientWithRepoVersions(t, map[string][]string{
		"bitnami": {"9.5.11"},
		"argo":    {"9.5.11"},
	})
}

func testHelmClientWithRepoVersions(t *testing.T, versionsByRepo map[string][]string) *Client {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoFile := filepath.Join(dir, "repositories.yaml")
	if err := os.WriteFile(repoFile, []byte(`apiVersion: v1
generated: "2026-05-05T00:00:00Z"
repositories:
- name: bitnami
  url: https://charts.bitnami.com/bitnami
- name: argo
  url: https://argoproj.github.io/argo-helm
`), 0o644); err != nil {
		t.Fatal(err)
	}

	writeIndex := func(name string, versions []string) {
		t.Helper()
		var b strings.Builder
		b.WriteString(`apiVersion: v1
entries:
  argo-cd:
`)
		for _, version := range versions {
			b.WriteString(fmt.Sprintf(`  - name: argo-cd
    version: %s
    urls:
    - argo-cd-%s.tgz
`, version, version))
		}
		b.WriteString(`generated: "2026-05-05T00:00:00Z"
`)
		if err := os.WriteFile(filepath.Join(cacheDir, name+"-index.yaml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, versions := range versionsByRepo {
		writeIndex(name, versions)
	}

	return &Client{settings: &cli.EnvSettings{
		RepositoryConfig: repoFile,
		RepositoryCache:  cacheDir,
	}}
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

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
