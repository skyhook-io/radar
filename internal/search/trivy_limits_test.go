package search

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/filter"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// limitsProvider holds one matching report plus a Pod, so a test can tell
// "findings skipped" apart from "search failed".
func limitsProvider() *fakeProvider {
	p := trivyProvider(vulnGVR, "VulnerabilityReport",
		vulnReport("shop", "replicaset-web-php", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")))
	p.typed = map[string][]runtime.Object{"pods": {newPod("shop", "cve-2026-109618-canary", "php:8.3", nil)}}
	return p
}

func TestSearch_FindingsTimeLimitIsReported(t *testing.T) {
	defer func(b time.Duration) { findingsBudget = b }(findingsBudget)
	// Already spent: a zero budget can read as unspent when two clock
	// readings land on the same tick.
	findingsBudget = -time.Second

	res, err := Search(context.Background(), limitsProvider(), Parse("CVE-2026-109618"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if res.IncompleteFindings != FindingsTimeLimit {
		t.Fatalf("incomplete = %q, want %q", res.IncompleteFindings, FindingsTimeLimit)
	}
	if got := hitNames(res); !slices.Equal(got, []string{"shop/cve-2026-109618-canary"}) {
		t.Fatalf("hits = %v, want only the Pod", got)
	}
}

func TestSearch_CancelledRequestStopsScanningReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Search(ctx, limitsProvider(), Parse("CVE-2026-109618"), Options{Include: IncludeNone})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSearch_MalformedReportsDoNotBreakSearch(t *testing.T) {
	odd := func(name string, report any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "aquasecurity.github.io/v1alpha1", "kind": "VulnerabilityReport",
			"metadata": map[string]any{"name": name, "namespace": "shop"},
			"report":   report,
		}}
	}
	p := trivyProvider(vulnGVR, "VulnerabilityReport",
		odd("a", "not a map"),
		odd("b", map[string]any{"vulnerabilities": "not a list", "components": []any{1, 2}}),
		odd("c", map[string]any{"vulnerabilities": []any{nil, 7, map[string]any{"vulnerabilityID": 42}}, "checks": []any{map[string]any{"checkID": "KSV001", "success": "no"}}}),
		vulnReport("shop", "good", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
	)
	for _, q := range []string{"CVE-2026-109618", "KSV001", "kind:VulnerabilityReport"} {
		res, err := Search(context.Background(), p, Parse(q), Options{Include: IncludeNone})
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if q == "CVE-2026-109618" && !slices.Equal(hitNames(res), []string{"shop/good"}) {
			t.Fatalf("hits = %v", hitNames(res))
		}
	}
}

func TestSearch_UnconfirmedAccessIsReportedNotDropped(t *testing.T) {
	reports := []*unstructured.Unstructured{
		vulnReport("team-a", "a", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
		vulnReport("team-b", "b", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
	}
	access := &kindAccess{namespaces: []string{"team-a"}, unchecked: []string{"team-b"}}
	res, err := Search(context.Background(), trivyProvider(vulnGVR, "VulnerabilityReport", reports...), Parse("CVE-2026-109618"),
		Options{Include: IncludeNone, Namespaces: []string{"team-a", "team-b"}, KindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hitNames(res), []string{"team-a/a"}) || !res.AccessIncomplete {
		t.Fatalf("hits = %v, access incomplete = %v", hitNames(res), res.AccessIncomplete)
	}
}

func TestCheckNamespaces(t *testing.T) {
	cachedDecisions := map[string]bool{"cached-yes": true, "cached-no": false}
	var checks atomic.Int32
	cached := func(ns string) (bool, bool) { v, ok := cachedDecisions[ns]; return v, ok }
	check := func(ns string) (bool, bool) {
		checks.Add(1)
		switch ns {
		case "fails":
			return false, false
		case "denied":
			return false, true
		}
		return true, true
	}
	budget := new(atomic.Int32)
	budget.Store(3)
	allowed, unchecked := CheckNamespaces([]string{"cached-yes", "cached-no", "fresh", "denied", "fails", "over-budget"}, budget, cached, check)
	if !slices.Equal(allowed, []string{"cached-yes", "fresh"}) {
		t.Fatalf("allowed = %v", allowed)
	}
	if !slices.Equal(unchecked, []string{"fails", "over-budget"}) {
		t.Fatalf("unchecked = %v", unchecked)
	}
	if checks.Load() != 3 {
		t.Fatalf("fresh checks = %d, want the budget of 3", checks.Load())
	}
	// The budget is shared: a second call in the same search sends nothing.
	_, unchecked = CheckNamespaces([]string{"fresh2"}, budget, cached, check)
	if !slices.Equal(unchecked, []string{"fresh2"}) || checks.Load() != 3 {
		t.Fatalf("unchecked = %v, checks = %d", unchecked, checks.Load())
	}
}

// What a caller learns about unconfirmed access must not depend on whether
// the query matched a report they may not read.
func TestSearch_AccessIncompleteDoesNotDependOnTheQuery(t *testing.T) {
	reports := []*unstructured.Unstructured{
		vulnReport("team-a", "a", "library/php", "8.3", vuln("CVE-2026-000001", "php", "8.3.10", "8.3.36", "CRITICAL")),
		vulnReport("team-b", "b", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
	}
	for _, q := range []string{"CVE-2026-109618", "zzz-no-such-thing"} {
		access := &kindAccess{namespaces: []string{"team-a"}, unchecked: []string{"team-b"}}
		res, err := Search(context.Background(), trivyProvider(vulnGVR, "VulnerabilityReport", reports...), Parse(q),
			Options{Include: IncludeNone, Namespaces: []string{"team-a", "team-b"}, KindAccess: access.options()})
		if err != nil {
			t.Fatal(err)
		}
		if !res.AccessIncomplete || len(res.Hits) != 0 {
			t.Fatalf("q=%s: hits = %v, access incomplete = %v", q, hitNames(res), res.AccessIncomplete)
		}
		if want := []string{"cluster:vulnerabilityreports", "ns:vulnerabilityreports:[team-a team-b]"}; !slices.Equal(access.asked, want) {
			t.Fatalf("q=%s: asked = %v, want %v", q, access.asked, want)
		}
	}
}

// A search that waits on access checks keeps its findings budget for
// scanning, and doesn't hold the findings slot while it waits.
func TestSearch_UnconfirmedAccessToAnUnreadableNamespaceIsReported(t *testing.T) {
	p := trivyProvider(vulnGVR, "VulnerabilityReport")
	p.dynamicErrNamespaces = map[string]bool{"team-a": true, "team-b": true}
	access := &kindAccess{namespaces: []string{"team-a"}, unchecked: []string{"team-b"}}
	res, err := Search(context.Background(), p, Parse("kind:VulnerabilityReport"),
		Options{Include: IncludeNone, Namespaces: []string{"team-a", "team-b"}, KindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AccessIncomplete || len(res.UnsearchedKinds) != 1 || !slices.Equal(res.UnsearchedKinds[0].Namespaces, []string{"team-a"}) {
		t.Fatalf("access incomplete = %v, unsearched = %+v", res.AccessIncomplete, res.UnsearchedKinds)
	}
}

// A filter that can't see a report's findings must not match it: an empty
// stand-in would satisfy predicates like !has(report.vulnerabilities).
func TestSearch_FilterSkipsReportsWhoseFindingsWereNotRead(t *testing.T) {
	defer func(b time.Duration) { findingsBudget = b }(findingsBudget)
	findingsBudget = -time.Second

	f, err := filter.CompileObjectFilter(`!has(report.vulnerabilities)`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), limitsProvider(), Parse("kind:VulnerabilityReport"), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || res.IncompleteFindings != FindingsTimeLimit {
		t.Fatalf("hits = %v, incomplete = %q", hitNames(res), res.IncompleteFindings)
	}
}

// Cancellation reaches into a large report, not only between reports.
func TestSearch_CancellationStopsInsideALargeReport(t *testing.T) {
	var vulns []map[string]any
	for i := range 50000 {
		vulns = append(vulns, vuln(fmt.Sprintf("CVE-2026-%06d", i), "pkg", "1", "2", "LOW"))
	}
	p := trivyProvider(vulnGVR, "VulnerabilityReport", vulnReport("shop", "huge", "library/php", "8.3", vulns...))
	ctx, cancel := context.WithCancel(context.Background())
	scan := newFindingsScan(ctx)
	if !scan.allow() {
		t.Fatal("budget not available")
	}
	c := candidate{report: asMap(p.dynamic[vulnGVR][0].Object["report"]), findings: scan}
	cancel()
	start := time.Now()
	if _, _, _, ok := scoreToken("zzz-no-match", c); ok {
		t.Fatal("matched")
	}
	if scan.reason != FindingsTimeLimit {
		t.Fatalf("reason = %q", scan.reason)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("walk took %v after cancellation", d)
	}
}

// A cancelled search stops waiting on a check another search started, and
// that check still completes for the search that is still waiting.
// An ns: filter narrows the namespaces worth an access check, so a narrowed
// search doesn't spend its checks on namespaces it will discard.
func TestSearch_NamespaceFilterNarrowsAccessChecks(t *testing.T) {
	var reports []*unstructured.Unstructured
	for i := range 100 {
		reports = append(reports, vulnReport(fmt.Sprintf("team-%d", i), "r", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")))
	}
	access := &kindAccess{namespaces: []string{"team-42"}}
	res, err := Search(context.Background(), trivyProvider(vulnGVR, "VulnerabilityReport", reports...), Parse("ns:team-42 CVE-2026-109618"),
		Options{Include: IncludeNone, KindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hitNames(res), []string{"team-42/r"}) {
		t.Fatalf("hits = %v", hitNames(res))
	}
	if want := []string{"cluster:vulnerabilityreports", "ns:vulnerabilityreports:[team-42]"}; !slices.Equal(access.asked, want) {
		t.Fatalf("asked = %v, want %v", access.asked, want)
	}
}

// partialProvider serves its objects but says coverage isn't complete yet.
// A namespace whose reports Radar can't read yet is reported on a plain
// query too, not only on kind: queries.
// Entries that aren't searchable (passed checks) still count toward the
// cancellation and budget checks.
func TestSearch_CancellationStopsInsideAReportOfPassedChecks(t *testing.T) {
	var checks []any
	for i := range 50000 {
		checks = append(checks, map[string]any{"checkID": fmt.Sprintf("KSV%05d", i), "success": true})
	}
	report := map[string]any{"checks": checks}
	ctx, cancel := context.WithCancel(context.Background())
	scan := newFindingsScan(ctx)
	if !scan.allow() {
		t.Fatal("budget not available")
	}
	cancel()
	walked := 0
	eachTrivyFinding(report, scan, func(*trivyFinding) bool { walked++; return true })
	if scan.reason != FindingsTimeLimit || walked != 0 {
		t.Fatalf("reason = %q, walked = %d", scan.reason, walked)
	}
}

// slowProvider blocks one kind's list until released.
// A search waiting on a slow list of some other kind doesn't hold the
// findings slot, so another search can still read findings.
