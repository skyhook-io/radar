package vulnerabilities

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func mustExport(t *testing.T, items []*unstructured.Unstructured, targets []target, view View, f Filter) Response {
	t.Helper()
	resp, err := build(context.Background(), items, targets, view, f, true)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// exportRows renders resp and reads it back as a CSV reader would, one map
// per row keyed by header.
func exportRows(t *testing.T, resp Response, f Filter) []map[string]string {
	t.Helper()
	body, err := WriteCSV(context.Background(), resp, f, "prod")
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, body)
	}
	if !slices.Equal(records[0], ExportColumns[resp.View]) {
		t.Fatalf("header %.200v", records[0])
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := map[string]string{}
		for i, col := range records[0] {
			row[col] = rec[i]
		}
		if row["cluster"] != "prod" {
			t.Fatalf("cluster cell: %v", row)
		}
		rows = append(rows, row)
	}
	return rows
}

func withStatus(rows []map[string]string, status string) []map[string]string {
	var out []map[string]string
	for _, r := range rows {
		if r["status"] == status {
			out = append(out, r)
		}
	}
	return out
}

// sharedImage is one image (digest sha256:app) run by n Deployments, with
// the given findings.
func sharedImage(n int, vulns []vuln) ([]*unstructured.Unstructured, []target) {
	items := []*unstructured.Unstructured{vulnReport(reportSpec{name: "app", ns: "shop", ownerKind: "ReplicaSet", ownerName: "w0-1", container: "app", repo: "acme/app", tag: "1", digest: "sha256:app", scanned: "2026-11-10T00:00:00Z", vulns: vulns})}
	var targets []target
	for i := range n {
		name := fmt.Sprintf("w%d", i)
		ref := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name}
		targets = append(targets, running(liveTarget("shop", "ReplicaSet", name+"-1", "app", "acme/app:1", "", ref), "sha256:app"))
	}
	return items, targets
}

// The export lists every value the list endpoint caps: CVE IDs per fix,
// workloads per image and per affected image, and rows.
func TestExportListsWhatTheListCaps(t *testing.T) {
	vulns := make([]vuln, MaxRows+1)
	for i := range vulns {
		vulns[i] = vuln{fmt.Sprintf("CVE-%04d", i), "HIGH", "zlib", "1", "2"}
	}
	workloads := MaxWorkloadsPerRow + 5
	items, targets := sharedImage(workloads, vulns)

	fix := mustBuild(t, items, targets, ViewFix, Filter{})
	if len(fix.Fixes[0].CVEs) != MaxCVEsPerFix || len(fix.Fixes[0].Affected[0].Workloads) != MaxWorkloadsPerAffected {
		t.Fatalf("the list still caps: %d CVEs, %d workloads", len(fix.Fixes[0].CVEs), len(fix.Fixes[0].Affected[0].Workloads))
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewFix, Filter{}), Filter{})
	if got := strings.Split(rows[0]["cves"], "; "); len(got) != MaxRows+1 {
		t.Fatalf("every CVE ID: %d", len(got))
	}
	if got := strings.Split(rows[0]["running_in"], "; "); len(got) != workloads || rows[0]["workloads"] != fmt.Sprint(workloads) {
		t.Fatalf("every workload: %d (%s)", len(got), rows[0]["workloads"])
	}

	img := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(img.Images[0].Workloads) != MaxWorkloadsPerRow {
		t.Fatalf("the list caps an image's workloads: %d", len(img.Images[0].Workloads))
	}
	rows = exportRows(t, mustExport(t, items, targets, ViewImage, Filter{}), Filter{})
	if got := strings.Split(rows[0]["running_in"], "; "); len(got) != workloads || got[0] != "Deployment shop/w0 (app)" {
		t.Fatalf("every container: %d %q", len(got), got[0])
	}

	cves := mustBuild(t, items, targets, ViewCVE, Filter{})
	if len(cves.CVEs) != MaxRows || cves.Coverage.RowsOmitted != 1 {
		t.Fatalf("the list caps rows: %d", len(cves.CVEs))
	}
	if rows := exportRows(t, mustExport(t, items, targets, ViewCVE, Filter{}), Filter{}); len(rows) != MaxRows+1 {
		t.Fatalf("every row: %d", len(rows))
	}
}

