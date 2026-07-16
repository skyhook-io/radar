package cloudinstall

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/subject"
)

type OwnershipClassification string

const (
	OwnershipGitOpsVerified   OwnershipClassification = "gitops_verified"
	OwnershipGitOpsSuspected  OwnershipClassification = "gitops_suspected"
	OwnershipGitOpsUnreadable OwnershipClassification = "gitops_unreadable"
	OwnershipGitOpsStale      OwnershipClassification = "gitops_stale"
	OwnershipAmbiguous        OwnershipClassification = "ambiguous"
	OwnershipNativeHelm       OwnershipClassification = "native_helm"
	OwnershipGeneric          OwnershipClassification = "generic"
)

type ControllerVerification string

const (
	ControllerVerified   ControllerVerification = "verified"
	ControllerSuspected  ControllerVerification = "suspected"
	ControllerUnreadable ControllerVerification = "unreadable"
	ControllerStale      ControllerVerification = "stale"
	ControllerAmbiguous  ControllerVerification = "ambiguous"
)

type ControllerCandidate struct {
	Ref              subject.Ref
	Verification     ControllerVerification
	Matches          []subject.Ref
	ReleaseName      string
	TargetNamespace  string
	StorageNamespace string
	Error            string
}

type TargetOwnership struct {
	Classification          OwnershipClassification
	Controllers             []ControllerCandidate
	NativeHelm              *subject.Ref
	NativeHelmMatchesTarget bool
}

type RadarTarget struct {
	Namespace      string
	DeploymentName string
	Chart          string
	ReleaseName    string
	Runtime        DeploymentRuntime
	Ownership      TargetOwnership
}

type DeploymentRuntime struct {
	Image                 string
	DesiredReplicas       int32
	TotalReplicas         int32
	UpdatedReplicas       int32
	ReadyReplicas         int32
	AvailableReplicas     int32
	StatusObserved        bool
	AuthMode              string
	CloudModeConfigured   bool
	CloudMode             bool
	CloudModeUnresolved   bool
	CloudURL              string
	CloudURLConfigured    bool
	CloudURLUnresolved    bool
	ClusterName           string
	ClusterNameConfigured bool
	ClusterNameUnresolved bool
	CloudTokenConfigured  bool
	CloudTokenSecretName  string
	CloudTokenSecretKey   string
	AlreadyCloud          bool
}

type DiscoveryOptions struct {
	Namespace   string
	ReleaseName string
	ClusterWide bool
}

// DiscoveryResult deliberately returns candidates instead of selecting one.
// Selected contains only candidates matching the explicit/default target.
type DiscoveryResult struct {
	Selected         []RadarTarget
	Namespace        []RadarTarget
	ClusterWide      []RadarTarget
	ClusterWideError error
}

// DiscoverRadarTargets finds official Radar chart Deployments before Helm
// preparation. A forbidden cluster-wide list does not discard the authoritative
// selected-namespace inspection.
func DiscoverRadarTargets(ctx context.Context, kc kubernetes.Interface, dc dynamic.Interface, opts DiscoveryOptions) (DiscoveryResult, error) {
	if kc == nil {
		return DiscoveryResult{}, fmt.Errorf("discover Radar targets: nil kubernetes client")
	}

	namespace := opts.Namespace
	if namespace == "" {
		namespace = DefaultInstallNamespace
	}
	releaseName := opts.ReleaseName
	if releaseName == "" {
		releaseName = DefaultReleaseName
	}

	deployments, err := kc.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "helm.sh/chart",
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return DiscoveryResult{}, fmt.Errorf("list Radar Deployments in namespace %q: %w", namespace, err)
	}

	result := DiscoveryResult{}
	if err == nil {
		result.Namespace = radarTargets(ctx, deployments.Items, dc)
		for _, target := range result.Namespace {
			if target.ReleaseName == releaseName {
				result.Selected = append(result.Selected, target)
			}
		}
	}

	if !opts.ClusterWide {
		return result, nil
	}

	all, err := kc.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: "helm.sh/chart",
	})
	if err != nil {
		result.ClusterWideError = fmt.Errorf("list Radar Deployments across visible namespaces: %w", err)
		return result, nil
	}
	result.ClusterWide = withoutNamespaceTargets(radarTargets(ctx, all.Items, dc), result.Namespace)
	return result, nil
}

