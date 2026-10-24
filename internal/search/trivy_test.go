package search

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
)

var (
	vulnGVR   = schema.GroupVersionResource{Group: trivyGroup, Version: "v1alpha1", Resource: "vulnerabilityreports"}
	sbomGVR   = schema.GroupVersionResource{Group: trivyGroup, Version: "v1alpha1", Resource: "sbomreports"}
	auditGVR  = schema.GroupVersionResource{Group: trivyGroup, Version: "v1alpha1", Resource: "configauditreports"}
	secretGVR = schema.GroupVersionResource{Group: trivyGroup, Version: "v1alpha1", Resource: "exposedsecretreports"}
)

func trivyReport(kind, ns, name string, report map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]any{
				"trivy-operator.resource.kind": "ReplicaSet",
				"trivy-operator.resource.name": name,
			},
		},
		"report": report,
	}}
}

func vulnReport(ns, name, repo, tag string, vulns ...map[string]any) *unstructured.Unstructured {
	items := make([]any, len(vulns))
	for i, v := range vulns {
		items[i] = v
	}
	return trivyReport("VulnerabilityReport", ns, name, map[string]any{
		"updateTimestamp": "2026-10-21T08:00:00Z",
		"registry":        map[string]any{"server": "index.docker.io"},
		"artifact":        map[string]any{"repository": repo, "tag": tag},
		"summary":         map[string]any{"criticalCount": int64(1)},
		"vulnerabilities": items,
	})
}

func vuln(id, pkg, installed, fixed, severity string) map[string]any {
	v := map[string]any{"vulnerabilityID": id, "resource": pkg, "installedVersion": installed, "severity": severity, "title": "php: something " + severity}
	if fixed != "" {
		v["fixedVersion"] = fixed
	}
	return v
}

func trivyProvider(gvr schema.GroupVersionResource, kind string, items ...*unstructured.Unstructured) *fakeProvider {
	return &fakeProvider{
		dynamic:                  map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: items},
		kinds:                    map[schema.GroupVersionResource]string{gvr: kind},
		namespaced:               map[schema.GroupVersionResource]bool{gvr: true},
		filterDynamicByNamespace: true,
	}
}

func hitNames(res Result) []string {
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.Namespace+"/"+h.Name)
	}
	slices.Sort(out)
	return out
}

func TestTrivyReportContent_IndexesIdentifiersOnly(t *testing.T) {
	report := map[string]any{
		"vulnerabilities": []any{
			vuln("CVE-2026-109618", "php8.2", "8.2.20-1~deb12u1", "8.2.29-1~deb12u2", "CRITICAL"),
			vuln("CVE-2024-0001", "libc6", "2.36-9", "", "LOW"),
		},
		"components": map[string]any{"components": []any{
			map[string]any{"name": "php", "version": "8.3.10", "purl": "pkg:generic/php@8.3.10"},
		}},
		"checks": []any{
			map[string]any{"checkID": "KSV001", "title": "Process can elevate its own privileges", "severity": "MEDIUM", "success": false},
			map[string]any{"checkID": "KSV002", "title": "Default AppArmor profile", "severity": "LOW", "success": true},
		},
		"secrets": []any{
			map[string]any{"ruleID": "aws-access-key-id", "category": "AWS", "severity": "CRITICAL", "target": "/app/.env", "match": "AWS_ACCESS_KEY_ID=AKIAREALSECRETVALUE"},
		},
	}
	got := map[string]string{}
	for _, cf := range trivyReportContent(report) {
		got[cf.Path] = cf.Value
	}
	want := map[string]string{
		"report.vulnerabilities[0]":       "CVE-2026-109618 php8.2 8.2.20-1~deb12u1 → 8.2.29-1~deb12u2",
		"report.vulnerabilities[1]":       "CVE-2024-0001 libc6 2.36-9 → unfixed",
		"report.components.components[0]": "php 8.3.10",
		"report.checks[0]":                "KSV001",
		"report.secrets[0]":               "aws-access-key-id /app/.env",
	}
	if len(got) != len(want) {
		t.Fatalf("indexed %d fields, want %d: %v", len(got), len(want), got)
	}
	for path, v := range want {
		if got[path] != v {
			t.Errorf("%s = %q, want %q", path, got[path], v)
		}
	}
	for path, v := range got {
		for _, leak := range []string{"AKIAREALSECRETVALUE", "CRITICAL", "elevate"} {
			if strings.Contains(v, leak) {
				t.Errorf("%s indexes %q: %q", path, leak, v)
			}
		}
	}
}