// A CVE in more images than the list names lands in the file with every
// image.
func TestExportListsEveryAffectedImage(t *testing.T) {
	var items []*unstructured.Unstructured
	var targets []target
	n := MaxAffectedPerCVE + 3
	for i := range n {
		name := fmt.Sprintf("svc%d", i)
		ref := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name}
		items = append(items, vulnReport(reportSpec{name: name, ns: "shop", ownerKind: "ReplicaSet", ownerName: name + "-1", container: "app", repo: "acme/" + name, tag: "1", digest: "sha256:" + name, scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-SHARED", "CRITICAL", "openssl", "3.0", "3.1"}}}))
		targets = append(targets, liveTarget("shop", "ReplicaSet", name+"-1", "app", "acme/"+name+":1", "", ref))
	}
	if list := mustBuild(t, items, targets, ViewCVE, Filter{}); len(list.CVEs[0].Affected) != MaxAffectedPerCVE {
		t.Fatalf("the list caps images: %d", len(list.CVEs[0].Affected))
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewCVE, Filter{}), Filter{})
	if got := strings.Split(rows[0]["image_names"], "; "); len(got) != n || rows[0]["images"] != fmt.Sprint(n) || rows[0]["fix"] != "Yes" {
		t.Fatalf("every image: %d %v", len(got), rows[0])
	}
	if rows[0]["installed"] != "openssl 3.0" || rows[0]["fixed_versions"] != "openssl 3.1" || rows[0]["severity"] != "Critical" || rows[0]["score"] != "7.5" {
		t.Fatalf("cve cells: %v", rows[0])
	}
}

// Running images without a scan are rows in every view, following a search
// but not a severity filter; a workload whose containers weren't scanned
// says so and leaves its counts blank.
func TestExportNotScannedRows(t *testing.T) {
	items, targets := filterFixture()
	pending := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "pending"}
	targets = append(targets,
		liveTarget("shop", "ReplicaSet", "pending-1", "app", "acme/pending:1", "", pending),
		liveTarget("shop", "ReplicaSet", "worker-1", "sidecar", "acme/sidecar:1", "", worker),
	)
	for _, view := range []View{ViewImage, ViewFix, ViewCVE} {
		f := Filter{Severities: []Severity{SeverityCritical}}
		rows := withStatus(exportRows(t, mustExport(t, items, targets, view, f), f), StatusNotScanned)
		if len(rows) != 2 {
			t.Fatalf("%s: not-scanned rows ignore the severity filter: %v", view, rows)
		}
		where := "image"
		if view != ViewImage {
			where = "image_names"
		}
		if rows[0][where] != "acme/pending:1" || rows[0]["scanned"] != "" || !strings.HasPrefix(rows[0]["running_in"], "Deployment shop/pending") {
			t.Fatalf("%s: %v", view, rows[0])
		}
		f = Filter{Query: "sidecar"}
		if rows := withStatus(exportRows(t, mustExport(t, items, targets, view, f), f), StatusNotScanned); len(rows) != 1 || rows[0][where] != "acme/sidecar:1" {
			t.Fatalf("%s: a search narrows them: %v", view, rows)
		}
	}

	rows := exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{})
	byName := map[string]map[string]string{}
	for _, r := range rows {
		byName[r["workload"]] = r
	}
	if p := byName["pending"]; p["status"] != StatusNotScanned || p["critical"] != "" || p["containers"] != "app acme/pending:1 [not scanned]" {
		t.Fatalf("unscanned workload: %v", p)
	}
	if w := byName["worker"]; w["status"] != StatusPartlyScanned || w["critical"] != "1" || w["containers"] != "app acme/worker:1; sidecar acme/sidecar:1 [not scanned]" {
		t.Fatalf("partly scanned workload: %v", w)
	}
	if c := byName["checkout"]; c["status"] != StatusOK || c["critical"] != "1" || c["with_a_fix"] != "2" {
		t.Fatalf("scanned workload: %v", c)
	}

	// Under a filter that rules out worker's scanned image, worker is still
	// a row: its unscanned sidecar might carry a match.
	f := Filter{Severities: []Severity{SeverityLow}}
	byName = map[string]map[string]string{}
	for _, r := range exportRows(t, mustExport(t, items, targets, ViewWorkload, f), f) {
		byName[r["workload"]] = r
	}
	if w := byName["worker"]; w["status"] != StatusPartlyScanned || w["containers"] != "app acme/worker:1; sidecar acme/sidecar:1 [not scanned]" || w["critical"] != "1" || w["scanned"] == "" {
		t.Fatalf("filtered-out workload's unscanned container: %v", w)
	}
	if p := byName["pending"]; p["status"] != StatusNotScanned || byName["checkout"]["status"] != StatusOK || len(byName) != 3 {
		t.Fatalf("filtered workload view: %v", byName)
	}

	// checkout runs the sidecar image too: a search or workload filter for
	// worker still leaves checkout out, as the table does.
	shared := append(slices.Clone(targets), liveTarget("shop", "ReplicaSet", "checkout-1", "sidecar", "acme/sidecar:1", "", checkout))
	for _, f := range []Filter{
		{Query: "worker", Severities: []Severity{SeverityLow}},
		{Workload: &WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "worker"}, Severities: []Severity{SeverityLow}},
	} {
		for _, r := range exportRows(t, mustExport(t, items, shared, ViewWorkload, f), f) {
			if r["workload"] != "worker" {
				t.Fatalf("%+v: %v", f, r)
			}
		}
	}

	// A search matches a workload by the images its own containers name, as
	// the table does.
	spelled := append(slices.Clone(targets), liveTarget("shop", "ReplicaSet", "pending-1", "proxy", "docker.io/acme/sidecar:1", "", pending))
	f = Filter{Query: "docker.io/acme", Severities: []Severity{SeverityLow}}
	rows = exportRows(t, mustExport(t, items, spelled, ViewWorkload, f), f)
	if len(rows) != 1 || rows[0]["workload"] != "pending" || rows[0]["status"] != StatusNotScanned {
		t.Fatalf("search by image spelling: %v", rows)
	}
}

