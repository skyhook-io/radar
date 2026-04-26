package packages

import (
	"fmt"
	"strings"
)

// Parsers for GitOps controller resources. Take generic JSON-decoded
// maps (CRDs are dynamic; we don't import the controllers' typed Go
// modules) and return Declaration. Returns (Declaration{}, false) when
// the input doesn't look like the expected shape.

// ParseArgoApplication parses an argoproj.io/Application into a
// Declaration. Argo apps have multiple source shapes:
//
//   - Helm chart (spec.source.chart + spec.source.helm.parameters)
//   - Git repo with kustomize (spec.source.path + spec.source.kustomize)
//   - Git repo with raw manifests (spec.source.path)
//   - Multi-source apps (spec.sources[]) — first Helm source wins for
//     chart info; we don't model multiple charts per app.
//
// Status mapping: status.health.status → our health vocab via
// mapArgoHealth (which falls back to "unknown" for any unrecognized
// state, including future additions to Argo's HealthStatus enum).
func ParseArgoApplication(obj map[string]any) (Declaration, bool) {
	meta := mapAt(obj, "metadata")
	if meta == nil {
		return Declaration{}, false
	}
	d := Declaration{
		Source:    "argocd",
		Namespace: stringAt(meta, "namespace"),
		Name:      stringAt(meta, "name"),
	}
	if d.Name == "" {
		return Declaration{}, false
	}
	spec := mapAt(obj, "spec")
	if spec == nil {
		return d, true
	}
	// Destination → target.
	if dest := mapAt(spec, "destination"); dest != nil {
		d.TargetNamespace = stringAt(dest, "namespace")
		// "name" field is the destination cluster (Argo cluster name),
		// not the resource name. Use empty target name; the Helm chart
		// or app name fills it later.
	}
	// Single-source vs multi-source.
	chart, version := argoSourceChart(spec)
	d.Chart = chart
	d.ChartVersion = version
	if d.TargetName == "" {
		// Conventionally Argo apps name == release name when source is
		// Helm. Fall back to app name.
		d.TargetName = d.Name
	}
	// Status from status.health.status.
	if status := mapAt(obj, "status"); status != nil {
		if health := mapAt(status, "health"); health != nil {
			d.Status = mapArgoHealth(stringAt(health, "status"))
		}
	}
	return d, true
}

// argoSourceChart pulls (chart, version) from spec.source or the first
// spec.sources[] entry that's a Helm chart.
func argoSourceChart(spec map[string]any) (string, string) {
	// Single source.
	if src := mapAt(spec, "source"); src != nil {
		chart := stringAt(src, "chart")
		ver := stringAt(src, "targetRevision")
		if chart != "" {
			return chart, ver
		}
	}
	// Multi source.
	if sources, ok := spec["sources"].([]any); ok {
		for _, s := range sources {
			if src, ok := s.(map[string]any); ok {
				chart := stringAt(src, "chart")
				ver := stringAt(src, "targetRevision")
				if chart != "" {
					return chart, ver
				}
			}
		}
	}
	return "", ""
}

func mapArgoHealth(s string) Health {
	switch strings.ToLower(s) {
	case "healthy":
		return HealthHealthy
	case "progressing":
		return HealthDegraded // not yet ready
	case "degraded":
		return HealthUnhealthy
	case "suspended":
		return HealthDegraded
	case "missing":
		// Argo "Missing" = "should exist but doesn't" — a real signal
		// (controller hasn't been able to apply); not the same as
		// Unknown ("haven't observed yet"). Surface as Degraded.
		return HealthDegraded
	case "unknown":
		return HealthUnknown
	}
	return HealthUnknown
}

