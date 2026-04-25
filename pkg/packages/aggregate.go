package packages

import (
	"sort"
	"strings"
	"time"
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
	now := time.Now()
	// We accumulate rows in a map keyed by (chart, namespace, releaseName).
	// CRD-only rows that don't resolve to a chart get a synthetic key
	// using FromCRDGroup.
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
			Chart:        k.chart,
			Namespace:    k.namespace,
			ReleaseName:  k.releaseName,
			AggregatedAt: now,
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
		addSource(r, SourceHelm)
		// Helm fields win over later sources for these (highest signal).
		if r.Version == "" {
			r.Version = chartVersion
		}
		if r.AppVersion == "" {
			r.AppVersion = h.AppVersion
		}
		r.Health = worseHealth(r.Health, h.ResourceHealth)
	}

	// 2. Workloads with Helm labels (source L).
	for _, w := range s.Workloads {
		releaseName := w.Annotations["meta.helm.sh/release-name"]
		releaseNs := w.Annotations["meta.helm.sh/release-namespace"]
		chartLabel := w.Labels["helm.sh/chart"]
		if releaseName == "" && chartLabel == "" {
			continue
		}
		// Derive chart name + version from the label first; fall back to
		// release name + no version when the label's missing.
		var chartName, chartVersion string
		if chartLabel != "" {
			chartName, chartVersion = splitChart(chartLabel)
		}
		if chartName == "" && releaseName != "" {
			// Best-guess: release names often equal chart names ("cert-manager")
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
		addSource(r, SourceLabels)
		// Label version is a secondary signal — only fill when Helm didn't.
		if r.Version == "" {
			r.Version = chartVersion
		}
		// Worst-of health across all workloads for this release.
		r.Health = worseHealth(r.Health, w.Health)
	}

	// 3. GitOps declarations (sources A / F) — declared installs, may
	//    or may not be running yet.
	for _, d := range s.GitOpsDeclarations {
		var src string
		switch strings.ToLower(d.Source) {
		case "argocd", "argo-cd", "argo":
			src = SourceArgoCD
		case "flux", "fluxcd":
			src = SourceFluxCD
		default:
			// Unknown declaration source — skip rather than misattribute.
			continue
		}
		chartName := d.Chart
		// When the declaration omits the chart (e.g. raw-YAML Flux
		// Kustomization), fall back to the declaration name itself —
		// produces a usable row, just less rich.
		if chartName == "" {
			chartName = d.Name
		}
		if chartName == "" {
			continue
		}
		// Use the declaration's target identity to merge with Helm/L
		// rows when the GitOps controller manages a Helm release the
		// Helm API also reports. Argo Helm-source apps + Flux
		// HelmReleases land here.
		ns := d.TargetNamespace
		release := d.TargetName
		if release == "" {
			release = chartName
		}
		k := key{chart: chartName, namespace: ns, releaseName: release}
		r := get(k)
		addSource(r, src)
		if r.Version == "" {
			r.Version = d.ChartVersion
		}
		// Declarations contribute health when no runtime source did
		// (typical for declared-but-not-yet-running installs).
		r.Health = worseHealth(r.Health, d.Status)
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
		if known {
			// Find any rows for this chart and add C to them.
			matched := false
			for k, r := range rows {
				if k.chart == chartName {
					addSource(r, SourceCRDs)
					if r.Version == "" {
						r.Version = version
					}
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
			addSource(r, SourceCRDs)
			if r.Version == "" {
				r.Version = version
			}
			if r.Health == "" {
				r.Health = "unknown"
			}
			continue
		}
		// Unknown group — standalone CRD-only row keyed on the group
		// string itself. Multiple CRDs in the same group fold into a
		// single row.
		k := key{chart: c.Group, namespace: "", releaseName: ""}
		r := get(k)
		addSource(r, SourceCRDs)
		r.FromCRDGroup = c.Group
		if r.Version == "" {
			r.Version = version
		}
		if r.Health == "" {
			r.Health = "unknown"
		}
	}

	// Default health to unknown for any row that ended up with none
	// (CRD-only rows; declarations without a status; etc.).
	for _, r := range rows {
		if r.Health == "" {
			r.Health = "unknown"
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

// addSource appends src to r.Sources if not already present. Maintains
// canonical order H, L, C, A, F.
func addSource(r *PackageRow, src string) {
	for _, s := range r.Sources {
		if s == src {
			return
		}
	}
	r.Sources = append(r.Sources, src)
	// Re-sort into canonical order.
	sort.Slice(r.Sources, func(i, j int) bool {
		return sourceRank(r.Sources[i]) < sourceRank(r.Sources[j])
	})
}

func sourceRank(s string) int {
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
		// Version: starts with digit, or v followed by digit.
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

// worseHealth returns the worse of two health strings using the order:
// unhealthy > degraded > unknown > healthy. (Unknown beats healthy
// because we don't want a CRD-only "unknown" row to be promoted to
// "healthy" just because no other source contributed.)
//
// Empty strings are treated as "no opinion" — the other side wins.
func worseHealth(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	rank := func(h string) int {
		switch strings.ToLower(h) {
		case "unhealthy", "danger", "critical", "failed", "stalled":
			return 4
		case "degraded", "warning", "warn", "progressing", "reconciling":
			return 3
		case "unknown", "":
			return 2
		case "healthy", "ok", "ready", "available":
			return 1
		}
		return 2
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}
