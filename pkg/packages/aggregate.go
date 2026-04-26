package packages

import (
	"sort"
	"strings"
)

// Aggregate is the merge function. Given a Sources struct, returns a
// deduplicated, source-attributed list of PackageRow.
//
// Merge keys:
//   - Helm-shaped rows (sources H/L/A-with-chart/F-with-chart) merge on
//     (release_namespace, release_name) — so "cert-manager Helm release
//     in cert-manager namespace" + "cert-manager workload labels
//     pointing to that same release" become one row.
//   - CRD-only rows merge into Helm-shaped rows when crdGroupToChart
//     resolves to the same chart name. So "cert-manager.io CRDs
//     detected" + "cert-manager Helm release" → one row with sources
//     [H,C].
//   - Unknown CRD groups stay as their own rows (FromCRDGroup set).
//
// Determinism: rows are returned sorted by (chart, namespace,
// release_name) so consumers (SPA tables, MCP tool output) get stable
// ordering across calls.
func Aggregate(s Sources) []PackageRow {
	// CRD-only rows that don't resolve to a chart get a synthetic key
	// using the group string; non-CRD rows key on (chart, namespace,
	// releaseName) so multiple sources for the same release merge.
	type key struct {
		chart       string
		namespace   string
		releaseName string
	}
	rows := map[key]*PackageRow{}

	get := func(k key) *PackageRow {
		if r, ok := rows[k]; ok {
			return r
		}
		r := &PackageRow{
			Chart:       k.chart,
			Namespace:   k.namespace,
			ReleaseName: k.releaseName,
		}
		rows[k] = r
		return r
	}

	// 1. Helm releases (source H) — primary signal.
	for _, h := range s.Helm {
		chartName := h.ChartName
		chartVersion := h.ChartVersion
		if chartName == "" || chartVersion == "" {
			parsedName, parsedVer := splitChart(h.Chart)
			if chartName == "" {
				chartName = parsedName
			}
			if chartVersion == "" {
				chartVersion = parsedVer
			}
		}
		if chartName == "" {
			// Unparseable chart string and no name supplied — skip
			// rather than create a row keyed on empty-string (which
			// would absorb every other no-name row into one).
			continue
		}
		k := key{chart: chartName, namespace: h.Namespace, releaseName: h.Name}
		r := get(k)
		r.AddContribution(SourceContribution{
			Source:           SourceHelm,
			Health:           h.ResourceHealth,
			Version:          chartVersion,
			AppVersion:       h.AppVersion,
			ReleaseName:      h.Name,
			ReleaseNamespace: h.Namespace,
		})
	}

	// 2. Workloads with Helm labels (source L).
	for _, w := range s.Workloads {
		releaseName := w.Annotations["meta.helm.sh/release-name"]
		releaseNs := w.Annotations["meta.helm.sh/release-namespace"]
		chartLabel := w.Labels["helm.sh/chart"]
		if releaseName == "" && chartLabel == "" {
			continue
		}
		var chartName, chartVersion string
		if chartLabel != "" {
			chartName, chartVersion = splitChart(chartLabel)
		}
		if chartName == "" && releaseName != "" {
			chartName = releaseName
		}
		if chartName == "" {
			continue
		}
		// Without an explicit release-namespace annotation, fall back
		// to the workload's namespace — covers Argo-applied Helm charts
		// that don't always set the annotation.
		if releaseNs == "" {
			releaseNs = w.Namespace
		}
		if releaseName == "" {
			releaseName = chartName
		}
		k := key{chart: chartName, namespace: releaseNs, releaseName: releaseName}
		r := get(k)
		r.AddContribution(SourceContribution{
			Source:           SourceLabels,
			Health:           w.Health,
			Version:          chartVersion,
			ReleaseName:      releaseName,
			ReleaseNamespace: releaseNs,
		})
	}

	// 3. GitOps declarations (sources A / F) — declared installs, may
	//    or may not be running yet.
	for _, d := range s.GitOpsDeclarations {
		var src SourceCode
		switch strings.ToLower(d.Source) {
		case "argocd", "argo-cd", "argo":
			src = SourceArgoCD
		case "flux", "fluxcd":
			src = SourceFluxCD
		default:
			continue
		}
		chartName := d.Chart
		// When the declaration omits the chart (e.g. raw-YAML Flux
		// Kustomization), fall back to the declaration name itself.
		if chartName == "" {
			chartName = d.Name
		}
		if chartName == "" {
			continue
		}
		ns := d.TargetNamespace
		release := d.TargetName
		if release == "" {
			release = chartName
		}
		k := key{chart: chartName, namespace: ns, releaseName: release}
		r := get(k)
		r.AddContribution(SourceContribution{
			Source:               src,
			Health:               d.Status,
			Version:              d.ChartVersion,
			ReleaseName:          release,
			ReleaseNamespace:     ns,
			DeclarationName:      d.Name,
			DeclarationNamespace: d.Namespace,
		})
	}

	// 4. CRD registrations (source C). Two cases:
	//    a. Group resolves to a known chart → merge into existing Helm/L
	//       row for that chart (any namespace). When multiple Helm rows
	//       exist for the same chart in different namespaces, we
	//       contribute C to ALL of them (defensible: the CRDs are the
	//       cluster-scoped underpinning that all releases share).
	//    b. Group doesn't resolve → standalone row, FromCRDGroup set.
	for _, c := range s.CRDs {
		chartName, known := chartFromCRDGroup(c.Group)
		var version string
		if len(c.Versions) > 0 {
			version = c.Versions[0]
		}
		// Build a fresh contribution per row to avoid future fragility
		// — SourceContribution is a value struct today (safe by Go
		// semantics) but adding a slice or map field later would
		// silently expose mutation across rows if the literal were
		// shared.
		newContribution := func() SourceContribution {
			return SourceContribution{Source: SourceCRDs, Version: version}
		}
		if known {
			matched := false
			for k, r := range rows {
				if k.chart == chartName {
					r.AddContribution(newContribution())
					matched = true
				}
			}
			if matched {
				continue
			}
			// Known chart but no Helm/L row for it — synthesize a
			// CRD-only row so the install is visible.
			k := key{chart: chartName, namespace: "", releaseName: ""}
			r := get(k)
			r.AddContribution(newContribution())
			if r.Health == "" {
				r.Health = HealthUnknown
			}
			continue
		}
		// Unknown group — standalone CRD-only row keyed on the group
		// string itself. Multiple CRDs in the same group fold into a
		// single row.
		k := key{chart: c.Group, namespace: "", releaseName: ""}
		r := get(k)
		r.AddContribution(newContribution())
		r.FromCRDGroup = c.Group
		if r.Health == "" {
			r.Health = HealthUnknown
		}
	}

	// Default health to unknown for any row that ended up with none.
	for _, r := range rows {
		if r.Health == "" {
			r.Health = HealthUnknown
		}
	}

	// Stable sort: chart, then namespace, then release name.
	out := make([]PackageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Chart != out[j].Chart {
			return out[i].Chart < out[j].Chart
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].ReleaseName < out[j].ReleaseName
	})
	return out
}

