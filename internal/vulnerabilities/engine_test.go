package vulnerabilities

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type vuln struct{ id, sev, pkg, installed, fixed string }

type reportSpec struct {
	name, ns, ownerKind, ownerName, container string
	uid                                       types.UID
	registry, repo, tag, digest, scanned      string
	vulns                                     []vuln
}

func vulnReport(s reportSpec) *unstructured.Unstructured {
	vs := make([]any, 0, len(s.vulns))
	for _, v := range s.vulns {
		vs = append(vs, map[string]any{"vulnerabilityID": v.id, "severity": v.sev, "resource": v.pkg, "installedVersion": v.installed, "fixedVersion": v.fixed, "score": 7.5})
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]any{
			"name": s.name, "namespace": s.ns,
			"labels": map[string]any{"trivy-operator.container.name": s.container},
		},
		"report": map[string]any{
			"registry":        map[string]any{"server": s.registry},
			"artifact":        map[string]any{"repository": s.repo, "tag": s.tag, "digest": s.digest},
			"os":              map[string]any{"family": "debian", "name": "11"},
			"updateTimestamp": s.scanned,
			"vulnerabilities": vs,
		},
	}}
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: s.ownerKind, Name: s.ownerName, UID: s.uid, Controller: &controller}})
	return u
}

func liveTarget(ns, kind, name, container, image string, uid types.UID, workload WorkloadRef) target {
	digest := pinnedDigest(image)
	return target{key: targetKey{namespace: ns, kind: kind, name: name, container: container, image: image, digest: digest}, uid: uid, image: image, digest: digest, workload: workload, container: container}
}

// running is t once its container has started on the given registry digest.
func running(t target, digest string) target {
	t.digest, t.key.digest = digest, digest
	return t
}

var (
	checkout = WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "checkout"}
	worker   = WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "worker"}
	db       = WorkloadRef{Kind: "StatefulSet", Group: "apps", Namespace: "shop", Name: "db"}
)

func mustBuild(t *testing.T, items []*unstructured.Unstructured, targets []target, view View, f Filter) Response {
	t.Helper()
	resp, err := build(context.Background(), items, targets, view, f)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A Deployment's report is about its live ReplicaSet; an older revision's
// report is left out and counted as inactive, and a recreated ReplicaSet
// with the same name is not the object that was scanned.
func TestOnlyLiveRevisionsCount(t *testing.T) {
	findings := []vuln{{"CVE-1", "CRITICAL", "zlib", "1.0", "1.1"}}
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "rs-new", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-new", uid: "uid-new", container: "app", repo: "acme/checkout", tag: "2.0", digest: "sha256:new", scanned: "2026-11-10T10:00:00Z", vulns: findings}),
		vulnReport(reportSpec{name: "rs-old", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-old", uid: "uid-old", container: "app", repo: "acme/checkout", tag: "1.0", digest: "sha256:old", scanned: "2026-11-01T10:00:00Z", vulns: findings}),
		vulnReport(reportSpec{name: "rs-recreated", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-abc", uid: "uid-gone", container: "app", repo: "acme/worker", tag: "1.0", digest: "sha256:w", scanned: "2026-11-10T10:00:00Z", vulns: findings}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-new", "app", "acme/checkout:2.0", "uid-new", checkout),
		liveTarget("shop", "ReplicaSet", "worker-abc", "app", "acme/worker:1.0", "uid-current", worker),
	}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(resp.Images) != 1 || resp.Images[0].ID != "sha256:new" {
		t.Fatalf("images: %+v", resp.Images)
	}
	if resp.Coverage.InactiveReports != 2 {
		t.Fatalf("inactive: %d", resp.Coverage.InactiveReports)
	}
	if len(resp.Unscanned) != 1 || resp.Unscanned[0].Image != "acme/worker:1.0" {
		t.Fatalf("the recreated ReplicaSet's image has no report of its own: %+v", resp.Unscanned)
	}
}

// A StatefulSet mid-rollout runs two images under one owner and container:
// the report is about the image it names, and the other revision stays
// unscanned rather than borrowing its findings.
func TestRolloutRevisionsKeepTheirOwnImage(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "sts-db", ns: "shop", ownerKind: "StatefulSet", ownerName: "db", uid: "uid-db", container: "pg", registry: "index.docker.io", repo: "library/postgres", tag: "16.4", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-2", "HIGH", "openssl", "3.0", ""}}}),
	}
	targets := []target{
		liveTarget("shop", "StatefulSet", "db", "pg", "postgres:16.4", "uid-db", db),
		liveTarget("shop", "StatefulSet", "db", "pg", "postgres:16.3", "uid-db", db),
	}
	resp := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if len(resp.Workloads) != 1 {
		t.Fatalf("workloads: %+v", resp.Workloads)
	}
	var scanned, unscanned int
	for _, c := range resp.Workloads[0].Containers {
		if c.Scanned {
			scanned++
			if c.Image != "postgres:16.4" || c.Counts == nil || c.Counts.High != 1 {
				t.Fatalf("scanned container: %+v", c)
			}
		} else {
			unscanned++
			if c.Counts != nil {
				t.Fatalf("an unscanned container's counts are unknown, not zero: %+v", c)
			}
		}
	}
	if scanned != 1 || unscanned != 1 || len(resp.Unscanned) != 1 || resp.Unscanned[0].Image != "postgres:16.3" {
		t.Fatalf("containers %+v, unscanned %+v", resp.Workloads[0].Containers, resp.Unscanned)
	}
}

// An owner running one image matches its report however the reference is
// spelled; a pinned digest must match the report's digest.
func TestSameImage(t *testing.T) {
	spec := func(image string) *target {
		tg := liveTarget("shop", "ReplicaSet", "web-1", "app", image, "", checkout)
		return &tg
	}
	r := &report{image: "index.docker.io/library/nginx:1.23.4", digest: "sha256:abc"}
	for _, pod := range []string{"nginx:1.23.4", "docker.io/library/nginx:1.23.4", "nginx@sha256:abc"} {
		if !sameImage(spec(pod), r) {
			t.Errorf("%s should match", pod)
		}
	}
	for _, pod := range []string{"nginx:1.23.5", "nginx@sha256:def", "ghcr.io/x/nginx:1.23.4"} {
		if sameImage(spec(pod), r) {
			t.Errorf("%s should not match", pod)
		}
	}
	if !sameImage(spec("redis"), &report{image: "index.docker.io/library/redis:latest"}) {
		t.Error("an untagged reference is :latest")
	}
	if sameImage(spec("acme/app:release"), &report{image: "acme/app:Release"}) {
		t.Error("tags are case-sensitive")
	}
	if !sameImage(spec("ACME/app:Release"), &report{image: "acme/app:Release"}) {
		t.Error("repository names compare case-insensitively")
	}
	if sameImage(spec("acme/app:stable@sha256:new"), &report{image: "acme/app:stable"}) {
		t.Error("a pinned digest needs a report that recorded that digest")
	}
	tag := liveTarget("shop", "ReplicaSet", "web-1", "app", "nginx:1.23.4", "", checkout)
	if fresh := running(tag, "sha256:new"); sameImage(&fresh, r) {
		t.Error("a tag pushed again runs another build: the older build's scan doesn't describe it")
	}
	if same := running(tag, "sha256:abc"); !sameImage(&same, r) {
		t.Error("the running digest the report scanned matches")
	}
	if started := running(tag, "sha256:new"); sameImage(&started, &report{image: "nginx:1.23.4"}) {
		t.Error("a started container needs a report of its digest, not one that recorded none")
	}
	if !sameImage(&tag, &report{image: "nginx:1.23.4"}) {
		t.Error("a container that hasn't started matches by reference")
	}
	local := tag
	local.unresolved = true
	if sameImage(&local, &report{image: "nginx:1.23.4", digest: "sha256:abc"}) {
		t.Error("a started container with only a local image ID matches nothing")
	}
}

// One image run by two workloads is one row, linked to the newest report,
// with both reports' findings: each scan of a build can leave out what the
// other lists.
func TestOneRowPerImageMergesItsReports(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-OLD", "CRITICAL", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-NEW", "HIGH", "curl", "7", ""}}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/base:1", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/base:1", "", worker),
	}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(resp.Images) != 1 {
		t.Fatalf("images: %+v", resp.Images)
	}
	img := resp.Images[0]
	if len(img.Workloads) != 2 || img.Counts.Critical != 1 || img.Counts.High != 1 || img.Report.Name != "b" {
		t.Fatalf("image: %+v", img)
	}
	if resp.Summary.ImagesScanned != 1 || resp.Summary.Workloads != 2 || resp.Summary.WorkloadsWithCriticalOrHigh != 2 {
		t.Fatalf("summary: %+v", resp.Summary)
	}
}

