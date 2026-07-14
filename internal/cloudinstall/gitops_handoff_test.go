package cloudinstall

import (
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/pkg/subject"
)

func verifiedHandoffTarget(candidate ControllerCandidate) RadarTarget {
	return RadarTarget{
		Namespace:      "observability",
		DeploymentName: "prod-radar",
		Chart:          "radar-1.5.4",
		ReleaseName:    "prod",
		Ownership: TargetOwnership{
			Classification: OwnershipGitOpsVerified,
			Controllers:    []ControllerCandidate{candidate},
		},
	}
}

func handoffConfig(candidate ControllerCandidate) GitOpsHandoffConfig {
	return GitOpsHandoffConfig{
		Target:    verifiedHandoffTarget(candidate),
		CloudURL:  "wss://api.radarhq.io/agent",
		ClusterID: "cluster-id",
		Current: GitOpsVersionSummary{
			Chart: "radar-1.5.4",
			App:   "ghcr.io/skyhook-io/radar:1.5.8",
		},
		TargetVersion: GitOpsVersionSummary{Chart: "1.6.0", App: "1.6.2"},
	}
}

func TestBuildGitOpsHandoffFluxHelmReleaseIsTokenSafe(t *testing.T) {
	ref := subject.Ref{
		Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease",
		Namespace: "flux-system", Name: "radar-prod",
	}
	candidate := ControllerCandidate{
		Ref: ref, Verification: ControllerVerified,
		ReleaseName: "prod", TargetNamespace: "observability", StorageNamespace: "flux-system",
	}

	handoff, err := BuildGitOpsHandoff(handoffConfig(candidate))
	if err != nil {
		t.Fatalf("BuildGitOpsHandoff: %v", err)
	}
	if !reflect.DeepEqual(handoff.Controller, ref) {
		t.Fatalf("controller = %#v, want exact %#v", handoff.Controller, ref)
	}
	if handoff.Tool != GitOpsFluxHelmRelease || handoff.Namespace != "observability" || handoff.ReleaseName != "prod" || handoff.StorageNamespace != "flux-system" {
		t.Fatalf("handoff identity = %#v", handoff)
	}

	var values map[string]any
	if err := yaml.Unmarshal([]byte(handoff.ValuesFragment), &values); err != nil {
		t.Fatalf("unmarshal values fragment: %v", err)
	}
	cloudValues, ok := values["cloud"].(map[string]any)
	if !ok {
		t.Fatalf("cloud values = %#v", values["cloud"])
	}
	if len(cloudValues) != 4 || cloudValues["enabled"] != true || cloudValues["url"] != "wss://api.radarhq.io/agent" || cloudValues["clusterName"] != "cluster-id" || cloudValues["existingSecret"] != CloudTokenSecretName {
		t.Fatalf("cloud values = %#v", cloudValues)
	}
	rbac, ok := values["rbac"].(map[string]any)
	if !ok || len(rbac) != 1 || rbac["selfUpgrade"] != false {
		t.Fatalf("preserved RBAC fragment = %#v", values["rbac"])
	}
	image, ok := values["image"].(map[string]any)
	if !ok || len(image) != 1 || image["tag"] != "" {
		t.Fatalf("stable image override = %#v", values["image"])
	}
	for _, forbidden := range []string{"rhc_test_secret", "RADAR_CLOUD_TOKEN", "token:"} {
		if strings.Contains(handoff.ValuesFragment, forbidden) {
			t.Fatalf("values fragment contains credential material %q:\n%s", forbidden, handoff.ValuesFragment)
		}
	}
	if !strings.Contains(handoff.SecretCommand, "${RADAR_CLOUD_TOKEN:?set RADAR_CLOUD_TOKEN}") || !strings.Contains(handoff.SecretCommand, "--dry-run=client") {
		t.Fatalf("Secret command = %q", handoff.SecretCommand)
	}
	if strings.Contains(handoff.SecretCommand, "rhc_test_secret") {
		t.Fatal("Secret command interpolated a raw token")
	}
	for _, want := range []string{
		"Flux HelmRelease flux-system/radar-prod",
		"Radar target: observability/prod",
		"Chart: radar-1.5.4 -> 1.6.0",
		"App: ghcr.io/skyhook-io/radar:1.5.8 -> 1.6.2",
		"merge the fragment",
		"set the Radar container tag to 1.6.2",
		"rbac.selfUpgrade is forced off",
		"does not apply anything",
	} {
		if !strings.Contains(handoff.Guidance, want) {
			t.Errorf("guidance missing %q:\n%s", want, handoff.Guidance)
		}
	}
}

func TestGitOpsHandoffConfigHasNoTokenField(t *testing.T) {
	typ := reflect.TypeOf(GitOpsHandoffConfig{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "token") {
			t.Fatalf("GitOpsHandoffConfig exposes token-bearing field %q", typ.Field(i).Name)
		}
	}
}

