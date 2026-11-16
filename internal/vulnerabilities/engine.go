package vulnerabilities

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// report is one scan report reduced to what the rollup reads. Findings are
// parsed only for the report chosen for an image.
type report struct {
	u         *unstructured.Unstructured
	ref       resourceapi.ObjectRef
	owner     ownerKey
	uid       types.UID
	imageID   string
	image     string
	digest    string
	os        string
	osFamily  string
	scannedAt time.Time
	findings  []Finding
	// unread is true when the findings budget ran out before this report.
	unread bool
}

// reportHeader reads a Trivy Operator VulnerabilityReport's identity without
// its findings. A report names the object it scanned by its controller owner
// reference, or by Trivy's resource labels when it has none, and the
// container by label.
func reportHeader(u *unstructured.Unstructured) (report, bool) {
	body, _ := u.Object["report"].(map[string]any)
	labels := u.GetLabels()
	r := report{
		u:     u,
		ref:   resourceapi.ObjectRef{Kind: u.GetKind(), Group: u.GroupVersionKind().Group, Namespace: u.GetNamespace(), Name: u.GetName()},
		owner: ownerKey{namespace: u.GetNamespace(), container: labels["trivy-operator.container.name"]},
	}
	if owner := controllerOf(u.GetOwnerReferences()); owner != nil {
		r.owner.kind, r.owner.name, r.uid = owner.Kind, owner.Name, owner.UID
	} else {
		r.owner.kind, r.owner.name = labels["trivy-operator.resource.kind"], labels["trivy-operator.resource.name"]
	}
	if r.owner.kind == "" || r.owner.name == "" {
		return report{}, false
	}
	r.image, r.digest = artifactImage(body)
	r.imageID = cmp.Or(r.digest, r.image)
	if r.imageID == "" {
		return report{}, false
	}
	if osInfo, _ := body["os"].(map[string]any); osInfo != nil {
		r.osFamily = str(osInfo["family"])
		r.os = strings.TrimSpace(r.osFamily + " " + str(osInfo["name"]))
	}
	if ts, err := time.Parse(time.RFC3339, str(body["updateTimestamp"])); err == nil {
		r.scannedAt = ts
	} else {
		r.scannedAt = u.GetCreationTimestamp().Time
	}
	return r, true
}

// parseFindings reads a report's findings, one per (CVE, package, installed
// version), worst first.
func parseFindings(u *unstructured.Unstructured) []Finding {
	body, _ := u.Object["report"].(map[string]any)
	vulns, _ := body["vulnerabilities"].([]any)
	findings := make([]Finding, 0, len(vulns))
	seen := make(map[string]bool, len(vulns))
	for _, item := range vulns {
		v, _ := item.(map[string]any)
		f := Finding{
			ID:               str(v["vulnerabilityID"]),
			Severity:         severityOf(str(v["severity"])),
			Package:          str(v["resource"]),
			InstalledVersion: str(v["installedVersion"]),
			FixedVersion:     str(v["fixedVersion"]),
			Title:            str(v["title"]),
			Link:             str(v["primaryLink"]),
			PackageType:      str(v["packageType"]),
		}
		f.ecosystem = ecosystemOf(f.PackageType, str(v["packagePURL"]))
		if f.ID == "" {
			continue
		}
		key := f.ID + "\x00" + f.ecosystem + "\x00" + f.Package + "\x00" + f.InstalledVersion
		if seen[key] {
			continue
		}
		seen[key] = true
		if score, ok := number(v["score"]); ok {
			f.Score = &score
			// Trivy takes severity from one vendor database per package type,
			// which often has none (Go modules especially) while the score
			// from another is right there.
			if f.Severity == SeverityUnknown && score > 0 {
				f.Severity, f.SeverityFromScore = cvssSeverity(score), true
			}
		}
		f.FixTarget, _ = fixTarget(f.ecosystem, f.InstalledVersion, f.FixedVersion)
		findings = append(findings, f)
	}
	slices.SortFunc(findings, compareFindings)
	return findings
}

func findingCount(u *unstructured.Unstructured) int {
	body, _ := u.Object["report"].(map[string]any)
	vulns, _ := body["vulnerabilities"].([]any)
	return len(vulns)
}