func filterFixture() ([]*unstructured.Unstructured, []target) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-A", "CRITICAL", "zlib", "1", "2"},
			{"CVE-B", "HIGH", "curl", "7", ""},
			{"CVE-C", "LOW", "bash", "5", "6"},
		}}),
		vulnReport(reportSpec{name: "w", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/worker", tag: "1", digest: "sha256:w", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-A", "CRITICAL", "zlib", "1", "2"},
			{"CVE-D", "MEDIUM", "tar", "1", ""},
		}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	return items, targets
}

// Filters narrow the rows and the findings listed; counts and the summary
// still describe everything.
func TestFilters(t *testing.T) {
	items, targets := filterFixture()
	resp := mustBuild(t, items, targets, ViewImage, Filter{Severities: []Severity{SeverityCritical, SeverityHigh}, FixableOnly: true})
	if len(resp.Images) != 2 {
		t.Fatalf("both images carry a fixable Critical: %+v", resp.Images)
	}
	for _, img := range resp.Images {
		if img.Matching != 1 || len(img.Findings) != 1 || img.Findings[0].ID != "CVE-A" {
			t.Fatalf("matching findings: %+v", img)
		}
	}
	if resp.Images[0].Counts.Low+resp.Images[1].Counts.Low != 1 || resp.Summary.Counts.Medium != 1 {
		t.Fatalf("counts stay whole: %+v / %+v", resp.Images, resp.Summary)
	}
	if resp.Summary.FixableCriticalHighCVEs != 1 || resp.Summary.ImagesWithCritical != 2 {
		t.Fatalf("summary: %+v", resp.Summary)
	}

	byCVE := mustBuild(t, items, targets, ViewImage, Filter{Query: "cve-d"})
	if len(byCVE.Images) != 1 || byCVE.Images[0].Image != "acme/worker:1" || len(byCVE.Images[0].Findings) != 1 {
		t.Fatalf("a CVE query keeps the findings it names: %+v", byCVE.Images)
	}
	byName := mustBuild(t, items, targets, ViewImage, Filter{Query: "checkout"})
	if len(byName.Images) != 1 || len(byName.Images[0].Findings) != 3 {
		t.Fatalf("a workload query keeps the row's findings whole: %+v", byName.Images)
	}
	one := mustBuild(t, items, targets, ViewImage, Filter{Workload: &WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "worker"}})
	if len(one.Images) != 1 || one.Images[0].Image != "acme/worker:1" {
		t.Fatalf("workload filter: %+v", one.Images)
	}
}

// By CVE, a CVE counts the images and workloads it is in, each image once,
// and the worst, fixable, most widespread CVEs come first.
func TestCVEView(t *testing.T) {
	items, targets := filterFixture()
	resp := mustBuild(t, items, targets, ViewCVE, Filter{})
	if len(resp.CVEs) != 4 {
		t.Fatalf("cves: %+v", resp.CVEs)
	}
	top := resp.CVEs[0]
	if top.ID != "CVE-A" || top.Images != 2 || top.Workloads != 2 || !top.Fixable || len(top.Affected) != 2 || len(top.Affected[0].Packages) != 1 {
		t.Fatalf("top: %+v", top)
	}
	if resp.CVEs[1].ID != "CVE-B" || resp.CVEs[1].Fixable {
		t.Fatalf("second: %+v", resp.CVEs[1])
	}
	fixable := mustBuild(t, items, targets, ViewCVE, Filter{FixableOnly: true})
	if len(fixable.CVEs) != 2 {
		t.Fatalf("fixable cves: %+v", fixable.CVEs)
	}
}

