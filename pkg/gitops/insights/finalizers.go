package insights

import (
	"path"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FinalizerOwner identifies the controller responsible for processing a
// finalizer key during deletion. The mapping is best-effort: the K8s API
// doesn't expose finalizer ownership, so we rely on a static catalog of
// known controllers. The catalog is documented inline so
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
	// discover this here; empty means no conventional install namespace.
	// Operators do customize it (e.g. argocd-system,
	// custom flux namespaces) but the conventional defaults cover the
	// vast majority of installs.
	Namespace string
	// SelectorKey + SelectorValue: label selector to find the
	// controller's pods.
	SelectorKey   string
	SelectorValue string
	// OffClusterOfferings names managed offerings that run this controller
	// outside the cluster it serves. For these controllers, finding none of
	// their pods in the cluster does not show that they are gone.
	OffClusterOfferings string
	// ReleasesInfrastructure marks finalizers whose cleanup drains nodes or
	// releases cloud resources. Removing one leaves that infrastructure
	// behind even when the controller is down.
	ReleasesInfrastructure bool
	// Images are the repository paths of the controller's published image,
	// without registry host, tag or digest, so a mirror that keeps the path
	// still matches. They recognize installs whose names and labels were
	// changed (a Helm nameOverride renames both).
	Images []string
	// ImageBinary, when set, must also be the binary the container runs: the
	// image ships several components and only this one is the controller.
	ImageBinary string
}

// ResolveFinalizerOwner returns a best-guess FinalizerOwner for the
// given finalizer key + resource. The `root` resource's apiVersion is
// used to disambiguate generic Flux finalizers that span multiple
// controllers (a single `finalizers.fluxcd.io` key is set by every Flux
// controller; the resource kind tells us which one).
//
// Returns nil when the finalizer isn't recognized — caller should
// degrade gracefully (skip the controller-health enrichment, keep the
// rest of the Issue intact). Namespace may be empty; callers must not use
// an empty namespace to list controller pods cluster-wide.
func ResolveFinalizerOwner(finalizer string, root *unstructured.Unstructured) *FinalizerOwner {
	if strings.HasPrefix(finalizer, "karpenter.sh/") || strings.HasPrefix(finalizer, "karpenter.k8s.aws/") || strings.HasPrefix(finalizer, "karpenter.azure.com/") {
		return &FinalizerOwner{
			Controller:             "karpenter",
			SelectorKey:            "app.kubernetes.io/name",
			SelectorValue:          "karpenter",
			OffClusterOfferings:    "EKS Auto Mode, AKS node auto-provisioning",
			ReleasesInfrastructure: true,
			// public.ecr.aws/karpenter/controller (AWS provider chart),
			// mcr.microsoft.com/aks/karpenter/controller (Azure provider chart).
			Images: []string{"karpenter/controller", "aks/karpenter/controller"},
		}
	}
	switch finalizer {
	case "apps.victoriametrics.com/finalizer":
		if resourceid.GroupFromAPIVersion(root.GetAPIVersion()) != "operator.victoriametrics.com" {
			return nil
		}
		return &FinalizerOwner{Controller: "victoria-metrics-operator", SelectorKey: "app.kubernetes.io/name", SelectorValue: "victoria-metrics-operator", Images: []string{"victoriametrics/operator"}}

	// Argo CD: a single Application controller owns all Argo-side
	// finalizers, including the deprecated "foreground-cascade" key
	// retained for installs that pre-date the rename.
	case "resources-finalizer.argocd.argoproj.io",
		"foreground-cascade.argocd.argoproj.io",
		"resources-finalizer.argocd.argoproj.io/foreground":
		return &FinalizerOwner{
			Controller:          "argocd-application-controller",
			Namespace:           "argocd",
			SelectorKey:         "app.kubernetes.io/name",
			SelectorValue:       "argocd-application-controller",
			OffClusterOfferings: "Amazon EKS Capabilities",
			// quay.io/argoproj/argocd also runs the API server, repo server
			// and the other Argo CD components.
			Images:      []string{"argoproj/argocd"},
			ImageBinary: "argocd-application-controller",
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
		return resolveFluxImageOwner(root)
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
		return resolveFluxImageOwner(root)
	}
	return nil
}

// Two controllers serve image.toolkit.fluxcd.io: image-automation-controller
// owns ImageUpdateAutomation, image-reflector-controller the rest.
func resolveFluxImageOwner(root *unstructured.Unstructured) *FinalizerOwner {
	if root.GetKind() == "ImageUpdateAutomation" {
		return &fluxImageAutomationController
	}
	return &fluxImageController
}

// Standard Flux controller catalog. Values match the official Helm chart
// defaults for `flux-system`; images are ghcr.io/fluxcd/<controller>.
var (
	fluxKustomizeController = FinalizerOwner{
		Controller:    "kustomize-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "kustomize-controller",
		Images:        []string{"fluxcd/kustomize-controller"},
	}
	fluxHelmController = FinalizerOwner{
		Controller:    "helm-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "helm-controller",
		Images:        []string{"fluxcd/helm-controller"},
	}
	fluxSourceController = FinalizerOwner{
		Controller:    "source-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "source-controller",
		Images:        []string{"fluxcd/source-controller"},
	}
	fluxNotificationController = FinalizerOwner{
		Controller:    "notification-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "notification-controller",
		Images:        []string{"fluxcd/notification-controller"},
	}
	fluxImageController = FinalizerOwner{
		Controller:    "image-reflector-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "image-reflector-controller",
		Images:        []string{"fluxcd/image-reflector-controller"},
	}
	fluxImageAutomationController = FinalizerOwner{
		Controller:    "image-automation-controller",
		Namespace:     "flux-system",
		SelectorKey:   "app",
		SelectorValue: "image-automation-controller",
		Images:        []string{"fluxcd/image-automation-controller"},
	}
)

// MatchesWorkload reports whether a workload runs this owner's controller,
// recognized by the workload's name or labels, or by its pod template's
// labels or container image.
func (o *FinalizerOwner) MatchesWorkload(workload metav1.Object, template corev1.PodTemplateSpec) bool {
	return workload.GetLabels()[o.SelectorKey] == o.SelectorValue || controllerNameMatches(workload.GetName(), o.Controller) || o.MatchesPod(template.Labels, template.Spec)
}

// MatchesPod reports whether a pod (or pod template) runs this owner's
// controller, recognized by the catalog label or a container image.
func (o *FinalizerOwner) MatchesPod(labels map[string]string, spec corev1.PodSpec) bool {
	if labels[o.SelectorKey] == o.SelectorValue {
		return true
	}
	for _, container := range spec.Containers {
		if o.runsImage(container) {
			return true
		}
	}
	return false
}

func (o *FinalizerOwner) runsImage(container corev1.Container) bool {
	repository := ImageRepositoryPath(container.Image)
	if !slices.Contains(o.Images, repository) {
		return false
	}
	if repository == "victoriametrics/operator" {
		// The reloader shares the operator's repository; its container name
		// still identifies that role when the image is pinned by digest.
		image, _, _ := strings.Cut(container.Image, "@")
		_, tag, _ := strings.Cut(image[strings.LastIndex(image, "/")+1:], ":")
		if container.Name == "config-reloader" || strings.HasPrefix(tag, "config-reloader") {
			return false
		}
	}
	if o.ImageBinary == "" {
		return true
	}
	for _, token := range slices.Concat(container.Command, container.Args) {
		if path.Base(token) == o.ImageBinary {
			return true
		}
	}
	return false
}

// ImageRepositoryPath returns an image reference's repository path without
// its registry host, tag or digest: "ghcr.io/fluxcd/helm-controller:v1@sha256:…"
// and "fluxcd/helm-controller" both give "fluxcd/helm-controller".
func ImageRepositoryPath(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	// Like Docker, the first segment is a registry host only when it has a dot
	// or a port, or is localhost; otherwise it is a Docker Hub namespace.
	if host, rest, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(host, ".:") || host == "localhost") {
		ref = rest
	}
	return ref
}

