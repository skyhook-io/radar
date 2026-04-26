// Package packages aggregates "what's installed" signals into a unified
// package list. Pure Go, no internal/ imports. Entry point is
// Aggregate(Sources) []PackageRow.
//
// Source codes (single character — stable on-wire):
//
//	H — Helm API (release secret read)
//	L — Workload labels (helm.sh/chart, meta.helm.sh/release-name)
//	C — CRD registration (spec.group → chart via crdGroupToChart)
//	A — Argo Application declaration
//	F — Flux HelmRelease / Kustomization declaration
package packages

import "time"

// Source codes returned in PackageRow.Sources. Stable on-wire — agents,
// SPAs, and other consumers rely on these single characters.
const (
	SourceHelm        = "H"
	SourceLabels      = "L"
	SourceCRDs        = "C"
	SourceArgoCD      = "A"
	SourceFluxCD      = "F"
)

// HelmRelease is the Helm-side input shape. Mirrors the on-wire shape
// of `internal/helm.HelmRelease` but lives here so pkg/packages stays
// dependency-free of internal/.
type HelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`         // raw chart string from Helm release ("cert-manager-1.14.0")
	ChartName      string `json:"chartName"`     // optional pre-parsed name; empty → derived from Chart
	ChartVersion   string `json:"chartVersion"`  // optional pre-parsed version; empty → derived from Chart
	AppVersion     string `json:"appVersion"`
	Status         string `json:"status"`
	ResourceHealth string `json:"resourceHealth,omitempty"` // healthy|degraded|unhealthy|unknown
}

// Workload is the labels-side input shape. We need just enough to look up
// helm.sh/chart + meta.helm.sh/release-{name,namespace} annotations and
// derive aggregated health. Callers translate from their concrete types
// (corev1.Deployment, etc.) using the helpers in workloads.go.
type Workload struct {
	Kind        string            `json:"kind"`        // Deployment | DaemonSet | StatefulSet | Job | CronJob
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	// Health is the workload's aggregated runtime status. Caller decides
	// the rule (e.g. ready/desired ratio for Deployments). One of:
	// healthy|degraded|unhealthy|unknown.
	Health string `json:"health"`
}

// CRD is the CRD-side input shape. We need just enough to map
// spec.group → chart name and pick a version.
type CRD struct {
	Name    string   `json:"name"`              // metadata.name (e.g., "certificates.cert-manager.io")
	Group   string   `json:"group"`             // spec.group (e.g., "cert-manager.io")
	Kind    string   `json:"kind"`              // spec.names.kind
	Plural  string   `json:"plural"`            // spec.names.plural
	Versions []string `json:"versions,omitempty"` // spec.versions[*].name (first one used)
}

// Declaration is the GitOps-side input shape — what a GitOps controller
// declares should be installed. Argo Applications, Flux HelmReleases,
// and Flux Kustomizations all collapse to this shape.
//
// Cross-cluster caveat: when Argo CD runs in cluster A but deploys to
// cluster B, the Application resources live in A — a `/api/packages`
// call against cluster B won't see those declarations.
type Declaration struct {
	Source    string `json:"source"`    // "argocd" | "flux"
	Namespace string `json:"namespace"` // declaration's own namespace
	Name      string `json:"name"`      // declaration's own name (App name / Kustomization name)
	// Target install identity (where the declaration says the package
	// lives). Argo Application: spec.destination.{namespace,name}
	// Flux HelmRelease: spec.releaseName (in spec.targetNamespace)
	// Flux Kustomization: name itself (no Helm shape — chart will be empty)
	TargetNamespace string `json:"targetNamespace"`
	TargetName      string `json:"targetName"`
	// Chart info (when known — Argo Helm-source apps + Flux HelmReleases
	// know it; Flux Kustomizations may not).
	Chart        string `json:"chart,omitempty"`
	ChartVersion string `json:"chartVersion,omitempty"`
	// Status as the GitOps controller sees it. One of:
	// healthy|degraded|unhealthy|unknown — caller maps from their
	// vocabulary (Argo: Healthy/Progressing/Degraded/Suspended/Missing/Unknown;
	// Flux: Ready/Stalled/Reconciling/Suspended).
	Status string `json:"status"`
}

// Sources is the input struct fed to Aggregate. Every field is optional;
// missing data sources just don't contribute. Callers populate from
// whatever they have access to — Hub-mode might pass all five, a
// minimal RBAC-restricted Radar might pass only Workloads + CRDs.
type Sources struct {
	Helm                []HelmRelease `json:"helm,omitempty"`
	Workloads           []Workload    `json:"workloads,omitempty"`
	CRDs                []CRD         `json:"crds,omitempty"`
	GitOpsDeclarations  []Declaration `json:"gitopsDeclarations,omitempty"`
}

// PackageRow is the output shape — one row per detected package.
// Multiple sources contribute to a single row when they agree; the
// `Sources` field carries the deduplicated voters.
type PackageRow struct {
	// Chart name. Always populated. Derived from (in priority order):
	// Helm release ChartName, helm.sh/chart label parse, crdGroupToChart
	// lookup, GitOps declaration Chart field.
	Chart string `json:"chart"`
	// Where the install lives. Empty for CRD-only rows (CRDs are
	// cluster-scoped registrations with no namespaced release identity).
	Namespace   string `json:"namespace,omitempty"`
	ReleaseName string `json:"releaseName,omitempty"`
	// Version (Helm chart version > label version > CRD spec.versions[0].name
	// > GitOps declared version). Empty if no source supplied one.
	Version string `json:"version,omitempty"`
	// AppVersion if Helm provided one. Optional.
	AppVersion string `json:"appVersion,omitempty"`
	// Health: healthy|degraded|unhealthy|unknown. Worst of contributors.
	Health string `json:"health"`
	// Sources is the deduplicated set of source codes that contributed.
	// At least one element. Order: H, L, C, A, F (declaration order).
	Sources []string `json:"sources"`
	// FromCRDGroup, when set, indicates this row originated from a CRD
	// whose group wasn't in crdGroupToChart — Chart is the group string
	// itself in that case. Lets the SPA render with appropriate framing
	// ("cert-manager.io CRDs detected") vs a real chart row.
	FromCRDGroup string `json:"fromCRDGroup,omitempty"`
	// AggregatedAt is the time Aggregate ran. Useful for cache freshness
	// debugging in distributed traces.
	AggregatedAt time.Time `json:"aggregatedAt"`
}