// Findings past MaxFindings are not read; the answer says so instead of
// reading as clean.
func TestFindingsBudget(t *testing.T) {
	vs := make([]vuln, MaxFindings+1)
	for i := range vs {
		vs[i] = vuln{"CVE-X", "LOW", "p", "1", ""}
	}
	items := []*unstructured.Unstructured{vulnReport(reportSpec{name: "big", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/big", tag: "1", scanned: "2026-11-10T00:00:00Z", vulns: vs})}
	targets := []target{liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/big:1", "", checkout)}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if !resp.Coverage.Truncated || len(resp.Images) != 1 || !resp.Images[0].FindingsUnread {
		t.Fatalf("an unread image is marked unread, not clean: %+v", resp)
	}
	filtered := mustBuild(t, items, targets, ViewImage, Filter{Severities: []Severity{SeverityCritical}, FixableOnly: true})
	if len(filtered.Images) != 1 || !filtered.Images[0].FindingsUnread {
		t.Fatalf("an unread image can't be ruled out by a finding filter: %+v", filtered.Images)
	}
	w := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if c := w.Workloads[0].Containers[0]; !c.FindingsUnread || c.Counts != nil {
		t.Fatalf("workload container: %+v", c)
	}
	wf := mustBuild(t, items, targets, ViewWorkload, Filter{Severities: []Severity{SeverityCritical}})
	if len(wf.Workloads) != 1 {
		t.Fatalf("a workload whose findings weren't read can't be ruled out by a filter: %+v", wf.Workloads)
	}
}

// After a StatefulSet moves to a new image, the report about the old one
// describes nothing that runs: the new image is unscanned until Trivy
// rescans, never credited with the old scan's findings.
func TestReportAboutAnotherImageDoesNotMatch(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "sts-db", ns: "shop", ownerKind: "StatefulSet", ownerName: "db", uid: "uid-db", container: "pg", registry: "index.docker.io", repo: "library/postgres", tag: "16.3", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-2", "HIGH", "openssl", "3.0", ""}}}),
	}
	targets := []target{liveTarget("shop", "StatefulSet", "db", "pg", "postgres:16.4", "uid-db", db)}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(resp.Images) != 0 || resp.Coverage.InactiveReports != 1 || len(resp.Unscanned) != 1 {
		t.Fatalf("%+v", resp)
	}
}

// A Deployment mid-rollout with an unchanged image runs two ReplicaSets; the
// new one isn't scanned yet. The workload shows the image scanned, and it
// isn't listed as unscanned.
func TestRolloutWithUnchangedImageStaysScanned(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "rs-old", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-1", "CRITICAL", "zlib", "1", "2"}}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-2", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
	}
	resp := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if len(resp.Workloads) != 1 || len(resp.Workloads[0].Containers) != 1 || !resp.Workloads[0].Containers[0].Scanned || resp.Workloads[0].Counts.Critical != 1 {
		t.Fatalf("%+v", resp.Workloads)
	}
	if len(resp.Unscanned) != 0 {
		t.Fatalf("unscanned: %+v", resp.Unscanned)
	}
}

// A CVE in two packages of one image is one affected image.
func TestCVEInTwoPackagesIsOneImage(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-A", "HIGH", "libssl3", "3.0", "3.1"},
			{"CVE-A", "HIGH", "openssl", "3.0", "3.1"},
		}}),
	}
	targets := []target{liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout)}
	resp := mustBuild(t, items, targets, ViewCVE, Filter{})
	if len(resp.CVEs) != 1 || resp.CVEs[0].Images != 1 || len(resp.CVEs[0].Affected) != 1 || len(resp.CVEs[0].Affected[0].Packages) != 2 {
		t.Fatalf("%+v", resp.CVEs)
	}
}

// An image counts as fixed for a CVE only when every package the CVE is in
// there has a fixed version.
func TestCVEFixableImages(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-A", "HIGH", "libssl3", "3.0", "3.1"},
			{"CVE-A", "HIGH", "openssl", "3.0", ""},
		}}),
		vulnReport(reportSpec{name: "w", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/worker", tag: "1", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-A", "HIGH", "libssl3", "3.0", "3.1"},
		}}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	resp := mustBuild(t, items, targets, ViewCVE, Filter{})
	if len(resp.CVEs) != 1 || resp.CVEs[0].Images != 2 || resp.CVEs[0].FixableImages != 1 || !resp.CVEs[0].Fixable {
		t.Fatalf("%+v", resp.CVEs)
	}
	// Narrowed to findings with a fix, checkout's unfixed openssl still
	// keeps the upgrade from clearing the CVE there.
	if resp := mustBuild(t, items, targets, ViewCVE, Filter{FixableOnly: true}); resp.CVEs[0].FixableImages != 1 {
		t.Fatalf("filtered: %+v", resp.CVEs)
	}
}

// Two ReplicaSets of one Deployment spelling the same Docker Hub image
// differently share the scanned slot: neither reads as unscanned.
func TestRolloutWithImageAliasesStaysScanned(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "rs-old", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-1", container: "nginx", registry: "index.docker.io", repo: "library/nginx", tag: "1.25", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-1", "HIGH", "zlib", "1", "2"}}}),
	}
	web := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "web-1", "nginx", "nginx:1.25", "", web),
		liveTarget("shop", "ReplicaSet", "web-2", "nginx", "docker.io/library/nginx:1.25", "", web),
	}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(resp.Unscanned) != 0 {
		t.Fatalf("unscanned: %+v", resp.Unscanned)
	}
	w := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if len(w.Workloads) != 1 || len(w.Workloads[0].Containers) != 1 || !w.Workloads[0].Containers[0].Scanned {
		t.Fatalf("one image under two spellings is one scanned container: %+v", w.Workloads)
	}
}

