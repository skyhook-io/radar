package subject

import (
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ============================= TIER 2: APP OVERLAY =============================

// GitOps / Helm signal keys. Tiers 1-5 consolidate the signal keys that
// pkg/topology/managedby.go reads.
const (
	argoTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel        = "argocd.argoproj.io/instance"
	fluxKustomizeNameLabel   = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNSLabel     = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmNameLabel        = "helm.toolkit.fluxcd.io/name"
	fluxHelmNSLabel          = "helm.toolkit.fluxcd.io/namespace"
	helmReleaseNameAnno      = "meta.helm.sh/release-name"
	helmReleaseNSAnno        = "meta.helm.sh/release-namespace"

	appInstanceLabel = "app.kubernetes.io/instance"
	partOfLabel      = "app.kubernetes.io/part-of"
	appNameLabel     = "app.kubernetes.io/name"
	bareAppLabel     = "app"
)

// API groups for the synthesized manager refs (tiers 1-5). The
// {HelmRelease,Group:""} native-Helm sentinel that summary.go:sourceForOwner
// keys "helm" off MUST NOT change.
const (
	argoApplicationGroup = "argoproj.io"
	fluxKustomizeGroup   = "kustomize.toolkit.fluxcd.io"
	fluxHelmGroup        = "helm.toolkit.fluxcd.io"
)

// Tier is the 9-tier precedence rank of the winning app signal. Tiers 1-5 are
// the CONSOLIDATED managedby.go precedence, in its native order (argo-instance
// #4, Helm-release #5 — matching managedby.go). The locked rule is "Helm ranks
// above the app.kubernetes.io label tiers (6-9)", NOT above Argo. Tiers 6-9 are
// NET-NEW.
type Tier int

const (
	TierNone            Tier = 0
	TierFluxHelmRelease Tier = 1 // helm.toolkit.fluxcd.io/{name,namespace}
	TierFluxKustomize   Tier = 2 // kustomize.toolkit.fluxcd.io/{name,namespace}
	TierArgoTrackingID  Tier = 3 // argocd.argoproj.io/tracking-id
	TierArgoInstance    Tier = 4 // argocd.argoproj.io/instance (legacy Argo app label)
	TierHelmRelease     Tier = 5 // meta.helm.sh/release-name — above the label tiers (6-9)
	// TierInstance is Argo CD's DEFAULT resource-tracking label and Helm's
	// standard installation-identity label. It disambiguates installations the
	// way the semantic name/part-of labels cannot (two releases of one chart
	// share app.kubernetes.io/name but differ on /instance), so it ranks above
	// them — but below the explicit Helm-release annotation. NET-NEW.
	TierInstance Tier = 6 // app.kubernetes.io/instance  NET-NEW
	TierPartOf   Tier = 7 // app.kubernetes.io/part-of   NET-NEW
	TierAppName  Tier = 8 // app.kubernetes.io/name      NET-NEW
	TierBareApp  Tier = 9 // bare `app` / name heuristic NET-NEW
)

// Confidence is the rendered trust tier.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"   // tiers 1-4 (Flux / Argo) — core GitOps
	ConfidenceMedium Confidence = "medium" // tiers 5-8 (Helm-release / instance / part-of / name) — badge
	ConfidenceLow    Confidence = "low"    // tier 9 (bare app) — opt-in, never silent
)

