package vulnerabilities

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/vulnintel"
)

const (
	kevFixture = `{"catalogVersion":"2026.11.20","dateReleased":"2026-11-20T17:48:39Z","vulnerabilities":[
		{"cveID":"CVE-2023-44487","dateAdded":"2023-10-10","dueDate":"2023-10-31","knownRansomwareCampaignUse":"Unknown","requiredAction":"Apply mitigations."},
		{"cveID":"CVE-2021-44228","dateAdded":"2021-12-10","dueDate":"2021-12-24","knownRansomwareCampaignUse":"Known","requiredAction":"Apply updates."}]}`
	epssFixture = "#model_version:v2026.06.15,score_date:2026-11-22T12:03:04Z\ncve,epss,percentile\nCVE-2023-44487,0.99999,0.99998\nCVE-2024-0001,0.00123,0.41\n"
)

func readyIntel(t *testing.T) *vulnintel.Snapshot {
	t.Helper()
	k, err := vulnintel.ParseKEV(strings.NewReader(kevFixture))
	if err != nil {
		t.Fatal(err)
	}
	e, err := vulnintel.ParseEPSS(strings.NewReader(epssFixture))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	return &vulnintel.Snapshot{
		KEV: k, EPSS: e,
		KEVStatus:  vulnintel.FeedStatus{State: vulnintel.StateReady, Count: k.Len(), LoadedAt: &now},
		EPSSStatus: vulnintel.FeedStatus{State: vulnintel.StateReady, Count: e.Len(), LoadedAt: &now},
	}
}

func unavailableIntel() *vulnintel.Snapshot {
	return &vulnintel.Snapshot{
		KEVStatus:  vulnintel.FeedStatus{State: vulnintel.StateUnavailable, Error: "couldn't reach the feed"},
		EPSSStatus: vulnintel.FeedStatus{State: vulnintel.StateUnavailable},
	}
}

func intelFixture() ([]*unstructured.Unstructured, []target) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-2023-44487", "HIGH", "golang.org/x/net", "0.7.0", "0.17.0"},
			{"CVE-2024-0001", "CRITICAL", "zlib", "1", "2"},
		}}),
		vulnReport(reportSpec{name: "w", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/worker", tag: "1", digest: "sha256:w", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-2021-44228", "CRITICAL", "log4j-core", "2.14.1", "2.17.1"},
			{"CVE-2023-44487", "HIGH", "golang.org/x/net", "0.7.0", ""},
			{"GHSA-aaaa-bbbb-cccc", "LOW", "lodash", "4.0.0", ""},
		}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	return items, targets
}

func buildIntel(t *testing.T, view View, f Filter, export bool, intel *vulnintel.Snapshot) Response {
	t.Helper()
	items, targets := intelFixture()
	resp, err := buildWithIntel(context.Background(), items, targets, view, f, export, nil, intel)
	if err != nil {
		t.Fatal(err)
	}
	resp.Intel = intelOf(intel)
	return resp
}

