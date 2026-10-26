package gitops

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ClaimSource names the metadata a claim was read from.
type ClaimSource string

const (
	ClaimFluxHelmRelease   ClaimSource = "flux-helm-labels"
	ClaimFluxKustomization ClaimSource = "flux-kustomize-labels"
	ClaimArgoTrackingID    ClaimSource = "argo-tracking-id"
	ClaimArgoInstanceLabel ClaimSource = "argo-instance-label"
	ClaimHelmRelease       ClaimSource = "helm-release"
)

var (
	argoApplicationKind   = resourceid.GroupKind{Group: "argoproj.io", Kind: "Application"}
	fluxKustomizationKind = resourceid.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}
	fluxHelmReleaseKind   = resourceid.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}
)

// HelmReleaseKind is the kind of a native Helm release a claim names: Helm
// keeps no object of that kind, so the claim can't be checked against a list.
var HelmReleaseKind = resourceid.GroupKind{Kind: "HelmRelease"}

const (
	argoTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel        = "argocd.argoproj.io/instance"
	fluxKustomizeNameLabel   = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNSLabel     = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmNameLabel        = "helm.toolkit.fluxcd.io/name"
	fluxHelmNSLabel          = "helm.toolkit.fluxcd.io/namespace"
	helmReleaseNameAnno      = "meta.helm.sh/release-name"
	helmReleaseNSAnno        = "meta.helm.sh/release-namespace"
)

// Claim is what an object's own metadata says manages it. It is a candidate,
// not a verdict: a manager decides membership by its own rules (Argo CD's
// tracking method and installation ID, which Radar may not be able to read),
// and its status list is the evidence of what it applied.
type Claim struct {
	// Manager is the manager the metadata names. Its namespace is empty when
	// the metadata doesn't say, as with an Argo CD instance label naming an
	// Application in Argo CD's own namespace.
	Manager resourceid.Ref
	Source  ClaimSource
	// Copied: an Argo CD tracking ID naming another object, as when a Secret
	// was copied with its source's annotations. Argo CD doesn't count such an
	// object as its own.
	Copied bool
}

// Claims returns every claim the metadata of object makes, in a fixed order
// (Flux, Argo CD, Helm). A malformed value makes no claim.
func Claims(object resourceid.Ref, labels, annotations map[string]string) []Claim {
	var out []Claim
	if name, ns := labels[fluxHelmNameLabel], labels[fluxHelmNSLabel]; name != "" && ns != "" {
		out = append(out, Claim{Manager: resourceid.Ref{Group: fluxHelmReleaseKind.Group, Kind: fluxHelmReleaseKind.Kind, Namespace: ns, Name: name}, Source: ClaimFluxHelmRelease})
	}
	if name, ns := labels[fluxKustomizeNameLabel], labels[fluxKustomizeNSLabel]; name != "" && ns != "" {
		out = append(out, Claim{Manager: resourceid.Ref{Group: fluxKustomizationKind.Group, Kind: fluxKustomizationKind.Kind, Namespace: ns, Name: name}, Source: ClaimFluxKustomization})
	}
	if id := annotations[argoTrackingIDAnnotation]; id != "" {
		if app, tracked, ok := parseTrackingID(id); ok {
			if object.Namespace == "" {
				// Argo CD writes its destination namespace into a
				// cluster-scoped object's ID and ignores it when comparing.
				tracked.Namespace = ""
			}
			out = append(out, Claim{Manager: app, Source: ClaimArgoTrackingID, Copied: tracked != object})
		}
	}
	if v := labels[argoInstanceLabel]; v != "" {
		if app, ok := argoApplication(v); ok {
			out = append(out, Claim{Manager: app, Source: ClaimArgoInstanceLabel})
		}
	}
	if name := annotations[helmReleaseNameAnno]; name != "" {
		out = append(out, Claim{Manager: resourceid.Ref{Kind: HelmReleaseKind.Kind, Namespace: annotations[helmReleaseNSAnno], Name: name}, Source: ClaimHelmRelease})
	}
	return out
}

// parseTrackingID parses Argo CD's tracking annotation,
// "<app>:<group>/<kind>:<namespace>/<name>", where <app> is "<namespace>_<name>"
// for an Application outside Argo CD's own namespace. tracked is the object
// the annotation says it is on.
func parseTrackingID(id string) (app, tracked resourceid.Ref, ok bool) {
	head, rest, ok := strings.Cut(id, ":")
	if !ok {
		return app, tracked, false
	}
	if app, ok = argoApplication(head); !ok {
		return app, tracked, false
	}
	groupKind, namespaceName, ok := strings.Cut(rest, ":")
	if !ok {
		return app, tracked, false
	}
	group, kind, ok := strings.Cut(groupKind, "/")
	if !ok || kind == "" {
		return app, tracked, false
	}
	namespace, name, ok := strings.Cut(namespaceName, "/")
	if !ok || name == "" {
		return app, tracked, false
	}
	return app, resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}, true
}

// argoApplication reads an Application's identity from a tracking value:
// "<name>", or "<namespace>_<name>". Kubernetes names can't contain "_".
func argoApplication(v string) (resourceid.Ref, bool) {
	ref := resourceid.Ref{Group: argoApplicationKind.Group, Kind: argoApplicationKind.Kind}
	if ns, name, ok := strings.Cut(v, "_"); ok {
		ref.Namespace, ref.Name = ns, name
	} else {
		ref.Name = v
	}
	return ref, ref.Name != "" && (ref.Namespace != "" || !strings.Contains(v, "_"))
}