// Trivy Operator records a digest-pinned Pod's tag as "name:tag"; the image
// still reads as people write it.
func TestArtifactImageWithNameInTag(t *testing.T) {
	img, digest := artifactImage(map[string]any{
		"registry": map[string]any{"server": "index.docker.io"},
		"artifact": map[string]any{"repository": "library/busybox", "tag": "busybox:1.38.0", "digest": "sha256:dc2d"},
	})
	if img != "busybox:1.38.0" || digest != "sha256:dc2d" {
		t.Fatalf("%q %q", img, digest)
	}
}

// Clean images and workloads are listed apart, not as rows, so they don't
// bury the ones with findings; a search that names one brings its row back.
func TestCleanImagesListedApart(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-A", "HIGH", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "p", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/pause", tag: "3", digest: "sha256:p", scanned: "2026-11-10T00:00:00Z"}),
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/pause:3", "", worker),
	}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	if len(resp.Images) != 1 || resp.Images[0].Image != "acme/checkout:2" || len(resp.Clean) != 1 || resp.Clean[0].Image != "acme/pause:3" {
		t.Fatalf("rows %+v clean %+v", resp.Images, resp.Clean)
	}
	if resp.Summary.ImagesClean != 1 || resp.Summary.ImagesScanned != 2 {
		t.Fatalf("summary %+v", resp.Summary)
	}
	named := mustBuild(t, items, targets, ViewImage, Filter{Query: "pause"})
	if len(named.Images) != 1 || named.Images[0].Image != "acme/pause:3" || len(named.Clean) != 0 {
		t.Fatalf("a search that names a clean image shows its row: %+v / %+v", named.Images, named.Clean)
	}
	w := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if len(w.Workloads) != 1 || w.Workloads[0].Name != "checkout" || len(w.CleanWorkloads) != 1 || w.CleanWorkloads[0].Name != "worker" {
		t.Fatalf("workloads %+v clean %+v", w.Workloads, w.CleanWorkloads)
	}
}

// By fix: one row per upgrade, to the version that clears every fixable CVE
// of the package; images needing the same upgrade share it; a package whose
// versions can't be ordered keeps the scanner's list.
func TestFixView(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-1", "CRITICAL", "protobufjs", "6.11.4", "8.0.1, 7.5.5, 6.11.6"},
			{"CVE-2", "HIGH", "protobufjs", "6.11.4", "7.5.6, 6.11.7"},
			{"CVE-3", "LOW", "bash", "5.2", ""},
		}}),
		vulnReport(reportSpec{name: "w", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/worker", tag: "1", digest: "sha256:w", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
			{"CVE-1", "CRITICAL", "protobufjs", "6.11.4", "8.0.1, 7.5.5, 6.11.6"},
			{"CVE-2", "HIGH", "protobufjs", "6.11.4", "7.5.6, 6.11.7"},
			{"CVE-9", "MEDIUM", "libfoo", "1.0", "1.1"},
		}}),
	}
	for _, u := range items {
		vs := u.Object["report"].(map[string]any)["vulnerabilities"].([]any)
		for _, v := range vs {
			m := v.(map[string]any)
			if m["resource"] == "protobufjs" {
				m["packageType"] = "npm"
			} else {
				m["packageType"] = "conan"
			}
		}
	}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	resp := mustBuild(t, items, targets, ViewFix, Filter{})
	if len(resp.Fixes) != 2 {
		t.Fatalf("fixes: %+v", resp.Fixes)
	}
	top := resp.Fixes[0]
	if top.Package != "protobufjs" || top.To != "6.11.7" || top.CVEsTotal != 2 || top.Counts.Critical != 1 || top.Counts.High != 1 || top.Images != 2 || top.Workloads != 2 {
		t.Fatalf("one bump clears both CVEs in both images: %+v", top)
	}
	other := resp.Fixes[1]
	if other.Package != "libfoo" || other.To != "" || len(other.FixedVersions) != 1 || other.FixedVersions[0] != "1.1" {
		t.Fatalf("an ecosystem Radar can't order keeps the scanner's list: %+v", other)
	}
	crit := mustBuild(t, items, targets, ViewFix, Filter{Severities: []Severity{SeverityCritical}})
	if len(crit.Fixes) != 1 || crit.Fixes[0].To != "6.11.6" {
		t.Fatalf("filtered to Critical, the bump is the one Critical needs: %+v", crit.Fixes)
	}

	// Every view names the same per-finding target, so By image and By CVE
	// don't show the scanner's branch list where By fix shows one version.
	byImage := mustBuild(t, items, targets, ViewImage, Filter{})
	got := map[string]string{}
	for _, f := range byImage.Images[0].Findings {
		got[f.ID] = f.FixTarget
	}
	if got["CVE-1"] != "6.11.6" || got["CVE-2"] != "6.11.7" || got["CVE-3"] != "" {
		t.Fatalf("finding targets: %+v", got)
	}
	byCVE := mustBuild(t, items, targets, ViewCVE, Filter{})
	for _, c := range byCVE.CVEs {
		if c.ID == "CVE-1" && c.Affected[0].Packages[0].FixTarget != "6.11.6" {
			t.Fatalf("CVE view target: %+v", c.Affected[0].Packages)
		}
	}
}

// A package name is only an identity within its ecosystem: npm and Python
// packages called "redis" are two upgrades, never one.
func TestFixRowsKeepEcosystemsApart(t *testing.T) {
	u := vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
		{"CVE-1", "HIGH", "redis", "4.0.0", "4.0.1"},
		{"CVE-2", "HIGH", "redis", "4.0.0", "4.0.2"},
	}})
	vs := u.Object["report"].(map[string]any)["vulnerabilities"].([]any)
	vs[0].(map[string]any)["packagePURL"] = "pkg:npm/redis@4.0.0"
	vs[1].(map[string]any)["packagePURL"] = "pkg:pypi/redis@4.0.0"
	targets := []target{liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout)}
	resp := mustBuild(t, []*unstructured.Unstructured{u}, targets, ViewFix, Filter{})
	if len(resp.Fixes) != 2 {
		t.Fatalf("two ecosystems, two upgrades: %+v", resp.Fixes)
	}
	for _, f := range resp.Fixes {
		if f.CVEsTotal != 1 || (f.PackageType == "npm") != (f.To == "4.0.1") {
			t.Fatalf("each upgrade clears its own ecosystem's CVE: %+v", f)
		}
	}
}

