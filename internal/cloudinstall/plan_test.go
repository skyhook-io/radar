package cloudinstall

import (
	"errors"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/subject"
)

type fakeReleaseInspector struct {
	inspection helm.CloudReleaseInspection
	err        error
	calls      int
}

func (f *fakeReleaseInspector) InspectCloudRelease(_, _ string) (helm.CloudReleaseInspection, error) {
	f.calls++
	return f.inspection, f.err
}

func nativeRadarTarget(namespace, release string) RadarTarget {
	ref := subject.Ref{Kind: "HelmRelease", Namespace: namespace, Name: release}
	return RadarTarget{
		Namespace: namespace, ReleaseName: release, DeploymentName: release,
		Chart: "radar-1.5.4",
		Ownership: TargetOwnership{
			Classification: OwnershipNativeHelm,
			NativeHelm:     &ref, NativeHelmMatchesTarget: true,
		},
	}
}

func TestClassifyInstallPlanFreshAdoptAndGitOps(t *testing.T) {
	t.Run("fresh exact target", func(t *testing.T) {
		releases := &fakeReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}}
		plan, err := ClassifyInstallPlan(DiscoveryResult{}, releases, "radar", "radar", true)
		if err != nil || plan.Mode != InstallModeFresh || plan.Namespace != "radar" || plan.Release != "radar" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("auto-discovery miss preserves requested fresh target", func(t *testing.T) {
		releases := &fakeReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}}
		plan, err := ClassifyInstallPlan(DiscoveryResult{}, releases, "observability", "prod", false)
		if err != nil || plan.Mode != InstallModeFresh || plan.Namespace != "observability" || plan.Release != "prod" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("auto-select native Helm", func(t *testing.T) {
		target := nativeRadarTarget("observability", "prod")
		releases := &fakeReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseDeployed, Revision: 3}}
		plan, err := ClassifyInstallPlan(DiscoveryResult{ClusterWide: []RadarTarget{target}}, releases, "radar", "radar", false)
		if err != nil || plan.Mode != InstallModeAdopt || plan.Namespace != "observability" || plan.Release != "prod" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("verified GitOps bypasses Helm storage", func(t *testing.T) {
		target := nativeRadarTarget("observability", "prod")
		target.Ownership = TargetOwnership{
			Classification: OwnershipGitOpsVerified,
			Controllers: []ControllerCandidate{{
				Ref:          subject.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "radar"},
				Verification: ControllerVerified,
			}},
		}
		releases := &fakeReleaseInspector{err: errors.New("should not be called")}
		plan, err := ClassifyInstallPlan(DiscoveryResult{Namespace: []RadarTarget{target}}, releases, "radar", "radar", false)
		if err != nil || plan.Mode != InstallModeGitOps || releases.calls != 0 {
			t.Fatalf("plan = %#v, calls = %d, err = %v", plan, releases.calls, err)
		}
	})
}

func TestClassifyInstallPlanFailsClosed(t *testing.T) {
	t.Run("multiple auto-discovered targets", func(t *testing.T) {
		result := DiscoveryResult{Namespace: []RadarTarget{
			nativeRadarTarget("radar", "one"), nativeRadarTarget("radar", "two"),
		}}
		_, err := ClassifyInstallPlan(result, &fakeReleaseInspector{}, "radar", "radar", false)
		var multiple *MultipleTargetsError
		if !errors.As(err, &multiple) || len(multiple.Targets) != 2 {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(err.Error(), "multiple Radar installations") || !strings.Contains(err.Error(), `release "one"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unmanaged workload collision", func(t *testing.T) {
		target := nativeRadarTarget("radar", "radar")
		target.Ownership = TargetOwnership{Classification: OwnershipGeneric}
		_, err := ClassifyInstallPlan(
			DiscoveryResult{Selected: []RadarTarget{target}},
			&fakeReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}},
			"radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "unmanaged installation") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("uncertain GitOps ownership", func(t *testing.T) {
		target := nativeRadarTarget("radar", "radar")
		target.Ownership.Classification = OwnershipGitOpsUnreadable
		_, err := ClassifyInstallPlan(
			DiscoveryResult{Selected: []RadarTarget{target}},
			&fakeReleaseInspector{}, "radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "refusing an imperative upgrade") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("pending Helm release", func(t *testing.T) {
		_, err := ClassifyInstallPlan(
			DiscoveryResult{},
			&fakeReleaseInspector{inspection: helm.CloudReleaseInspection{
				State: helm.CloudReleasePending, Status: "pending-upgrade", Revision: 4,
			}},
			"radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "pending-upgrade") || !strings.Contains(err.Error(), "revision 4") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestClassifyInstallPlanCarriesClusterWideUncertainty(t *testing.T) {
	scanErr := errors.New("cluster-wide list forbidden")
	plan, err := ClassifyInstallPlan(
		DiscoveryResult{ClusterWideError: scanErr},
		&fakeReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}},
		"radar", "radar", false,
	)
	if err != nil || !errors.Is(plan.ClusterWideScanError, scanErr) {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}
}