// Images past the findings budget are "not read" rows in the views that
// can't show them otherwise, never silently missing.
func TestExportNotReadRows(t *testing.T) {
	vs := make([]vuln, MaxFindings+1)
	for i := range vs {
		vs[i] = vuln{"CVE-X", "LOW", "p", "1", "2"}
	}
	items, targets := filterFixture()
	items = append(items, vulnReport(reportSpec{name: "big", ns: "shop", ownerKind: "ReplicaSet", ownerName: "big-1", container: "app", repo: "acme/big", tag: "1", scanned: "2026-11-10T00:00:00Z", vulns: vs}))
	big := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "big"}
	targets = append(targets, liveTarget("shop", "ReplicaSet", "big-1", "app", "acme/big:1", "", big))
	for _, view := range []View{ViewFix, ViewCVE} {
		f := Filter{Severities: []Severity{SeverityCritical}}
		rows := withStatus(exportRows(t, mustExport(t, items, targets, view, f), f), StatusNotRead)
		if len(rows) != 1 || rows[0]["image_names"] != "acme/big:1" || rows[0]["running_in"] != "Deployment shop/big" || rows[0]["scanned"] != "2026-11-10T00:00:00Z" {
			t.Fatalf("%s: %v", view, rows)
		}
	}
	rows := withStatus(exportRows(t, mustExport(t, items, targets, ViewImage, Filter{}), Filter{}), StatusNotRead)
	if len(rows) != 1 || rows[0]["critical"] != "" || rows[0]["scanned"] == "" {
		t.Fatalf("image view: %v", rows)
	}
	rows = withStatus(exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{}), StatusNotRead)
	if len(rows) != 1 || rows[0]["workload"] != "big" || rows[0]["low"] != "" {
		t.Fatalf("workload view: %v", rows)
	}
	for _, f := range []Filter{{}, {Severities: []Severity{SeverityCritical}}} {
		rows = withStatus(exportRows(t, mustExport(t, items, targets, ViewFindings, f), f), StatusNotRead)
		if len(rows) != 1 || rows[0]["image"] != "acme/big:1" || rows[0]["workload"] != "big" || rows[0]["container"] != "app" || rows[0]["cve"] != "" || rows[0]["scanned"] == "" {
			t.Fatalf("findings view %+v: %v", f, rows)
		}
	}
}

