package vulnerabilities

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// reportFieldsFixture is an end-of-life image (old) whose report carries the
// scanner, OS and per-CVE dates and purls, and a current one (new) whose
// report has no eosl, no scanner and no dates, both with CVE-SHARED. A
// third workload runs an image no report covers.
func reportFieldsFixture() ([]*unstructured.Unstructured, []target) {
	old := vulnReport(reportSpec{name: "old", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/old", tag: "1", digest: "sha256:old", scanned: "2026-11-01T00:00:00Z",
		vulns: []vuln{{"CVE-SHARED", "HIGH", "openssl", "1.1", "1.2"}, {"CVE-OLD", "LOW", "zlib", "1.0", ""}}})
	body := old.Object["report"].(map[string]any)
	body["os"] = map[string]any{"family": "debian", "name": "10.4", "eosl": true}
	body["scanner"] = map[string]any{"name": "Trivy", "vendor": "Aqua Security", "version": "0.76.1"}
	vulns := body["vulnerabilities"].([]any)
	shared := vulns[0].(map[string]any)
	shared["publishedDate"] = "2020-01-10T00:00:00Z"
	shared["lastModifiedDate"] = "2026-06-17T03:09:09Z"
	shared["packagePURL"] = "pkg:deb/debian/openssl@1.1?arch=amd64&distro=debian-10.4"

	cur := vulnReport(reportSpec{name: "new", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/new", tag: "1", digest: "sha256:new", scanned: "2026-11-02T00:00:00Z",
		vulns: []vuln{{"CVE-SHARED", "HIGH", "openssl", "3.0", "3.1"}}})
	curShared := cur.Object["report"].(map[string]any)["vulnerabilities"].([]any)[0].(map[string]any)
	curShared["publishedDate"] = "2020-03-01T00:00:00Z"
	curShared["lastModifiedDate"] = "2026-08-01T00:00:00Z"

	pending := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "pending"}
	targets := []target{
		running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/old:1", "", checkout), "sha256:old"),
		running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/new:1", "", worker), "sha256:new"),
		liveTarget("shop", "ReplicaSet", "pending-1", "app", "acme/pending:1", "", pending),
	}
	return []*unstructured.Unstructured{old, cur}, targets
}

func imageByID(images []Image, id string) *Image {
	for i := range images {
		if images[i].ID == id {
			return &images[i]
		}
	}
	return nil
}

func TestReportFieldsAreRead(t *testing.T) {
	items, targets := reportFieldsFixture()
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	old, cur := imageByID(resp.Images, "sha256:old"), imageByID(resp.Images, "sha256:new")
	if old == nil || cur == nil {
		t.Fatalf("images: %+v", resp.Images)
	}
	if old.OS != "debian 10.4" || !old.OSEndOfLife || old.Scanner == nil || *old.Scanner != (Scanner{Name: "Trivy", Version: "0.76.1"}) {
		t.Fatalf("old image: os %q eol %v scanner %+v", old.OS, old.OSEndOfLife, old.Scanner)
	}
	if cur.OSEndOfLife || cur.Scanner != nil {
		t.Fatalf("Trivy omits eosl when false, and a report may name no scanner: %+v", cur)
	}
	var shared *Finding
	for i := range old.Findings {
		if old.Findings[i].ID == "CVE-SHARED" {
			shared = &old.Findings[i]
		}
	}
	if shared == nil || shared.PackageURL != "pkg:deb/debian/openssl@1.1?arch=amd64&distro=debian-10.4" ||
		!shared.publishedAt.Equal(time.Date(2020, 1, 10, 0, 0, 0, 0, time.UTC)) ||
		!shared.lastModified.Equal(time.Date(2026, 6, 17, 3, 9, 9, 0, time.UTC)) {
		t.Fatalf("finding: %+v", shared)
	}
	for _, f := range old.Findings {
		if f.ID == "CVE-OLD" && (!f.publishedAt.IsZero() || !f.lastModified.IsZero() || f.PackageURL != "") {
			t.Fatalf("a finding without dates or purl has none: %+v", f)
		}
	}
	if resp.Summary.ImagesOSEndOfLife != 1 {
		t.Fatalf("summary: %+v", resp.Summary)
	}

	// The CVE row is as old as its earliest publication, and as fresh as its
	// latest change.
	cves := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs
	i := slices.IndexFunc(cves, func(c CVE) bool { return c.ID == "CVE-SHARED" })
	if i < 0 || cves[i].PublishedAt == nil || !cves[i].PublishedAt.Equal(time.Date(2020, 1, 10, 0, 0, 0, 0, time.UTC)) ||
		!cves[i].lastModified.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("CVE row: %+v", cves)
	}
}