// Rows carry KEV and EPSS from the feeds, and count their KEV CVEs, each CVE
// once however many packages or images carry it.
func TestRowsCarryKEVAndEPSS(t *testing.T) {
	intel := readyIntel(t)
	byCVE := buildIntel(t, ViewCVE, Filter{}, false, intel)
	got := map[string]CVE{}
	for _, c := range byCVE.CVEs {
		got[c.ID] = c
	}
	if k := got["CVE-2021-44228"].KEV; k == nil || k.RansomwareUse != "known" || k.DueDate != "2021-12-24" {
		t.Fatalf("log4shell KEV: %+v", k)
	}
	if e := got["CVE-2023-44487"].EPSS; e == nil || e.Score != 0.99999 || e.Percentile != 0.99998 {
		t.Fatalf("rapid reset EPSS: %+v", e)
	}
	if got["CVE-2024-0001"].KEV != nil || got["CVE-2024-0001"].EPSS == nil {
		t.Fatalf("CVE-2024-0001: %+v", got["CVE-2024-0001"])
	}
	if got["GHSA-aaaa-bbbb-cccc"].EPSS != nil {
		t.Fatal("a GHSA ID has an EPSS score")
	}
	if byCVE.Summary.KEVCVEs != 2 {
		t.Fatalf("summary KEV CVEs %d, want 2", byCVE.Summary.KEVCVEs)
	}

	byImage := buildIntel(t, ViewImage, Filter{}, false, intel)
	for _, img := range byImage.Images {
		want := map[string]int{"acme/checkout:2": 1, "acme/worker:1": 2}[img.Image]
		if img.KEVCVEs != want {
			t.Fatalf("%s KEV CVEs %d, want %d", img.Image, img.KEVCVEs, want)
		}
		for _, f := range img.Findings {
			if (f.KEV != nil) != (f.ID == "CVE-2021-44228" || f.ID == "CVE-2023-44487") {
				t.Fatalf("%s finding %s KEV %+v", img.Image, f.ID, f.KEV)
			}
		}
	}
	// The parsed reports are shared across reads: enriching a row mustn't
	// reach them.
	if plain := buildIntel(t, ViewImage, Filter{}, false, nil); plain.Images[0].Findings[0].KEV != nil {
		t.Fatal("a read without intel shows another read's KEV")
	}

	byWorkload := buildIntel(t, ViewWorkload, Filter{}, false, intel)
	for _, w := range byWorkload.Workloads {
		if want := map[string]int{"checkout": 1, "worker": 2}[w.Name]; w.KEVCVEs != want {
			t.Fatalf("%s KEV CVEs %d, want %d", w.Name, w.KEVCVEs, want)
		}
	}
	byFix := buildIntel(t, ViewFix, Filter{}, false, intel)
	for _, fx := range byFix.Fixes {
		if want := map[string]int{"golang.org/x/net": 1, "log4j-core": 1, "zlib": 0}[fx.Package]; fx.KEVCVEs != want {
			t.Fatalf("%s KEV CVEs %d, want %d", fx.Package, fx.KEVCVEs, want)
		}
	}
}

// Known exploited keeps KEV findings in every view; without the catalog it
// refuses rather than answering "none".
func TestKnownExploitedFilter(t *testing.T) {
	intel := readyIntel(t)
	byCVE := buildIntel(t, ViewCVE, Filter{KEVOnly: true}, false, intel)
	if len(byCVE.CVEs) != 2 || byCVE.CVEs[0].ID != "CVE-2021-44228" || byCVE.CVEs[1].ID != "CVE-2023-44487" {
		t.Fatalf("KEV CVEs: %+v", byCVE.CVEs)
	}
	byImage := buildIntel(t, ViewImage, Filter{KEVOnly: true, Severities: []Severity{SeverityCritical}}, false, intel)
	if len(byImage.Images) != 1 || byImage.Images[0].Image != "acme/worker:1" {
		t.Fatalf("Critical and KEV: %+v", byImage.Images)
	}
	byWorkload := buildIntel(t, ViewWorkload, Filter{KEVOnly: true}, false, intel)
	if len(byWorkload.Workloads) != 2 {
		t.Fatalf("both workloads carry a KEV CVE: %+v", byWorkload.Workloads)
	}

	items, targets := intelFixture()
	for _, snap := range []*vulnintel.Snapshot{nil, unavailableIntel()} {
		if _, err := buildWithIntel(context.Background(), items, targets, ViewCVE, Filter{KEVOnly: true}, false, nil, snap); !errors.Is(err, ErrKEVUnavailable) {
			t.Fatalf("KEV filter without the catalog: %v", err)
		}
	}
}