// Clean images and workloads are rows unless a severity or fix filter is on,
// as the screen's clean fold.
func TestExportCleanRows(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-A", "HIGH", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "p", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/pause", tag: "3", digest: "sha256:p", scanned: "2026-11-09T00:00:00Z"}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/pause:3", "", worker),
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewImage, Filter{}), Filter{})
	if len(rows) != 2 || rows[1]["image"] != "acme/pause:3" || rows[1]["critical"] != "0" || rows[1]["digest"] != "sha256:p" || rows[1]["os"] == "" || rows[1]["scanned"] != "2026-11-09T00:00:00Z" {
		t.Fatalf("clean image row: %v", rows)
	}
	f := Filter{Severities: []Severity{SeverityHigh}}
	if rows := exportRows(t, mustExport(t, items, targets, ViewImage, f), f); len(rows) != 1 {
		t.Fatalf("a severity filter hides clean rows: %v", rows)
	}
	rows = exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{})
	if len(rows) != 2 || rows[1]["workload"] != "worker" || rows[1]["containers"] != "app acme/pause:3" || rows[1]["status"] != StatusOK || rows[1]["scanned"] != "2026-11-09T00:00:00Z" {
		t.Fatalf("clean workload row: %v", rows)
	}
}

// A row's scan time is its oldest input: an image merging two reports, and
// a CVE spanning two images.
func TestExportScannedIsTheOldest(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-1", "CRITICAL", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-1", "CRITICAL", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "d", ns: "shop", ownerKind: "StatefulSet", ownerName: "db", container: "pg", repo: "acme/db", tag: "1", digest: "sha256:db", scanned: "2026-11-05T00:00:00Z", vulns: []vuln{{"CVE-1", "CRITICAL", "zlib", "1", "2"}}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/base:1", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/base:1", "", worker),
		liveTarget("shop", "StatefulSet", "db", "pg", "acme/db:1", "", db),
	}
	for _, view := range []View{ViewImage, ViewCVE, ViewFix, ViewWorkload} {
		rows := exportRows(t, mustExport(t, items, targets, view, Filter{}), Filter{})
		if rows[0]["scanned"] != "2026-11-01T00:00:00Z" {
			t.Fatalf("%s: %v", view, rows[0])
		}
	}
}