func withoutNamespaceTargets(all, namespace []RadarTarget) []RadarTarget {
	seen := make(map[string]struct{}, len(namespace))
	for _, target := range namespace {
		seen[target.Namespace+"\x00"+target.DeploymentName] = struct{}{}
	}
	other := make([]RadarTarget, 0, len(all))
	for _, target := range all {
		if _, exists := seen[target.Namespace+"\x00"+target.DeploymentName]; exists {
			continue
		}
		other = append(other, target)
	}
	return other
}

func radarTargets(ctx context.Context, deployments []appsv1.Deployment, dc dynamic.Interface) []RadarTarget {
	targets := make([]RadarTarget, 0, len(deployments))
	for i := range deployments {
		deployment := &deployments[i]
		chart := deployment.Labels["helm.sh/chart"]
		if !isOfficialRadarChartLabel(chart) {
			continue
		}
		targets = append(targets, RadarTarget{
			Namespace:      deployment.Namespace,
			DeploymentName: deployment.Name,
			Chart:          chart,
			ReleaseName:    deployment.Labels["app.kubernetes.io/instance"],
			Runtime:        inspectDeploymentRuntime(deployment),
			Ownership:      classifyTargetOwnership(ctx, deployment, dc),
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Namespace != targets[j].Namespace {
			return targets[i].Namespace < targets[j].Namespace
		}
		return targets[i].DeploymentName < targets[j].DeploymentName
	})
	return targets
}

func isOfficialRadarChartLabel(label string) bool {
	if label == chartName {
		return true
	}
	version, ok := strings.CutPrefix(label, chartName+"-")
	if !ok {
		return false
	}
	_, err := semver.StrictNewVersion(version)
	return err == nil
}

func inspectDeploymentRuntime(deployment *appsv1.Deployment) DeploymentRuntime {
	runtime := DeploymentRuntime{AuthMode: "none"}
	if deployment == nil {
		return runtime
	}
	runtime.DesiredReplicas = 1
	if deployment.Spec.Replicas != nil {
		runtime.DesiredReplicas = *deployment.Spec.Replicas
	}
	runtime.TotalReplicas = deployment.Status.Replicas
	runtime.UpdatedReplicas = deployment.Status.UpdatedReplicas
	runtime.ReadyReplicas = deployment.Status.ReadyReplicas
	runtime.AvailableReplicas = deployment.Status.AvailableReplicas
	runtime.StatusObserved = deployment.Status.ObservedGeneration >= deployment.Generation
	var container *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == chartName {
			container = &deployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if container == nil {
		return runtime
	}

	runtime.Image = container.Image
	if value, ok := argumentValue(container.Args, "--auth-mode"); ok && value != "" {
		runtime.AuthMode = value
	}
	runtime.CloudURL, runtime.CloudURLConfigured = argumentValue(container.Args, "--cloud-url")
	runtime.ClusterName, runtime.ClusterNameConfigured = argumentValue(container.Args, "--cluster-name")
	cloudTokenValue, cloudTokenArg := argumentValue(container.Args, "--cloud-token")

	for _, env := range container.Env {
		switch env.Name {
		case "RADAR_CLOUD_MODE":
			runtime.CloudModeConfigured = true
			if env.ValueFrom != nil {
				runtime.CloudModeUnresolved = true
			} else {
				runtime.CloudMode, _ = strconv.ParseBool(env.Value)
			}
		case "RADAR_CLOUD_URL":
			if !runtime.CloudURLConfigured {
				runtime.CloudURL = env.Value
				runtime.CloudURLConfigured = env.Value != "" || env.ValueFrom != nil
				runtime.CloudURLUnresolved = env.ValueFrom != nil
			}
		case "RADAR_CLOUD_CLUSTER_NAME":
			if !runtime.ClusterNameConfigured {
				runtime.ClusterName = env.Value
				runtime.ClusterNameConfigured = env.Value != "" || env.ValueFrom != nil
				runtime.ClusterNameUnresolved = env.ValueFrom != nil
			}
		case "RADAR_CLOUD_TOKEN":
			runtime.CloudTokenConfigured = env.Value != "" || env.ValueFrom != nil
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				runtime.CloudTokenSecretName = env.ValueFrom.SecretKeyRef.Name
				runtime.CloudTokenSecretKey = env.ValueFrom.SecretKeyRef.Key
			}
		}
	}
	runtime.CloudTokenConfigured = runtime.CloudTokenConfigured || (cloudTokenArg && cloudTokenValue != "")
	runtime.AlreadyCloud = runtime.CloudMode || runtime.CloudModeUnresolved || runtime.CloudURLConfigured || runtime.ClusterNameConfigured || runtime.CloudTokenConfigured
	return runtime
}

func argumentValue(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if value, found := strings.CutPrefix(arg, name+"="); found {
			return value, true
		}
	}
	return "", false
}

