package vulnerabilities

import (
	"cmp"
	"context"
	"maps"
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
	row    Fix
	eco    string
	from   map[string]bool
	listed map[string]bool
	cves   map[string]Severity
	images map[string]bool
}

// fixes groups fixable findings by the upgrade that clears them. Within one
// image, every fixable finding of a package at one installed version is one
// upgrade, to the highest version any of them needs, so the row says what
// clears them all. Images whose package needs the same upgrade share a row.
// When a version can't be ordered (unknown ecosystem, unparseable version),
// the row carries the scanner's fixed versions instead of a target.
func (j *rollup) fixes(ctx context.Context, f Filter) ([]Fix, error) {
	m := j.match(f)
	byKey := map[string]*fixAgg{}
	// Images built on one base share package states: each is worked out once.
	type upgrade struct {
		to     string
		listed []string
	}
	upgrades := map[string]upgrade{}
	imageWorkloads := map[string][]WorkloadRef{}
	imageExposed := map[string]bool{}
	imageUnknown := map[string]bool{}
	imageRisk := map[string]riskBits{}
	for _, agg := range j.sortedImages() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := agg.report
		workloads := sortedWorkloads(agg.workloads)
		if !m.workloadKept(workloads) || !m.imageKept(r) {
			continue
		}
		imageWorkloads[r.imageID] = workloads
		distinct := withoutContainers(workloads)
		imageExposed[r.imageID] = slices.ContainsFunc(distinct, j.exposed)
		imageUnknown[r.imageID] = j.exposureUnknown(distinct)
		for _, w := range distinct {
			imageRisk[r.imageID] |= j.riskOf(w)
		}
		shown := m.listed(distinct, j.lim.workloadsPerAffected)
		rowMatched := m.rowMatches(agg.searchText(), workloads)
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
					eco:  p.eco,
					from: map[string]bool{}, listed: map[string]bool{}, cves: map[string]Severity{},
					images: map[string]bool{},
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
				if !j.charge(len(shown)) {
					return nil, j.stopped()
				}
				a.images[r.imageID] = true
				a.row.Affected = append(a.row.Affected, FixImage{
					ImageID: r.imageID, Image: cmp.Or(r.image, r.digest), InstalledVersions: []string{p.installed},
					Workloads: shown, WorkloadsTotal: len(distinct),
				})
			} else {
				// The same package at another version in this image: one
				// upgrade replaces both, and the image lists both.
				for i := range a.row.Affected {
					fi := &a.row.Affected[i]
					if fi.ImageID == r.imageID && !slices.Contains(fi.InstalledVersions, p.installed) {
						fi.InstalledVersions = append(fi.InstalledVersions, p.installed)
						sortVersions(p.eco, fi.InstalledVersions)
					}
				}
			}
		}
	}

	workloadsBySet := map[string]int{}
	exposedBySet := map[string]exposedSet{}
	rows := make([]Fix, 0, len(byKey))
	for _, a := range byKey {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := a.row
		row.From = sortVersions(a.eco, sortedKeys(a.from))
		if row.To == "" {
			row.FixedVersions = sortVersions(a.eco, sortedKeys(a.listed))
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
		row.CVEs = ids[:min(len(ids), j.lim.cvesPerFix)]
		row.Images = len(a.images)
		imageIDs := slices.Sorted(maps.Keys(a.images))
		row.Workloads = distinctWorkloads(imageIDs, imageWorkloads, workloadsBySet)
		var ok bool
		if row.Exposed, row.ExposedTotal, ok = j.exposedAcross(imageIDs, imageWorkloads, exposedBySet); !ok {
			return nil, j.stopped()
		}
		row.ExposureUnknown = row.ExposedTotal == 0 && slices.ContainsFunc(imageIDs, func(id string) bool { return imageUnknown[id] })
		row.RiskFactors = imageRisks(imageIDs, imageRisk)
		slices.SortStableFunc(row.Affected, func(x, y FixImage) int {
			return cmp.Or(compareBool(imageExposed[y.ImageID], imageExposed[x.ImageID]), cmp.Compare(y.WorkloadsTotal, x.WorkloadsTotal), cmp.Compare(x.Image, y.Image))
		})
		row.Affected = row.Affected[:min(len(row.Affected), j.lim.affectedPerCVE)]
		rows = append(rows, row)
	}
	// Most severe first, exposed first within each severity (see
	// exposureTier), then most exposed workloads, then most workloads.
	slices.SortFunc(rows, func(x, y Fix) int {
		return cmp.Or(
			cmp.Compare(severityRank(y.Severity), severityRank(x.Severity)),
			cmp.Compare(exposureTier(y.ExposedTotal > 0, y.ExposureUnknown), exposureTier(x.ExposedTotal > 0, x.ExposureUnknown)),
			cmp.Compare(y.ExposedTotal, x.ExposedTotal),
			cmp.Compare(y.Workloads, x.Workloads),
			compareCounts(y.Counts, x.Counts),
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