// Cells holding commas, quotes or line breaks round-trip, and a cell that a
// spreadsheet would run as a formula is made plain text.
func TestExportCells(t *testing.T) {
	items, targets := filterFixture()
	for _, v := range items[0].Object["report"].(map[string]any)["vulnerabilities"].([]any) {
		m := v.(map[string]any)
		if m["vulnerabilityID"] == "CVE-A" {
			m["title"] = "zlib: overflow, \"inflate\"\rsecond line"
		}
		if m["vulnerabilityID"] == "CVE-B" {
			m["title"] = "=HYPERLINK(\"http://x\")"
		}
	}
	resp := mustExport(t, items, targets, ViewCVE, Filter{})
	body, err := WriteCSV(context.Background(), resp, Filter{}, "prod")
	if err != nil {
		t.Fatal(err)
	}
	// Header, one line per row, and the title's own line break (a bare CR
	// in the report) inside its quotes, written as CRLF too.
	if !bytes.HasSuffix(body, []byte("\r\n")) || bytes.Count(body, []byte("\r\n")) != len(resp.CVEs)+2 {
		t.Fatalf("CRLF line ends: %q", body)
	}
	titles := map[string]string{}
	for _, r := range exportRows(t, resp, Filter{}) {
		titles[r["cve"]] = r["title"]
	}
	if titles["CVE-A"] != "zlib: overflow, \"inflate\"\nsecond line" || titles["CVE-B"] != "'=HYPERLINK(\"http://x\")" {
		t.Fatalf("titles: %q", titles)
	}
	for in, want := range map[string]string{"=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@SUM(A1)": "'@SUM(A1)", "\tx": "'\tx", "\rx": "'\nx", "a=b": "a=b", "": "", "a\rb": "a\nb", "a\r\nb": "a\nb"} {
		if got := safeCell(in); got != want {
			t.Fatalf("safeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// Filters change which rows the file holds, as they change the table.
func TestExportFollowsFilters(t *testing.T) {
	items, targets := filterFixture()
	count := func(view View, f Filter) int {
		return len(withStatus(exportRows(t, mustExport(t, items, targets, view, f), f), StatusOK))
	}
	if all, crit := count(ViewCVE, Filter{}), count(ViewCVE, Filter{Severities: []Severity{SeverityCritical}}); all != 4 || crit != 1 {
		t.Fatalf("severity: %d -> %d", all, crit)
	}
	if fixable := count(ViewCVE, Filter{FixableOnly: true}); fixable != 2 {
		t.Fatalf("has a fix: %d", fixable)
	}
	if one := count(ViewImage, Filter{Query: "worker"}); one != 1 {
		t.Fatalf("search: %d", one)
	}
	if one := count(ViewWorkload, Filter{Workload: &WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "checkout"}}); one != 1 {
		t.Fatalf("workload: %d", one)
	}
}

// Each coverage gap is a row saying why, with its namespaces where the view
// says where things run.
func TestExportGapRows(t *testing.T) {
	items, targets := filterFixture()
	gaps := []resourceapi.Gap{
		{Code: readreason.UserForbidden, Kinds: reportKind, Namespaces: resourceapi.Names([]string{"a", "b"}, 64)},
		{Code: readreason.KindSyncPending, Kinds: podKind, Namespaces: resourceapi.AllNames()},
		{Code: readreason.ReadError, Kinds: reportKind, Namespaces: resourceapi.UnknownNames()},
	}
	want := []struct{ status, where string }{{StatusNoAccess, "a; b"}, {StatusLoading, "*"}, {StatusReadFailed, "unknown"}}
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload, ViewFindings} {
		resp := mustExport(t, items, targets, view, Filter{})
		resp.Coverage.Gaps = gaps
		rows := exportRows(t, resp, Filter{})
		rows = rows[len(rows)-3:]
		col := "running_in"
		if view == ViewWorkload || view == ViewFindings {
			col = "namespace"
		}
		for i, w := range want {
			if rows[i]["status"] != w.status || rows[i][col] != w.where {
				t.Fatalf("%s gap %d: %v", view, i, rows[i])
			}
		}
	}
}

// Past the list's image cap, a wider image takes the narrowest one's place,
// and a CVE found in two of its packages stays one entry for it.
func TestListKeepsTheWidestAffectedImages(t *testing.T) {
	var items []*unstructured.Unstructured
	var targets []target
	for i := range MaxAffectedPerCVE + 1 {
		name := fmt.Sprintf("svc%02d", i)
		vulns := []vuln{{"CVE-SHARED", "HIGH", "openssl", "3.0", "3.1"}}
		if i == MaxAffectedPerCVE {
			vulns = append(vulns, vuln{"CVE-SHARED", "HIGH", "libssl3", "3.0", "3.1"})
		}
		items = append(items, vulnReport(reportSpec{name: name, ns: "shop", ownerKind: "ReplicaSet", ownerName: name + "-1", container: "app", repo: "acme/" + name, tag: "1", digest: "sha256:" + name, scanned: "2026-11-10T00:00:00Z", vulns: vulns}))
		targets = append(targets, liveTarget("shop", "ReplicaSet", name+"-1", "app", "acme/"+name+":1", "", WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name}))
		if i == MaxAffectedPerCVE {
			// A second workload on the last image makes it the widest.
			extra := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name + "-b"}
			targets = append(targets, running(liveTarget("shop", "ReplicaSet", name+"-b-1", "app", "acme/"+name+":1", "", extra), "sha256:"+name))
		}
	}
	affected := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs[0].Affected
	if len(affected) != MaxAffectedPerCVE || affected[0].Image != fmt.Sprintf("acme/svc%02d:1", MaxAffectedPerCVE) || len(affected[0].Packages) != 2 {
		t.Fatalf("widest image: %+v", affected[0])
	}
	for _, a := range affected[1:] {
		if len(a.Packages) != 1 {
			t.Fatalf("a package landed on the wrong image: %+v", a)
		}
	}
}

// The file names package types as the screen does.
func TestEcosystemLabelsMatchTheScreen(t *testing.T) {
	const parts = "../../web/src/components/vulnerabilities/parts.tsx"
	raw, err := os.ReadFile(parts)
	if err != nil {
		t.Fatalf("read %s: %v", parts, err)
	}
	block := regexp.MustCompile(`(?s)const ECOSYSTEMS: Record<string, string> = \{(.*?)\n\}`).FindSubmatch(raw)
	if block == nil {
		t.Fatalf("no ECOSYSTEMS table in %s; did its format change?", parts)
	}
	screen := map[string]string{}
	for _, m := range regexp.MustCompile(`'?([a-z0-9-]+)'?: '([^']+)'`).FindAllSubmatch(block[1], -1) {
		screen[string(m[1])] = string(m[2])
	}
	if len(screen) == 0 || !maps.Equal(screen, ecosystems) {
		t.Fatalf("export labels differ from the screen's:\nscreen %v\nexport %v", screen, ecosystems)
	}
	if ecosystemLabel("GoBinary") != "Go module, compiled in" || ecosystemLabel("conda") != "conda" || ecosystemLabel("") != "" {
		t.Fatal("ecosystemLabel")
	}
}