func classifyTargetOwnership(ctx context.Context, deployment *appsv1.Deployment, dc dynamic.Interface) TargetOwnership {
	overlay := subject.ResolveOverlay(deployment, false)
	if overlay == nil {
		return TargetOwnership{Classification: OwnershipGeneric}
	}

	signals := append([]subject.Signal{overlay.Winner}, overlay.Conflicts...)
	var nativeHelm *subject.Ref
	gitOpsRefs := make([]subject.Ref, 0, len(signals))
	for _, signal := range signals {
		switch signal.Tier {
		case subject.TierFluxHelmRelease, subject.TierFluxKustomize, subject.TierArgoTrackingID, subject.TierArgoInstance:
			gitOpsRefs = appendControllerRef(gitOpsRefs, signal.Ref)
		case subject.TierHelmRelease:
			ref := signal.Ref
			nativeHelm = &ref
		}
	}

	nativeHelmMatchesTarget := nativeHelm != nil &&
		nativeHelm.Name == deployment.Labels["app.kubernetes.io/instance"] &&
		(nativeHelm.Namespace == "" || nativeHelm.Namespace == deployment.Namespace)
	if len(gitOpsRefs) == 0 {
		if nativeHelm != nil {
			classification := OwnershipNativeHelm
			if !nativeHelmMatchesTarget {
				classification = OwnershipAmbiguous
			}
			return TargetOwnership{
				Classification:          classification,
				NativeHelm:              nativeHelm,
				NativeHelmMatchesTarget: nativeHelmMatchesTarget,
			}
		}
		return TargetOwnership{Classification: OwnershipGeneric}
	}

	ownership := TargetOwnership{
		NativeHelm:              nativeHelm,
		NativeHelmMatchesTarget: nativeHelmMatchesTarget,
	}
	for _, ref := range gitOpsRefs {
		ownership.Controllers = append(ownership.Controllers, verifyController(
			ctx,
			dc,
			ref,
			deployment.Namespace,
			deployment.Labels["app.kubernetes.io/instance"],
		))
	}
	ownership.Classification = controllerClassification(ownership.Controllers)
	return ownership
}

func appendControllerRef(refs []subject.Ref, candidate subject.Ref) []subject.Ref {
	for i := range refs {
		ref := refs[i]
		if ref.Group != candidate.Group || ref.Kind != candidate.Kind || ref.Name != candidate.Name {
			continue
		}
		if ref.Namespace == candidate.Namespace || ref.Namespace == "" || candidate.Namespace == "" {
			if refs[i].Namespace == "" && candidate.Namespace != "" {
				refs[i] = candidate
			}
			return refs
		}
	}
	return append(refs, candidate)
}

func controllerClassification(candidates []ControllerCandidate) OwnershipClassification {
	verified := 0
	uncertain := 0
	stale := 0
	for _, candidate := range candidates {
		switch candidate.Verification {
		case ControllerVerified:
			verified++
		case ControllerStale:
			stale++
		case ControllerSuspected, ControllerUnreadable, ControllerAmbiguous:
			uncertain++
		}
	}

	if verified > 1 || (verified == 1 && uncertain > 0) || len(candidates) > 1 && uncertain > 0 {
		return OwnershipAmbiguous
	}
	if verified == 1 {
		return OwnershipGitOpsVerified
	}
	if uncertain > 0 {
		for _, candidate := range candidates {
			if candidate.Verification == ControllerAmbiguous {
				return OwnershipAmbiguous
			}
		}
		for _, candidate := range candidates {
			if candidate.Verification == ControllerUnreadable {
				return OwnershipGitOpsUnreadable
			}
		}
		return OwnershipGitOpsSuspected
	}
	if stale == len(candidates) {
		return OwnershipGitOpsStale
	}
	return OwnershipGitOpsSuspected
}

