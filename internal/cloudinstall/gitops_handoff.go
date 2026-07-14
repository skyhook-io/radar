package cloudinstall

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/subject"
)

type GitOpsTool string

const (
	GitOpsFluxHelmRelease GitOpsTool = "Flux HelmRelease"
	GitOpsFluxKustomize   GitOpsTool = "Flux Kustomization"
	GitOpsArgoApplication GitOpsTool = "Argo CD Application"
)

type GitOpsVersionSummary struct {
	Chart string
	App   string
}

type GitOpsHandoffConfig struct {
	Target              RadarTarget
	CloudURL            string
	ClusterID           string
	Current             GitOpsVersionSummary
	TargetVersion       GitOpsVersionSummary
	EnableCloudFeatures bool
}

type GitOpsHandoff struct {
	Tool                 GitOpsTool
	Controller           subject.Ref
	Namespace            string
	ReleaseName          string
	StorageNamespace     string
	Current              GitOpsVersionSummary
	TargetVersion        GitOpsVersionSummary
	ValuesFragment       string
	SecretCommand        string
	CloudFeaturesEnabled bool
	Guidance             string
}

// BuildGitOpsHandoff produces instructions only; it never mutates a controller,
// Secret, or source repository. Its input deliberately has no token field.
func BuildGitOpsHandoff(cfg GitOpsHandoffConfig) (GitOpsHandoff, error) {
	if strings.TrimSpace(cfg.Target.Namespace) == "" || strings.TrimSpace(cfg.Target.ReleaseName) == "" {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: target namespace and release are required")
	}
	if problems := validation.IsDNS1123Label(cfg.Target.Namespace); len(problems) > 0 {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: invalid target namespace %q", cfg.Target.Namespace)
	}
	if err := cloud.ValidateWebSocketURL(cfg.CloudURL); err != nil {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: %w", err)
	}
	if strings.TrimSpace(cfg.ClusterID) == "" {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: cluster id is required")
	}
	if strings.TrimSpace(cfg.TargetVersion.Chart) == "" || strings.TrimSpace(cfg.TargetVersion.App) == "" {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: resolved target chart and app are required")
	}
	if currentChart := strings.TrimPrefix(strings.TrimSpace(cfg.Current.Chart), "radar-"); currentChart != "" && currentChart != "radar" {
		if err := helm.RejectChartDowngrade(currentChart, cfg.TargetVersion.Chart); err != nil {
			return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff: %w", err)
		}
	}

	candidate, tool, err := verifiedGitOpsController(cfg.Target)
	if err != nil {
		return GitOpsHandoff{}, err
	}

	values := cloudAdoptionValues(cfg.CloudURL, cfg.ClusterID, cfg.EnableCloudFeatures, true)
	// Make the selected stable chart's AppVersion authoritative, matching native
	// Helm adoption. If this key were absent, merging into existing values would
	// preserve an old image.tag pin.
	values["image"] = map[string]any{"tag": ""}
	valuesYAML, err := yaml.Marshal(values)
	if err != nil {
		return GitOpsHandoff{}, fmt.Errorf("build GitOps handoff values: %w", err)
	}
	valuesFragment := string(valuesYAML)
	secretCommand := fmt.Sprintf(
		`kubectl --namespace=%s create secret generic %s --from-literal=token="${RADAR_CLOUD_TOKEN:?set RADAR_CLOUD_TOKEN}" --dry-run=client --output=yaml`,
		cfg.Target.Namespace,
		CloudTokenSecretName,
	)

	handoff := GitOpsHandoff{
		Tool:                 tool,
		Controller:           candidate.Ref,
		Namespace:            cfg.Target.Namespace,
		ReleaseName:          cfg.Target.ReleaseName,
		StorageNamespace:     candidate.StorageNamespace,
		Current:              cfg.Current,
		TargetVersion:        cfg.TargetVersion,
		ValuesFragment:       valuesFragment,
		SecretCommand:        secretCommand,
		CloudFeaturesEnabled: cfg.EnableCloudFeatures,
	}
	handoff.Guidance = renderGitOpsGuidance(handoff)
	return handoff, nil
}

