package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
)

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

func TestMultipleTargetsErrorGetsCLIFlagHint(t *testing.T) {
	targets := []cloudinstall.RadarTarget{
		{Namespace: "radar", ReleaseName: "one", DeploymentName: "one"},
		{Namespace: "radar", ReleaseName: "two", DeploymentName: "two"},
	}
	wrapped := renderMultipleTargetsHint(&cloudinstall.MultipleTargetsError{Targets: targets})
	for _, want := range []string{"--namespace and --release", `release "one"`, `release "two"`} {
		if !strings.Contains(wrapped.Error(), want) {
			t.Fatalf("wrapped error missing %q: %v", want, wrapped)
		}
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

// The canceled-after-approval guidance ends by pointing at the cluster link,
// so the link must print after the prose — not before it, which left a
// dangling colon.
func TestCanceledAfterApprovalPrintsClusterLinkLast(t *testing.T) {
	var out bytes.Buffer
	printCanceledAfterApproval(&out, "clus_x", "https://app.radarhq.io/c/clus_x")
	got := out.String()
	colon := strings.Index(got, "delete it later:")
	link := strings.Index(got, "https://app.radarhq.io/c/clus_x")
	if colon < 0 || link < 0 || link < colon {
		t.Fatalf("cluster link does not follow the sentence that introduces it:\n%s", got)
	}
}