// A file past the size cap is refused while it's built, never finished.
func TestExportStopsAtTheSizeCap(t *testing.T) {
	vulns := make([]vuln, 3000)
	for i := range vulns {
		vulns[i] = vuln{fmt.Sprintf("CVE-%04d", i), "HIGH", "zlib", "1", "2"}
	}
	items, targets := sharedImage(50, vulns)
	resp := mustExport(t, items, targets, ViewCVE, Filter{})
	full, err := WriteCSV(context.Background(), resp, Filter{}, "prod")
	if err != nil {
		t.Fatal(err)
	}
	saved := maxExportBytes
	maxExportBytes = len(full) / 4
	t.Cleanup(func() { maxExportBytes = saved })
	if body, err := WriteCSV(context.Background(), resp, Filter{}, "prod"); !errors.Is(err, ErrExportTooLarge) || body != nil {
		t.Fatalf("over the cap: %d bytes, %v", len(body), err)
	}
}

// A request that goes away stops the export.
func TestExportStopsWhenCancelled(t *testing.T) {
	items, targets := filterFixture()
	resp := mustExport(t, items, targets, ViewCVE, Filter{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if body, err := WriteCSV(ctx, resp, Filter{}, "prod"); !errors.Is(err, context.Canceled) || body != nil {
		t.Fatalf("cancelled: %d bytes, %v", len(body), err)
	}
}

// A field larger than the CSV writer's own buffer goes straight to the
// destination; the cap holds there too.
func TestExportCapHoldsForLargeFields(t *testing.T) {
	dst := &cappedBuffer{max: 16 << 10}
	w := csv.NewWriter(dst)
	_ = w.Write([]string{"a", strings.Repeat("x", 64<<10)})
	w.Flush()
	if !errors.Is(w.Error(), ErrExportTooLarge) || dst.buf.Len() > dst.max {
		t.Fatalf("wrote %d bytes against a %d byte cap: %v", dst.buf.Len(), dst.max, w.Error())
	}

	// Through WriteCSV: a title past the cap, and a list of workloads that
	// alone would pass it.
	items, targets := filterFixture()
	for _, v := range items[0].Object["report"].(map[string]any)["vulnerabilities"].([]any) {
		v.(map[string]any)["title"] = strings.Repeat("t", 64<<10)
	}
	saved := maxExportBytes
	maxExportBytes = 16 << 10
	t.Cleanup(func() { maxExportBytes = saved })
	if _, err := WriteCSV(context.Background(), mustExport(t, items, targets, ViewCVE, Filter{}), Filter{}, "prod"); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("large title: %v", err)
	}
	wide, wideTargets := sharedImage(2000, []vuln{{"CVE-1", "HIGH", "zlib", "1", "2"}})
	if _, err := WriteCSV(context.Background(), mustExport(t, wide, wideTargets, ViewImage, Filter{}), Filter{}, "prod"); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("wide workload list: %v", err)
	}
	// The exposed_workloads cell goes through the same budget check: the
	// other cells fit, the exposed list alone wouldn't.
	resp := mustExport(t, wide[:1], wideTargets[:1], ViewFix, Filter{})
	resp.Fixes[0].Exposed = nil
	for i := range 2000 {
		resp.Fixes[0].Exposed = append(resp.Fixes[0].Exposed, ExposedWorkload{
			WorkloadRef: WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("web-%04d", i)},
			Routes:      []Route{{Via: "LoadBalancer", Namespace: "shop", Name: fmt.Sprintf("web-%04d", i)}},
		})
	}
	if _, err := WriteCSV(context.Background(), resp, Filter{}, "prod"); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("wide exposed list: %v", err)
	}
}