func TestSearch_FindsTrivyReportByCVE(t *testing.T) {
	hit := vulnReport("shop", "replicaset-web-5d8f9-php", "library/php", "8.3.10-apache",
		vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL"))
	other := vulnReport("shop", "replicaset-api-7c6b4-node", "library/node", "20",
		vuln("CVE-2024-0002", "openssl", "3.0.1", "3.0.2", "HIGH"))
	p := trivyProvider(vulnGVR, "VulnerabilityReport", hit, other)

	res, err := Search(context.Background(), p, Parse("CVE-2026-109618"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitNames(res); !slices.Equal(got, []string{"shop/replicaset-web-5d8f9-php"}) {
		t.Fatalf("hits = %v", got)
	}
	snips := res.Hits[0].Snippets
	if len(snips) != 1 || snips[0].Snippet != "CVE-2026-109618 php 8.3.10 → 8.3.36" {
		t.Fatalf("snippet should carry package and both versions, got %+v", snips)
	}

	res, err = Search(context.Background(), p, Parse("image:php kind:VulnerabilityReport"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitNames(res); !slices.Equal(got, []string{"shop/replicaset-web-5d8f9-php"}) {
		t.Fatalf("image: modifier should match the scanned image, got %v", got)
	}
}

func TestSearch_SeverityWordsDoNotMatchReports(t *testing.T) {
	p := trivyProvider(vulnGVR, "VulnerabilityReport", vulnReport("shop", "r1", "library/php", "8.3",
		vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")))
	res, err := Search(context.Background(), p, Parse("critical"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("severity words should not be indexed, got %v", hitNames(res))
	}
}

func TestSearch_ReportFieldOutsidetrivyGroupIsNotIndexed(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "scans"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Scan",
		"metadata":   map[string]any{"name": "s", "namespace": "shop"},
		"report":     map[string]any{"vulnerabilities": []any{vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")}},
	}}
	p := trivyProvider(gvr, "Scan", u)
	res, err := Search(context.Background(), p, Parse("CVE-2026-109618"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("only Trivy's group is read under report, got %v", hitNames(res))
	}
}

func TestSearch_ReportCELBindingMatchesOneFinding(t *testing.T) {
	// The PHP report has CVE-X on openssl and an unrelated php finding, so
	// free tokens "CVE-X php" match it across entries; the CEL predicate
	// must not.
	mixed := vulnReport("shop", "mixed", "library/php", "8.3",
		vuln("CVE-2026-0001", "openssl", "3.0.1", "3.0.2", "HIGH"),
		vuln("CVE-2025-9999", "php", "8.3.10", "8.3.11", "LOW"))
	exact := vulnReport("shop", "exact", "library/php", "8.3",
		vuln("CVE-2026-0001", "php", "8.3.10", "8.3.36", "CRITICAL"))
	p := trivyProvider(vulnGVR, "VulnerabilityReport", mixed, exact)

	res, err := Search(context.Background(), p, Parse("CVE-2026-0001 php"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitNames(res); !slices.Equal(got, []string{"shop/exact", "shop/mixed"}) {
		t.Fatalf("free tokens: hits = %v", got)
	}

	f, err := filter.CompileObjectFilter(`report.vulnerabilities.exists(v, v.vulnerabilityID == "CVE-2026-0001" && v.resource.contains("php") && v.severity == "CRITICAL")`)
	if err != nil {
		t.Fatal(err)
	}
	res, err = Search(context.Background(), p, Parse("kind:VulnerabilityReport"), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitNames(res); !slices.Equal(got, []string{"shop/exact"}) {
		t.Fatalf("CEL: hits = %v (filter errors %d: %s)", got, res.FilterErrors, res.FilterErrorSample)
	}
}

func TestSearch_ReportCELBindingCannotReadSecretMatch(t *testing.T) {
	u := trivyReport("ExposedSecretReport", "shop", "secrets", map[string]any{
		"secrets": []any{map[string]any{"ruleID": "aws-access-key-id", "target": "/app/.env", "match": "AWS_ACCESS_KEY_ID=AKIAREALSECRETVALUE"}},
	})
	p := trivyProvider(secretGVR, "ExposedSecretReport", u)
	for _, expr := range []string{
		`report.secrets.exists(s, has(s.match))`,
		`report.secrets.exists(s, s.ruleID == "aws-access-key-id" && has(s.target)) && !has(report.secrets[0].match)`,
	} {
		f, err := filter.CompileObjectFilter(expr)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Search(context.Background(), p, Parse("kind:ExposedSecretReport"), Options{Include: IncludeNone, Filter: f})
		if err != nil {
			t.Fatal(err)
		}
		wantHit := strings.Contains(expr, "!has")
		if (len(res.Hits) == 1) != wantHit {
			t.Errorf("%s: hits = %v, want hit = %v", expr, hitNames(res), wantHit)
		}
	}
	res, err := Search(context.Background(), p, Parse("AKIAREALSECRETVALUE"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("secret match must not be searchable, got %v", hitNames(res))
	}
}

func TestSearch_ReportBindingIsEmptyForOtherObjects(t *testing.T) {
	f, err := filter.CompileObjectFilter(`size(report) == 0`)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"pods": {newPod("shop", "web", "php:8.3", nil)}}}
	res, err := Search(context.Background(), p, Parse("kind:Pod"), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %v, filter errors %d: %s", hitNames(res), res.FilterErrors, res.FilterErrorSample)
	}
}

// kindAccess grants list on Trivy kinds only in the given namespaces (or
// everywhere) and records what it was asked.
type kindAccess struct {
	clusterWide bool
	namespaces  []string
	unchecked   []string
	asked       []string
}

func (k *kindAccess) options() *KindAccess {
	return &KindAccess{
		ClusterWide: func(group, resource string) bool {
			k.asked = append(k.asked, "cluster:"+resource)
			return k.clusterWide
		},
		InNamespaces: func(group, resource string, namespaces []string) ([]string, []string) {
			k.asked = append(k.asked, fmt.Sprintf("ns:%s:%v", resource, namespaces))
			var out, unchecked []string
			for _, ns := range namespaces {
				switch {
				case slices.Contains(k.unchecked, ns):
					unchecked = append(unchecked, ns)
				case k.clusterWide || slices.Contains(k.namespaces, ns):
					out = append(out, ns)
				}
			}
			return out, unchecked
		},
	}
}

func TestSearch_TrivyKindsNeedTheCallersListPermission(t *testing.T) {
	reports := []*unstructured.Unstructured{
		vulnReport("team-a", "a", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
		vulnReport("team-b", "b", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL")),
	}
	q := Parse("CVE-2026-109618")

	cases := []struct {
		name       string
		namespaces []string // Options.Namespaces; nil = cluster-wide pod reader
		access     *kindAccess
		want       []string
	}{
		{"auth off", nil, nil, []string{"team-a/a", "team-b/b"}},
		{"namespace-scoped caller with report RBAC in one namespace", []string{"team-a", "team-b"}, &kindAccess{namespaces: []string{"team-a"}}, []string{"team-a/a"}},
		{"namespace-scoped caller without report RBAC", []string{"team-a", "team-b"}, &kindAccess{}, []string{}},
		{"cluster-wide pod reader with cluster-wide report RBAC", nil, &kindAccess{clusterWide: true}, []string{"team-a/a", "team-b/b"}},
		{"cluster-wide pod reader with report RBAC in one namespace", nil, &kindAccess{namespaces: []string{"team-b"}}, []string{"team-b/b"}},
		{"cluster-wide pod reader without report RBAC", nil, &kindAccess{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := trivyProvider(vulnGVR, "VulnerabilityReport", reports...)
			opts := Options{Include: IncludeNone, Namespaces: tc.namespaces}
			if tc.access != nil {
				opts.KindAccess = tc.access.options()
			}
			res, err := Search(context.Background(), p, q, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := hitNames(res); !slices.Equal(got, tc.want) {
				t.Fatalf("hits = %v, want %v (asked %v)", got, tc.want, tc.access)
			}
		})
	}
}

func TestSearch_TrivyCoverageGapsOnlyWhereTheCallerMayList(t *testing.T) {
	p := trivyProvider(vulnGVR, "VulnerabilityReport")
	p.filterDynamicByNamespace = true
	p.dynamicErrNamespaces = map[string]bool{"team-a": true, "team-b": true}
	access := &kindAccess{namespaces: []string{"team-b"}}
	res, err := Search(context.Background(), p, Parse("kind:VulnerabilityReport"), Options{Include: IncludeNone, Namespaces: []string{"team-a", "team-b"}, KindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	want := []UnsearchedKind{{Kind: "VulnerabilityReport", Group: trivyGroup, Reason: UnsearchedUnreadable, Namespaces: []string{"team-b"}}}
	if !reflect.DeepEqual(res.UnsearchedKinds, want) {
		t.Fatalf("unsearched = %+v, want %+v", res.UnsearchedKinds, want)
	}
}

func TestSearch_KindAccessLeavesOtherCRDsAlone(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "shop", "namespace": "argocd"},
	}}
	p := trivyProvider(gvr, "Application", app)
	access := &kindAccess{}
	res, err := Search(context.Background(), p, Parse("shop"), Options{Include: IncludeNone, Namespaces: []string{"argocd"}, KindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || len(access.asked) != 0 {
		t.Fatalf("hits = %v, asked = %v", hitNames(res), access.asked)
	}
}

func TestSearch_SbomAndFailedChecksAreSearchable(t *testing.T) {
	sbom := trivyReport("SbomReport", "shop", "sbom-web", map[string]any{
		"components": map[string]any{"components": []any{map[string]any{"name": "php-cli", "version": "8.3.10"}}},
	})
	audit := trivyReport("ConfigAuditReport", "shop", "audit-web", map[string]any{
		"checks": []any{
			map[string]any{"checkID": "KSV014", "success": false},
			map[string]any{"checkID": "KSV020", "success": true},
		},
	})
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{sbomGVR: {sbom}, auditGVR: {audit}},
		kinds:   map[schema.GroupVersionResource]string{sbomGVR: "SbomReport", auditGVR: "ConfigAuditReport"},
	}
	for q, want := range map[string][]string{
		"php-cli 8.3.10": {"shop/sbom-web"},
		"KSV014":         {"shop/audit-web"},
		"KSV020":         {},
	} {
		res, err := Search(context.Background(), p, Parse(q), Options{Include: IncludeNone})
		if err != nil {
			t.Fatal(err)
		}
		if got := hitNames(res); !slices.Equal(got, want) {
			t.Errorf("%q: hits = %v, want %v", q, got, want)
		}
	}
}

// BenchmarkSearch_TrivyReports measures a free-text query over a large Trivy
// install: 1,000 VulnerabilityReports with 300 findings each.
func BenchmarkSearch_TrivyReports(b *testing.B) {
	items := make([]*unstructured.Unstructured, 0, 1000)
	for r := 0; r < 1000; r++ {
		vulns := make([]map[string]any, 300)
		for i := range vulns {
			vulns[i] = vuln(fmt.Sprintf("CVE-2024-%05d", r*300+i), fmt.Sprintf("lib%03d", i), "1.2.3-4", "1.2.4", "MEDIUM")
		}
		items = append(items, vulnReport(fmt.Sprintf("ns-%02d", r%40), fmt.Sprintf("report-%04d", r), "library/app", "1.0", vulns...))
	}
	p := trivyProvider(vulnGVR, "VulnerabilityReport", items...)
	for _, bc := range []struct {
		name    string
		query   string
		include IncludeMode
	}{
		{"token matching nothing", "CVE-2026-109618", IncludeNone},
		{"token matching every report, with summaries", "CVE-2024", IncludeSummary},
		{"modifier only", "kind:VulnerabilityReport image:app", IncludeNone},
	} {
		b.Run(bc.name, func(b *testing.B) {
			q := Parse(bc.query)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Search(context.Background(), p, q, Options{Include: bc.include, Limit: 50}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