// artifactImage renders a report's scanned image as registry/repository:tag
// and returns its digest separately.
func artifactImage(body map[string]any) (image, digest string) {
	artifact, _ := body["artifact"].(map[string]any)
	digest = str(artifact["digest"])
	repo := str(artifact["repository"])
	if repo == "" {
		return "", digest
	}
	image = repo
	switch server := str(asMap(body["registry"])["server"]); server {
	case "", "index.docker.io", "docker.io", "registry-1.docker.io":
		// Docker Hub images read as people write them: nginx:1.25, not
		// index.docker.io/library/nginx:1.25.
		image = strings.TrimPrefix(repo, "library/")
	default:
		image = server + "/" + repo
	}
	// For a Pod that pins a digest, Trivy Operator records the tag as
	// "name:tag" (busybox:1.38.0); keep only the tag.
	if tag := str(artifact["tag"]); tag != "" {
		image += ":" + tag[strings.LastIndex(tag, ":")+1:]
	}
	return image, digest
}

// newer is whether a is the report to use over b for one image: the later
// scan, then the lower namespace/name, so the pick doesn't depend on list
// order.
func newer(a, b *report) bool {
	return a.scannedAt.After(b.scannedAt) || (a.scannedAt.Equal(b.scannedAt) && a.ref.Namespace+"/"+a.ref.Name < b.ref.Namespace+"/"+b.ref.Name)
}

// preferred is whether report a should describe an image over report b,
// both scans of it: the one listing more findings, else the newer. One
// build's scans list fewer only when the scanner was told to leave some
// out (an ignore policy scoped to one workload, unfixed or low findings),
// so a shorter list doesn't show that another workload's CVEs went away.
func preferred(a, b *report) bool {
	if na, nb := findingCount(a.u), findingCount(b.u); na != nb {
		return na > nb
	}
	return newer(a, b)
}

// sameImage reports whether a report scanned what a container runs. A
// container with a digest (the kubelet's once it has started, else one its
// spec pins) matches only a report of that digest, so a tag pushed again
// doesn't carry another build's scan. A started container with no registry
// digest matches nothing. Only a container that hasn't started, or a
// CronJob's template, matches by reference, with Docker Hub's implicit
// registry, library/ prefix and :latest tag filled in.
func sameImage(t *target, r *report) bool {
	if t.unresolved {
		return false
	}
	if t.digest != "" {
		return r.digest != "" && strings.EqualFold(t.digest, r.digest)
	}
	return r.image != "" && normalizeImage(t.image) == normalizeImage(r.image)
}

// canonicalImage is a Pod image reference as one identity: the pinned
// digest when there is one, else the normalized reference.
func canonicalImage(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return strings.ToLower(ref[i+1:])
	}
	return normalizeImage(ref)
}

// normalizeImage fills in Docker Hub's implicit registry, library/ prefix
// and :latest tag. Registry and repository are case-insensitive in practice;
// tags are not.
func normalizeImage(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	name, tag := ref, "latest"
	if slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":"); colon > slash {
		name, tag = ref[:colon], ref[colon+1:]
	}
	name = strings.ToLower(name)
	for _, hub := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		name = strings.TrimPrefix(name, hub)
	}
	return strings.TrimPrefix(name, "library/") + ":" + tag
}

// slotKey is one container of one workload running one image.
type slotKey struct {
	workload         WorkloadRef
	container, image string
}

type imageAgg struct {
	report    *report
	workloads map[WorkloadRef]bool
	// refs are the spellings the running Pods give the image, which a
	// search finds it by beside the report's own.
	refs map[string]bool
}

// searchText is what a search matches an image row against: every spelling
// of the image and its digest.
func (a *imageAgg) searchText() string {
	return strings.Join(append(slices.Sorted(maps.Keys(a.refs)), a.report.image, a.report.digest), "\n")
}

// unscannedAgg is a running image with no report: grouped by canonical
// image, shown by a reference as the Pods spell it. A digest-pinned image's
// canonical form is its bare digest, which names no repository.
type unscannedAgg struct {
	ref       string
	refs      map[string]bool
	workloads map[WorkloadRef]bool
	// otherBuild are the workload containers whose own scanned object has a
	// report of another build of this tag, as after the tag moved.
	otherBuild map[WorkloadRef]bool
}

// rollup is the joined state the three pivots read.
type rollup struct {
	images          map[string]*imageAgg
	targetImage     map[targetKey]string
	targets         []target
	unscanned       map[string]*unscannedAgg
	otherBuild      map[targetKey]bool
	inactiveReports int
	truncated       bool
}