// ParseFluxHelmRelease parses a helm.toolkit.fluxcd.io/HelmRelease into
// a Declaration. Flux HRs always declare a Helm chart, so Chart is
// always populated.
func ParseFluxHelmRelease(obj map[string]any) (Declaration, bool) {
	meta := mapAt(obj, "metadata")
	if meta == nil {
		return Declaration{}, false
	}
	d := Declaration{
		Source:    "flux",
		Namespace: stringAt(meta, "namespace"),
		Name:      stringAt(meta, "name"),
	}
	if d.Name == "" {
		return Declaration{}, false
	}
	spec := mapAt(obj, "spec")
	if spec == nil {
		return d, true
	}
	// Chart info — primary path is spec.chart.spec.{chart,version} (the
	// HelmChartTemplate shape used by stable Flux v2 APIs). Fallback
	// handles defensive flat-shape variants seen in the wild.
	// NOTE: spec.chartRef (OCI HelmChart reference) is NOT yet parsed —
	// OCI-sourced HelmReleases will report empty Chart/ChartVersion.
	if chart := mapAt(spec, "chart"); chart != nil {
		if cspec := mapAt(chart, "spec"); cspec != nil {
			d.Chart = stringAt(cspec, "chart")
			d.ChartVersion = stringAt(cspec, "version")
		}
	}
	if d.Chart == "" {
		if chart := mapAt(spec, "chart"); chart != nil {
			d.Chart = stringAt(chart, "chart")
			d.ChartVersion = stringAt(chart, "version")
		}
	}
	// Target.
	d.TargetNamespace = stringAt(spec, "targetNamespace")
	if d.TargetNamespace == "" {
		d.TargetNamespace = d.Namespace
	}
	d.TargetName = stringAt(spec, "releaseName")
	if d.TargetName == "" {
		d.TargetName = d.Name
	}
	// Status: status.conditions[type=Ready].status / .reason.
	d.Status = fluxConditionStatus(obj)
	return d, true
}

// ParseFluxKustomization parses a kustomize.toolkit.fluxcd.io/Kustomization
// into a Declaration. Kustomizations don't have Helm chart info — they
// render raw YAML — so Chart stays empty (Aggregate will fall back to
// the Kustomization name as the row identity).
func ParseFluxKustomization(obj map[string]any) (Declaration, bool) {
	meta := mapAt(obj, "metadata")
	if meta == nil {
		return Declaration{}, false
	}
	d := Declaration{
		Source:    "flux",
		Namespace: stringAt(meta, "namespace"),
		Name:      stringAt(meta, "name"),
	}
	if d.Name == "" {
		return Declaration{}, false
	}
	spec := mapAt(obj, "spec")
	if spec != nil {
		d.TargetNamespace = stringAt(spec, "targetNamespace")
		if d.TargetNamespace == "" {
			d.TargetNamespace = d.Namespace
		}
	}
	d.TargetName = d.Name
	d.Status = fluxConditionStatus(obj)
	return d, true
}

// fluxConditionStatus reads status.conditions[type=Ready] and returns
// our health vocabulary. Flux conditions are status: True/False with
// a reason like "ReconciliationSucceeded", "InstallFailed",
// "DependencyNotReady".
func fluxConditionStatus(obj map[string]any) Health {
	status := mapAt(obj, "status")
	if status == nil {
		return HealthUnknown
	}
	conds, ok := status["conditions"].([]any)
	if !ok {
		return HealthUnknown
	}
	// Look for Ready first; fall back to Stalled if present.
	var ready, stalled map[string]any
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		switch stringAt(cm, "type") {
		case "Ready":
			ready = cm
		case "Stalled":
			stalled = cm
		}
	}
	if stalled != nil && stringAt(stalled, "status") == "True" {
		return HealthUnhealthy
	}
	if ready == nil {
		return HealthUnknown
	}
	switch stringAt(ready, "status") {
	case "True":
		return HealthHealthy
	case "False":
		// Whitelist transient reasons; everything else (including any
		// unrecognized future reason) classifies as unhealthy. Errs
		// loud — false-positive unhealthy is preferable to silently
		// masking a hard failure we don't yet recognize.
		reason := stringAt(ready, "reason")
		if isTransientFluxReason(reason) {
			return HealthDegraded
		}
		return HealthUnhealthy
	}
	return HealthUnknown
}

func isTransientFluxReason(r string) bool {
	switch r {
	case "Progressing", "DependencyNotReady", "ReconciliationInProgress",
		"ChartNotReady", "ArtifactFailed":
		return true
	}
	return false
}

// Tiny typed lookup helpers — Go stdlib doesn't expose nice JSON
// path-walking and writing it inline gets noisy.
func mapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func stringAt(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}
