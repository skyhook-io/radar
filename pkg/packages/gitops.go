package packages

import (
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/pkg/conditions"
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
		// Suspended = an operator deliberately paused sync (or a suspended
		// CronJob/Rollout). Intentional, not a degradation — neutral (sky), so it
		// doesn't light up amber on a surface meant for things that need action.
		return HealthNeutral
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
		// A False Ready is degraded when its reason is a known transient
		// (mid-reconcile / pending / in-progress), else unhealthy. The transient
		// set is the SHARED packages.IsTransientConditionReason — one source of
		// truth, so this GitOps-health path and the Issues CRD noise-floor
		// (internal/issues.detectGenericCRDIssues) can't drift. It deliberately
		// spans the common in-progress vocabulary across Flux/Argo/Crossplane/
		// cert-manager; any unrecognized reason still errs loud (→ unhealthy).
		reason := stringAt(ready, "reason")
		if isTransientFluxReason(reason) {
			return HealthDegraded
		}
		return HealthUnhealthy
	}
	return HealthUnknown
}

func isTransientFluxReason(r string) bool {
	return IsTransientConditionReason(r)
}

// IsTransientConditionReason re-exports the neutral condition-state predicate
// from pkg/conditions (the canonical home of the transient vocabulary) so the
// GitOps health mapping here keeps a local entry point.
func IsTransientConditionReason(r string) bool {
	return conditions.IsTransientConditionReason(r)
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