// join matches reports to what runs. A report counts when its scanned object
// and container run the image it names now and, when both carry a UID, it is
// the same object: a ReplicaSet recreated under the same name is not what was
// scanned. A report about another image of the same object (a StatefulSet
// before Trivy rescanned its new image) describes nothing that runs; the
// running image stays unscanned rather than borrowing its findings. Of
// several reports about one image the preferred one wins, whole (see preferred). Findings are read
// only for the winners, at most MaxFindings in all; an image past that is
// marked unread, never shown as clean.
// join matches reports to targets and reads the chosen reports' findings
// within MaxFindings. Images run by a workload first() names are read
// first, so a narrowed view isn't starved by images it doesn't show.
func join(ctx context.Context, items []*unstructured.Unstructured, targets []target, first func(WorkloadRef) bool) (*rollup, error) {
	byOwner := map[ownerKey][]*target{}
	for i := range targets {
		o := targets[i].key.owner()
		byOwner[o] = append(byOwner[o], &targets[i])
	}
	j := &rollup{images: map[string]*imageAgg{}, targetImage: map[targetKey]string{}, targets: targets, unscanned: map[string]*unscannedAgg{}, otherBuild: map[targetKey]bool{}}
	reports := make([]report, 0, len(items))
	for _, u := range items {
		if r, ok := reportHeader(u); ok {
			reports = append(reports, r)
		}
	}
	idle := map[string]*report{}
	inactive := 0
	for i := range reports {
		r := &reports[i]
		var matched []*target
		for _, t := range byOwner[r.owner] {
			if (r.uid == "" || t.uid == "" || r.uid == t.uid) && sameImage(t, r) {
				matched = append(matched, t)
			}
		}
		// The object's other builds of this tag: Trivy Operator scans by tag,
		// so it won't scan the build these containers pulled.
		for _, t := range byOwner[r.owner] {
			if t.digest != "" && r.digest != "" && r.image != "" && !strings.EqualFold(t.digest, r.digest) && normalizeImage(t.image) == normalizeImage(r.image) {
				j.otherBuild[t.key] = true
			}
		}
		if len(matched) == 0 {
			// Its object no longer runs, but its build may: after a restart,
			// the new ReplicaSet runs the build the old one's report scanned.
			if prev := idle[r.digest]; r.digest != "" && (prev == nil || preferred(r, prev)) {
				idle[r.digest] = r
			}
			inactive++
			continue
		}
		agg := j.images[r.imageID]
		if agg == nil {
			agg = &imageAgg{report: r, workloads: map[WorkloadRef]bool{}, refs: map[string]bool{}}
			j.images[r.imageID] = agg
		} else if preferred(r, agg.report) {
			agg.report = r
		}
		for _, t := range matched {
			w := t.workload
			w.Container = t.container
			agg.workloads[w] = true
			agg.refs[t.image] = true
			j.targetImage[t.key] = r.imageID
		}
	}
	// A report whose object no longer runs still describes its build where
	// a container runs it (a restarted ReplicaSet), and competes for the
	// image like any other (see preferred).
	// Only for a container the digest's image would hold.
	runs := map[string]bool{}
	for i := range targets {
		t := &targets[i]
		if id, ok := j.targetImage[t.key]; t.digest != "" && (!ok || id == t.digest) {
			runs[t.digest] = true
		}
	}
	for d, r := range idle {
		if !runs[d] {
			continue
		}
		if agg := j.images[d]; agg == nil {
			j.images[d] = &imageAgg{report: r, workloads: map[WorkloadRef]bool{}, refs: map[string]bool{}}
		} else if preferred(r, agg.report) {
			agg.report = r
		} else {
			continue
		}
		inactive--
	}
	// A scanned build counts for every container that runs it: another
	// workload, a namespace Trivy doesn't scan, or a restarted ReplicaSet.
	for i := range targets {
		t := &targets[i]
		if _, ok := j.targetImage[t.key]; ok || t.digest == "" {
			continue
		}
		agg := j.images[t.digest]
		if agg == nil {
			continue
		}
		w := t.workload
		w.Container = t.container
		agg.workloads[w] = true
		agg.refs[t.image] = true
		j.targetImage[t.key] = t.digest
	}
	j.inactiveReports = inactive
	read := 0
	aggs := j.sortedImages()
	if first != nil {
		runs := map[*imageAgg]bool{}
		for _, agg := range aggs {
			for w := range agg.workloads {
				if first(w) {
					runs[agg] = true
					break
				}
			}
		}
		slices.SortStableFunc(aggs, func(a, b *imageAgg) int { return compareBool(!runs[a], !runs[b]) })
	}
	for _, agg := range aggs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := findingCount(agg.report.u)
		if read+n > MaxFindings {
			j.truncated = true
			agg.report.findings = nil
			agg.report.unread = true
			continue
		}
		read += n
		agg.report.findings = parsedFindings.get(agg.report.u)
	}
	// A slot is a workload's container running one build: by digest once
	// the container has started, by reference before.
	slot := func(t *target) slotKey {
		if t.digest != "" {
			return slotKey{t.workload, t.container, t.digest}
		}
		return slotKey{t.workload, t.container, "ref:" + canonicalImage(t.image)}
	}
	scannedSlot := map[slotKey]string{}
	for i := range targets {
		if id, ok := j.targetImage[targets[i].key]; ok {
			scannedSlot[slot(&targets[i])] = id
			scannedSlot[slotKey{targets[i].workload, targets[i].container, "ref:" + canonicalImage(targets[i].image)}] = id
		}
	}
	for i := range targets {
		t := &targets[i]
		if _, ok := j.targetImage[t.key]; ok {
			continue
		}
		w := t.workload
		w.Container = t.container
		// A new ReplicaSet of the same build, not yet scanned, is covered by
		// the older one's report.
		// Never for a started container whose build is unknown.
		if id, ok := scannedSlot[slot(t)]; ok && !t.unresolved {
			j.targetImage[t.key] = id
			j.images[id].refs[t.image] = true
			continue
		}
		// Listed by the image as its spec names it: builds of one tag that no
		// report describes are one thing to scan.
		u := j.unscanned[canonicalImage(t.image)]
		if u == nil {
			u = &unscannedAgg{ref: t.image, refs: map[string]bool{}, workloads: map[WorkloadRef]bool{}, otherBuild: map[WorkloadRef]bool{}}
			j.unscanned[canonicalImage(t.image)] = u
		}
		// One spelling of the image, the same whatever order Pods list in.
		u.ref = min(u.ref, t.image)
		if j.otherBuild[t.key] {
			u.otherBuild[w] = true
		}
		u.refs[t.image] = true
		u.workloads[w] = true
	}
	return j, nil
}

