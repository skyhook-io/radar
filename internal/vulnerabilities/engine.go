package vulnerabilities

import (
	"cmp"
	"context"
	"fmt"
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
	scannedAt time.Time
	findings  []Finding
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
		r.os = strings.TrimSpace(str(osInfo["family"]) + " " + str(osInfo["name"]))
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
		}
		if f.ID == "" {
			continue
		}
		key := f.ID + "\x00" + f.Package + "\x00" + f.InstalledVersion
		if seen[key] {
			continue
		}
		seen[key] = true
		if score, ok := number(v["score"]); ok {
			f.Score = &score
		}
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
	if registry, _ := body["registry"].(map[string]any); str(registry["server"]) != "" {
		image = str(registry["server"]) + "/" + repo
	}
	if tag := str(artifact["tag"]); tag != "" {
		image += ":" + tag
	}
	return image, digest
}

// sameImage reports whether a Pod's image reference names the image a report
// scanned: by digest when the Pod pins one, else by reference with Docker
// Hub's implicit registry, library/ prefix and :latest tag filled in.
func sameImage(podImage string, r *report) bool {
	if i := strings.Index(podImage, "@"); i >= 0 {
		if r.digest != "" {
			return strings.EqualFold(podImage[i+1:], r.digest)
		}
		podImage = podImage[:i]
	}
	return r.image != "" && normalizeImage(podImage) == normalizeImage(r.image)
}

func normalizeImage(ref string) string {
	ref = strings.ToLower(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if slash := strings.LastIndex(ref, "/"); !strings.Contains(ref[slash+1:], ":") {
		ref += ":latest"
	}
	for _, hub := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, hub)
	}
	return strings.TrimPrefix(ref, "library/")
}

type imageAgg struct {
	report    *report
	workloads map[WorkloadRef]bool
}

// rollup is the joined state the three pivots read.
type rollup struct {
	images          map[string]*imageAgg
	targetImage     map[targetKey]string
	targets         []target
	unscanned       map[string]map[WorkloadRef]bool
	inactiveReports int
	truncated       bool
}

// join matches reports to what runs. A report counts when its scanned object
// and container run that image now and, when both carry a UID, it is the
// same object: a ReplicaSet recreated under the same name is not what was
// scanned. When the object runs one image the report is about it even if
// the references are written differently; when it runs two (a StatefulSet
// mid-rollout) the report is about the one its image names, and the other
// stays unscanned. Of several reports about one image the newest wins, whole.
// Findings are read only for the winners, at most MaxFindings in all.
func join(ctx context.Context, items []*unstructured.Unstructured, targets []target) (*rollup, error) {
	byOwner := map[ownerKey][]*target{}
	for i := range targets {
		o := targets[i].key.owner()
		byOwner[o] = append(byOwner[o], &targets[i])
	}
	j := &rollup{images: map[string]*imageAgg{}, targetImage: map[targetKey]string{}, targets: targets, unscanned: map[string]map[WorkloadRef]bool{}}
	reports := make([]report, 0, len(items))
	for _, u := range items {
		if r, ok := reportHeader(u); ok {
			reports = append(reports, r)
		}
	}
	for i := range reports {
		r := &reports[i]
		var candidates []*target
		images := map[string]bool{}
		for _, t := range byOwner[r.owner] {
			if r.uid == "" || t.uid == "" || r.uid == t.uid {
				candidates = append(candidates, t)
				images[t.image] = true
			}
		}
		var matched []*target
		for _, t := range candidates {
			if sameImage(t.image, r) {
				matched = append(matched, t)
			}
		}
		if len(matched) == 0 && len(images) == 1 {
			matched = candidates
		}
		if len(matched) == 0 {
			j.inactiveReports++
			continue
		}
		agg := j.images[r.imageID]
		if agg == nil {
			agg = &imageAgg{report: r, workloads: map[WorkloadRef]bool{}}
			j.images[r.imageID] = agg
		} else if r.scannedAt.After(agg.report.scannedAt) || (r.scannedAt.Equal(agg.report.scannedAt) && r.ref.Namespace+"/"+r.ref.Name < agg.report.ref.Namespace+"/"+agg.report.ref.Name) {
			agg.report = r
		}
		for _, t := range matched {
			w := t.workload
			w.Container = t.container
			agg.workloads[w] = true
			j.targetImage[t.key] = r.imageID
		}
	}
	read := 0
	for _, agg := range j.sortedImages() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := findingCount(agg.report.u)
		if read+n > MaxFindings {
			j.truncated = true
			agg.report.findings = nil
			continue
		}
		read += n
		agg.report.findings = parseFindings(agg.report.u)
	}
	for _, t := range targets {
		if _, ok := j.targetImage[t.key]; ok {
			continue
		}
		w := t.workload
		w.Container = t.container
		if j.unscanned[t.image] == nil {
			j.unscanned[t.image] = map[WorkloadRef]bool{}
		}
		j.unscanned[t.image][w] = true
	}
	return j, nil
}