// One image can carry a package at two versions (nested dependencies): the
// upgrade lists both under that image.
func TestFixRowListsEveryInstalledVersion(t *testing.T) {
	u := vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{
		{"CVE-1", "HIGH", "lodash", "4.17.19", "4.17.21"},
		{"CVE-1", "HIGH", "lodash", "4.17.20", "4.17.21"},
	}})
	for _, v := range u.Object["report"].(map[string]any)["vulnerabilities"].([]any) {
		v.(map[string]any)["packagePURL"] = "pkg:npm/lodash"
	}
	targets := []target{liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout)}
	resp := mustBuild(t, []*unstructured.Unstructured{u}, targets, ViewFix, Filter{})
	if len(resp.Fixes) != 1 || len(resp.Fixes[0].Affected) != 1 || !slices.Equal(resp.Fixes[0].Affected[0].InstalledVersions, []string{"4.17.19", "4.17.20"}) {
		t.Fatalf("both installed versions: %+v", resp.Fixes)
	}
}

// One unscanned image under two spellings is one unscanned image.
func TestUnscannedAliasesAreOneImage(t *testing.T) {
	web := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}
	targets := []target{
		liveTarget("shop", "ReplicaSet", "web-1", "nginx", "nginx:1.25", "", web),
		liveTarget("shop", "ReplicaSet", "web-2", "nginx", "docker.io/library/nginx:1.25", "", web),
	}
	resp := mustBuild(t, nil, targets, ViewImage, Filter{})
	if resp.Summary.ImagesUnscanned != 1 || len(resp.Unscanned) != 1 {
		t.Fatalf("unscanned: %d %+v", resp.Summary.ImagesUnscanned, resp.Unscanned)
	}
}

// A digest-pinned image is grouped by its digest but still shown, and found,
// by the name the Pod spec gives it.
func TestSearchFindsAScannedImageByEverySpelling(t *testing.T) {
	a := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/frontend", tag: "1", digest: "sha256:same", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}}})
	b := vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/backend", tag: "1", digest: "sha256:same", scanned: "2026-11-09T10:00:00Z", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}}})
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/frontend:1", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/backend:1", "", worker),
	}
	for _, q := range []string{"acme/backend", "acme/frontend", "sha256:same"} {
		got := mustBuild(t, []*unstructured.Unstructured{a, b}, targets, ViewImage, Filter{Query: q})
		if len(got.Images) != 1 || len(got.Images[0].Findings) != 1 {
			t.Fatalf("%s: %+v", q, got.Images)
		}
	}
}

func TestUnscannedPinnedImageKeepsItsName(t *testing.T) {
	ref := "registry.k8s.io/ingress-nginx/controller:v1.9.4@sha256:5b16"
	targets := []target{liveTarget("shop", "ReplicaSet", "web-1", "nginx", ref, "", WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"})}
	resp := mustBuild(t, nil, targets, ViewImage, Filter{Query: "controller:v1.9.4"})
	if len(resp.Unscanned) != 1 || resp.Unscanned[0].Image != ref {
		t.Fatalf("unscanned: %+v", resp.Unscanned)
	}
	// Two repositories pushing one digest are one image, found by either name.
	targets = append(targets, liveTarget("shop", "ReplicaSet", "pay-1", "app", "zcorp/payment-runtime@sha256:5b16", "", WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "pay"}))
	resp = mustBuild(t, nil, targets, ViewImage, Filter{Query: "payment-runtime"})
	if len(resp.Unscanned) != 1 || resp.Unscanned[0].WorkloadsTotal != 2 {
		t.Fatalf("by the other name: %+v", resp.Unscanned)
	}
}

// Narrowed to one workload, a row shared by more workloads than it lists
// still lists that workload's containers.
func TestWorkloadFilterListsItsOwnContainers(t *testing.T) {
	var targets []target
	var last WorkloadRef
	for i := range MaxWorkloadsPerRow + 1 {
		last = WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("web-%03d", i)}
		targets = append(targets, liveTarget("shop", "ReplicaSet", last.Name+"-1", "proxy", "acme/proxy:1", "", last))
	}
	resp := mustBuild(t, nil, targets, ViewImage, Filter{Workload: &last})
	if len(resp.Unscanned) != 1 || resp.Unscanned[0].WorkloadsTotal != MaxWorkloadsPerRow+1 {
		t.Fatalf("unscanned: %+v", resp.Unscanned)
	}
	if got := resp.Unscanned[0].Workloads[0]; got.Name != last.Name || got.Container != "proxy" {
		t.Fatalf("first listed: %+v", got)
	}
	// Under the cap too: a client trimming the list keeps it.
	few := targets[:20]
	last = few[19].workload
	if got := mustBuild(t, nil, few, ViewImage, Filter{Workload: &last}).Unscanned[0].Workloads[0]; got.Name != last.Name {
		t.Fatalf("first listed under the cap: %+v", got)
	}
}