// build is the response for the readable reports and the containers that
// run, grouped by view and narrowed by f.
func build(ctx context.Context, items []*unstructured.Unstructured, targets []target, view View, f Filter) (Response, error) {
	var first func(WorkloadRef) bool
	if f.Workload != nil {
		first = newMatcher(f).wanted
	}
	j, err := join(ctx, items, targets, first)
	if err != nil {
		return Response{}, err
	}
	resp := Response{Source: SourceTrivyOperator, View: view, Summary: j.summary()}
	resp.WorkloadRuns = f.Workload != nil && slices.ContainsFunc(targets, func(t target) bool { return newMatcher(f).wanted(t.workload) })
	if view != ViewSummary {
		resp.Unscanned, resp.Coverage.UnscannedOmitted = capRows(j.unscannedRows(f))
	}
	switch view {
	case ViewSummary:
		// Totals and coverage only, for a page that shows counts.
	case ViewCVE:
		cves, err := j.cves(ctx, f)
		if err != nil {
			return Response{}, err
		}
		resp.CVEs, resp.Coverage.RowsOmitted = capRows(cves)
	case ViewFix:
		fixes, err := j.fixes(ctx, f)
		if err != nil {
			return Response{}, err
		}
		resp.Fixes, resp.Coverage.RowsOmitted = capRows(fixes)
	case ViewWorkload:
		rows, clean := j.workloads(f)
		resp.Workloads, resp.Coverage.RowsOmitted = capRows(rows)
		resp.CleanWorkloads, resp.Coverage.CleanOmitted = capRows(clean)
	default:
		resp.View = ViewImage
		rows, clean := j.imageRows(f)
		resp.Images, resp.Coverage.RowsOmitted = capRows(rows)
		resp.Clean, resp.Coverage.CleanOmitted = capRows(clean)
	}
	resp.Coverage.InactiveReports = j.inactiveReports
	resp.Coverage.Truncated = j.truncated
	return resp, nil
}

func capRows[T any](rows []T) ([]T, int) {
	if len(rows) <= MaxRows {
		return rows, 0
	}
	return rows[:MaxRows], len(rows) - MaxRows
}

func (j *rollup) summary() Summary {
	var s Summary
	fixableCH := map[string]bool{}
	affected := map[WorkloadRef]bool{}
	running := map[WorkloadRef]bool{}
	for _, t := range j.targets {
		running[t.workload] = true
	}
	for _, agg := range j.images {
		r := agg.report
		s.ImagesScanned++
		counts, _ := countFindings(r.findings)
		addCounts(&s.Counts, counts)
		if !r.unread && len(r.findings) == 0 {
			s.ImagesClean++
		}
		if counts.Critical > 0 {
			s.ImagesWithCritical++
		}
		for _, f := range r.findings {
			if (f.Severity == SeverityCritical || f.Severity == SeverityHigh) && f.FixedVersion != "" {
				fixableCH[f.ID] = true
			}
		}
		if counts.Critical+counts.High > 0 {
			for w := range agg.workloads {
				w.Container = ""
				affected[w] = true
			}
		}
		ts := r.scannedAt
		if s.NewestScan == nil || ts.After(*s.NewestScan) {
			s.NewestScan = &ts
		}
		if s.OldestScan == nil || ts.Before(*s.OldestScan) {
			s.OldestScan = &ts
		}
	}
	s.ImagesUnscanned = len(j.unscanned)
	s.FixableCriticalHighCVEs = len(fixableCH)
	s.WorkloadsWithCriticalOrHigh = len(affected)
	s.Workloads = len(running)
	return s
}

