package vulnerabilities

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// Row statuses in an export: why a row has no findings data, or "ok". Radar
// Cloud writes the same columns and words, adding rows for clusters that
// couldn't answer.
const (
	StatusOK            = "ok"
	StatusNotScanned    = "not scanned"
	StatusPartlyScanned = "partly scanned"
	StatusNotRead       = "not read"
	StatusNoAccess      = "no access"
	StatusLoading       = "loading"
	StatusReadFailed    = "read failed"
)

var exportLead = []string{"cluster", "scanned", "status"}

// ExportColumns are each view's CSV header. Radar Cloud maps an agent's
// columns by these names, so renaming one is a breaking change there.
var ExportColumns = map[View][]string{
	ViewImage:    slices.Concat(exportLead, []string{"image", "digest", "os", "critical", "high", "medium", "low", "unrated", "with_a_fix", "running_in", "exposed_workloads"}),
	ViewFix:      slices.Concat(exportLead, []string{"package", "type", "installed", "upgrade_to", "fixed_versions", "critical", "high", "medium", "low", "unrated", "cves", "images", "workloads", "image_names", "running_in", "exposed_workloads"}),
	ViewCVE:      slices.Concat(exportLead, []string{"cve", "title", "severity", "score", "packages", "installed", "fixed_versions", "fix", "images", "workloads", "image_names", "running_in", "link", "exposed_workloads"}),
	ViewWorkload: slices.Concat(exportLead, []string{"kind", "namespace", "workload", "critical", "high", "medium", "low", "unrated", "with_a_fix", "containers", "exposed_workloads"}),
}

// exportExtras is what an export's CSV needs beyond the wire shape: scan
// metadata for every image, the images whose findings went unread (which
// the CVE and fix pivots can't show), and clean workloads with their
// containers.
type exportExtras struct {
	images         map[string]imageMeta
	unread         []unreadImage
	cleanWorkloads []Workload
	// unscannedWorkloads are the workloads a severity or fix filter left
	// out that run an image no report covers.
	unscannedWorkloads []Workload
}

type imageMeta struct {
	digest, os string
	// scanned is the oldest of the reports merged for the image: the row
	// is only as current as its stalest input.
	scanned time.Time
}

type unreadImage struct {
	id, image string
	scanned   time.Time
	workloads []WorkloadRef
	exposed   []ExposedWorkload
}

func (j *rollup) imageMetas() map[string]imageMeta {
	out := make(map[string]imageMeta, len(j.images))
	for id, agg := range j.images {
		out[id] = imageMeta{digest: agg.report.digest, os: agg.report.os, scanned: agg.oldestScan()}
	}
	return out
}

// oldestScan is the oldest of the reports merged for the image.
func (a *imageAgg) oldestScan() time.Time {
	t := a.report.scannedAt
	for _, r := range a.others {
		if r.scannedAt.Before(t) {
			t = r.scannedAt
		}
	}
	return t
}

// unreadRows are the images past the findings budget in f's workload scope.
// Neither a search nor a severity or fix filter rules them out: their
// findings, and so whether a CVE or package search would match them, are
// unknown, and a file without them would look complete.
func (j *rollup) unreadRows(f Filter) []unreadImage {
	m := j.match(f)
	var rows []unreadImage
	for _, agg := range j.sortedImages() {
		if !agg.report.unread {
			continue
		}
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		if !j.charge(len(workloads)) {
			return rows
		}
		exposed, _, ok := j.exposedWorkloads(workloads, j.lim.workloadsPerRow)
		if !ok {
			return rows
		}
		rows = append(rows, unreadImage{id: agg.report.imageID, image: cmp.Or(agg.report.image, agg.report.digest), scanned: agg.oldestScan(), workloads: workloads, exposed: exposed})
	}
	return rows
}

// maxExportRefs bounds the workload references an export's rows keep while
// it is built, before any of the file is written: an export lists every
// workload of every row, and rows that share images still keep a list each.
// Four million references (about 35 bytes each once written) is a file near
// maxExportBytes. A var so tests can lower it.
var maxExportRefs = 4_000_000

