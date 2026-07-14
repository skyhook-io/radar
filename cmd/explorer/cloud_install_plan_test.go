package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/subject"
)

type fakeCloudReleaseInspector struct {
	inspection helm.CloudReleaseInspection
	err        error
	calls      int
}

func (f *fakeCloudReleaseInspector) InspectCloudRelease(_, _ string) (helm.CloudReleaseInspection, error) {
	f.calls++
	return f.inspection, f.err
}

func nativeRadarTarget(namespace, release string) cloudinstall.RadarTarget {
	ref := subject.Ref{Kind: "HelmRelease", Namespace: namespace, Name: release}
	return cloudinstall.RadarTarget{
		Namespace: namespace, ReleaseName: release, DeploymentName: release,
		Chart: "radar-1.5.4",
		Ownership: cloudinstall.TargetOwnership{
			Classification: cloudinstall.OwnershipNativeHelm,
			NativeHelm:     &ref, NativeHelmMatchesTarget: true,
		},
	}
}

func TestClassifyCloudInstallPlanFreshAdoptAndGitOps(t *testing.T) {
	t.Run("fresh exact target", func(t *testing.T) {
		releases := &fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}}
		plan, err := classifyCloudInstallPlan(cloudinstall.DiscoveryResult{}, releases, "radar", "radar", true)
		if err != nil || plan.Mode != cloudInstallFresh || plan.Namespace != "radar" || plan.Release != "radar" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("auto-discovery miss preserves requested fresh target", func(t *testing.T) {
		releases := &fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}}
		plan, err := classifyCloudInstallPlan(cloudinstall.DiscoveryResult{}, releases, "observability", "prod", false)
		if err != nil || plan.Mode != cloudInstallFresh || plan.Namespace != "observability" || plan.Release != "prod" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("auto-select native Helm", func(t *testing.T) {
		target := nativeRadarTarget("observability", "prod")
		releases := &fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseDeployed, Revision: 3}}
		plan, err := classifyCloudInstallPlan(cloudinstall.DiscoveryResult{ClusterWide: []cloudinstall.RadarTarget{target}}, releases, "radar", "radar", false)
		if err != nil || plan.Mode != cloudInstallAdopt || plan.Namespace != "observability" || plan.Release != "prod" {
			t.Fatalf("plan = %#v, err = %v", plan, err)
		}
	})

	t.Run("verified GitOps bypasses Helm storage", func(t *testing.T) {
		target := nativeRadarTarget("observability", "prod")
		target.Ownership = cloudinstall.TargetOwnership{
			Classification: cloudinstall.OwnershipGitOpsVerified,
			Controllers: []cloudinstall.ControllerCandidate{{
				Ref:          subject.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "radar"},
				Verification: cloudinstall.ControllerVerified,
			}},
		}
		releases := &fakeCloudReleaseInspector{err: errors.New("should not be called")}
		plan, err := classifyCloudInstallPlan(cloudinstall.DiscoveryResult{Namespace: []cloudinstall.RadarTarget{target}}, releases, "radar", "radar", false)
		if err != nil || plan.Mode != cloudInstallGitOps || releases.calls != 0 {
			t.Fatalf("plan = %#v, calls = %d, err = %v", plan, releases.calls, err)
		}
	})
}

