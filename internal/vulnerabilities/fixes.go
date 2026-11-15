package vulnerabilities

import (
	"cmp"
	"context"
	"slices"
	"strings"
)

// imagePackage is one package as installed in one image, with the fixable
// findings that name it.
type imagePackage struct {
	pkg, pkgType, eco, installed string
	findings                     []Finding
}

type fixAgg struct {
	row       Fix
	from      map[string]bool
	listed    map[string]bool
	cves      map[string]Severity
	images    map[string]bool
	workloads map[WorkloadRef]bool
}

// fixes groups fixable findings by the upgrade that clears them. Within one
// image, every fixable finding of a package at one installed version is one
// upgrade, to the highest version any of them needs, so the row says what
// clears them all. Images whose package needs the same upgrade share a row.
// When a version can't be ordered (unknown ecosystem, unparseable version),
// the row carries the scanner's fixed versions instead of a target.
func (j *rollup) fixes(ctx context.Context, f Filter) ([]Fix, error) {
	m := newMatcher(f)
	byKey := map[string]*fixAgg{}
	// Images built on one base share package states: each is worked out once.
	type upgrade struct {
		to     string
		listed []string
	}
	upgrades := map[string]upgrade{}
	for _, agg := range j.sortedImages() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) {
			continue
		}
		rowMatched := m.rowMatches(r.image, workloads)
		packages := map[string]*imagePackage{}
		var order []string
		for _, fd := range r.findings {
			if fd.FixedVersion == "" || !m.findingMatches(fd, rowMatched) {
				continue
			}
			key := fd.ecosystem + "\x00" + fd.Package + "\x00" + fd.InstalledVersion
			p := packages[key]
			if p == nil {
				p = &imagePackage{pkg: fd.Package, pkgType: cmp.Or(fd.PackageType, fd.ecosystem), eco: fd.ecosystem, installed: fd.InstalledVersion}
				packages[key] = p
				order = append(order, key)
			}
			p.findings = append(p.findings, fd)
		}
		for _, key := range order {
			p := packages[key]
			state := upgradeState(p)
			u, ok := upgrades[state]
			if !ok {
				u.to, u.listed = upgradeFor(p)
				upgrades[state] = u
			}
			to, listed := u.to, u.listed
			rowKey := p.eco + "\x00" + p.pkg + "\x00" + to
			if to == "" {
				rowKey = p.eco + "\x00" + p.pkg + "\x00?\x00" + strings.Join(listed, "|")
			}
			a := byKey[rowKey]
			if a == nil {
				a = &fixAgg{
					row:  Fix{Package: p.pkg, PackageType: p.pkgType, To: to},
					from: map[string]bool{}, listed: map[string]bool{}, cves: map[string]Severity{},
					images: map[string]bool{}, workloads: map[WorkloadRef]bool{},
				}
				byKey[rowKey] = a
			}
			a.from[p.installed] = true
			for _, l := range listed {
				a.listed[l] = true
			}
			for _, fd := range p.findings {
				if prev, seen := a.cves[fd.ID]; !seen || severityRank(fd.Severity) > severityRank(prev) {
					a.cves[fd.ID] = fd.Severity
				}
			}
			if !a.images[r.imageID] {
				a.images[r.imageID] = true
				for _, w := range workloads {
					w.Container = ""
					a.workloads[w] = true
				}
				a.row.Affected = append(a.row.Affected, FixImage{
					ImageID: r.imageID, Image: cmp.Or(r.image, r.digest), InstalledVersion: p.installed,
					Workloads: m.listed(workloads, MaxWorkloadsPerAffected), WorkloadsTotal: len(workloads),
				})
			} else {
				// The same package at another version in this image: one
				// upgrade replaces both, and the image lists both.
				for i := range a.row.Affected {
					fi := &a.row.Affected[i]
					if fi.ImageID == r.imageID && !slices.Contains(strings.Split(fi.InstalledVersion, ", "), p.installed) {
						fi.InstalledVersion += ", " + p.installed
					}
				}
			}
		}
	}

	rows := make([]Fix, 0, len(byKey))
	for _, a := range byKey {
		row := a.row
		row.From = sortedKeys(a.from)
		if row.To == "" {
			row.Listed = sortedKeys(a.listed)
		}
		ids := make([]string, 0, len(a.cves))
		for id, sev := range a.cves {
			ids = append(ids, id)
			bump(&row.Counts, sev)
			if severityRank(sev) > severityRank(row.Severity) || row.Severity == "" {
				row.Severity = sev
			}
		}
		slices.SortFunc(ids, func(x, y string) int {
			return cmp.Or(cmp.Compare(severityRank(a.cves[y]), severityRank(a.cves[x])), cmp.Compare(x, y))
		})
		row.CVEsTotal = len(ids)
		row.CVEs = ids[:min(len(ids), MaxCVEsPerFix)]
		row.Images = len(a.images)
		row.Workloads = len(a.workloads)
		slices.SortStableFunc(row.Affected, func(x, y FixImage) int {
			return cmp.Or(cmp.Compare(y.WorkloadsTotal, x.WorkloadsTotal), cmp.Compare(x.Image, y.Image))
		})
		row.Affected = row.Affected[:min(len(row.Affected), MaxAffectedPerCVE)]
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(x, y Fix) int {
		return cmp.Or(
			compareCounts(y.Counts, x.Counts),
			cmp.Compare(y.Workloads, x.Workloads),
			compareBool(x.To == "", y.To == ""),
			cmp.Compare(x.Package, y.Package),
			cmp.Compare(x.To, y.To),
		)
	})
	return rows, nil
}

// upgradeState is everything upgradeFor's answer depends on: the
// ecosystem, the installed version and the findings' fixed releases.
func upgradeState(p *imagePackage) string {
	fixed := make([]string, 0, len(p.findings))
	for _, fd := range p.findings {
		fixed = append(fixed, fd.FixedVersion)
	}
	slices.Sort(fixed)
	return p.eco + "\x00" + p.installed + "\x00" + strings.Join(slices.Compact(fixed), "\x01")
}

// upgradeFor is the one version that fixes every fixable finding of an
// installed package. It starts at the highest of their own targets, then
// checks it against each finding's fixed releases: a scanner lists one fix
// per maintained branch, so a version above one finding's fix can still be
// on another branch that lacks it (with "7.5.5, 6.11.6" listed, 7.5.4 is
// not fixed). Such a finding moves the candidate up to its next fix, and the
// check repeats. When no listed version fixes them all, or the versions
// can't be ordered, it returns no target and the scanner's lists.
func upgradeFor(p *imagePackage) (to string, listed []string) {
	for _, fd := range p.findings {
		listed = append(listed, fd.FixedVersion)
	}
	slices.Sort(listed)
	listed = slices.Compact(listed)
	compare := comparerFor(p.eco)
	if compare == nil {
		return "", listed
	}
	for _, fd := range p.findings {
		target := fd.FixTarget
		if target == "" {
			return "", listed
		}
		if to == "" {
			to = target
		} else if higher, ok := compare(target, to); !ok {
			return "", listed
		} else if higher > 0 {
			to = target
		}
	}
	for range len(p.findings) * 4 {
		moved := false
		for _, fd := range p.findings {
			fixes := fixedReleases(fd.FixedVersion)
			fixed, ok := fixedBy(compare, to, fixes)
			if !ok {
				return "", listed
			}
			if fixed {
				continue
			}
			next, ok := nextFix(compare, to, fixes)
			if !ok {
				return "", listed
			}
			to, moved = next, true
		}
		if !moved {
			return to, nil
		}
	}
	return "", listed
}
