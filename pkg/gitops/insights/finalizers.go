package insights

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FinalizerOwner identifies the controller responsible for processing a
// finalizer key during deletion. The mapping is best-effort: the K8s API
// doesn't expose finalizer ownership, so we rely on a static catalog of
// known controllers (Argo, Flux). The catalog is documented inline so
// future Flux/Argo releases that introduce new finalizer keys are easy
// to add — see the pattern below.
//
// SelectorKey + SelectorValue describe a label selector to find the
// controller's pods in its install namespace. Most controllers ship with
// `app.kubernetes.io/name=<controller>` or `app=<controller>` labels;
// when both conventions appear in the wild, ResolveFinalizerOwner
// returns the entry that matches first via best-effort lookup at the
// resolver layer.
type FinalizerOwner struct {
	// Controller is the human-readable controller name surfaced in
	// Issue messages ("argocd-application-controller is CrashLoopBackOff").
	Controller string
	// Namespace is the typical install namespace. We don't dynamically
	// discover this — operators do customize it (e.g. argocd-system,
	// custom flux namespaces) but the conventional defaults cover the
	// vast majority of installs.
	Namespace string
	// SelectorKey + SelectorValue: label selector to find the
	// controller's pods.
	SelectorKey   string
	SelectorValue string
}

// ResolveFinalizerOwner returns a best-guess FinalizerOwner for the
// given finalizer key + resource. The `root` resource's apiVersion is
// used to disambiguate generic Flux finalizers that span multiple
// controllers (a single `finalizers.fluxcd.io` key is set by every Flux
// controller; the resource kind tells us which one).
//
// Returns nil when the finalizer isn't recognized — caller should
// degrade gracefully (skip the controller-health enrichment, keep the
// rest of the Issue intact).
func ResolveFinalizerOwner(finalizer string, root *unstructured.Unstructured) *FinalizerOwner {
	switch finalizer {
	// Argo CD: a single Application controller owns all Argo-side
	// finalizers, including the deprecated "foreground-cascade" key
	// retained for installs that pre-date the rename.
	case "resources-finalizer.argocd.argoproj.io",
		"foreground-cascade.argocd.argoproj.io",
		"resources-finalizer.argocd.argoproj.io/foreground":
		return &FinalizerOwner{
			Controller:    "argocd-application-controller",
			Namespace:     "argocd",
			SelectorKey:   "app.kubernetes.io/name",
			SelectorValue: "argocd-application-controller",
		}

	// Flux: legacy "finalizers.fluxcd.io" key is shared across all
	// Flux controllers. Disambiguate via the resource's apiVersion.
	case "finalizers.fluxcd.io":
		return resolveFluxOwnerByKind(root)

	// Flux: post-1.0 split finalizer keys per controller. Names are
	// stable since Flux v2.0+ — newer installs default to these.
	case "finalizers.kustomize.toolkit.fluxcd.io":
		return &fluxKustomizeController
	case "finalizers.helm.toolkit.fluxcd.io":
		return &fluxHelmController
	case "finalizers.source.toolkit.fluxcd.io":
		return &fluxSourceController
	case "finalizers.notification.toolkit.fluxcd.io":
		return &fluxNotificationController
	case "finalizers.image.toolkit.fluxcd.io":
		return &fluxImageController
	}
	return nil
}

// resolveFluxOwnerByKind disambiguates the generic "finalizers.fluxcd.io"
// finalizer by the resource's API group. Returns nil when the resource
// is from an unfamiliar Flux group — better to skip the enrichment than
// to misattribute to the wrong controller and send the operator
// debugging the wrong pod.
func resolveFluxOwnerByKind(root *unstructured.Unstructured) *FinalizerOwner {
	api := strings.ToLower(root.GetAPIVersion())
	switch {
	case strings.HasPrefix(api, "kustomize.toolkit.fluxcd.io/"):
		return &fluxKustomizeController
	case strings.HasPrefix(api, "helm.toolkit.fluxcd.io/"):
		return &fluxHelmController
	case strings.HasPrefix(api, "source.toolkit.fluxcd.io/"):
		return &fluxSourceController
	case strings.HasPrefix(api, "notification.toolkit.fluxcd.io/"):
		return &fluxNotificationController
	case strings.HasPrefix(api, "image.toolkit.fluxcd.io/"):
		return &fluxImageController
	}
	return nil
}

// Standard Flux controller catalog. Values match the official Helm chart
// defaults for `flux-system`; custom-namespace installs return nil from
// the lookup intentionally — omitting the controller-health line beats
// pointing the operator at the wrong pod.
var (
	fluxKustomizeController = FinalizerOwner{
		Controller:    "kustomize-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "kustomize-controller",
	}
	fluxHelmController = FinalizerOwner{
		Controller:    "helm-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "helm-controller",
	}
	fluxSourceController = FinalizerOwner{
		Controller:    "source-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "source-controller",
	}
	fluxNotificationController = FinalizerOwner{
		Controller:    "notification-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "notification-controller",
	}
	fluxImageController = FinalizerOwner{
		Controller:    "image-reflector-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "image-reflector-controller",
	}
)