// matcher applies a Filter. A query that names an image or workload keeps
// that row's findings whole; one that only names a CVE or package keeps the
// findings it names.
type matcher struct {
	f        Filter
	severity map[Severity]bool
	query    string
}

func newMatcher(f Filter) matcher {
	m := matcher{f: f, query: strings.ToLower(strings.TrimSpace(f.Query))}
	if len(f.Severities) > 0 {
		m.severity = map[Severity]bool{}
		for _, s := range f.Severities {
			m.severity[s] = true
		}
	}
	return m
}

func (m matcher) narrowsFindings() bool { return m.severity != nil || m.f.FixableOnly }

func (m matcher) rowMatches(image string, workloads []WorkloadRef) bool {
	if m.query == "" || strings.Contains(strings.ToLower(image), m.query) {
		return true
	}
	for _, w := range workloads {
		if strings.Contains(strings.ToLower(w.Namespace+"/"+w.Name), m.query) {
			return true
		}
	}
	return false
}

func (m matcher) findingMatches(f Finding, rowMatched bool) bool {
	if m.severity != nil && !m.severity[f.Severity] {
		return false
	}
	if m.f.FixableOnly && f.FixedVersion == "" {
		return false
	}
	return rowMatched || strings.Contains(strings.ToLower(f.ID), m.query) || strings.Contains(strings.ToLower(f.Package), m.query)
}

func (m matcher) workloadKept(workloads []WorkloadRef) bool {
	return m.f.Workload == nil || slices.ContainsFunc(workloads, m.wanted)
}

func (m matcher) wanted(w WorkloadRef) bool {
	want := m.f.Workload
	return want != nil && strings.EqualFold(w.Kind, want.Kind) && w.Namespace == want.Namespace && w.Name == want.Name && (want.Group == "" || w.Group == want.Group)
}

// listed is the first n of a row's workloads. Narrowed to one workload, its
// own containers come first, so no cap, here or a client's, drops the one
// asked about.
func (m matcher) listed(workloads []WorkloadRef, n int) []WorkloadRef {
	if m.f.Workload != nil {
		workloads = slices.Clone(workloads)
		slices.SortStableFunc(workloads, func(a, b WorkloadRef) int {
			return compareBool(!m.wanted(a), !m.wanted(b))
		})
	}
	return workloads[:min(len(workloads), n)]
}

func (j *rollup) sortedImages() []*imageAgg {
	aggs := make([]*imageAgg, 0, len(j.images))
	for _, agg := range j.images {
		aggs = append(aggs, agg)
	}
	slices.SortFunc(aggs, func(a, b *imageAgg) int { return cmp.Compare(a.report.imageID, b.report.imageID) })
	return aggs
}

// imageRows lists the images with findings. Images scanned with no
// findings are listed apart as clean, so they don't bury the ones that need
// work, unless a search names them.
func (j *rollup) imageRows(f Filter) (rows []Image, clean []CleanImage) {
	m := newMatcher(f)
	rows = []Image{}
	clean = []CleanImage{}
	for _, agg := range j.sortedImages() {
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		rowMatched := m.rowMatches(agg.searchText(), workloads)
		if !r.unread && len(r.findings) == 0 && (m.query == "" || !rowMatched || m.narrowsFindings()) {
			if rowMatched {
				clean = append(clean, CleanImage{
					ID: r.imageID, Image: cmp.Or(r.image, r.digest), ScannedAt: r.scannedAt, Report: r.ref,
					Workloads: m.listed(workloads, MaxWorkloadsPerRow), WorkloadsTotal: len(workloads),
				})
			}
			continue
		}
		var matching []Finding
		for _, fd := range r.findings {
			if m.findingMatches(fd, rowMatched) {
				matching = append(matching, fd)
			}
		}
		// An unread image's findings are unknown, so no finding filter can
		// rule it out.
		if len(matching) == 0 && !(r.unread && rowMatched) && (m.narrowsFindings() || !rowMatched) {
			continue
		}
		counts, fixable := countFindings(r.findings)
		row := Image{
			ID: r.imageID, Image: cmp.Or(r.image, r.digest), Digest: r.digest, OS: r.os, ScannedAt: r.scannedAt,
			Counts: counts, Fixable: fixable, Matching: len(matching), FindingsUnread: r.unread,
			Workloads: m.listed(workloads, MaxWorkloadsPerRow), WorkloadsTotal: len(workloads),
			FindingsTotal: len(r.findings), Report: r.ref,
		}
		row.Findings = matching[:min(len(matching), MaxFindingsPerImage)]
		if row.Findings == nil {
			row.Findings = []Finding{}
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b Image) int {
		return cmp.Or(compareCounts(b.Counts, a.Counts), cmp.Compare(b.WorkloadsTotal, a.WorkloadsTotal), cmp.Compare(a.Image, b.Image))
	})
	slices.SortFunc(clean, func(a, b CleanImage) int { return cmp.Compare(a.Image, b.Image) })
	return rows, clean
}