// The summary view is the totals and coverage of the image view, without
// any rows: what a page showing counts needs.
func TestSummaryView(t *testing.T) {
	u := vulnReport(reportSpec{name: "c", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-1", "CRITICAL", "zlib", "1", "2"}}})
	targets := []target{
		liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:2", "", checkout),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	full := mustBuild(t, []*unstructured.Unstructured{u}, targets, ViewImage, Filter{})
	sum := mustBuild(t, []*unstructured.Unstructured{u}, targets, ViewSummary, Filter{})
	if !reflect.DeepEqual(sum.Summary, full.Summary) || !reflect.DeepEqual(sum.Coverage, full.Coverage) {
		t.Fatalf("summary differs: %+v / %+v", sum.Summary, full.Summary)
	}
	if sum.Images != nil || sum.Unscanned != nil || sum.Clean != nil || sum.Summary.ImagesUnscanned != 1 {
		t.Fatalf("no rows: %+v", sum)
	}
}

// A container running one digest is described only by a report of that
// digest, and by every report of it: two workloads running one build share
// its scan whichever of them Trivy scanned.
func TestDigestDecidesWhichScanDescribesAContainer(t *testing.T) {
	stale := vulnReport(reportSpec{name: "web", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-1", container: "app", repo: "acme/web", tag: "latest", digest: "sha256:old"})
	web := running(liveTarget("shop", "ReplicaSet", "web-1", "app", "acme/web:latest", "", checkout), "sha256:new")
	got := mustBuild(t, []*unstructured.Unstructured{stale}, []target{web}, ViewImage, Filter{})
	if got.Summary.ImagesClean != 0 || got.Summary.ImagesUnscanned != 1 || got.Coverage.InactiveReports != 1 {
		t.Fatalf("an older build's clean scan must not describe the new one: %+v", got.Summary)
	}
	if len(got.Unscanned) != 1 || !got.Unscanned[0].OtherBuildScanned {
		t.Fatalf("the unscanned build says another build of its tag was scanned: %+v", got.Unscanned)
	}

	scanned := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/web", tag: "1", digest: "sha256:same", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}}})
	targets := []target{
		running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/web:1", "", checkout), "sha256:same"),
		running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/web:1", "", worker), "sha256:same"),
	}
	got = mustBuild(t, []*unstructured.Unstructured{scanned}, targets, ViewCVE, Filter{})
	if len(got.CVEs) != 1 || got.CVEs[0].Workloads != 2 || len(got.Unscanned) != 0 || got.Summary.WorkloadsWithCriticalOrHigh != 2 {
		t.Fatalf("worker runs the scanned build: %+v / %+v", got.CVEs, got.Unscanned)
	}
}

// Builds of one tag that no report describes are one unscanned image, whether
// or not their containers have reported a digest yet.
func TestUnscannedBuildsOfATagAreOneImage(t *testing.T) {
	started := running(liveTarget("kube-system", "DaemonSet", "aws-node", "cni", "acme/cni:1", "", checkout), "sha256:a")
	pending := liveTarget("kube-system", "DaemonSet", "aws-node", "cni", "acme/cni:1", "", checkout)
	older := running(liveTarget("kube-system", "DaemonSet", "aws-node", "cni", "acme/cni:1", "", checkout), "sha256:b")
	got := mustBuild(t, nil, []target{started, pending, older}, ViewImage, Filter{})
	if len(got.Unscanned) != 1 || got.Summary.ImagesUnscanned != 1 {
		t.Fatalf("%+v", got.Unscanned)
	}
}

// Pods of one workload on two builds of a tag: each build is its own
// container entry, whichever order the Pods list in, so a build no report
// describes keeps the workload from reading clean.
func TestWorkloadListsEachBuild(t *testing.T) {
	cleanA := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "latest", digest: "sha256:a"})
	critB := vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-2", container: "app", repo: "acme/checkout", tag: "latest", digest: "sha256:b", vulns: []vuln{{"CVE-B", "CRITICAL", "zlib", "1", "2"}}})
	onA := running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:latest", "", checkout), "sha256:a")
	onB := running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:latest", "", checkout), "sha256:b")
	for _, targets := range [][]target{{onA, onB}, {onB, onA}} {
		got := mustBuild(t, []*unstructured.Unstructured{cleanA}, targets, ViewWorkload, Filter{})
		if len(got.CleanWorkloads) != 0 || len(got.Workloads) != 1 || len(got.Workloads[0].Containers) != 2 {
			t.Fatalf("a build no report describes isn't clean: %+v / %+v", got.Workloads, got.CleanWorkloads)
		}
	}
	rolling := running(liveTarget("shop", "ReplicaSet", "checkout-2", "app", "acme/checkout:latest", "", checkout), "sha256:b")
	for _, targets := range [][]target{{onA, rolling}, {rolling, onA}} {
		got := mustBuild(t, []*unstructured.Unstructured{cleanA, critB}, targets, ViewWorkload, Filter{})
		if len(got.Workloads) != 1 || got.Workloads[0].Counts.Critical != 1 || len(got.Workloads[0].Containers) != 2 {
			t.Fatalf("both builds count: %+v", got.Workloads)
		}
	}
}

// The flag that Trivy scanned another build of a tag: set for an object's
// Pods on a build its report doesn't describe, even when its other Pods match,
// and only for the workloads whose own scanned object has that report.
func TestOtherBuildScannedIsPerWorkload(t *testing.T) {
	onlyA := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "latest", digest: "sha256:a"})
	onA := running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:latest", "", checkout), "sha256:a")
	onB := running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:latest", "", checkout), "sha256:b")
	other := running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/checkout:latest", "", worker), "sha256:c")
	targets := []target{onA, onB, other}
	got := mustBuild(t, []*unstructured.Unstructured{onlyA}, targets, ViewImage, Filter{})
	if len(got.Unscanned) != 1 || !got.Unscanned[0].OtherBuildScanned {
		t.Fatalf("checkout's Pods on build B: %+v", got.Unscanned)
	}
	w := worker
	got = mustBuild(t, []*unstructured.Unstructured{onlyA}, targets, ViewImage, Filter{Workload: &w})
	if len(got.Unscanned) != 1 || got.Unscanned[0].OtherBuildScanned {
		t.Fatalf("worker's object has no report of another build: %+v", got.Unscanned)
	}
}

// After a restart, the old ReplicaSet's report still describes the build the
// new one runs.
func TestRestartedReplicaSetKeepsItsBuildsScan(t *testing.T) {
	old := vulnReport(reportSpec{name: "old", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "2", digest: "sha256:c", vulns: []vuln{{"CVE-A", "HIGH", "zlib", "1", "2"}}})
	fresh := running(liveTarget("shop", "ReplicaSet", "checkout-2", "app", "acme/checkout:2", "", checkout), "sha256:c")
	got := mustBuild(t, []*unstructured.Unstructured{old}, []target{fresh}, ViewImage, Filter{})
	if len(got.Images) != 1 || len(got.Unscanned) != 0 || got.Coverage.InactiveReports != 0 {
		t.Fatalf("%+v / %+v / %d", got.Images, got.Unscanned, got.Coverage.InactiveReports)
	}
}

// A Pod whose spec moved to a new digest still runs the old one until its
// container restarts; and a Pending Pod's spelling of a scanned image finds it.
func TestRunningBuildWinsAndPendingSpellingSearches(t *testing.T) {
	critA := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "Pod", ownerName: "web", container: "app", repo: "acme/web", digest: "sha256:a", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}}})
	moved := running(liveTarget("shop", "Pod", "web", "app", "acme/web@sha256:b", "", checkout), "sha256:a")
	got := mustBuild(t, []*unstructured.Unstructured{critA}, []target{moved}, ViewImage, Filter{})
	if len(got.Images) != 1 || got.Images[0].Counts.Critical != 1 {
		t.Fatalf("the build that runs is the kubelet's: %+v", got.Images)
	}

	nginx := vulnReport(reportSpec{name: "n", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-1", container: "app", repo: "library/nginx", tag: "1.25", digest: "sha256:n", vulns: []vuln{{"CVE-N", "HIGH", "zlib", "1", "2"}}})
	old := running(liveTarget("shop", "ReplicaSet", "web-1", "app", "nginx:1.25", "", checkout), "sha256:n")
	pending := liveTarget("shop", "ReplicaSet", "web-2", "app", "docker.io/library/nginx:1.25", "", checkout)
	got = mustBuild(t, []*unstructured.Unstructured{nginx}, []target{old, pending}, ViewImage, Filter{Query: "docker.io/library/nginx"})
	if len(got.Images) != 1 {
		t.Fatalf("the pending Pod's spelling finds the image: %+v", got.Images)
	}
}

// A report whose object no longer runs still describes its build: its
// findings merge with the running owner's, and the newest report (by name on
// a tie) is the one the row links to, whatever order the reports list in.
func TestIdleReportJoinsItsBuild(t *testing.T) {
	spec := func(name, owner, scanned string, vulns ...vuln) *unstructured.Unstructured {
		return vulnReport(reportSpec{name: name, ns: "shop", ownerKind: "ReplicaSet", ownerName: owner, container: "app", repo: "acme/web", tag: "1", digest: "sha256:d", scanned: scanned, vulns: vulns})
	}
	active := running(liveTarget("shop", "ReplicaSet", "web-1", "app", "acme/web:1", "", checkout), "sha256:d")
	fresh := running(liveTarget("shop", "ReplicaSet", "web-3", "app", "acme/web:1", "", checkout), "sha256:d")
	activeReport := spec("a", "web-1", "2026-11-17T10:00:00Z", vuln{"CVE-Y", "HIGH", "zlib", "1", "2"})
	idleReport := spec("b", "web-2", "2026-11-16T10:00:00Z", vuln{"CVE-X", "CRITICAL", "zlib", "1", "2"})
	for _, items := range [][]*unstructured.Unstructured{{activeReport, idleReport}, {idleReport, activeReport}} {
		got := mustBuild(t, items, []target{active, fresh}, ViewImage, Filter{})
		if len(got.Images) != 1 || got.Summary.Counts.Critical != 1 || got.Summary.Counts.High != 1 || got.Images[0].Report.Name != "a" || got.Coverage.InactiveReports != 0 {
			t.Fatalf("merged, linked to the newest: %+v %+v", got.Summary, got.Coverage)
		}
	}
	tieA := spec("a", "web-2", "2026-11-16T10:00:00Z", vuln{"CVE-Y", "HIGH", "zlib", "1", "2"})
	tieB := spec("b", "web-4", "2026-11-16T10:00:00Z", vuln{"CVE-Y", "HIGH", "zlib", "1", "2"}, vuln{"CVE-X", "CRITICAL", "zlib", "1", "2"})
	only := running(liveTarget("shop", "ReplicaSet", "web-3", "app", "acme/web:1", "", checkout), "sha256:d")
	for _, items := range [][]*unstructured.Unstructured{{tieA, tieB}, {tieB, tieA}} {
		got := mustBuild(t, items, []target{only}, ViewImage, Filter{})
		if got.Summary.Counts.Critical != 1 || got.Summary.Counts.High != 1 || got.Images[0].Report.Name != "a" {
			t.Fatalf("a tie links the lower name, findings counted once: %+v", got.Summary)
		}
	}
}

// Two filtered scans of one build list different subsets: every known
// finding counts, once.
func TestCrossingScansOfABuildBothCount(t *testing.T) {
	a := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/shared", tag: "1", digest: "sha256:d", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-CRIT", "CRITICAL", "lib", "1", "2"}, {"CVE-HIGH", "HIGH", "lib", "1", "2"}}})
	b := vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/shared", tag: "1", digest: "sha256:d", scanned: "2026-11-11T00:00:00Z", vulns: []vuln{{"CVE-HIGH", "HIGH", "lib", "1", "2"}, {"CVE-MED", "MEDIUM", "lib", "1", "2"}, {"CVE-LOW", "LOW", "lib", "1", "2"}}})
	targets := []target{running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/shared:1", "", checkout), "sha256:d"), running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/shared:1", "", worker), "sha256:d")}
	for _, order := range [][]*unstructured.Unstructured{{a, b}, {b, a}} {
		got := mustBuild(t, order, targets, ViewImage, Filter{Workload: &checkout})
		if c := got.Summary.Counts; c.Critical != 1 || c.High != 1 || c.Medium != 1 || c.Low != 1 {
			t.Fatalf("every known finding, once: %+v", c)
		}
	}
}