func TestBuildGitOpsHandoffCanExplicitlyEnableFeatureRBAC(t *testing.T) {
	candidate := ControllerCandidate{
		Ref: subject.Ref{
			Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization",
			Namespace: "flux-system", Name: "platform",
		},
		Verification: ControllerVerified,
	}
	cfg := handoffConfig(candidate)
	cfg.EnableCloudFeatures = true

	handoff, err := BuildGitOpsHandoff(cfg)
	if err != nil {
		t.Fatalf("BuildGitOpsHandoff: %v", err)
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(handoff.ValuesFragment), &values); err != nil {
		t.Fatal(err)
	}
	rbac := values["rbac"].(map[string]any)
	want := map[string]bool{
		"helm": true, "secrets": true, "podExec": true,
		"portForward": true, "metrics": true, "selfUpgrade": false,
	}
	if len(rbac) != len(want) {
		t.Fatalf("feature RBAC = %#v", rbac)
	}
	for key, value := range want {
		if rbac[key] != value {
			t.Errorf("rbac.%s = %#v, want %t", key, rbac[key], value)
		}
	}
	if !handoff.CloudFeaturesEnabled || !strings.Contains(handoff.Guidance, "explicitly enables the Cloud feature RBAC") {
		t.Fatalf("handoff = %#v", handoff)
	}
}

func TestBuildGitOpsHandoffPreservesExactControllerRefs(t *testing.T) {
	tests := []struct {
		name string
		ref  subject.Ref
		tool GitOpsTool
	}{
		{
			name: "Flux Kustomization",
			ref: subject.Ref{
				Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization",
				Namespace: "flux-tenancy", Name: "radar-stack",
			},
			tool: GitOpsFluxKustomize,
		},
		{
			name: "Argo Application",
			ref: subject.Ref{
				Group: "argoproj.io", Kind: "Application",
				Namespace: "argocd", Name: "radar-production",
			},
			tool: GitOpsArgoApplication,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := handoffConfig(ControllerCandidate{Ref: tc.ref, Verification: ControllerVerified})
			cfg.Current = GitOpsVersionSummary{}
			handoff, err := BuildGitOpsHandoff(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(handoff.Controller, tc.ref) || handoff.Tool != tc.tool {
				t.Fatalf("controller = %#v (%s), want %#v (%s)", handoff.Controller, handoff.Tool, tc.ref, tc.tool)
			}
			if !strings.Contains(handoff.Guidance, "Chart: unknown -> 1.6.0") || !strings.Contains(handoff.Guidance, "App: unknown -> 1.6.2") {
				t.Fatalf("unknown current version not rendered safely:\n%s", handoff.Guidance)
			}
			if tc.tool == GitOpsArgoApplication && !strings.Contains(handoff.Guidance, "Kustomize/plain manifests") {
				t.Fatalf("Argo non-Helm source guidance missing:\n%s", handoff.Guidance)
			}
		})
	}
}

func TestBuildGitOpsHandoffRejectsUnverifiedOrInexactOwnership(t *testing.T) {
	base := ControllerCandidate{
		Ref: subject.Ref{
			Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "radar",
		},
		Verification: ControllerVerified,
	}
	tests := []struct {
		name   string
		mutate func(*GitOpsHandoffConfig)
	}{
		{
			name: "suspected ownership",
			mutate: func(cfg *GitOpsHandoffConfig) {
				cfg.Target.Ownership.Classification = OwnershipGitOpsSuspected
			},
		},
		{
			name: "namespace-less controller",
			mutate: func(cfg *GitOpsHandoffConfig) {
				cfg.Target.Ownership.Controllers[0].Ref.Namespace = ""
			},
		},
		{
			name: "two verified controllers",
			mutate: func(cfg *GitOpsHandoffConfig) {
				cfg.Target.Ownership.Controllers = append(cfg.Target.Ownership.Controllers, cfg.Target.Ownership.Controllers[0])
			},
		},
		{
			name: "unsupported controller",
			mutate: func(cfg *GitOpsHandoffConfig) {
				cfg.Target.Ownership.Controllers[0].Ref.Group = "example.com"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := handoffConfig(base)
			tc.mutate(&cfg)
			if _, err := BuildGitOpsHandoff(cfg); err == nil {
				t.Fatal("expected handoff rejection")
			}
		})
	}
}

func TestBuildGitOpsHandoffRejectsMismatchedHelmReleaseTarget(t *testing.T) {
	candidate := ControllerCandidate{
		Ref: subject.Ref{
			Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease",
			Namespace: "flux-system", Name: "radar-prod",
		},
		Verification: ControllerVerified,
		ReleaseName:  "other", TargetNamespace: "other",
	}
	if _, err := BuildGitOpsHandoff(handoffConfig(candidate)); err == nil {
		t.Fatal("expected mismatched HelmRelease target rejection")
	}
}

func TestBuildGitOpsHandoffRejectsChartDowngrade(t *testing.T) {
	candidate := ControllerCandidate{
		Ref: subject.Ref{
			Group: "argoproj.io", Kind: "Application",
			Namespace: "argocd", Name: "radar",
		},
		Verification: ControllerVerified,
	}
	cfg := handoffConfig(candidate)
	cfg.Current.Chart = "radar-2.0.0"
	cfg.TargetVersion.Chart = "1.9.0"
	if _, err := BuildGitOpsHandoff(cfg); err == nil || !strings.Contains(err.Error(), "refusing to downgrade") {
		t.Fatalf("downgrade error = %v", err)
	}
}