type cveAgg struct {
	row      CVE
	packages map[string]bool
	// images holds each affected image, true while every package the CVE
	// is in there has a fixed version.
	images map[string]bool
}

// keepAffected keeps the MaxAffectedPerCVE most widely run images, so a CVE
// in thousands of images holds a bounded list that still shows where it
// runs most.
func (c *cveAgg) keepAffected(a CVEImage) {
	// An image's findings arrive together, so a second package of the same
	// image joins its entry.
	if n := len(c.row.Affected); n > 0 && c.row.Affected[n-1].ImageID == a.ImageID {
		c.row.Affected[n-1].Packages = append(c.row.Affected[n-1].Packages, a.Packages...)
		return
	}
	for i := range c.row.Affected {
		if c.row.Affected[i].ImageID == a.ImageID {
			c.row.Affected[i].Packages = append(c.row.Affected[i].Packages, a.Packages...)
			return
		}
	}
	if len(c.row.Affected) < MaxAffectedPerCVE {
		c.row.Affected = append(c.row.Affected, a)
		return
	}
	least := 0
	for i, x := range c.row.Affected {
		if x.WorkloadsTotal < c.row.Affected[least].WorkloadsTotal {
			least = i
		}
	}
	if a.WorkloadsTotal > c.row.Affected[least].WorkloadsTotal {
		c.row.Affected[least] = a
	}
}