// A started container is described by the scan of its running digest, even
// one whose object no longer runs, never by its owner's report that recorded
// no digest.
func TestRunningDigestBeatsATagOnlyReport(t *testing.T) {
	byRef := vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-1", container: "app", repo: "acme/web", tag: "1", scanned: "2026-11-10T10:00:00Z"})
	idle := vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-2", container: "app", repo: "acme/web", tag: "1", digest: "sha256:d", scanned: "2026-11-16T10:00:00Z", vulns: []vuln{{"CVE-X", "CRITICAL", "zlib", "1", "2"}}})
	web := running(liveTarget("shop", "ReplicaSet", "web-1", "app", "acme/web:1", "", checkout), "sha256:d")
	got := mustBuild(t, []*unstructured.Unstructured{byRef, idle}, []target{web}, ViewImage, Filter{})
	if got.Summary.ImagesScanned != 1 || got.Coverage.InactiveReports != 1 || got.Summary.Counts.Critical != 1 {
		t.Fatalf("the scan of the running digest describes it, not the tag-only one: %+v / %d", got.Summary, got.Coverage.InactiveReports)
	}
}

// A finding the scanner left unrated takes the CVSS rating of its score, and
// says so; one with no score stays unknown.
func TestUnratedFindingTakesItsScoresRating(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"report": map[string]any{"vulnerabilities": []any{
		map[string]any{"vulnerabilityID": "CVE-1", "severity": "UNKNOWN", "resource": "x", "installedVersion": "1", "score": 8.2},
		map[string]any{"vulnerabilityID": "CVE-2", "severity": "UNKNOWN", "resource": "y", "installedVersion": "1"},
		map[string]any{"vulnerabilityID": "CVE-3", "severity": "LOW", "resource": "z", "installedVersion": "1", "score": 9.8},
	}}}}
	got := map[string]Finding{}
	for _, f := range parseFindings(u) {
		got[f.ID] = f
	}
	if f := got["CVE-1"]; f.Severity != SeverityHigh || !f.SeverityFromScore {
		t.Errorf("rated from 8.2: %+v", f)
	}
	if f := got["CVE-2"]; f.Severity != SeverityUnknown || f.SeverityFromScore {
		t.Errorf("no score stays unknown: %+v", f)
	}
	if f := got["CVE-3"]; f.Severity != SeverityLow || f.SeverityFromScore {
		t.Errorf("the scanner's own rating stands: %+v", f)
	}
}