func TestClassifyCloudInstallPlanFailsClosed(t *testing.T) {
	t.Run("multiple auto-discovered targets", func(t *testing.T) {
		result := cloudinstall.DiscoveryResult{Namespace: []cloudinstall.RadarTarget{
			nativeRadarTarget("radar", "one"), nativeRadarTarget("radar", "two"),
		}}
		_, err := classifyCloudInstallPlan(result, &fakeCloudReleaseInspector{}, "radar", "radar", false)
		if err == nil || !strings.Contains(err.Error(), "multiple Radar installations") || !strings.Contains(err.Error(), `release "one"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unmanaged workload collision", func(t *testing.T) {
		target := nativeRadarTarget("radar", "radar")
		target.Ownership = cloudinstall.TargetOwnership{Classification: cloudinstall.OwnershipGeneric}
		_, err := classifyCloudInstallPlan(
			cloudinstall.DiscoveryResult{Selected: []cloudinstall.RadarTarget{target}},
			&fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}},
			"radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "unmanaged installation") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("uncertain GitOps ownership", func(t *testing.T) {
		target := nativeRadarTarget("radar", "radar")
		target.Ownership.Classification = cloudinstall.OwnershipGitOpsUnreadable
		_, err := classifyCloudInstallPlan(
			cloudinstall.DiscoveryResult{Selected: []cloudinstall.RadarTarget{target}},
			&fakeCloudReleaseInspector{}, "radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "refusing an imperative upgrade") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("pending Helm release", func(t *testing.T) {
		_, err := classifyCloudInstallPlan(
			cloudinstall.DiscoveryResult{},
			&fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{
				State: helm.CloudReleasePending, Status: "pending-upgrade", Revision: 4,
			}},
			"radar", "radar", true,
		)
		if err == nil || !strings.Contains(err.Error(), "pending-upgrade") || !strings.Contains(err.Error(), "revision 4") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestClassifyCloudInstallPlanCarriesClusterWideUncertainty(t *testing.T) {
	scanErr := errors.New("cluster-wide list forbidden")
	plan, err := classifyCloudInstallPlan(
		cloudinstall.DiscoveryResult{ClusterWideError: scanErr},
		&fakeCloudReleaseInspector{inspection: helm.CloudReleaseInspection{State: helm.CloudReleaseNone}},
		"radar", "radar", false,
	)
	if err != nil || !errors.Is(plan.ClusterWideScanError, scanErr) {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}
}

func TestConfirmDiscoveryUncertaintyDoesNotMislabelAdoptionAsFresh(t *testing.T) {
	var out bytes.Buffer
	if !confirmDiscoveryUncertainty(strings.NewReader("yes\n"), &out, errors.New("forbidden"), true, "observability", "prod") {
		t.Fatal("interactive confirmation was rejected")
	}
	got := out.String()
	if !strings.Contains(got, `namespace "observability", Helm release "prod"`) || !strings.Contains(got, "Continue with this selected target") {
		t.Fatalf("selected-target guidance missing:\n%s", got)
	}
	if strings.Contains(got, "fresh install") || strings.Contains(got, "no Radar Deployment") {
		t.Fatalf("uncertainty guidance made an unsupported fresh-install claim:\n%s", got)
	}
}

func TestExistingInstallConsentIsIndependentOfYes(t *testing.T) {
	plan := cloudInstallPlan{Mode: cloudInstallAdopt}
	var out bytes.Buffer
	if confirmExistingInstall(strings.NewReader(""), &out, plan, false, false) {
		t.Fatal("non-interactive adoption proceeded without --adopt-existing")
	}
	if !strings.Contains(out.String(), "-y only confirms the kube context") {
		t.Fatalf("guidance = %q", out.String())
	}
	if !confirmExistingInstall(strings.NewReader(""), &out, plan, true, false) {
		t.Fatal("--adopt-existing did not approve non-interactive adoption")
	}
}

func TestGitOpsHandoffGenerationFailureRecoversExistingHubCluster(t *testing.T) {
	var out bytes.Buffer
	printGitOpsHandoffFailure(&out, errors.New("render failed"), "clus_pending", "https://app.radarhq.io/c/clus_pending")
	got := out.String()
	for _, want := range []string{
		"render failed", "clus_pending", "no live Kubernetes resource was changed",
		"no GitOps instructions or token Secret were generated", "Do not rerun",
		"organization owner", "Resume install", "rotate credentials",
		"delete it", "https://app.radarhq.io/c/clus_pending",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
	for _, wrong := range []string{"command was canceled", "Rerun `radar cloud install`"} {
		if strings.Contains(got, wrong) {
			t.Errorf("guidance contains false recovery claim %q:\n%s", wrong, got)
		}
	}
}

func TestGitOpsUnconfirmedConnectionIsRecoverableFailure(t *testing.T) {
	var out bytes.Buffer
	printGitOpsPendingHandoff(&out, cloud.ErrConnectConsumptionTimeout, "clus_pending", "https://app.radarhq.io/c/clus_pending")
	got := out.String()
	for _, want := range []string{"connection was not confirmed", "configuration handoff is ready", "Do not rerun", "clus_pending", "https://app.radarhq.io/c/clus_pending"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "not an install failure") {
		t.Errorf("guidance still reports an unconfirmed connection as success:\n%s", got)
	}
	if strings.Contains(got, "Radar Cloud") {
		t.Errorf("recovery guidance hard-codes the hosted product name:\n%s", got)
	}
}