func verifyController(ctx context.Context, dc dynamic.Interface, ref subject.Ref, targetNamespace, releaseName string) ControllerCandidate {
	candidate := ControllerCandidate{Ref: ref}
	if dc == nil {
		candidate.Verification = ControllerSuspected
		return candidate
	}

	gvrs := controllerGVRs(ref)
	if len(gvrs) == 0 {
		candidate.Verification = ControllerSuspected
		return candidate
	}

	if ref.Namespace == "" && ref.Group == "argoproj.io" && ref.Kind == "Application" {
		return verifyClusterVisibleArgoApplication(ctx, dc, ref, gvrs)
	}

	for _, gvr := range gvrs {
		obj, err := dc.Resource(gvr).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err == nil {
			if ref.Group == "helm.toolkit.fluxcd.io" && ref.Kind == "HelmRelease" {
				candidate.ReleaseName, candidate.TargetNamespace, candidate.StorageNamespace = fluxHelmTarget(obj)
				if releaseName == "" || candidate.ReleaseName != releaseName || candidate.TargetNamespace != targetNamespace {
					candidate.Verification = ControllerStale
					candidate.Error = fmt.Sprintf(
						"HelmRelease targets release %q in namespace %q, not %q in namespace %q",
						candidate.ReleaseName,
						candidate.TargetNamespace,
						releaseName,
						targetNamespace,
					)
					return candidate
				}
			}
			candidate.Verification = ControllerVerified
			candidate.Matches = []subject.Ref{ref}
			return candidate
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		candidate.Verification = ControllerUnreadable
		candidate.Error = err.Error()
		return candidate
	}

	candidate.Verification = ControllerStale
	return candidate
}

func fluxHelmTarget(obj metav1.Object) (releaseName, targetNamespace, storageNamespace string) {
	unstructuredObj, ok := obj.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return "", "", ""
	}
	spec, _ := unstructuredObj.UnstructuredContent()["spec"].(map[string]any)
	targetNamespace, _ = spec["targetNamespace"].(string)
	if targetNamespace == "" {
		targetNamespace = obj.GetNamespace()
	}
	releaseName, _ = spec["releaseName"].(string)
	if releaseName == "" {
		if configuredTarget, _ := spec["targetNamespace"].(string); configuredTarget != "" {
			releaseName = configuredTarget + "-" + obj.GetName()
		} else {
			releaseName = obj.GetName()
		}
		releaseName = shortenFluxReleaseName(releaseName)
	}
	storageNamespace, _ = spec["storageNamespace"].(string)
	if storageNamespace == "" {
		storageNamespace = obj.GetNamespace()
	}
	return releaseName, targetNamespace, storageNamespace
}

func shortenFluxReleaseName(name string) string {
	if len(name) <= 53 {
		return name
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	return name[:40] + "-" + sum[:12]
}

func verifyClusterVisibleArgoApplication(ctx context.Context, dc dynamic.Interface, ref subject.Ref, gvrs []schema.GroupVersionResource) ControllerCandidate {
	candidate := ControllerCandidate{Ref: ref}
	for _, gvr := range gvrs {
		list, err := dc.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "metadata.name=" + ref.Name,
		})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			candidate.Verification = ControllerUnreadable
			candidate.Error = err.Error()
			return candidate
		}
		for _, item := range list.Items {
			if item.GetName() != ref.Name {
				continue
			}
			candidate.Matches = append(candidate.Matches, subject.Ref{
				Group:     ref.Group,
				Kind:      ref.Kind,
				Namespace: item.GetNamespace(),
				Name:      item.GetName(),
			})
		}
		break
	}

	sort.Slice(candidate.Matches, func(i, j int) bool {
		return candidate.Matches[i].Namespace < candidate.Matches[j].Namespace
	})
	switch len(candidate.Matches) {
	case 0:
		candidate.Verification = ControllerStale
	case 1:
		candidate.Verification = ControllerVerified
		candidate.Ref = candidate.Matches[0]
	default:
		candidate.Verification = ControllerAmbiguous
	}
	return candidate
}

func controllerGVRs(ref subject.Ref) []schema.GroupVersionResource {
	switch {
	case ref.Group == "argoproj.io" && ref.Kind == "Application":
		return []schema.GroupVersionResource{
			{Group: ref.Group, Version: "v1alpha1", Resource: "applications"},
		}
	case ref.Group == "kustomize.toolkit.fluxcd.io" && ref.Kind == "Kustomization":
		return []schema.GroupVersionResource{
			{Group: ref.Group, Version: "v1", Resource: "kustomizations"},
			{Group: ref.Group, Version: "v1beta2", Resource: "kustomizations"},
			{Group: ref.Group, Version: "v1beta1", Resource: "kustomizations"},
		}
	case ref.Group == "helm.toolkit.fluxcd.io" && ref.Kind == "HelmRelease":
		return []schema.GroupVersionResource{
			{Group: ref.Group, Version: "v2", Resource: "helmreleases"},
			{Group: ref.Group, Version: "v2beta2", Resource: "helmreleases"},
			{Group: ref.Group, Version: "v2beta1", Resource: "helmreleases"},
		}
	default:
		return nil
	}
}