// Two workloads run one build. A newer scan listing nothing (say, an ignore
// policy scoped to one of them) doesn't clear the other's reported CVE.
func TestEmptyScanDoesNotClearAnotherWorkloadsFindings(t *testing.T) {
	prod := vulnReport(reportSpec{name: "prod", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/web", tag: "1", digest: "sha256:d", scanned: "2026-11-10T10:00:00Z", vulns: []vuln{{"CVE-X", "CRITICAL", "zlib", "1", "2"}}})
	ignored := vulnReport(reportSpec{name: "ignored", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/web", tag: "1", digest: "sha256:d", scanned: "2026-11-16T10:00:00Z"})
	a := running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/web:1", "", checkout), "sha256:d")
	b := running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/web:1", "", worker), "sha256:d")
	for _, order := range [][]*unstructured.Unstructured{{prod, ignored}, {ignored, prod}} {
		got := mustBuild(t, order, []target{a, b}, ViewImage, Filter{})
		if got.Summary.Counts.Critical != 1 || got.Summary.ImagesClean != 0 {
			t.Fatalf("the reported Critical stands: %+v", got.Summary)
		}
	}
}

// Two non-empty scans of one build: a policy that drops a Critical for one
// workload doesn't drop it for another.
func TestFilteredScanKeepsAnotherWorkloadsCritical(t *testing.T) {
	full := vulnReport(reportSpec{name: "full", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/shared", tag: "1", digest: "sha256:d", scanned: "2026-11-10T00:00:00Z", vulns: []vuln{{"CVE-CRIT", "CRITICAL", "lib", "1", "2"}, {"CVE-HIGH", "HIGH", "lib", "1", "2"}}})
	filtered := vulnReport(reportSpec{name: "filtered", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/shared", tag: "1", digest: "sha256:d", scanned: "2026-11-11T00:00:00Z", vulns: []vuln{{"CVE-HIGH", "HIGH", "lib", "1", "2"}}})
	targets := []target{running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/shared:1", "", checkout), "sha256:d"), running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/shared:1", "", worker), "sha256:d")}
	for _, order := range [][]*unstructured.Unstructured{{full, filtered}, {filtered, full}} {
		if got := mustBuild(t, order, targets, ViewImage, Filter{}); got.Summary.Counts.Critical != 1 {
			t.Fatalf("the fuller scan describes the build: %+v", got.Summary)
		}
	}
}

// Narrowed to one workload, its images are read before the findings budget
// runs out on images the view doesn't show.
func TestNarrowedViewReadsItsImagesFirst(t *testing.T) {
	var items []*unstructured.Unstructured
	var targets []target
	for i := range MaxFindings/500 + 1 {
		owner := fmt.Sprintf("a-%04d", i)
		r := vulnReport(reportSpec{name: owner, ns: "shop", ownerKind: "ReplicaSet", ownerName: owner, container: "app", repo: "acme/a", tag: "1", digest: fmt.Sprintf("sha256:a%04d", i)})
		many := make([]any, 500)
		for k := range many {
			many[k] = map[string]any{"vulnerabilityID": fmt.Sprintf("CVE-A-%03d", k), "severity": "LOW", "resource": "a", "installedVersion": "1"}
		}
		r.Object["report"].(map[string]any)["vulnerabilities"] = many
		items = append(items, r)
		targets = append(targets, liveTarget("shop", "ReplicaSet", owner, "app", "acme/a:1", "", checkout))
	}
	items = append(items, vulnReport(reportSpec{name: "small", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/z", tag: "1", digest: "sha256:z", vulns: []vuln{{"CVE-Z", "CRITICAL", "z", "1", "2"}}}))
	targets = append(targets, liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/z:1", "", worker))
	got := mustBuild(t, items, targets, ViewImage, Filter{Workload: &worker})
	if len(got.Images) != 1 || got.Images[0].FindingsUnread || !got.Coverage.Truncated {
		t.Fatalf("the workload's image is read, the budget still reported: %d images, coverage %+v", len(got.Images), got.Coverage)
	}
}