// An export whose rows would keep too many workload references stops while
// it is built, well before it has built them all; a normal export is
// unaffected.
func TestExportStopsAtTheWorkloadBudget(t *testing.T) {
	var items []*unstructured.Unstructured
	var targets []target
	vulns := make([]vuln, 200)
	for i := range vulns {
		vulns[i] = vuln{fmt.Sprintf("CVE-%03d", i), "HIGH", "zlib", "1", "2"}
	}
	// 40 images, each run by its own 100 workloads plus 100 shared ones:
	// every CVE row lists 200 workloads per image.
	for i := range 40 {
		img := fmt.Sprintf("acme/app%02d", i)
		items = append(items, vulnReport(reportSpec{name: img, ns: "shop", ownerKind: "ReplicaSet", ownerName: img + "-0", container: "app", repo: img, tag: "1", digest: "sha256:" + img, scanned: "2026-11-10T00:00:00Z", vulns: vulns}))
		for k := range 200 {
			name := fmt.Sprintf("w%02d-%03d", i, k)
			if k >= 100 {
				name = fmt.Sprintf("shared-%03d", k)
			}
			ref := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name}
			t := liveTarget("shop", "ReplicaSet", fmt.Sprintf("%s-%02d", name, i), "app", img+":1", "", ref)
			if k == 0 {
				t = liveTarget("shop", "ReplicaSet", img+"-0", "app", img+":1", "", ref)
			}
			targets = append(targets, running(t, "sha256:"+img))
		}
	}
	alloc := func(view View) (uint64, error) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := build(context.Background(), items, targets, view, Filter{}, true)
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, err
	}
	full, err := alloc(ViewCVE)
	if err != nil {
		t.Fatalf("within the default budget: %v", err)
	}
	// What reading the reports costs before any row is built.
	base, err := alloc(ViewSummary)
	if err != nil {
		t.Fatal(err)
	}
	// The lowered budget below still fits a smaller export of the same shape.
	saved := maxExportRefs
	maxExportRefs = 5_000
	if _, err := build(context.Background(), items[:1], targets[:200], ViewImage, Filter{}, true); err != nil {
		t.Fatalf("a smaller export: %v", err)
	}
	t.Cleanup(func() { maxExportRefs = saved })
	for _, view := range []View{ViewCVE, ViewFix, ViewImage} {
		used, err := alloc(view)
		if !errors.Is(err, ErrExportTooLarge) {
			t.Fatalf("%s: %v", view, err)
		}
		if view == ViewCVE && used-base > (full-base)/4 {
			t.Fatalf("rows cost %d of a full build's %d", used-base, full-base)
		}
	}
}

// Past the findings budget, a CVE or package search can't rule an unread
// image out: its findings are unknown, so every view keeps its not-read row.
func TestExportSearchKeepsNotReadRows(t *testing.T) {
	vs := make([]vuln, MaxFindings+1)
	for i := range vs {
		vs[i] = vuln{"CVE-X", "LOW", "p", "1", "2"}
	}
	items, targets := filterFixture()
	items = append(items, vulnReport(reportSpec{name: "big", ns: "shop", ownerKind: "ReplicaSet", ownerName: "big-1", container: "app", repo: "acme/big", tag: "1", digest: "sha256:big", scanned: "2026-11-10T00:00:00Z", vulns: vs}))
	big := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "big"}
	targets = append(targets, liveTarget("shop", "ReplicaSet", "big-1", "app", "acme/big:1", "", big))
	for _, q := range []string{"CVE-A", "zlib"} {
		for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload} {
			f := Filter{Query: q}
			resp := mustExport(t, items, targets, view, f)
			if !resp.Coverage.Truncated {
				t.Fatal("the fixture should pass the findings budget")
			}
			rows := withStatus(exportRows(t, resp, f), StatusNotRead)
			if len(rows) != 1 {
				t.Fatalf("%s, search %q: not-read rows %v", view, q, rows)
			}
			r := rows[0]
			switch view {
			case ViewImage:
				if r["image"] != "acme/big:1" || r["digest"] != "sha256:big" || r["critical"] != "" || r["running_in"] != "Deployment shop/big (app)" {
					t.Fatalf("image: %v", r)
				}
			case ViewWorkload:
				if r["workload"] != "big" || r["containers"] != "app acme/big:1 [not read]" {
					t.Fatalf("workload: %v", r)
				}
			default:
				if r["image_names"] != "acme/big:1" || r["running_in"] != "Deployment shop/big" {
					t.Fatalf("%s: %v", view, r)
				}
			}
		}
	}
}

