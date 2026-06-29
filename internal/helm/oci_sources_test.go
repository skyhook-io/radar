package helm

import (
	"fmt"
	"slices"
	"testing"
)

func TestNormalizeOCIPrefix(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "oci://ghcr.io/myorg/charts", want: "oci://ghcr.io/myorg/charts"},
		{name: "trailing slash trimmed", in: "oci://ghcr.io/myorg/charts/", want: "oci://ghcr.io/myorg/charts"},
		{name: "whitespace trimmed", in: "  oci://ghcr.io/myorg/charts  ", want: "oci://ghcr.io/myorg/charts"},
		{name: "host only", in: "oci://ghcr.io", want: "oci://ghcr.io"},
		{name: "missing scheme", in: "ghcr.io/myorg/charts", wantErr: true},
		{name: "http scheme rejected", in: "https://charts.example.com", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "scheme only", in: "oci://", wantErr: true},
		{name: "metadata IP blocked", in: "oci://169.254.169.254/charts", wantErr: true},
		{name: "link-local with port blocked", in: "oci://169.254.0.1:5000/charts", wantErr: true},
		{name: "loopback allowed (local dev registry)", in: "oci://localhost:5000/charts", want: "oci://localhost:5000/charts"},
		{name: "private IP allowed (local dev registry)", in: "oci://10.0.0.5:5000/charts", want: "oci://10.0.0.5:5000/charts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOCIPrefix(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeOCIPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOCIRefAndURL(t *testing.T) {
	prefix := "oci://ghcr.io/myorg/charts"
	if got, want := ociRef(prefix, "mychart"), "ghcr.io/myorg/charts/mychart"; got != want {
		t.Errorf("ociRef = %q, want %q (registry client wants no scheme)", got, want)
	}
	if got, want := ociChartURL(prefix, "mychart"), "oci://ghcr.io/myorg/charts/mychart"; got != want {
		t.Errorf("ociChartURL = %q, want %q (LocateChart wants the scheme)", got, want)
	}
}

// fakeTagLister returns canned tags per ref and records calls.
type fakeTagLister struct {
	tags  map[string][]string
	errs  map[string]error
	calls []string
}

func (f *fakeTagLister) Tags(ref string) ([]string, error) {
	f.calls = append(f.calls, ref)
	if err, ok := f.errs[ref]; ok {
		return nil, err
	}
	return f.tags[ref], nil
}

func TestDiscoverOCIUpgrade(t *testing.T) {
	// Two registered prefixes; the chart lives under the second one only.
	withOCISources(t, []string{"oci://ghcr.io/orgA/charts", "oci://ghcr.io/orgB/charts"})

	lister := &fakeTagLister{
		tags: map[string][]string{
			// Tags() returns semver-sorted newest-first.
			"ghcr.io/orgB/charts/mychart": {"1.4.0", "1.3.0", "1.2.0"},
		},
		errs: map[string]error{
			"ghcr.io/orgA/charts/mychart": fmt.Errorf("not found"),
		},
	}

	c := &Client{}
	match := c.discoverOCIUpgrade("mychart", lister, map[string][]string{})
	if match == nil {
		t.Fatal("expected a match from orgB, got nil")
	}
	if match.LatestVersion != "1.4.0" {
		t.Errorf("LatestVersion = %q, want 1.4.0", match.LatestVersion)
	}
	if match.ChartURL != "oci://ghcr.io/orgB/charts/mychart" {
		t.Errorf("ChartURL = %q, want oci://ghcr.io/orgB/charts/mychart", match.ChartURL)
	}
}

func TestDiscoverOCIUpgrade_PicksNewestAcrossPrefixes(t *testing.T) {
	withOCISources(t, []string{"oci://reg1/c", "oci://reg2/c"})
	lister := &fakeTagLister{
		tags: map[string][]string{
			"reg1/c/app": {"2.0.0"},
			"reg2/c/app": {"2.1.0"},
		},
	}
	c := &Client{}
	match := c.discoverOCIUpgrade("app", lister, map[string][]string{})
	if match == nil || match.LatestVersion != "2.1.0" {
		t.Fatalf("expected newest 2.1.0 across prefixes, got %+v", match)
	}
}

// Regression: discovery and upgrade resolution must agree on which prefix wins.
// reg1 (first in settings order) also publishes the target version, but reg2 has the
// higher newest tag, so the version picker shows reg2's list — the upgrade must
// resolve from reg2, not "first prefix that happens to contain the tag" (reg1).
func TestSelectBestOCIPrefix_ConsistentAcrossDiscoveryAndUpgrade(t *testing.T) {
	withOCISources(t, []string{"oci://reg1/c", "oci://reg2/c"})
	lister := &fakeTagLister{
		tags: map[string][]string{
			"reg1/c/app": {"1.0.0"},          // older, first in order, also has 1.0.0
			"reg2/c/app": {"2.0.0", "1.0.0"}, // newer newest tag → wins
		},
	}
	c := &Client{}
	prefix, tags := c.selectBestOCIPrefix("app", lister, map[string][]string{})
	if prefix != "oci://reg2/c" {
		t.Fatalf("best prefix = %q, want oci://reg2/c (highest newest tag, not first-in-order)", prefix)
	}
	if len(tags) != 2 || tags[0] != "2.0.0" {
		t.Fatalf("tags = %v, want reg2's [2.0.0 1.0.0]", tags)
	}
	// The version picker would offer 1.0.0 (in reg2's list); the upgrade must resolve
	// 1.0.0 from reg2 too — the same prefix — even though reg1 also has it.
	matchPrefix, matchTags := c.selectBestOCIPrefix("app", lister, map[string][]string{})
	if matchPrefix != "oci://reg2/c" || !slices.Contains(matchTags, "1.0.0") {
		t.Fatalf("upgrade resolution diverged: prefix=%q tags=%v", matchPrefix, matchTags)
	}
}

