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

// SourceCode is the single-character code identifying which signal
// contributed to a PackageRow. Stable on-wire — agents, frontends, Hub
// fan-in, etc. depend on these exact strings. Defined as a named type
// so call sites get compile-time checks against typos.
type SourceCode string

const (
	SourceHelm   SourceCode = "H"
	SourceLabels SourceCode = "L"
	SourceCRDs   SourceCode = "C"
	SourceArgoCD SourceCode = "A"
	SourceFluxCD SourceCode = "F"
)

// AllSourceCodes is the canonical-order enumeration. Used by sourcesUsed
// to emit deterministic output and by Hub fan-in to iterate sources.
var AllSourceCodes = []SourceCode{SourceHelm, SourceLabels, SourceCRDs, SourceArgoCD, SourceFluxCD}

// Valid reports whether s is one of the five known source codes.
func (s SourceCode) Valid() bool {
	switch s {
	case SourceHelm, SourceLabels, SourceCRDs, SourceArgoCD, SourceFluxCD:
		return true
	}
	return false
}

// Health is the package-level health vocabulary. All four contributors
// (Helm, Workloads, CRDs default, GitOps) normalize to one of these.
type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthDegraded  Health = "degraded"
	HealthUnhealthy Health = "unhealthy"
	HealthUnknown   Health = "unknown"
)

// Overlay is the optional Tier-2 "application" identity ABOVE a package — the
// grouping key the Applications fleet surface uses (see
// radar-hub/docs/ADDONS-AND-APPLICATIONS-PLAN.md). It mirrors
// pkg/subject.AppOverlay but with plain fields, so pkg/packages stays free of
// pkg/subject + metav1: the caller resolves subject.ResolveOverlay (for
// workloads) or maps a GitOps declaration's identity, then sets this.
//
//	Tier   — 1-9, mirrors pkg/subject.Tier (lower = higher precedence/confidence).
//	Key    — stable grouping key, e.g. "<ns>/Application/<name>" or "<ns>/app/<name>".
//	Confidence — "high" (tiers 1-4) | "medium" (5-8) | "low" (9).
type Overlay struct {
	Key        string `json:"key"`
	Tier       int    `json:"tier"`
	Confidence string `json:"confidence,omitempty"`
}