// charge counts n workload references a row is about to keep. It is false
// once an export has passed maxExportRefs, or the request is cancelled or
// timed out: the caller stops building, and build answers ErrExportTooLarge
// or the request's error (see stopped). A list read keeps capped lists and
// isn't counted. Any row part that keeps workload references charges here.
func (j *rollup) charge(n int) bool {
	if j.ctx.Err() != nil {
		return false
	}
	if !j.budgeted {
		return true
	}
	j.refsLeft -= n
	return j.refsLeft >= 0
}

// stopped is why charge refused: the request's end, or the budget.
func (j *rollup) stopped() error {
	return cmp.Or(j.ctx.Err(), ErrExportTooLarge)
}

// maxExportBytes bounds one export's file. Radar builds it in memory before
// sending, and Radar Cloud takes at most 32 MB from a cluster, so a larger
// file would only cost memory. A var so tests can lower it.
var maxExportBytes = 128 << 20

// ErrExportTooLarge: the file would pass maxExportBytes.
var ErrExportTooLarge = errors.New("this export is larger than 128 MB")

// cappedBuffer refuses to grow past max. The buffer is a named field, not
// embedded: bufio.Writer hands a large field to its destination's
// WriteString when it has one, so every write path must be capped here.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		return 0, ErrExportTooLarge
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) WriteString(s string) (int, error) {
	if b.buf.Len()+len(s) > b.max {
		return 0, ErrExportTooLarge
	}
	return b.buf.WriteString(s)
}

// left is how many more bytes the buffer takes.
func (b *cappedBuffer) left() int { return b.max - b.buf.Len() }