func confidenceForTier(t Tier) Confidence {
	switch {
	case t >= TierFluxHelmRelease && t <= TierArgoInstance:
		return ConfidenceHigh
	case t >= TierHelmRelease && t <= TierAppName:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// Signal is one matched app-grouping signal — winner OR retained runner-up.
// Ref is the implied app/manager (Flux/Argo/Helm CR ref for tiers 1-5; a
// synthetic {kind:"app"} ref for tiers 6-8). Retaining these instead of
// returning on first hit is the conflicts[] net-new logic.
type Signal struct {
	Tier       Tier
	Key        string // e.g. "<ns>/HelmRelease/<name>" or "<ns>/app/<name>"
	Ref        Ref    // hand-set Group for tiers 1-5 (Flux groups / "" native Helm); empty for 6-8
	Confidence Confidence
}

// AppOverlay is the OPTIONAL Tier-2 key ABOVE the Subject. Absent (nil) when no
// signal at/above tier 8 (TierAppName) exists -> raw-always: surface degrades to
// Subject-only.
type AppOverlay struct {
	Winner    Signal   // highest-precedence signal (provenance: which tier won)
	Conflicts []Signal // retained runner-ups (NET-NEW: collection + retention)
}

// ResolveOverlay is the Tier-2 entrypoint. It COLLECTS ALL matching signals from
// obj's labels/annotations, sorts by Tier, returns the winner + retained
// conflicts, or nil when nothing reaches TierAppName (TierBareApp alone is opt-in via
// allowBareApp; default off => never silent). SUBSUMES detectManagedByFromMeta
// (tiers 1-5, first-hit-return REPLACED by collect-all). The native-Helm
// sentinel {Kind:"HelmRelease",Group:""} the Source classifier keys on is set
// here; enrichRef MUST NOT be applied to tier 1-5 refs (Group is hand-set) —
// pkg/subject does no dp enrichment at all.
func ResolveOverlay(obj metav1.Object, allowBareApp bool) *AppOverlay {
	if obj == nil {
		return nil
	}
	labels := obj.GetLabels()
	annos := obj.GetAnnotations()
	ns := obj.GetNamespace()

	var signals []Signal

	if name, n := labels[fluxHelmNameLabel], labels[fluxHelmNSLabel]; name != "" && n != "" {
		signals = append(signals, Signal{
			Tier: TierFluxHelmRelease,
			Key:  n + "/HelmRelease/" + name,
			Ref:  Ref{Kind: "HelmRelease", Group: fluxHelmGroup, Namespace: n, Name: name},
		})
	}
	if name, n := labels[fluxKustomizeNameLabel], labels[fluxKustomizeNSLabel]; name != "" && n != "" {
		signals = append(signals, Signal{
			Tier: TierFluxKustomize,
			Key:  n + "/Kustomization/" + name,
			Ref:  Ref{Kind: "Kustomization", Group: fluxKustomizeGroup, Namespace: n, Name: name},
		})
	}
	if id := annos[argoTrackingIDAnnotation]; id != "" {
		if argoNS, argoName, ok := parseArgoTrackingID(id); ok {
			signals = append(signals, Signal{
				Tier: TierArgoTrackingID,
				Key:  argoNS + "/Application/" + argoName,
				Ref:  Ref{Kind: "Application", Group: argoApplicationGroup, Namespace: argoNS, Name: argoName},
			})
		}
	}
	if n := annos[helmReleaseNameAnno]; n != "" {
		// Native Helm: Group deliberately empty to distinguish from Flux's
		// HelmRelease CR — the sentinel sourceForOwner classifies "helm" off.
		signals = append(signals, Signal{
			Tier: TierHelmRelease,
			Key:  annos[helmReleaseNSAnno] + "/HelmRelease/" + n,
			Ref:  Ref{Kind: "HelmRelease", Group: "", Namespace: annos[helmReleaseNSAnno], Name: n},
		})
	}
	if n := labels[argoInstanceLabel]; n != "" {
		signals = append(signals, Signal{
			Tier: TierArgoInstance,
			Key:  "/Application/" + n,
			Ref:  Ref{Kind: "Application", Group: argoApplicationGroup, Namespace: "", Name: n},
		})
	}
	if n := labels[appInstanceLabel]; n != "" {
		// Same "/app/" key shape as part-of/name: an instance value unifies with
		// matching name/part-of values (same app). Argo's default tracking writes
		// the app name here; Helm writes the release name — both are the grouping
		// we want. Env-specific by nature, so this is excluded as a CROSS-cluster
		// join key in the Hub (single-cluster overlay only).
		signals = append(signals, Signal{
			Tier: TierInstance,
			Key:  ns + "/app/" + n,
			Ref:  Ref{Kind: "app", Name: n, Namespace: ns},
		})
	}
	if n := labels[partOfLabel]; n != "" {
		signals = append(signals, Signal{
			Tier: TierPartOf,
			Key:  ns + "/app/" + n,
			Ref:  Ref{Kind: "app", Name: n, Namespace: ns},
		})
	}
	if n := labels[appNameLabel]; n != "" {
		signals = append(signals, Signal{
			Tier: TierAppName,
			Key:  ns + "/app/" + n,
			Ref:  Ref{Kind: "app", Name: n, Namespace: ns},
		})
	}
	if n := labels[bareAppLabel]; n != "" {
		signals = append(signals, Signal{
			Tier: TierBareApp,
			Key:  ns + "/app/" + n,
			Ref:  Ref{Kind: "app", Name: n, Namespace: ns},
		})
	}

	if len(signals) == 0 {
		return nil
	}

	sort.SliceStable(signals, func(i, j int) bool { return signals[i].Tier < signals[j].Tier })
	for i := range signals {
		signals[i].Confidence = confidenceForTier(signals[i].Tier)
	}

	winner := signals[0]
	// Raw-always: a bare-app-only overlay is opt-in. Without allowBareApp,
	// TierBareApp alone means no overlay — never silent.
	if winner.Tier > TierAppName && !allowBareApp {
		return nil
	}

	return &AppOverlay{
		Winner:    winner,
		Conflicts: signals[1:],
	}
}

// parseArgoTrackingID parses ArgoCD's tracking-id annotation in either format:
//
//	"<appName>:<group>/<kind>:<resourceNs>/<resourceName>"               (default)
//	"<appNamespace>_<appName>:<group>/<kind>:<resourceNs>/<resourceName>" (namespaced)
//
// Returns (namespace, name, ok). The legacy single-name form yields an empty
// namespace.
func parseArgoTrackingID(value string) (namespace, name string, ok bool) {
	firstColon := strings.Index(value, ":")
	if firstColon < 0 {
		return "", "", false
	}
	head := value[:firstColon]
	sep := strings.Index(head, "_")
	if sep < 0 {
		if head == "" {
			return "", "", false
		}
		return "", head, true
	}
	ns := head[:sep]
	n := head[sep+1:]
	if n == "" {
		return "", "", false
	}
	return ns, n, true
}