// sortSources sorts in place into canonical order H, L, C, A, F.
func sortSources(s []SourceCode) {
	sort.Slice(s, func(i, j int) bool {
		return sourceRank(s[i]) < sourceRank(s[j])
	})
}

// sortContributors sorts contributions in place by canonical Source order.
func sortContributors(cs []SourceContribution) {
	sort.Slice(cs, func(i, j int) bool {
		return sourceRank(cs[i].Source) < sourceRank(cs[j].Source)
	})
}

func sourceRank(s SourceCode) int {
	switch s {
	case SourceHelm:
		return 0
	case SourceLabels:
		return 1
	case SourceCRDs:
		return 2
	case SourceArgoCD:
		return 3
	case SourceFluxCD:
		return 4
	}
	return 5
}

// splitChart splits a Helm chart string like "cert-manager-1.14.0" or
// "cert-manager-v1.14.0" into (name, version). Returns ("", "") if the
// string doesn't look like name-version. Handles charts whose own name
// contains hyphens ("kube-prometheus-stack-45.27.2").
//
// Heuristic: find the last hyphen followed by a digit-or-v-digit; the
// name is the prefix, the version is the suffix. Falls back to the
// whole string as name with empty version when no version part is
// found.
func splitChart(s string) (name, version string) {
	if s == "" {
		return "", ""
	}
	for i := len(s) - 1; i >= 1; i-- {
		if s[i-1] != '-' {
			continue
		}
		rest := s[i:]
		if rest == "" {
			continue
		}
		c := rest[0]
		if c >= '0' && c <= '9' {
			return s[:i-1], rest
		}
		if c == 'v' && len(rest) > 1 {
			d := rest[1]
			if d >= '0' && d <= '9' {
				return s[:i-1], rest
			}
		}
	}
	return s, ""
}

// worseHealth returns the worse of two Health values using the order:
// Unhealthy > Degraded > Unknown > Healthy. (Unknown beats Healthy
// because we don't want a CRD-only "unknown" row to be promoted to
// "healthy" just because no other source contributed.) Unrecognized
// vocab (typo, future GitOps reason) maps to the "unknown" rank —
// quieter than Degraded, still beats Healthy.
//
// Empty strings are "no opinion" — the other side wins.
func worseHealth(a, b Health) Health {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if healthRank(a) >= healthRank(b) {
		return a
	}
	return b
}

func healthRank(h Health) int {
	switch Health(strings.ToLower(string(h))) {
	case HealthUnhealthy, "danger", "critical", "failed", "stalled":
		return 4
	case HealthDegraded, "warning", "warn", "progressing", "reconciling":
		return 3
	case HealthUnknown:
		return 2
	case HealthHealthy, "ok", "ready", "available":
		return 1
	}
	return 2
}