// WriteCSV renders an export read (Request.Export) as RFC 4180 CSV with CRLF
// line ends, one row per item, then the rows that say why data is missing:
// running images not scanned yet, images whose findings weren't read, and
// coverage gaps.
func WriteCSV(ctx context.Context, resp Response, f Filter, cluster string) ([]byte, error) {
	cols, ok := ExportColumns[resp.View]
	if !ok || resp.export == nil {
		return nil, fmt.Errorf("no CSV export for view %q", resp.View)
	}
	x := resp.export
	buf := &cappedBuffer{max: maxExportBytes}
	w := csv.NewWriter(buf)
	w.UseCRLF = true
	if err := w.Write(cols); err != nil {
		return nil, err
	}
	// put writes one row. It stops the export when the request is gone or
	// the file passes maxExportBytes (the writer's buffer reports that as
	// soon as it spills), so nothing is built past the cap.
	// join builds a multi-value cell, unless the cell alone would pass what
	// the file has left: then it builds nothing and the next put fails.
	var tooLarge error
	join := func(values []string) string {
		n := 0
		for _, v := range values {
			n += len(v) + len("; ")
		}
		if n > buf.left() {
			tooLarge = ErrExportTooLarge
			return ""
		}
		return joinCells(values)
	}
	// exposedCell builds the exposed_workloads cell within what the file has
	// left; past it, or once the request is gone, it builds nothing and the
	// next put fails.
	exposedCell := func(exposed []ExposedWorkload, workloads []WorkloadRef) string {
		cell, err := exposureCell(ctx, exposed, workloads, resp.Exposure, buf.left())
		if err != nil && tooLarge == nil {
			tooLarge = err
		}
		return cell
	}
	put := func(scanned time.Time, status string, cells ...string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if tooLarge != nil {
			return tooLarge
		}
		row := append([]string{cluster, timeCell(scanned), status}, cells...)
		for i := range row {
			row[i] = safeCell(row[i])
		}
		return w.Write(row)
	}
	blank := func(n int) []string { return make([]string, n) }
	// Clean rows follow the screen's clean fold: hidden under a severity or
	// fix filter.
	unfiltered := len(f.Severities) == 0 && !f.FixableOnly

	switch resp.View {
	case ViewImage:
		for _, img := range resp.Images {
			meta := x.images[img.ID]
			counts := countCells(img.Counts, img.Fixable)
			status := StatusOK
			if img.FindingsUnread {
				status, counts = StatusNotRead, blank(6)
			}
			if err := put(meta.scanned, status, slices.Concat([]string{img.Image, meta.digest, meta.os}, counts, []string{join(workloadCells(img.Workloads, true)), exposedCell(img.Exposed, img.Workloads)})...); err != nil {
				return nil, err
			}
		}
		if unfiltered {
			for _, c := range resp.Clean {
				meta := x.images[c.ID]
				if err := put(meta.scanned, StatusOK, slices.Concat([]string{c.Image, meta.digest, meta.os}, countCells(Counts{}, Counts{}), []string{join(workloadCells(c.Workloads, true)), exposedCell(c.Exposed, c.Workloads)})...); err != nil {
					return nil, err
				}
			}
		}
		for _, u := range resp.Unscanned {
			if err := put(time.Time{}, StatusNotScanned, slices.Concat([]string{u.Image}, blank(8), []string{join(workloadCells(u.Workloads, true)), exposedCell(u.Exposed, u.Workloads)})...); err != nil {
				return nil, err
			}
		}
		// Images past the findings budget that a search left out of the rows.
		listed := map[string]bool{}
		for _, img := range resp.Images {
			listed[img.ID] = true
		}
		for _, u := range x.unread {
			if listed[u.id] {
				continue
			}
			meta := x.images[u.id]
			if err := put(meta.scanned, StatusNotRead, slices.Concat([]string{u.image, meta.digest, meta.os}, blank(6), []string{join(workloadCells(u.workloads, true)), exposedCell(u.exposed, u.workloads)})...); err != nil {
				return nil, err
			}
		}
	case ViewFix:
		for _, fx := range resp.Fixes {
			var images []string
			var workloads []WorkloadRef
			var scanned time.Time
			for _, a := range fx.Affected {
				images = append(images, a.Image)
				workloads = append(workloads, a.Workloads...)
				scanned = oldest(scanned, x.images[a.ImageID].scanned)
			}
			if err := put(scanned, StatusOK, slices.Concat(
				[]string{fx.Package, ecosystemLabel(fx.PackageType), join(fx.From), fx.To, join(fx.FixedVersions)},
				countCells(fx.Counts, Counts{})[:5],
				[]string{join(fx.CVEs), strconv.Itoa(fx.Images), strconv.Itoa(fx.Workloads), join(images), join(distinctWorkloadCells(workloads)), exposedCell(fx.Exposed, workloads)},
			)...); err != nil {
				return nil, err
			}
		}
		if err := writeImageGaps(put, join, exposedCell, cols, resp.Unscanned, x.unread); err != nil {
			return nil, err
		}
	case ViewCVE:
		for _, c := range resp.CVEs {
			var images, installed, fixed []string
			var workloads []WorkloadRef
			var scanned time.Time
			for _, a := range c.Affected {
				images = append(images, a.Image)
				workloads = append(workloads, a.Workloads...)
				scanned = oldest(scanned, x.images[a.ImageID].scanned)
				for _, p := range a.Packages {
					installed = append(installed, p.Package+" "+p.InstalledVersion)
					if p.FixedVersion != "" {
						fixed = append(fixed, p.Package+" "+p.FixedVersion)
					}
				}
			}
			score := ""
			if c.Score != nil {
				score = strconv.FormatFloat(*c.Score, 'f', 1, 64)
			}
			if err := put(scanned, StatusOK,
				c.ID, c.Title, severityLabel(c.Severity), score, join(c.Packages), join(distinct(installed)), join(distinct(fixed)), cveFix(c),
				strconv.Itoa(c.Images), strconv.Itoa(c.Workloads), join(images), join(distinctWorkloadCells(workloads)), c.Link, exposedCell(c.Exposed, workloads)); err != nil {
				return nil, err
			}
		}
		if err := writeImageGaps(put, join, exposedCell, cols, resp.Unscanned, x.unread); err != nil {
			return nil, err
		}
	case ViewWorkload:
		// A filter rules out what a workload's scanned images carry, never
		// what its unscanned ones might: those workloads stay, as not-scanned
		// images do in the other views.
		rows := slices.Concat(resp.Workloads, x.unscannedWorkloads)
		if unfiltered {
			rows = slices.Concat(rows, x.cleanWorkloads)
		}
		for _, wl := range rows {
			status, scanned, containers := workloadState(wl, x.images)
			counts := countCells(wl.Counts, wl.Fixable)
			if status == StatusNotScanned || status == StatusNotRead {
				counts = blank(6)
			}
			ref := WorkloadRef{Kind: wl.Kind, Group: wl.Group, Namespace: wl.Namespace, Name: wl.Name}
			var exposed []ExposedWorkload
			if len(wl.Routes) > 0 {
				exposed = []ExposedWorkload{{WorkloadRef: ref, Routes: wl.Routes}}
			}
			if err := put(scanned, status, slices.Concat([]string{wl.Kind, wl.Namespace, wl.Name}, counts, []string{join(containers), exposedCell(exposed, []WorkloadRef{ref})})...); err != nil {
				return nil, err
			}
		}
	}

	// A gap's namespaces go where the view says where things run.
	where := "running_in"
	if resp.View == ViewWorkload {
		where = "namespace"
	}
	gaps := resp.Coverage.Gaps
	// Under the Exposed filter, rows Radar couldn't tell were exposed are
	// missing from the file, so where it couldn't read routes is a gap too.
	if f.ExposedOnly && resp.Exposure != nil {
		gaps = slices.Concat(gaps, resp.Exposure.Gaps)
	}
	for _, g := range gaps {
		if err := put(time.Time{}, gapStatus(g.Code), named(cols, map[string]string{where: gapNamespaces(g.Namespaces)})...); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.buf.Bytes(), nil
}

// writeImageGaps writes the not-scanned and not-read images in the fix and
// CVE views: the image in image_names, its workloads in running_in, the rest
// blank.
func writeImageGaps(put func(time.Time, string, ...string) error, join func([]string) string, exposedCell func([]ExposedWorkload, []WorkloadRef) string, cols []string, unscanned []Unscanned, unread []unreadImage) error {
	row := func(image string, workloads []WorkloadRef, exposed []ExposedWorkload) []string {
		return named(cols, map[string]string{
			"image_names": image, "running_in": join(distinctWorkloadCells(workloads)), "exposed_workloads": exposedCell(exposed, workloads),
		})
	}
	for _, u := range unscanned {
		if err := put(time.Time{}, StatusNotScanned, row(u.Image, u.Workloads, u.Exposed)...); err != nil {
			return err
		}
	}
	for _, u := range unread {
		if err := put(u.scanned, StatusNotRead, row(u.image, u.workloads, u.exposed)...); err != nil {
			return err
		}
	}
	return nil
}

// exposureCell names a row's exposed workloads with their routes, as
// "Kind namespace/name (Via namespace/name, ...)" joined by "; ". With none
// seen it is "unknown" where one of the row's workloads sits in a namespace
// Radar couldn't read routes in, else empty: no route seen is not proof
// there is none, so the file never says "none". A cell longer than left
// bytes, what the file has left, is refused before it is built, and a
// request that is gone stops the measuring.
func exposureCell(ctx context.Context, exposed []ExposedWorkload, workloads []WorkloadRef, exposure *Exposure, left int) (string, error) {
	if len(exposed) == 0 {
		if exposure == nil || slices.ContainsFunc(workloads, func(w WorkloadRef) bool { return exposureUnknownIn(exposure, w.Namespace) }) {
			return "unknown", nil
		}
		return "", nil
	}
	// Measure first, so a cell past what the file has left is refused
	// before any of it is built; then build it in one allocation.
	n := 0
	for i, e := range exposed {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if i > 0 {
			n += len("; ")
		}
		n += len(e.Kind) + len(" ") + len(e.Namespace) + len("/") + len(e.Name) + len(" ()")
		for k, r := range e.Routes {
			if k > 0 {
				n += len(", ")
			}
			n += len(r.Via) + len(" ") + len(r.Namespace) + len("/") + len(r.Name)
		}
		if n > left {
			return "", ErrExportTooLarge
		}
	}
	var b strings.Builder
	b.Grow(n)
	for i, e := range exposed {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(e.Kind + " " + e.Namespace + "/" + e.Name + " (")
		for k, r := range e.Routes {
			if k > 0 {
				b.WriteString(", ")
			}
			b.WriteString(r.Via)
			b.WriteString(" ")
			b.WriteString(r.Namespace)
			b.WriteString("/")
			b.WriteString(r.Name)
		}
		b.WriteString(")")
	}
	return b.String(), nil
}

// exposureUnknownIn is whether an exposure gap covers a namespace.
func exposureUnknownIn(exposure *Exposure, namespace string) bool {
	return slices.ContainsFunc(exposure.Gaps, func(g resourceapi.Gap) bool {
		return g.Namespaces == nil || g.Namespaces.Extent != "" || len(g.Namespaces.Names) < g.Namespaces.Count || slices.Contains(g.Namespaces.Names, namespace)
	})
}

// named is a row's cells after the lead columns, blank except those given.
func named(cols []string, values map[string]string) []string {
	cells := make([]string, len(cols)-len(exportLead))
	for i, c := range cols[len(exportLead):] {
		cells[i] = values[c]
	}
	return cells
}

// workloadState is a workload row's status, oldest scan and container cells.
// Its counts cover only the containers whose findings were read.
func workloadState(wl Workload, images map[string]imageMeta) (status string, scanned time.Time, containers []string) {
	read, unscanned, unread := 0, 0, 0
	for _, c := range wl.Containers {
		cell := c.Name + " " + c.Image
		switch {
		case !c.Scanned:
			unscanned++
			cell += " [not scanned]"
		case c.FindingsUnread:
			unread++
			cell += " [not read]"
		default:
			read++
		}
		if c.Scanned {
			scanned = oldest(scanned, images[c.ImageID].scanned)
		}
		containers = append(containers, cell)
	}
	switch {
	case read == len(wl.Containers):
		status = StatusOK
	case read > 0:
		status = StatusPartlyScanned
	case unread > 0:
		status = StatusNotRead
	default:
		status = StatusNotScanned
	}
	return status, scanned, containers
}

// countCells are the critical..unrated counts and the fixable total.
func countCells(c, fixable Counts) []string {
	return []string{strconv.Itoa(c.Critical), strconv.Itoa(c.High), strconv.Itoa(c.Medium), strconv.Itoa(c.Low), strconv.Itoa(c.Unknown), strconv.Itoa(totalCount(fixable))}
}

// ecosystems are the words the screen uses for a scanner's package type;
// the table mirrors ECOSYSTEMS in web/src/components/vulnerabilities/parts.tsx
// (TestEcosystemLabelsMatchTheScreen keeps them equal).
var ecosystems = map[string]string{
	"deb": "OS package (deb)", "apk": "OS package (apk)", "rpm": "OS package (rpm)",
	"pypi": "Python package", "gem": "Ruby gem", "cocoapods": "CocoaPods pod", "maven": "Java library", "golang": "Go module",
	"cargo": "Rust crate", "pub": "Dart package", "hex": "Elixir package", "swift": "Swift package", "rustbinary": "Rust crate, compiled in",
	"debian": "Debian package", "ubuntu": "Ubuntu package", "alpine": "Alpine package", "wolfi": "Wolfi package", "chainguard": "Chainguard package",
	"redhat": "RPM package", "centos": "RPM package", "rocky": "RPM package", "alma": "RPM package", "amazon": "RPM package", "oracle": "RPM package",
	"fedora": "RPM package", "suse": "RPM package", "cbl-mariner": "RPM package", "azurelinux": "RPM package", "photon": "RPM package",
	"gobinary": "Go module, compiled in", "gomod": "Go module", "npm": "npm package", "node-pkg": "npm package", "yarn": "npm package", "pnpm": "npm package", "bun": "npm package",
	"python-pkg": "Python package", "pip": "Python package", "pipenv": "Python package", "poetry": "Python package", "uv": "Python package",
	"jar": "Java library", "pom": "Java library", "gradle": "Java library", "sbt": "Java library",
	"gemspec": "Ruby gem", "bundler": "Ruby gem", "rust-binary": "Rust crate, compiled in",
	"composer": "PHP package", "nuget": ".NET package", "dotnet-core": ".NET package",
}

// ecosystemLabel is a package type as the screen shows it, or the scanner's
// own word for a type the screen doesn't name.
func ecosystemLabel(packageType string) string {
	if label, ok := ecosystems[strings.ToLower(packageType)]; ok {
		return label
	}
	return packageType
}

// cveFix is the Fix column: whether an upgrade clears the CVE in every
// affected image, some, or none.
func cveFix(c CVE) string {
	switch {
	case c.FixableImages == 0:
		return "No"
	case c.FixableImages >= c.Images:
		return "Yes"
	default:
		return fmt.Sprintf("%d of %d", c.FixableImages, c.Images)
	}
}

func severityLabel(s Severity) string {
	switch s {
	case SeverityCritical:
		return "Critical"
	case SeverityHigh:
		return "High"
	case SeverityMedium:
		return "Medium"
	case SeverityLow:
		return "Low"
	default:
		return "Unrated"
	}
}

// gapStatus says why a coverage gap's data is missing, in the words Cloud
// uses for a whole cluster.
func gapStatus(code readreason.Code) string {
	switch code {
	case readreason.UserForbidden, readreason.UserNoNamespaceAccess, readreason.UserNamespaceNotVisible, readreason.OutsideNamespacePin,
		readreason.RadarForbidden, readreason.RadarCredentialsRejected, readreason.RadarNamespaceNotCollected, readreason.RadarKindNotCollected:
		return StatusNoAccess
	case readreason.KindSyncPending, readreason.ClusterConnecting:
		return StatusLoading
	default:
		return StatusReadFailed
	}
}

// gapNamespaces names a gap's namespaces: "*" for all of them, "unknown"
// when Radar can't say which.
func gapNamespaces(s *resourceapi.NameSet) string {
	switch {
	case s == nil || s.Extent == resourceapi.ExtentAll:
		return "*"
	case s.Extent == resourceapi.ExtentUnknown || len(s.Names) < s.Count:
		return "unknown"
	default:
		return joinCells(s.Names)
	}
}

func workloadCells(ws []WorkloadRef, withContainer bool) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		cell := w.Kind + " " + w.Namespace + "/" + w.Name
		if withContainer && w.Container != "" {
			cell += " (" + w.Container + ")"
		}
		out = append(out, cell)
	}
	return out
}

// distinctWorkloadCells names each workload once, without its container.
func distinctWorkloadCells(ws []WorkloadRef) []string {
	return distinct(workloadCells(ws, false))
}

// distinct keeps the first of each value, in order.
func distinct(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func joinCells(values []string) string { return strings.Join(values, "; ") }

func oldest(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func timeCell(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// safeCell keeps a cell from running as a spreadsheet formula. Excel and
// Sheets evaluate a cell opening with one of these even when it is quoted;
// image, package and workload names come from the cluster, so a leading
// apostrophe (which spreadsheets drop on import) makes them plain text.
// A bare carriage return becomes a line break: the CSV writer would
// otherwise drop it from the cell.
func safeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		v = "'" + v
	}
	return strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n")
}