// build is the response for the readable reports and the containers that
// run, grouped by view and narrowed by f.
func build(ctx context.Context, items []*unstructured.Unstructured, targets []target, view View, f Filter) (Response, error) {
	j, err := join(ctx, items, targets)
	if err != nil {
		return Response{}, err
	}
	resp := Response{Source: SourceTrivyOperator, View: view, Summary: j.summary(), Unscanned: j.unscannedRows(f)}
	switch view {
	case ViewCVE:
		resp.CVEs = j.cves(f)
		resp.CVEs, resp.Coverage.RowsOmitted = capRows(resp.CVEs)
	case ViewWorkload:
		resp.Workloads = j.workloads(f)
		resp.Workloads, resp.Coverage.RowsOmitted = capRows(resp.Workloads)
	default:
		resp.View = ViewImage
		resp.Images = j.imageRows(f)
		resp.Images, resp.Coverage.RowsOmitted = capRows(resp.Images)
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
	s.WorkloadsAffected = len(affected)
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
	if m.f.Workload == nil {
		return true
	}
	want := *m.f.Workload
	for _, w := range workloads {
		if w.Kind == want.Kind && w.Namespace == want.Namespace && w.Name == want.Name && (want.Group == "" || w.Group == want.Group) {
			return true
		}
	}
	return false
}

func (j *rollup) sortedImages() []*imageAgg {
	aggs := make([]*imageAgg, 0, len(j.images))
	for _, agg := range j.images {
		aggs = append(aggs, agg)
	}
	slices.SortFunc(aggs, func(a, b *imageAgg) int { return cmp.Compare(a.report.imageID, b.report.imageID) })
	return aggs
}

func (j *rollup) imageRows(f Filter) []Image {
	m := newMatcher(f)
	rows := []Image{}
	for _, agg := range j.sortedImages() {
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		rowMatched := m.rowMatches(r.image, workloads)
		var matching []Finding
		for _, fd := range r.findings {
			if m.findingMatches(fd, rowMatched) {
				matching = append(matching, fd)
			}
		}
		if len(matching) == 0 && (m.narrowsFindings() || !rowMatched) {
			continue
		}
		counts, fixable := countFindings(r.findings)
		row := Image{
			ID: r.imageID, Image: cmp.Or(r.image, r.digest), Digest: r.digest, OS: r.os, ScannedAt: r.scannedAt,
			Counts: counts, Fixable: fixable, Matching: len(matching), Workloads: workloads,
			FindingsTotal: len(r.findings), Report: r.ref,
		}
		row.Findings = matching[:min(len(matching), MaxFindingsPerImage)]
		if row.Findings == nil {
			row.Findings = []Finding{}
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b Image) int {
		return cmp.Or(compareCounts(b.Counts, a.Counts), cmp.Compare(len(b.Workloads), len(a.Workloads)), cmp.Compare(a.Image, b.Image))
	})
	return rows
}

type cveAgg struct {
	row       CVE
	packages  map[string]bool
	workloads map[WorkloadRef]bool
}

func (j *rollup) cves(f Filter) []CVE {
	m := newMatcher(f)
	byID := map[string]*cveAgg{}
	for _, agg := range j.sortedImages() {
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		rowMatched := m.rowMatches(r.image, workloads)
		for _, fd := range r.findings {
			if !m.findingMatches(fd, rowMatched) {
				continue
			}
			c := byID[fd.ID]
			if c == nil {
				c = &cveAgg{row: CVE{ID: fd.ID, Severity: fd.Severity}, packages: map[string]bool{}, workloads: map[WorkloadRef]bool{}}
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
			for _, w := range workloads {
				w.Container = ""
				c.workloads[w] = true
			}
			c.row.Images++
			c.row.Affected = append(c.row.Affected, CVEImage{
				ImageID: r.imageID, Image: cmp.Or(r.image, r.digest), Package: fd.Package,
				InstalledVersion: fd.InstalledVersion, FixedVersion: fd.FixedVersion, Workloads: workloads,
			})
		}
	}
	rows := make([]CVE, 0, len(byID))
	for _, c := range byID {
		c.row.Packages = sortedKeys(c.packages)
		c.row.Workloads = len(c.workloads)
		slices.SortStableFunc(c.row.Affected, func(a, b CVEImage) int {
			return cmp.Or(cmp.Compare(len(b.Workloads), len(a.Workloads)), cmp.Compare(a.Image, b.Image))
		})
		// One image can carry a CVE in several packages; count it once.
		images := map[string]bool{}
		for _, a := range c.row.Affected {
			images[a.ImageID] = true
		}
		c.row.Images = len(images)
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
	return rows
}

func (j *rollup) workloads(f Filter) []Workload {
	m := newMatcher(f)
	type wAgg struct {
		row    Workload
		images map[string]bool
		seen   map[string]bool
	}
	byRef := map[WorkloadRef]*wAgg{}
	var order []WorkloadRef
	for _, t := range j.targets {
		ref := t.workload
		a := byRef[ref]
		if a == nil {
			a = &wAgg{row: Workload{Kind: ref.Kind, Group: ref.Group, Namespace: ref.Namespace, Name: ref.Name}, images: map[string]bool{}, seen: map[string]bool{}}
			byRef[ref] = a
			order = append(order, ref)
		}
		// A rollout can run two images in one container slot; list both.
		slot := t.container + "\x00" + t.image
		if a.seen[slot] {
			continue
		}
		a.seen[slot] = true
		c := WorkloadContainer{Name: t.container, Image: t.image}
		if id, ok := j.targetImage[t.key]; ok {
			r := j.images[id].report
			counts, fixable := countFindings(r.findings)
			ts := r.scannedAt
			c.Scanned, c.ImageID, c.ScannedAt, c.Counts, c.Fixable = true, id, &ts, &counts, &fixable
			if !a.images[id] {
				a.images[id] = true
				addCounts(&a.row.Counts, counts)
				addCounts(&a.row.Fixable, fixable)
			}
		}
		a.row.Containers = append(a.row.Containers, c)
	}
	rows := []Workload{}
	for _, ref := range order {
		a := byRef[ref]
		if !m.workloadKept([]WorkloadRef{ref}) {
			continue
		}
		images := make([]string, 0, len(a.row.Containers))
		for _, c := range a.row.Containers {
			images = append(images, c.Image)
		}
		rowMatched := m.rowMatches(strings.Join(images, " "), []WorkloadRef{ref})
		kept := rowMatched && !m.narrowsFindings()
		for id := range a.images {
			if kept {
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
	return rows
}

func (j *rollup) unscannedRows(f Filter) []Unscanned {
	m := newMatcher(f)
	rows := []Unscanned{}
	for image, set := range j.unscanned {
		workloads := sortedWorkloads(set)
		if !m.workloadKept(workloads) || !m.rowMatches(image, workloads) {
			continue
		}
		rows = append(rows, Unscanned{Image: image, Workloads: workloads})
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
	case ViewCVE, ViewWorkload:
		return v, nil
	}
	return "", fmt.Errorf("unknown view %q (want image, cve or workload)", s)
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
