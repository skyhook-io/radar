package vulnerabilities

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// A report is parsed once per resourceVersion; a rescan is parsed again; an
// object without identity isn't cached at all.
func TestParseCache(t *testing.T) {
	c := &parseCache{entries: map[types.UID]parsedEntry{}}
	u := vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-1", "HIGH", "zlib", "1", "2"}}})
	if got := c.get(u); len(got) != 1 || len(c.entries) != 0 {
		t.Fatalf("no UID: parsed, not cached: %v %d", got, len(c.entries))
	}
	u.SetUID("uid-1")
	u.SetResourceVersion("1")
	first := c.get(u)
	if second := c.get(u); &first[0] != &second[0] {
		t.Fatal("the same resourceVersion is served from the cache")
	}
	u.Object["report"].(map[string]any)["vulnerabilities"] = append(u.Object["report"].(map[string]any)["vulnerabilities"].([]any),
		map[string]any{"vulnerabilityID": "CVE-2", "severity": "LOW", "resource": "bash", "installedVersion": "5", "fixedVersion": ""})
	u.SetResourceVersion("2")
	if got := c.get(u); len(got) != 2 || c.total != 2 || len(c.entries) != 1 {
		t.Fatalf("a rescan replaces the entry: %d findings, total %d, %d entries", len(got), c.total, len(c.entries))
	}
}

// A report listing one finding many times holds one finding's worth of memory.
func TestParseCacheHoldsOnlyDistinctFindings(t *testing.T) {
	c := &parseCache{entries: map[types.UID]parsedEntry{}}
	vulns := make([]vuln, 500)
	for i := range vulns {
		vulns[i] = vuln{"CVE-1", "HIGH", "zlib", "1", "2"}
	}
	u := vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", scanned: "2026-11-10T00:00:00Z", vulns: vulns})
	u.SetUID("uid-1")
	u.SetResourceVersion("1")
	if got := c.get(u); len(got) != 1 || cap(got) != 1 {
		t.Fatalf("len %d cap %d", len(got), cap(got))
	}
}