// FinalizerControllerCandidate reports a workload whose name or labels match
// an uncataloged finalizer's API group: the group's first DNS label plus
// "-operator" or "operator-". The match names a candidate only. A short label
// can match an unrelated operator (group cluster.example.io matches
// rabbitmq-cluster-operator), so callers must not treat the candidate as the
// finalizer's owner: not to judge it stopped or gone, not to raise severity,
// not to offer removing the finalizer. Domain alone never matches.
func FinalizerControllerCandidate(finalizer string, root *unstructured.Unstructured, workload metav1.Object) bool {
	if ResolveFinalizerOwner(finalizer, root) != nil {
		return false
	}
	domain, _, qualified := strings.Cut(finalizer, "/")
	group := resourceid.GroupFromAPIVersion(root.GetAPIVersion())
	if !qualified || domain != group || group == "k8s.io" || group == "kubernetes.io" || strings.HasSuffix(group, ".k8s.io") || strings.HasSuffix(group, ".kubernetes.io") {
		return false
	}
	token, _, _ := strings.Cut(group, ".")
	if token == "" || token == "operator" || token == "operators" || token == "apps" {
		return false
	}
	for _, value := range []string{workload.GetName(), workload.GetLabels()["app.kubernetes.io/name"], workload.GetLabels()["app.kubernetes.io/part-of"]} {
		if controllerNameMatches(value, token+"-operator") || controllerNameMatches(value, "operator-"+token) {
			return true
		}
	}
	return false
}

func controllerNameMatches(name, controller string) bool {
	return name == controller || strings.HasPrefix(name, controller+"-") || strings.HasSuffix(name, "-"+controller) || strings.Contains(name, "-"+controller+"-")
}