// HelmRelease is the Helm-side input shape. Mirrors the on-wire shape
// of `internal/helm.HelmRelease` but lives here so pkg/packages stays
// dependency-free of internal/.
type HelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`        // raw chart string from Helm release ("cert-manager-1.14.0")
	ChartName      string `json:"chartName"`    // optional pre-parsed name; empty → derived from Chart
	ChartVersion   string `json:"chartVersion"` // optional pre-parsed version; empty → derived from Chart
	AppVersion     string `json:"appVersion"`
	Status         string `json:"status"`
	ResourceHealth Health `json:"resourceHealth,omitempty"`
}

// Workload is the labels-side input shape. We need just enough to look up
// helm.sh/chart + meta.helm.sh/release-{name,namespace} annotations and
// derive aggregated health. Callers translate from their concrete types
// (corev1.Deployment, etc.).
type Workload struct {
	Kind        string            `json:"kind"` // Deployment | DaemonSet | StatefulSet | Job | CronJob
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	// Health is the workload's aggregated runtime status. Caller decides
	// the rule (e.g. ready/desired ratio for Deployments).
	Health Health `json:"health"`
	// Overlay is the resolved Tier-2 app identity for this workload (the
	// caller runs pkg/subject.ResolveOverlay on the workload's metadata).
	// Optional — nil when no app-overlay signal reached tier 7.
	Overlay *Overlay `json:"overlay,omitempty"`
}

// CRD is the CRD-side input shape. We need just enough to map
// spec.group → chart name and pick a version.
type CRD struct {
	Name     string   `json:"name"`               // metadata.name (e.g., "certificates.cert-manager.io")
	Group    string   `json:"group"`              // spec.group (e.g., "cert-manager.io")
	Kind     string   `json:"kind"`               // spec.names.kind
	Plural   string   `json:"plural"`             // spec.names.plural
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
	// Status as the GitOps controller sees it. Caller maps from their
	// vocabulary. Argo: Healthy/Progressing/Degraded/Suspended/Missing/
	// Unknown. Flux: derived from Ready and Stalled conditions; transient
	// reasons collapse to degraded. Suspended (spec.suspend) is not yet
	// surfaced separately.
	Status Health `json:"status"`
	// Overlay is the app identity derived from the declaration's own GitOps
	// identity (Argo Application → tier 3, Flux HelmRelease → tier 1, Flux
	// Kustomization → tier 2). Set by the caller. Optional.
	Overlay *Overlay `json:"overlay,omitempty"`
}

// Sources is the input struct fed to Aggregate. Every field is optional;
// missing data sources just don't contribute. Callers populate from
// whatever they have access to — Hub-mode might pass all five, a
// minimal RBAC-restricted Radar might pass only Workloads + CRDs.
type Sources struct {
	Helm               []HelmRelease `json:"helm,omitempty"`
	Workloads          []Workload    `json:"workloads,omitempty"`
	CRDs               []CRD         `json:"crds,omitempty"`
	GitOpsDeclarations []Declaration `json:"gitopsDeclarations,omitempty"`
}

// SourceContribution is what one signal saw for this package, before
// the worst-of-health / first-wins-version merge collapsed contributors
// into a single PackageRow. Consumers needing per-source detail (Hub's
// "Helm: healthy · Argo: degraded" tooltip, "managed by Argo →" deep
// link, same-cluster version-disagreement detection) read Contributors;
// simple consumers read the aggregated fields on PackageRow directly.
//
// Contributor merge identity is `Source` alone — at most one
// contribution per SourceCode per row. For cross-cluster fan-in (Hub),
// see `Cluster` below.
type SourceContribution struct {
	Source           SourceCode `json:"source"`
	Health           Health     `json:"health,omitempty"`
	Version          string     `json:"version,omitempty"`
	APIVersion       string     `json:"apiVersion,omitempty"`
	AppVersion       string     `json:"appVersion,omitempty"`
	ReleaseName      string     `json:"releaseName,omitempty"`
	ReleaseNamespace string     `json:"releaseNamespace,omitempty"`
	// For GitOps sources (A/F): the controller resource's own identity
	// (e.g. the Argo Application's namespace/name, the Flux HelmRelease's
	// namespace/name). Lets consumers deep-link to the controller view.
	// Always empty for non-GitOps sources (H/L/C).
	DeclarationName      string `json:"declarationName,omitempty"`
	DeclarationNamespace string `json:"declarationNamespace,omitempty"`
	// Cluster identifies the cluster this contribution came from.
	// Always empty in single-cluster output (Radar standalone). Hub
	// populates this when fanning rows in across clusters so the
	// merge key effectively becomes (Source, Cluster) — preserving
	// per-cluster Argo identity in the "same chart, different Apps,
	// different clusters" hub-and-spoke pattern.
	Cluster string `json:"cluster,omitempty"`
}

// PackageRow is the output shape — one row per detected package.
// Multiple sources contribute to a single row when they agree; the
// `Sources` field carries the deduplicated voters and `Contributors`
// carries each source's pre-merge view.
//
// Hub fan-in note: AddContribution dedupes on (Source, Cluster). When
// Hub merges rows from N clusters, it stamps each contribution with the
// originating cluster so Argo App identity from cluster A and from
// cluster B both survive on the merged row. Single-cluster Radar
// leaves Cluster empty, so dedupe collapses to Source alone.
type PackageRow struct {
	// Chart name. Always populated. Derived from (in priority order):
	// Helm release ChartName, helm.sh/chart label parse, crdGroupToChart
	// lookup, GitOps declaration Chart field.
	Chart string `json:"chart"`
	// Where the install lives. Empty for CRD-only rows (CRDs are
	// cluster-scoped registrations with no namespaced release identity).
	Namespace   string `json:"namespace,omitempty"`
	ReleaseName string `json:"releaseName,omitempty"`
	// Version (Helm chart version > label version > GitOps declared
	// version). Empty if no package source supplied one. CRD API
	// versions are source metadata and live on Contributors.APIVersion.
	// For per-source values (and to detect same-cluster version
	// disagreement), read Contributors.
	Version string `json:"version,omitempty"`
	// AppVersion if Helm provided one. Optional.
	AppVersion string `json:"appVersion,omitempty"`
	// Health is the worst-of across contributors.
	Health Health `json:"health"`
	// Sources is the deduplicated set of source codes that contributed.
	// At least one element. Order: H, L, C, A, F (canonical). Treat as
	// immutable after Aggregate returns; mutate via AddContribution.
	Sources []SourceCode `json:"sources"`
	// Contributors carries each source's pre-merge view (one entry per
	// SourceCode). Same canonical order as Sources. Hub uses this for
	// the "Helm says healthy, Argo says degraded" tooltip and to deep-
	// link to the GitOps controller resource that manages a row.
	Contributors []SourceContribution `json:"contributors,omitempty"`
	// FromCRDGroup, when set, indicates this row originated from a CRD
	// whose group wasn't in crdGroupToChart — Chart is the group string
	// itself in that case. Lets the frontend render with appropriate framing
	// ("cert-manager.io CRDs detected") vs a real chart row.
	FromCRDGroup string `json:"fromCRDGroup,omitempty"`
	// Overlay is the resolved Tier-2 app identity for this package (the
	// highest-confidence overlay across its contributing workloads /
	// declarations — lowest Tier wins). Drives the Applications fleet
	// surface's app grouping. Optional — nil when no app-overlay resolved.
	Overlay *Overlay `json:"overlay,omitempty"`
}

// AddSource appends src to r.Sources if not already present and
// re-sorts into canonical order H, L, C, A, F. Idempotent.
//
// Most callers should use AddContribution instead — it captures the
// per-source detail and calls AddSource as a side-effect.
func (r *PackageRow) AddSource(src SourceCode) {
	for _, s := range r.Sources {
		if s == src {
			return
		}
	}
	r.Sources = append(r.Sources, src)
	sortSources(r.Sources)
}

// MergeHealth replaces r.Health with the worse of (r.Health, h),
// using the order Unhealthy > Degraded > Unknown > Healthy. Empty h
// is "no opinion" — r.Health unchanged.
func (r *PackageRow) MergeHealth(h Health) {
	r.Health = worseHealth(r.Health, h)
}

// AddContribution merges a per-source contribution into r. Updates:
//  1. r.Sources (idempotent, canonical order — via AddSource)
//  2. r.Contributors (idempotent on (Source, Cluster): subsequent
//     contributions with the same key merge — Health worst-of, other
//     fields keep first-seen non-empty values; contributions with
//     different Cluster keys land as separate entries)
//  3. Aggregated fields (r.Health worst-of, r.Version/AppVersion
//     first-seen wins) — preserves the existing on-wire semantics
//     for simple consumers that ignore Contributors.
func (r *PackageRow) AddContribution(c SourceContribution) {
	c = normalizeContribution(c)
	r.AddSource(c.Source)
	r.MergeHealth(c.Health)
	if r.Version == "" && c.Source != SourceCRDs {
		r.Version = c.Version
	}
	if r.AppVersion == "" {
		r.AppVersion = c.AppVersion
	}
	for i := range r.Contributors {
		if r.Contributors[i].Source != c.Source || r.Contributors[i].Cluster != c.Cluster {
			continue
		}
		existing := &r.Contributors[i]
		existing.Health = worseHealth(existing.Health, c.Health)
		if existing.Version == "" {
			existing.Version = c.Version
		}
		if existing.APIVersion == "" {
			existing.APIVersion = c.APIVersion
		}
		if existing.AppVersion == "" {
			existing.AppVersion = c.AppVersion
		}
		if existing.ReleaseName == "" {
			existing.ReleaseName = c.ReleaseName
		}
		if existing.ReleaseNamespace == "" {
			existing.ReleaseNamespace = c.ReleaseNamespace
		}
		if existing.DeclarationName == "" {
			existing.DeclarationName = c.DeclarationName
		}
		if existing.DeclarationNamespace == "" {
			existing.DeclarationNamespace = c.DeclarationNamespace
		}
		return
	}
	r.Contributors = append(r.Contributors, c)
	sortContributors(r.Contributors)
}

func normalizeContribution(c SourceContribution) SourceContribution {
	if c.Source == SourceCRDs {
		if c.APIVersion == "" {
			c.APIVersion = c.Version
		}
		c.Version = ""
		c.AppVersion = ""
		return c
	}
	c.APIVersion = ""
	return c
}

// Contributor returns the first contribution from the given source
// (canonical order). Returns nil if no contribution from that source
// exists. Hub fan-in iterates Contributors directly (since it may have
// per-cluster entries for the same Source); this helper is for
// single-cluster consumers asking "what did Helm say?"
func (r *PackageRow) Contributor(s SourceCode) *SourceContribution {
	for i := range r.Contributors {
		if r.Contributors[i].Source == s {
			return &r.Contributors[i]
		}
	}
	return nil
}