func TestDiscoverOCIUpgrade_NoSourcesOrNoMatch(t *testing.T) {
	withOCISources(t, nil)
	c := &Client{}
	if m := c.discoverOCIUpgrade("app", &fakeTagLister{}, map[string][]string{}); m != nil {
		t.Errorf("expected nil with no registered sources, got %+v", m)
	}

	withOCISources(t, []string{"oci://reg/c"})
	lister := &fakeTagLister{tags: map[string][]string{}} // chart not published anywhere
	if m := c.discoverOCIUpgrade("app", lister, map[string][]string{}); m != nil {
		t.Errorf("expected nil when chart not found, got %+v", m)
	}
}

func TestDiscoverOCIUpgrade_TagCacheDedupes(t *testing.T) {
	withOCISources(t, []string{"oci://reg/c"})
	lister := &fakeTagLister{tags: map[string][]string{"reg/c/app": {"1.0.0"}}}
	cache := map[string][]string{}
	c := &Client{}
	c.discoverOCIUpgrade("app", lister, cache)
	c.discoverOCIUpgrade("app", lister, cache)
	if len(lister.calls) != 1 {
		t.Errorf("expected Tags called once (cached), got %d calls", len(lister.calls))
	}
}

// transientTagLister fails the first failCount calls for a ref, then succeeds —
// models a transient timeout/network error.
type transientTagLister struct {
	tags      []string
	failCount int
	calls     int
}

func (l *transientTagLister) Tags(string) ([]string, error) {
	l.calls++
	if l.calls <= l.failCount {
		return nil, fmt.Errorf("transient timeout")
	}
	return l.tags, nil
}

func TestDiscoverOCIUpgrade_DoesNotCacheFailures(t *testing.T) {
	withOCISources(t, []string{"oci://reg/c"})
	lister := &transientTagLister{tags: []string{"1.0.0"}, failCount: 1}
	cache := map[string][]string{}
	c := &Client{}

	// First probe fails transiently — must NOT be cached as "untracked".
	if m := c.discoverOCIUpgrade("app", lister, cache); m != nil {
		t.Fatalf("expected nil on transient failure, got %+v", m)
	}
	// Second probe retries (failure wasn't cached) and succeeds.
	m := c.discoverOCIUpgrade("app", lister, cache)
	if m == nil || m.LatestVersion != "1.0.0" {
		t.Fatalf("expected retry to succeed with 1.0.0, got %+v (calls=%d)", m, lister.calls)
	}
	if lister.calls != 2 {
		t.Errorf("expected 2 calls (retry after transient failure), got %d", lister.calls)
	}
}

func TestApplyOCIUpgrade_SetsFields(t *testing.T) {
	withOCISources(t, []string{"oci://reg/c"})
	lister := &fakeTagLister{tags: map[string][]string{"reg/c/app": {"1.5.0"}}}
	c := &Client{}

	info := &UpgradeInfo{CurrentVersion: "1.4.0"}
	if !c.applyOCIUpgrade(info, "app", "1.4.0", lister, map[string][]string{}) {
		t.Fatal("expected applyOCIUpgrade to return true")
	}
	if info.SourceType != "oci" || info.LatestVersion != "1.5.0" || !info.UpdateAvailable {
		t.Errorf("unexpected info: %+v", info)
	}
	if info.ChartRef != "oci://reg/c/app" {
		t.Errorf("ChartRef = %q, want oci://reg/c/app", info.ChartRef)
	}

	// Same version → no update available, but still tracked.
	info2 := &UpgradeInfo{CurrentVersion: "1.5.0"}
	c.applyOCIUpgrade(info2, "app", "1.5.0", lister, map[string][]string{})
	if info2.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when current == latest")
	}
}

func TestSortVersionsDesc(t *testing.T) {
	in := []string{"1.2.0", "1.10.0", "1.2.3", "0.9.0", "2.0.0"}
	got := sortVersionsDesc(in)
	want := []string{"2.0.0", "1.10.0", "1.2.3", "1.2.0", "0.9.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (semver order, not lexical)", got, want)
		}
	}
	// Input must not be mutated.
	if in[0] != "1.2.0" {
		t.Errorf("sortVersionsDesc mutated its input: %v", in)
	}
}

func TestCapVersions(t *testing.T) {
	short := []string{"3.0.0", "2.0.0", "1.0.0"}
	if got := capVersions(short); len(got) != 3 {
		t.Errorf("short list should pass through, got %d", len(got))
	}

	many := make([]string, maxAvailableVersions+25)
	for i := range many {
		many[i] = "v"
	}
	got := capVersions(many)
	if len(got) != maxAvailableVersions {
		t.Errorf("capped list = %d, want %d", len(got), maxAvailableVersions)
	}
}

// withOCISources points settings at a temp HOME and seeds the registered OCI
// sources for the duration of the test.
func withOCISources(t *testing.T, sources []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, s := range sources {
		if _, err := AddOCISource(s); err != nil {
			t.Fatalf("seeding source %q: %v", s, err)
		}
	}
}