// The summary's count is always on the wire: its presence tells the screen
// the eol filter is read.
func TestEndOfLifeCountIsAlwaysSent(t *testing.T) {
	body, err := json.Marshal(Summary{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"imagesOsEndOfLife":0`) {
		t.Fatalf("summary: %s", body)
	}
}

func TestOSEndOfLifeFilter(t *testing.T) {
	items, targets := reportFieldsFixture()
	f := Filter{OSEndOfLifeOnly: true}

	images := mustBuild(t, items, targets, ViewImage, f)
	if len(images.Images) != 1 || images.Images[0].ID != "sha256:old" {
		t.Fatalf("images: %+v", images.Images)
	}
	// An unscanned image's OS is unknown, so it stays, as under a severity
	// filter.
	if len(images.Unscanned) != 1 || images.Unscanned[0].Image != "acme/pending:1" {
		t.Fatalf("unscanned: %+v", images.Unscanned)
	}
	if images.Summary.ImagesScanned != 2 {
		t.Fatalf("the summary is unfiltered: %+v", images.Summary)
	}

	cves := mustBuild(t, items, targets, ViewCVE, f).CVEs
	i := slices.IndexFunc(cves, func(c CVE) bool { return c.ID == "CVE-SHARED" })
	if len(cves) != 2 || i < 0 || cves[i].Images != 1 || cves[i].Affected[0].ImageID != "sha256:old" {
		t.Fatalf("cves: %+v", cves)
	}

	fixes := mustBuild(t, items, targets, ViewFix, f).Fixes
	if len(fixes) != 1 || fixes[0].Package != "openssl" || slices.Compare(fixes[0].From, []string{"1.1"}) != 0 {
		t.Fatalf("fixes: %+v", fixes)
	}

	workloads := mustBuild(t, items, targets, ViewWorkload, f).Workloads
	if len(workloads) != 1 || workloads[0].Name != "checkout" {
		t.Fatalf("workloads: %+v", workloads)
	}

	// A clean end-of-life image stays in the clean list; a clean current one
	// doesn't.
	clean := vulnReport(reportSpec{name: "clean", ns: "shop", ownerKind: "ReplicaSet", ownerName: "db-1", container: "pg", repo: "acme/clean", tag: "1", digest: "sha256:clean", scanned: "2026-11-02T00:00:00Z"})
	clean.Object["report"].(map[string]any)["os"] = map[string]any{"family": "debian", "name": "9", "eosl": true}
	withClean := append(slices.Clone(items), clean)
	cleanTargets := append(slices.Clone(targets), running(liveTarget("shop", "ReplicaSet", "db-1", "pg", "acme/clean:1", "", db), "sha256:clean"))
	got := mustBuild(t, withClean, cleanTargets, ViewImage, f).Clean
	if len(got) != 1 || got[0].ID != "sha256:clean" || !got[0].OSEndOfLife {
		t.Fatalf("clean: %+v", got)
	}
}

// Radar Cloud maps an agent's columns by name, so the new columns come
// after every existing one, which keep their names and order.
func TestExportColumnsOnlyGrow(t *testing.T) {
	before := map[View][]string{
		ViewImage: {"cluster", "scanned", "status", "image", "digest", "os", "critical", "high", "medium", "low", "unrated", "with_a_fix", "running_in", "exposed_workloads"},
		ViewCVE:   {"cluster", "scanned", "status", "cve", "title", "severity", "score", "packages", "installed", "fixed_versions", "fix", "images", "workloads", "image_names", "running_in", "link", "exposed_workloads"},
		ViewFindings: {"cluster", "scanned", "status", "cve", "severity", "score", "title", "package", "package_type", "installed_version", "fixed_version", "fix_target", "fix_available",
			"image", "digest", "namespace", "workload_kind", "workload", "container", "exposure", "routes", "link"},
	}
	for view, cols := range before {
		if got := ExportColumns[view]; len(got) < len(cols) || !slices.Equal(got[:len(cols)], cols) {
			t.Fatalf("%s: %v", view, got)
		}
	}
}

func TestExportReportFields(t *testing.T) {
	defer func(prev func() time.Time) { now = prev }(now)
	now = func() time.Time { return time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC) }
	items, targets := reportFieldsFixture()

	rows := exportRows(t, mustExport(t, items, targets, ViewImage, Filter{}), Filter{})
	byImage := map[string]map[string]string{}
	for _, r := range rows {
		byImage[r["image"]] = r
	}
	for image, want := range map[string]map[string]string{
		"acme/old:1":     {"os": "debian 10.4", "os_end_of_life": "yes", "scanner": "Trivy", "scanner_version": "0.76.1"},
		"acme/new:1":     {"os": "debian 11", "os_end_of_life": "no", "scanner": "", "scanner_version": ""},
		"acme/pending:1": {"os": "", "os_end_of_life": "", "scanner": "", "scanner_version": ""},
	} {
		for col, v := range want {
			if byImage[image][col] != v {
				t.Fatalf("%s %s: %q, want %q (%v)", image, col, byImage[image][col], v, byImage[image])
			}
		}
	}

	rows = exportRows(t, mustExport(t, items, targets, ViewCVE, Filter{}), Filter{})
	byCVE := map[string]map[string]string{}
	for _, r := range rows {
		byCVE[r["cve"]] = r
	}
	if r := byCVE["CVE-SHARED"]; r["published"] != "2020-01-10T00:00:00Z" || r["last_modified"] != "2026-08-01T00:00:00Z" || r["days_since_published"] != "2192" {
		t.Fatalf("CVE-SHARED: %v", r)
	}
	if r := byCVE["CVE-OLD"]; r["published"] != "" || r["last_modified"] != "" || r["days_since_published"] != "" {
		t.Fatalf("CVE-OLD has no dates: %v", r)
	}

	rows = exportRows(t, mustExport(t, items, targets, ViewFindings, Filter{}), Filter{})
	var shared, pending map[string]string
	for _, r := range rows {
		switch {
		case r["cve"] == "CVE-SHARED" && r["image"] == "acme/old:1":
			shared = r
		case r["status"] == StatusNotScanned:
			pending = r
		}
	}
	for col, v := range map[string]string{
		"published": "2020-01-10T00:00:00Z", "last_modified": "2026-06-17T03:09:09Z", "days_since_published": "2192",
		"package_url": "pkg:deb/debian/openssl@1.1?arch=amd64&distro=debian-10.4", "os": "debian 10.4", "os_end_of_life": "yes",
		"scanner": "Trivy", "scanner_version": "0.76.1",
	} {
		if shared[col] != v {
			t.Fatalf("%s: %q, want %q (%v)", col, shared[col], v, shared)
		}
	}
	if pending == nil || pending["os"] != "" || pending["os_end_of_life"] != "" || pending["scanner"] != "" {
		t.Fatalf("not scanned: %v", pending)
	}

	// The export follows the eol filter.
	f := Filter{OSEndOfLifeOnly: true}
	rows = withStatus(exportRows(t, mustExport(t, items, targets, ViewFindings, f), f), StatusOK)
	for _, r := range rows {
		if r["image"] != "acme/old:1" {
			t.Fatalf("eol only: %v", r)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("eol only: %v", rows)
	}

	// By workload keeps a workload whose images aren't scanned yet, as every
	// other view keeps their images; one on a current OS drops out.
	workloads := map[string]string{}
	for _, r := range exportRows(t, mustExport(t, items, targets, ViewWorkload, f), f) {
		workloads[r["workload"]] = r["status"]
	}
	if len(workloads) != 2 || workloads["checkout"] != StatusOK || workloads["pending"] != StatusNotScanned {
		t.Fatalf("workloads, eol only: %v", workloads)
	}
}
