// Package finalizers recognizes finalizer owners from vendor keys and workload
// identity. It performs no cluster reads or issue-policy decisions.
package finalizers

import (
	"path"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Owner records controller identity and the reviewed limits of its cleanup advice.
type Owner struct {
	// Controller is the human-readable controller name surfaced in
	// Issue messages ("argocd-application-controller is CrashLoopBackOff").
	Controller string
	// Namespace is the conventional install namespace for GitOps enrichment;
	// the generic issues inventory searches all readable namespaces.
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
	// AllowConditionalCleanup opts into removal advice after a complete negative
	// search, subject to the user confirming intentional uninstall or permanent
	// scale-down. Recognition alone never enables removal. Leave false for
	// unreviewed cleanup semantics, off-cluster controllers or infrastructure.
	AllowConditionalCleanup bool
	// Images are the repository paths of the controller's published image,
	// without registry host, tag or digest, so a mirror that prefixes the path
	// still matches. They recognize installs whose names and labels were
	// changed (a Helm nameOverride renames both).
	Images []string
	// matchesImageRole distinguishes components sharing a repository. A nil
	// predicate means every container from Images is this controller.
	matchesImageRole  func(corev1.Container) bool
	requireImageMatch bool
}

// ResolveOwner returns a best-guess Owner for the
// given finalizer key + resource. The `root` resource's apiVersion is
// used to disambiguate generic Flux finalizers that span multiple
// controllers (a single `finalizers.fluxcd.io` key is set by every Flux
// controller; the resource kind tells us which one).
//
// Returns nil when the finalizer isn't recognized — caller should
// degrade gracefully (skip the controller-health enrichment, keep the
// rest of the Issue intact). Namespace is a convention, not a search boundary.
func ResolveOwner(finalizer string, root *unstructured.Unstructured) *Owner {
	if strings.HasPrefix(finalizer, "karpenter.sh/") || strings.HasPrefix(finalizer, "karpenter.k8s.aws/") || strings.HasPrefix(finalizer, "karpenter.azure.com/") {
		return &Owner{
			Controller:              "karpenter",
			SelectorKey:             "app.kubernetes.io/name",
			SelectorValue:           "karpenter",
			OffClusterOfferings:     "EKS Auto Mode, AKS node auto-provisioning",
			ReleasesInfrastructure:  true,
			AllowConditionalCleanup: false,
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
		return &Owner{
			Controller:              "victoria-metrics-operator",
			SelectorKey:             "app.kubernetes.io/name",
			SelectorValue:           "victoria-metrics-operator",
			Images:                  []string{"victoriametrics/operator"},
			matchesImageRole:        runsVictoriaMetricsOperator,
			AllowConditionalCleanup: true,
		}

	// Argo CD: a single Application controller owns all Argo-side
	// finalizers, including the deprecated "foreground-cascade" key
	// retained for installs that pre-date the rename.
	case "resources-finalizer.argocd.argoproj.io",
		"foreground-cascade.argocd.argoproj.io",
		"resources-finalizer.argocd.argoproj.io/foreground":
		return &Owner{
			Controller:              "argocd-application-controller",
			Namespace:               "argocd",
			SelectorKey:             "app.kubernetes.io/name",
			SelectorValue:           "argocd-application-controller",
			OffClusterOfferings:     "Amazon EKS Capabilities",
			AllowConditionalCleanup: false,
			// quay.io/argoproj/argocd also runs the API server, repo server
			// and the other Argo CD components.
			Images:           []string{"argoproj/argocd"},
			matchesImageRole: runsArgoApplicationController,
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
func resolveFluxOwnerByKind(root *unstructured.Unstructured) *Owner {
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
func resolveFluxImageOwner(root *unstructured.Unstructured) *Owner {
	if root.GetKind() == "ImageUpdateAutomation" {
		return &fluxImageAutomationController
	}
	return &fluxImageController
}

// Standard Flux controller catalog. Values match the official Helm chart
// defaults for `flux-system`; images are ghcr.io/fluxcd/<controller>.
var (
	fluxKustomizeController = Owner{
		Controller:              "kustomize-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "kustomize-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/kustomize-controller"},
	}
	fluxHelmController = Owner{
		Controller:              "helm-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "helm-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/helm-controller"},
		// Other projects also publish a controller named helm-controller.
		requireImageMatch: true,
	}
	fluxSourceController = Owner{
		Controller:              "source-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "source-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/source-controller"},
	}
	fluxNotificationController = Owner{
		Controller:              "notification-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "notification-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/notification-controller"},
	}
	fluxImageController = Owner{
		Controller:              "image-reflector-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "image-reflector-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/image-reflector-controller"},
	}
	fluxImageAutomationController = Owner{
		Controller:              "image-automation-controller",
		Namespace:               "flux-system",
		SelectorKey:             "app",
		SelectorValue:           "image-automation-controller",
		AllowConditionalCleanup: true,
		Images:                  []string{"fluxcd/image-automation-controller"},
	}
)

// MatchesWorkload reports whether a workload runs this owner's controller,
// recognized by the workload's name or labels, or by its pod template's
// labels or container image.
func (o *Owner) MatchesWorkload(workload metav1.Object, template corev1.PodTemplateSpec) bool {
	return (!o.requireImageMatch && o.MatchesWorkloadIdentity(workload, template)) || o.MatchesPod(template.Labels, template.Spec)
}

// MatchesWorkloadIdentity only checks names and labels. When an image is
// required, this resemblance must not establish ownership or absence.
func (o *Owner) MatchesWorkloadIdentity(workload metav1.Object, template corev1.PodTemplateSpec) bool {
	return workload.GetLabels()[o.SelectorKey] == o.SelectorValue || controllerNameMatches(workload.GetName(), o.Controller) || template.Labels[o.SelectorKey] == o.SelectorValue
}

// MatchesPod reports whether a pod (or pod template) runs this owner's
// controller, recognized by the catalog label or a container image.
func (o *Owner) MatchesPod(labels map[string]string, spec corev1.PodSpec) bool {
	if !o.requireImageMatch && labels[o.SelectorKey] == o.SelectorValue {
		return true
	}
	for _, container := range spec.Containers {
		if o.runsImage(container) {
			return true
		}
	}
	return false
}

func (o *Owner) runsImage(container corev1.Container) bool {
	repository := imageRepositoryPath(container.Image)
	if !slices.ContainsFunc(o.Images, func(image string) bool {
		return repository == image || strings.HasSuffix(repository, "/"+image)
	}) {
		return false
	}
	return o.matchesImageRole == nil || o.matchesImageRole(container)
}

func runsVictoriaMetricsOperator(container corev1.Container) bool {
	// The reloader shares the operator's repository; its container name
	// still identifies that role when the image is pinned by digest.
	image, _, _ := strings.Cut(container.Image, "@")
	_, tag, _ := strings.Cut(image[strings.LastIndex(image, "/")+1:], ":")
	return container.Name != "config-reloader" && !strings.HasPrefix(tag, "config-reloader")
}

func runsArgoApplicationController(container corev1.Container) bool {
	for _, token := range slices.Concat(container.Command, container.Args) {
		if path.Base(token) == "argocd-application-controller" {
			return true
		}
	}
	return false
}

// imageRepositoryPath returns an image reference's repository path without
// its registry host, tag or digest: "ghcr.io/fluxcd/helm-controller:v1@sha256:…"
// and "fluxcd/helm-controller" both give "fluxcd/helm-controller".
func imageRepositoryPath(image string) string {
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

// ControllerCandidate reports a workload whose name or labels match
// an uncataloged finalizer's API group: the group's first DNS label plus
// "-operator" or "operator-". The match names a candidate only. A short label
// can match an unrelated operator (group cluster.example.io matches
// rabbitmq-cluster-operator), so callers must not treat the candidate as the
// finalizer's owner: not to judge it stopped or gone, not to raise severity,
// not to offer removing the finalizer. Domain alone never matches.
func ControllerCandidate(finalizer string, root *unstructured.Unstructured, workload metav1.Object) bool {
	if ResolveOwner(finalizer, root) != nil {
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