// withoutContainers is a list's workloads once each, in order, without the
// container: a CVE's or fix's image counts workloads, as its row does.
func withoutContainers(workloads []WorkloadRef) []WorkloadRef {
	out := make([]WorkloadRef, 0, len(workloads))
	seen := make(map[WorkloadRef]bool, len(workloads))
	for _, w := range workloads {
		w.Container = ""
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// distinctWorkloads counts the workloads across a set of images. Rows from
// one base image share one image set, so memo counts each distinct set once
// rather than copying a widely shared image's workloads for every row.
func distinctWorkloads(ids []string, imageWorkloads map[string][]WorkloadRef, memo map[string]int) int {
	key := strings.Join(ids, "\x00")
	if n, ok := memo[key]; ok {
		return n
	}
	distinct := map[WorkloadRef]bool{}
	for _, id := range ids {
		for _, w := range imageWorkloads[id] {
			w.Container = ""
			distinct[w] = true
		}
	}
	memo[key] = len(distinct)
	return len(distinct)
}

// cves groups findings by CVE ID. A CVE's workload count is the distinct
// workloads across the images that carry it; CVEs from one base image share
// one image set, so each distinct set is counted once.
func (j *rollup) cves(ctx context.Context, f Filter) ([]CVE, error) {
	m := newMatcher(f)
	byID := map[string]*cveAgg{}
	imageWorkloads := map[string][]WorkloadRef{}
	for _, agg := range j.sortedImages() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		imageWorkloads[r.imageID] = workloads
		distinct := withoutContainers(workloads)
		listed := m.listed(distinct, MaxWorkloadsPerAffected)
		rowMatched := m.rowMatches(agg.searchText(), workloads)
		// Whether an upgrade clears each CVE here: every package it's in has
		// a fixed version, whichever findings the filters keep.
		fixedHere := map[string]bool{}
		for _, fd := range r.findings {
			fixed, seen := fixedHere[fd.ID]
			fixedHere[fd.ID] = (fixed || !seen) && fd.FixedVersion != ""
		}
		for _, fd := range r.findings {
			if !m.findingMatches(fd, rowMatched) {
				continue
			}
			c := byID[fd.ID]
			if c == nil {
				c = &cveAgg{row: CVE{ID: fd.ID, Severity: fd.Severity}, packages: map[string]bool{}, images: map[string]bool{}}
				byID[fd.ID] = c
			}
			if severityRank(fd.Severity) > severityRank(c.row.Severity) {
				c.row.Severity = fd.Severity
			}
			if fd.Score != nil && (c.row.Score == nil || *fd.Score > *c.row.Score) {
				c.row.Score = fd.Score
			}
			c.row.Title = cmp.Or(c.row.Title, fd.Title)
			c.row.Link = cmp.Or(c.row.Link, fd.Link)
			c.row.Fixable = c.row.Fixable || fd.FixedVersion != ""
			c.packages[fd.Package] = true
			c.images[r.imageID] = fixedHere[fd.ID]
			c.keepAffected(CVEImage{
				ImageID: r.imageID, Image: cmp.Or(r.image, r.digest),
				Packages:  []AffectedPackage{{Package: fd.Package, InstalledVersion: fd.InstalledVersion, FixedVersion: fd.FixedVersion, FixTarget: fd.FixTarget}},
				Workloads: listed, WorkloadsTotal: len(distinct),
			})
		}
	}
	workloadsBySet := map[string]int{}
	rows := make([]CVE, 0, len(byID))
	for _, c := range byID {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids := slices.Sorted(maps.Keys(c.images))
		for _, fixed := range c.images {
			if fixed {
				c.row.FixableImages++
			}
		}
		c.row.Workloads = distinctWorkloads(ids, imageWorkloads, workloadsBySet)
		c.row.Images = len(ids)
		c.row.Packages = sortedKeys(c.packages)
		slices.SortStableFunc(c.row.Affected, func(a, b CVEImage) int {
			return cmp.Or(cmp.Compare(b.WorkloadsTotal, a.WorkloadsTotal), cmp.Compare(a.Image, b.Image))
		})
		c.row.Affected = c.row.Affected[:min(len(c.row.Affected), MaxAffectedPerCVE)]
		rows = append(rows, c.row)
	}
	slices.SortFunc(rows, func(a, b CVE) int {
		return cmp.Or(
			cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)),
			compareBool(b.Fixable, a.Fixable),
			cmp.Compare(b.Workloads, a.Workloads),
			compareScore(b.Score, a.Score),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return rows, nil
}

// workloads lists the running workloads. Workloads whose every container is
// scanned and clean are listed apart, unless a search names them; a workload
// with an unscanned or unread container is never clean.
func (j *rollup) workloads(f Filter) (rows []Workload, clean []WorkloadRef) {
	m := newMatcher(f)
	type wAgg struct {
		row    Workload
		images map[string]bool
		seen   map[string]int
	}
	byRef := map[WorkloadRef]*wAgg{}
	var order []WorkloadRef
	for _, t := range j.targets {
		ref := t.workload
		a := byRef[ref]
		if a == nil {
			a = &wAgg{row: Workload{Kind: ref.Kind, Group: ref.Group, Namespace: ref.Namespace, Name: ref.Name}, images: map[string]bool{}, seen: map[string]int{}}
			byRef[ref] = a
			order = append(order, ref)
		}
		// One entry per container and build: a rollout, or Pods on two builds
		// of one tag, list each scanned build apart. Builds no report
		// describes are one entry, not scanned.
		id, scanned := j.targetImage[t.key]
		slot := t.container + "\x00?" + canonicalImage(t.image)
		if scanned {
			slot = t.container + "\x00" + id
		}
		if _, ok := a.seen[slot]; ok {
			continue
		}
		a.seen[slot] = len(a.row.Containers)
		a.row.Containers = append(a.row.Containers, WorkloadContainer{Name: t.container, Image: t.image})
		if !scanned {
			continue
		}
		c := &a.row.Containers[a.seen[slot]]
		r := j.images[id].report
		ts := r.scannedAt
		c.Scanned, c.ImageID, c.ScannedAt, c.FindingsUnread = true, id, &ts, r.unread
		if r.unread {
			// Kept so a filter can't rule out findings that weren't read.
			a.images[id] = true
			continue
		}
		counts, fixable := countFindings(r.findings)
		c.Counts, c.Fixable = &counts, &fixable
		if !a.images[id] {
			a.images[id] = true
			addCounts(&a.row.Counts, counts)
			addCounts(&a.row.Fixable, fixable)
		}
	}
	rows = []Workload{}
	clean = []WorkloadRef{}
	for _, ref := range order {
		a := byRef[ref]
		if !m.workloadKept([]WorkloadRef{ref}) {
			continue
		}
		images := make([]string, 0, len(a.row.Containers))
		allClean := true
		for _, c := range a.row.Containers {
			images = append(images, c.Image)
			if !c.Scanned || c.FindingsUnread || c.Counts == nil || totalCount(*c.Counts) > 0 {
				allClean = false
			}
		}
		rowMatched := m.rowMatches(strings.Join(images, " "), []WorkloadRef{ref})
		if allClean && (m.query == "" || !rowMatched || m.narrowsFindings()) {
			if rowMatched {
				clean = append(clean, ref)
			}
			continue
		}
		kept := rowMatched && !m.narrowsFindings()
		for id := range a.images {
			if kept {
				break
			}
			if j.images[id].report.unread && rowMatched {
				kept = true
				break
			}
			for _, fd := range j.images[id].report.findings {
				if m.findingMatches(fd, rowMatched) {
					kept = true
					break
				}
			}
		}
		if !kept {
			continue
		}
		slices.SortFunc(a.row.Containers, func(x, y WorkloadContainer) int {
			return cmp.Or(cmp.Compare(x.Name, y.Name), cmp.Compare(x.Image, y.Image))
		})
		rows = append(rows, a.row)
	}
	slices.SortStableFunc(rows, func(a, b Workload) int {
		return cmp.Or(compareCounts(b.Counts, a.Counts), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	slices.SortFunc(clean, func(a, b WorkloadRef) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return rows, clean
}

func totalCount(c Counts) int { return c.Critical + c.High + c.Medium + c.Low + c.Unknown }

func (j *rollup) unscannedRows(f Filter) []Unscanned {
	m := newMatcher(f)
	rows := []Unscanned{}
	for _, u := range j.unscanned {
		workloads := sortedWorkloads(u.workloads)
		// A search finds the image by any spelling a Pod gives it.
		matched := false
		for ref := range u.refs {
			matched = matched || m.rowMatches(ref, workloads)
		}
		if !m.workloadKept(workloads) || !matched {
			continue
		}
		otherBuild := slices.ContainsFunc(slices.Collect(maps.Keys(u.otherBuild)), func(w WorkloadRef) bool { return m.f.Workload == nil || m.wanted(w) })
		rows = append(rows, Unscanned{Image: u.ref, OtherBuildScanned: otherBuild, Workloads: m.listed(workloads, MaxWorkloadsPerRow), WorkloadsTotal: len(workloads)})
	}
	slices.SortFunc(rows, func(a, b Unscanned) int { return cmp.Compare(a.Image, b.Image) })
	return rows
}

func countFindings(findings []Finding) (all, fixable Counts) {
	for _, f := range findings {
		bump(&all, f.Severity)
		if f.FixedVersion != "" {
			bump(&fixable, f.Severity)
		}
	}
	return all, fixable
}

func bump(c *Counts, s Severity) {
	switch s {
	case SeverityCritical:
		c.Critical++
	case SeverityHigh:
		c.High++
	case SeverityMedium:
		c.Medium++
	case SeverityLow:
		c.Low++
	default:
		c.Unknown++
	}
}

func addCounts(dst *Counts, src Counts) {
	dst.Critical += src.Critical
	dst.High += src.High
	dst.Medium += src.Medium
	dst.Low += src.Low
	dst.Unknown += src.Unknown
}

func compareCounts(a, b Counts) int {
	return cmp.Or(cmp.Compare(a.Critical, b.Critical), cmp.Compare(a.High, b.High), cmp.Compare(a.Medium, b.Medium), cmp.Compare(a.Low, b.Low))
}

func compareFindings(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)),
		compareBool(b.FixedVersion != "", a.FixedVersion != ""),
		compareScore(b.Score, a.Score),
		cmp.Compare(a.ID, b.ID),
		cmp.Compare(a.Package, b.Package),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func compareScore(a, b *float64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return cmp.Compare(*a, *b)
}

// cvssSeverity is CVSS v3's qualitative rating for a score.
func cvssSeverity(score float64) Severity {
	switch {
	case score >= 9:
		return SeverityCritical
	case score >= 7:
		return SeverityHigh
	case score >= 4:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

func severityOf(s string) Severity {
	switch v := Severity(strings.ToLower(strings.TrimSpace(s))); v {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return v
	}
	return SeverityUnknown
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// ParseView reads a view name; empty is ViewImage.
func ParseView(s string) (View, error) {
	switch v := View(strings.ToLower(strings.TrimSpace(s))); v {
	case "", ViewImage:
		return ViewImage, nil
	case ViewCVE, ViewWorkload, ViewFix, ViewSummary:
		return v, nil
	}
	return "", fmt.Errorf("unknown view %q (want image, cve, workload, fix or summary)", s)
}

// ParseSeverities reads a comma-separated severity list.
func ParseSeverities(csv string) ([]Severity, error) {
	var out []Severity
	for _, word := range strings.Split(csv, ",") {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			continue
		}
		switch v := Severity(word); v {
		case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityUnknown:
			out = append(out, v)
		default:
			return nil, fmt.Errorf("unknown severity %q (want critical, high, medium, low or unknown)", word)
		}
	}
	return out, nil
}

func sortedWorkloads(set map[WorkloadRef]bool) []WorkloadRef {
	out := make([]WorkloadRef, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b WorkloadRef) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Container, b.Container))
	})
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}