func verifiedGitOpsController(target RadarTarget) (ControllerCandidate, GitOpsTool, error) {
	if target.Ownership.Classification != OwnershipGitOpsVerified {
		return ControllerCandidate{}, "", fmt.Errorf(
			"build GitOps handoff: target ownership is %q, not verified GitOps",
			target.Ownership.Classification,
		)
	}

	var verified []ControllerCandidate
	for _, candidate := range target.Ownership.Controllers {
		if candidate.Verification == ControllerVerified {
			verified = append(verified, candidate)
		}
	}
	if len(verified) != 1 {
		return ControllerCandidate{}, "", fmt.Errorf("build GitOps handoff: expected one verified controller, found %d", len(verified))
	}

	candidate := verified[0]
	if candidate.Ref.Namespace == "" || candidate.Ref.Name == "" {
		return ControllerCandidate{}, "", fmt.Errorf("build GitOps handoff: verified controller must have an exact namespace and name")
	}
	tool, ok := gitOpsTool(candidate.Ref)
	if !ok {
		return ControllerCandidate{}, "", fmt.Errorf(
			"build GitOps handoff: unsupported controller %s.%s",
			candidate.Ref.Kind,
			candidate.Ref.Group,
		)
	}
	if tool == GitOpsFluxHelmRelease &&
		(candidate.ReleaseName != target.ReleaseName || candidate.TargetNamespace != target.Namespace) {
		return ControllerCandidate{}, "", fmt.Errorf(
			"build GitOps handoff: HelmRelease targets %q/%q, not %q/%q",
			candidate.TargetNamespace,
			candidate.ReleaseName,
			target.Namespace,
			target.ReleaseName,
		)
	}
	return candidate, tool, nil
}

func gitOpsTool(ref subject.Ref) (GitOpsTool, bool) {
	switch {
	case ref.Group == "helm.toolkit.fluxcd.io" && ref.Kind == "HelmRelease":
		return GitOpsFluxHelmRelease, true
	case ref.Group == "kustomize.toolkit.fluxcd.io" && ref.Kind == "Kustomization":
		return GitOpsFluxKustomize, true
	case ref.Group == "argoproj.io" && ref.Kind == "Application":
		return GitOpsArgoApplication, true
	default:
		return "", false
	}
}

func renderGitOpsGuidance(handoff GitOpsHandoff) string {
	currentChart := unknownIfEmpty(handoff.Current.Chart)
	currentApp := unknownIfEmpty(handoff.Current.App)
	var sourceInstruction string
	switch handoff.Tool {
	case GitOpsFluxHelmRelease:
		sourceInstruction = fmt.Sprintf(
			"Update HelmRelease %s/%s in its source of truth: set the Radar chart target to %s and merge the fragment below under spec.values (or the values source it references).",
			handoff.Controller.Namespace,
			handoff.Controller.Name,
			handoff.TargetVersion.Chart,
		)
	case GitOpsFluxKustomize:
		sourceInstruction = fmt.Sprintf(
			"Update the source reconciled by Kustomization %s/%s. Set the Radar chart target to %s and merge the fragment below into its Helm values, or translate it into the rendered manifests if this source commits plain YAML.",
			handoff.Controller.Namespace,
			handoff.Controller.Name,
			handoff.TargetVersion.Chart,
		)
	case GitOpsArgoApplication:
		sourceInstruction = fmt.Sprintf(
			"Update the source reconciled by Application %s/%s. Set the Radar chart target to %s and merge the fragment below into its Helm values, or translate it into the Kustomize/plain manifests if this Application does not use Helm.",
			handoff.Controller.Namespace,
			handoff.Controller.Name,
			handoff.TargetVersion.Chart,
		)
	}

	rbacInstruction := "All other rbac.* keys are omitted so the installation's feature RBAC stays unchanged."
	if handoff.CloudFeaturesEnabled {
		rbacInstruction = "This fragment explicitly enables the Cloud feature RBAC for Helm, Secrets, terminal, port-forward, and metrics."
	}

	return fmt.Sprintf(`Radar GitOps handoff

Controller: %s %s/%s
Radar target: %s/%s
Chart: %s -> %s
App: %s -> %s

%s Radar cannot identify the repository file or values path from the live object, so make this change at the controller's actual source of truth rather than editing the live Deployment.

Merge fragment:
%s
image.tag is cleared so the selected chart's stable Radar version runs. If this source commits plain rendered manifests instead of Helm values, preserve the image repository and set the Radar container tag to %s.
rbac.selfUpgrade is forced off because an in-cluster image patch would drift from Git. %s

Generate the token Secret manifest separately:
%s

The command requires RADAR_CLOUD_TOKEN in the shell and only renders YAML; it does not apply anything. Feed the result through the installation's existing SOPS, Sealed Secrets, External Secrets, or equivalent workflow, and do not commit a plaintext token.`,
		handoff.Tool,
		handoff.Controller.Namespace,
		handoff.Controller.Name,
		handoff.Namespace,
		handoff.ReleaseName,
		currentChart,
		handoff.TargetVersion.Chart,
		currentApp,
		handoff.TargetVersion.App,
		sourceInstruction,
		indentFragment(handoff.ValuesFragment),
		handoff.TargetVersion.App,
		rbacInstruction,
		handoff.SecretCommand,
	)
}

func unknownIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func indentFragment(fragment string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(fragment), "\n", "\n  ")
}