// The findings export is one row per finding, image and workload container,
// in the page's order, with each container's exposure; not-scanned images
// are a row per container, and the filters narrow the containers too.
func TestExportFindings(t *testing.T) {
	items, targets := exposureFixture()
	pending := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "pending"}
	targets = append(targets, liveTarget("shop", "ReplicaSet", "pending-1", "app", "acme/pending:1", "", pending))
	resp := mustExport(t, items, targets, ViewFindings, Filter{})
	if resp.View != ViewFindings {
		t.Fatalf("view %s", resp.View)
	}
	rows := exportRows(t, resp, Filter{})
	type key struct{ cve, workload, exposure, status string }
	var got []key
	for _, r := range rows {
		got = append(got, key{r["cve"], r["workload"], r["exposure"], r["status"]})
	}
	want := []key{
		{"CVE-CRIT", "checkout", "", StatusOK},
		{"CVE-HIGH", "frontend", "exposed", StatusOK},
		{"CVE-HIGH", "worker", "", StatusOK},
		{"", "pending", "", StatusNotScanned},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	// No package type: the versions can't be ordered, so no fix target.
	crit := rows[0]
	for col, v := range map[string]string{
		"severity": "Critical", "score": "7.5", "package": "libxml", "installed_version": "2.0", "fixed_version": "2.1", "fix_target": "",
		"fix_available": "yes", "image": "acme/checkout:1", "digest": "sha256:co", "namespace": "shop", "workload_kind": "Deployment",
		"container": "app", "routes": "", "scanned": "2026-11-01T00:00:00Z",
	} {
		if crit[col] != v {
			t.Fatalf("%s: %q, want %q (%v)", col, crit[col], v, crit)
		}
	}
	if rows[1]["routes"] != "LoadBalancer shop/frontend" {
		t.Fatalf("routes: %q", rows[1]["routes"])
	}
	if p := rows[3]; p["image"] != "acme/pending:1" || p["container"] != "app" || p["scanned"] != "" || p["severity"] != "" {
		t.Fatalf("not scanned: %v", p)
	}

	// Where routes couldn't be read, a container with none seen is unknown.
	resp.Exposure = &Exposure{Gaps: []resourceapi.Gap{{Code: readreason.KindSyncPending, Kinds: ingressKind, Namespaces: resourceapi.Names([]string{"shop"}, 64)}}}
	if rows := exportRows(t, resp, Filter{}); rows[0]["exposure"] != "unknown" || rows[1]["exposure"] != "exposed" {
		t.Fatalf("unknown exposure: %v / %v", rows[0], rows[1])
	}

	// A workload filter and the Exposed filter keep only those containers.
	for _, f := range []Filter{{Workload: &WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "frontend"}}, {ExposedOnly: true}} {
		rows := exportRows(t, mustExport(t, items, targets, ViewFindings, f), f)
		if len(rows) != 1 || rows[0]["workload"] != "frontend" || rows[0]["cve"] != "CVE-HIGH" {
			t.Fatalf("%+v: %v", f, rows)
		}
	}
	// A severity filter keeps its findings; not-scanned rows stay.
	f := Filter{Severities: []Severity{SeverityCritical}}
	rows = exportRows(t, mustExport(t, items, targets, ViewFindings, f), f)
	if len(rows) != 2 || rows[0]["cve"] != "CVE-CRIT" || rows[1]["status"] != StatusNotScanned {
		t.Fatalf("critical only: %v", rows)
	}
	if severityLabel(SeverityUnknown) != "Unrated" {
		t.Fatal("Trivy's UNKNOWN is Unrated in a CSV, as on the screen")
	}
}

// The findings view is an export view only.
func TestParseExportView(t *testing.T) {
	if v, err := ParseExportView("findings"); err != nil || v != ViewFindings {
		t.Fatalf("findings: %v %v", v, err)
	}
	if _, err := ParseView("findings"); err == nil {
		t.Fatal("the list endpoint has no findings view")
	}
	if _, err := ParseExportView("summary"); err == nil {
		t.Fatal("no summary export")
	}
	if v, err := ParseExportView(""); err != nil || v != ViewImage {
		t.Fatalf("default: %v %v", v, err)
	}
}