// The CSV carries the KEV and EPSS cells; without a feed they read
// "unavailable", never "no" or blank.
func TestExportIntelCells(t *testing.T) {
	intel := readyIntel(t)
	rows := withStatus(exportRows(t, buildIntel(t, ViewFindings, Filter{}, true, intel), Filter{}), StatusOK)
	byCVE := map[string]map[string]string{}
	for _, r := range rows {
		byCVE[r["cve"]+" "+r["image"]] = r
	}
	log4j := byCVE["CVE-2021-44228 acme/worker:1"]
	if log4j["kev"] != "yes" || log4j["kev_date_added"] != "2021-12-10" || log4j["kev_due_date"] != "2021-12-24" || log4j["kev_ransomware"] != "known" {
		t.Fatalf("log4shell row: %v", log4j)
	}
	rapid := byCVE["CVE-2023-44487 acme/checkout:2"]
	if rapid["kev"] != "yes" || rapid["kev_ransomware"] != "unknown" || rapid["epss"] != "0.99999" || rapid["epss_percentile"] != "0.99998" {
		t.Fatalf("rapid reset row: %v", rapid)
	}
	ghsa := byCVE["GHSA-aaaa-bbbb-cccc acme/worker:1"]
	if ghsa["kev"] != "no" || ghsa["epss"] != "" || ghsa["kev_due_date"] != "" {
		t.Fatalf("GHSA row: %v", ghsa)
	}

	for _, r := range exportRows(t, buildIntel(t, ViewCVE, Filter{}, true, unavailableIntel()), Filter{}) {
		if r["kev"] != "unavailable" || r["epss"] != "unavailable" {
			t.Fatalf("unavailable feed cells: %v", r)
		}
	}
	for _, view := range []View{ViewImage, ViewWorkload, ViewFix} {
		ready := withStatus(exportRows(t, buildIntel(t, view, Filter{}, true, intel), Filter{}), StatusOK)
		if len(ready) == 0 || ready[0]["kev_cves"] == "" || ready[0]["kev_cves"] == "unavailable" {
			t.Fatalf("%s kev_cves: %v", view, ready)
		}
		for _, r := range withStatus(exportRows(t, buildIntel(t, view, Filter{}, true, unavailableIntel()), Filter{}), StatusOK) {
			if r["kev_cves"] != "unavailable" {
				t.Fatalf("%s kev_cves without the catalog: %v", view, r)
			}
		}
	}
	// No intel at all (a Radar with the feeds off reads as nil) is as
	// unknown as a failed load.
	for _, r := range exportRows(t, buildIntel(t, ViewCVE, Filter{}, true, nil), Filter{}) {
		if r["kev"] != "unavailable" {
			t.Fatalf("no intel: %v", r)
		}
	}
}

// Within a severity, rows with a KEV CVE sort first, ahead of exposure; a
// read without the catalog keeps the plain order.
func TestKEVSortsFirstWithinASeverity(t *testing.T) {
	intel := readyIntel(t)
	// checkout's image also runs in db, so its non-KEV Critical is in more
	// workloads and would lead without the catalog.
	items, targets := intelFixture()
	items = append(items, vulnReport(reportSpec{name: "d", ns: "shop", ownerKind: "StatefulSet", ownerName: "db", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
		{"CVE-2024-0001", "CRITICAL", "zlib", "1", "2"},
	}}))
	targets = append(targets, liveTarget("shop", "StatefulSet", "db", "app", "acme/checkout:2", "", db))
	critical := Filter{Severities: []Severity{SeverityCritical}}
	byCVE, err := buildWithIntel(context.Background(), items, targets, ViewCVE, critical, false, nil, intel)
	if err != nil {
		t.Fatal(err)
	}
	if len(byCVE.CVEs) != 2 || byCVE.CVEs[0].ID != "CVE-2021-44228" {
		t.Fatalf("KEV Critical first: %+v", byCVE.CVEs)
	}
	plain, err := buildWithIntel(context.Background(), items, targets, ViewCVE, critical, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CVEs[0].ID != "CVE-2024-0001" {
		t.Fatalf("without KEV the widest CVE leads: %+v", plain.CVEs)
	}
	// golang.org/x/net clears a KEV High; on its own severity it leads.
	byFix := buildIntel(t, ViewFix, Filter{Severities: []Severity{SeverityHigh}}, false, intel)
	if len(byFix.Fixes) == 0 || byFix.Fixes[0].Package != "golang.org/x/net" {
		t.Fatalf("fix order: %+v", byFix.Fixes)
	}
	byImage := buildIntel(t, ViewImage, Filter{Severities: []Severity{SeverityCritical}}, false, intel)
	if byImage.Images[0].Image != "acme/worker:1" {
		t.Fatalf("image with a KEV Critical first: %s", byImage.Images[0].Image)
	}
}
