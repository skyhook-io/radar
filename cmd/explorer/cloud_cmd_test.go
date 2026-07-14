package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestCloudConnectStopsBeforeHubAndPointsToSupportedPath(t *testing.T) {
	var out bytes.Buffer
	if code := cloudConnect([]string{"--hub-url", "https://hub.example", "--name", "prod"}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	got := out.String()
	for _, want := range []string{
		"local preview mode is not available yet",
		"no request was sent to the hub",
		`radar cloud install --hub-url="https://hub.example" --name="prod"`,
		"native Helm adoption or GitOps handoff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Radar Cloud") {
		t.Fatalf("custom-Hub guidance hard-codes the hosted product name:\n%s", got)
	}
}

func TestPostApprovalRecoveryGuidanceDoesNotRecommendImmediateRetry(t *testing.T) {
	var out bytes.Buffer
	printPostApprovalRecoveryGuidance(&out, "clus_existing", "https://app.radarhq.io/c/clus_existing", cloudProvisionRecovery{
		Mode: cloudinstall.ProvisionFresh, ReleaseName: "prod", Namespace: "radar-prod",
		Deployment: helm.DeploymentRef{Name: "prod-radar", Namespace: "radar-prod"},
	}, errors.New("install failed"), cloudCommandTarget{})
	got := out.String()
	for _, want := range []string{
		"clus_existing", "Do not rerun", "first inspect the existing attempt",
		"helm status prod -n radar-prod", "secret/radar-cloud-config", "deployment/prod-radar",
		"If the token Secret remains", "If the Secret was cleaned up", "token is no longer recoverable",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
}

func TestAdoptionPreMutationRecoveryDoesNotInventRollbackOrSecret(t *testing.T) {
	recovery := cloudProvisionRecovery{
		Mode: cloudinstall.ProvisionAdopt, ReleaseName: "radar", Namespace: "radar",
		Deployment: helm.DeploymentRef{Name: "radar", Namespace: "radar"}, CurrentRevision: 4,
	}
	var out bytes.Buffer
	printPostApprovalRecoveryGuidance(&out, "clus_existing", "https://app.radarhq.io/c/clus_existing", recovery,
		&cloudinstall.ProvisionPreMutationError{Err: errors.New("release changed")}, cloudCommandTarget{})
	got := out.String()
	for _, want := range []string{"Helm upgrade did not start", "created no Cloud token Secret", "Resume install"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
	for _, wrong := range []string{"could not prove that the original release was fully restored", "preserved the Cloud token Secret"} {
		if strings.Contains(got, wrong) {
			t.Errorf("pre-mutation guidance made false claim %q:\n%s", wrong, got)
		}
	}
}

func TestAdoptionUncertainSecretCreateRecoverySaysInspect(t *testing.T) {
	var out bytes.Buffer
	printPostApprovalRecoveryGuidance(&out, "clus_existing", "https://app.radarhq.io/c/clus_existing", cloudProvisionRecovery{
		Mode: cloudinstall.ProvisionAdopt, ReleaseName: "radar", Namespace: "radar",
		Deployment: helm.DeploymentRef{Name: "radar", Namespace: "radar"}, CurrentRevision: 4,
	}, &cloudinstall.ProvisionPreMutationError{Err: errors.New("connection reset"), TokenSecretMayExist: true}, cloudCommandTarget{})
	got := out.String()
	if !strings.Contains(got, "did not confirm whether the Cloud token Secret was created") || !strings.Contains(got, "get secret/radar-cloud-config") {
		t.Fatalf("uncertain Secret guidance is incomplete:\n%s", got)
	}
}

func TestAdoptionRollbackGuidanceIncludesHubCleanup(t *testing.T) {
	var out bytes.Buffer
	printAdoptionRollbackGuidance(&out, cloudProvisionRecovery{
		Mode: cloudinstall.ProvisionAdopt, ReleaseName: "prod", Namespace: "observability", CurrentRevision: 7,
	}, "https://app.radarhq.io/c/clus_existing", cloudCommandTarget{})
	got := out.String()
	for _, want := range []string{
		"helm rollback prod 7 -n observability",
		"delete secret/radar-cloud-config",
		"organization owner delete the connected cluster",
		"https://app.radarhq.io/c/clus_existing",
		"fleet row would remain disconnected",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rollback guidance missing %q:\n%s", want, got)
		}
	}
	for _, wrong := range []string{"Radar Cloud", "potentially billable"} {
		if strings.Contains(got, wrong) {
			t.Errorf("self-host-compatible rollback guidance contains %q:\n%s", wrong, got)
		}
	}
}

func TestPreparedInstallPlanUsesDeploymentNeutralOwnerCopy(t *testing.T) {
	var out bytes.Buffer
	printPreparedInstallPlan(&out, &cloudinstall.PreparedProvision{}, false, false)
	got := out.String()
	if !strings.Contains(got, "enabled for organization owners") {
		t.Fatalf("install plan omitted the self-upgrade authorization boundary:\n%s", got)
	}
	if strings.Contains(got, "Radar Cloud") {
		t.Fatalf("install plan hard-codes the hosted product name:\n%s", got)
	}
}

func TestCloudConnectHelpDoesNotAdvertisePreviewAsAvailable(t *testing.T) {
	var out bytes.Buffer
	if code := cloudConnect([]string{"--help"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "preview mode is not available yet") || !strings.Contains(out.String(), "radar cloud install") {
		t.Fatalf("help omitted availability guidance:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Radar Cloud") {
		t.Fatalf("custom-Hub help hard-codes the hosted product name:\n%s", out.String())
	}
}

func TestCloudConnectRejectsUnexpectedArguments(t *testing.T) {
	var out bytes.Buffer
	if code := cloudConnect([]string{"extra"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), `unexpected argument "extra"`) {
		t.Fatalf("unexpected-argument guidance missing:\n%s", out.String())
	}
}

func TestNormalizeCloudInstallNames(t *testing.T) {
	namespace, release, err := normalizeCloudInstallNames("  radar-prod  ", "  prod-radar  ")
	if err != nil {
		t.Fatalf("normalizeCloudInstallNames() error = %v", err)
	}
	if namespace != "radar-prod" || release != "prod-radar" {
		t.Fatalf("normalizeCloudInstallNames() = %q, %q", namespace, release)
	}
}

func TestCloudInstallExactTargetRequiresNamespaceAndRelease(t *testing.T) {
	for _, tc := range []struct {
		name              string
		explicitNamespace bool
		explicitRelease   bool
		want              bool
	}{
		{name: "neither"},
		{name: "namespace only", explicitNamespace: true},
		{name: "release only", explicitRelease: true},
		{name: "both", explicitNamespace: true, explicitRelease: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudInstallUsesExactTarget(tc.explicitNamespace, tc.explicitRelease); got != tc.want {
				t.Fatalf("cloudInstallUsesExactTarget(%t, %t) = %t, want %t", tc.explicitNamespace, tc.explicitRelease, got, tc.want)
			}
		})
	}
}

func TestNormalizeCloudInstallNamesRejectsInvalidNamespace(t *testing.T) {
	_, _, err := normalizeCloudInstallNames("Prod.Cluster", "radar")
	if err == nil || !strings.Contains(err.Error(), "invalid --namespace") {
		t.Fatalf("error = %v, want invalid namespace", err)
	}
}

func TestNormalizeCloudInstallNamesUsesHelmReleaseRules(t *testing.T) {
	for _, release := range []string{"Prod", strings.Repeat("a", 54), ""} {
		t.Run(release, func(t *testing.T) {
			_, _, err := normalizeCloudInstallNames("radar", release)
			if err == nil || !strings.Contains(err.Error(), "invalid --release") {
				t.Fatalf("error = %v, want invalid release", err)
			}
		})
	}
}

func TestNormalizeHubOrigin(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "  https://hub.example/  ", want: "https://hub.example"},
		{raw: "http://localhost:9091", want: "http://localhost:9091"},
		{raw: "http://127.0.0.2:9091", want: "http://127.0.0.2:9091"},
		{raw: "http://[::1]:9091", want: "http://[::1]:9091"},
		{raw: "https://[::1]:8443/", want: "https://[::1]:8443"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := normalizeHubOrigin(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("normalizeHubOrigin(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeHubOriginRejectsNonOrigins(t *testing.T) {
	for _, raw := range []string{
		"", "hub.example", "ftp://hub.example", "https://", "https://hub.example/api",
		"https://hub.example?org=acme", "https://hub.example#fragment",
		"https://user:password@hub.example", "https://hub.example:0", "https://hub.example:65536",
		"http://hub.example", "http://10.0.0.1", "http://localhost.example",
	} {
		t.Run(raw, func(t *testing.T) {
			if got, err := normalizeHubOrigin(raw); err == nil {
				t.Fatalf("normalizeHubOrigin(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestResolveCloudInstallClusterName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit string
		context  string
		want     string
	}{
		{name: "trim explicit", explicit: "  Production  ", context: "gke_acme_us-central1_cluster", want: "Production"},
		{name: "blank explicit uses short context", explicit: "   ", context: "gke_acme_us-central1_cluster", want: "cluster"},
		{name: "no usable name", explicit: "", context: "", want: "my-cluster"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCloudInstallClusterName(tc.explicit, tc.context); got != tc.want {
				t.Fatalf("resolveCloudInstallClusterName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveCloudInstallContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = "current"
	cfg.Contexts["current"] = &clientcmdapi.Context{Cluster: "cluster-a"}
	cfg.Contexts["other"] = &clientcmdapi.Context{Cluster: "cluster-b"}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}

	if got, err := resolveCloudInstallContext(path, ""); err != nil || got != "current" {
		t.Fatalf("current context = %q, %v; want current", got, err)
	}
	if got, err := resolveCloudInstallContext(path, " other "); err != nil || got != "other" {
		t.Fatalf("explicit context = %q, %v; want other", got, err)
	}
	if _, err := resolveCloudInstallContext(path, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing context error = %v", err)
	}

	cfg.CurrentContext = ""
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCloudInstallContext(path, ""); err == nil || !strings.Contains(err.Error(), "pass --context") {
		t.Fatalf("empty current context error = %v", err)
	}
}

func TestConfirmCloudInstallContext(t *testing.T) {
	for _, tc := range []struct {
		name            string
		input           string
		contextExplicit bool
		yes             bool
		interactive     bool
		want            bool
		wantPrompt      bool
		wantNoTerminal  bool
	}{
		{name: "implicit current accepts y", input: "y\n", interactive: true, want: true, wantPrompt: true},
		{name: "implicit current accepts yes", input: " YES \n", interactive: true, want: true, wantPrompt: true},
		{name: "implicit current defaults no", input: "\n", interactive: true, wantPrompt: true},
		{name: "implicit current rejects EOF", interactive: true, wantPrompt: true},
		{name: "explicit context skips prompt", contextExplicit: true, want: true},
		{name: "yes flag skips prompt", yes: true, want: true},
		{name: "non-interactive current fails fast", wantNoTerminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := confirmCloudInstallContext(strings.NewReader(tc.input), &out, "prod-context", tc.contextExplicit, tc.yes, tc.interactive)
			if got != tc.want {
				t.Fatalf("confirmCloudInstallContext() = %v, want %v", got, tc.want)
			}
			text := out.String()
			if want := `Kubernetes context: "prod-context"`; !strings.Contains(text, want) {
				t.Errorf("context output missing %q: %q", want, text)
			}
			if gotPrompt := strings.Contains(text, "[y/N]"); gotPrompt != tc.wantPrompt {
				t.Errorf("prompt presence = %v, want %v: %q", gotPrompt, tc.wantPrompt, text)
			}
			if gotNoTerminal := strings.Contains(text, "No interactive terminal"); gotNoTerminal != tc.wantNoTerminal {
				t.Errorf("non-interactive message presence = %v, want %v: %q", gotNoTerminal, tc.wantNoTerminal, text)
			}
		})
	}
}

func TestCanceledAfterApprovalPointsToPendingInstallRecovery(t *testing.T) {
	var out bytes.Buffer
	printCanceledAfterApproval(&out, "clus_existing", "https://app.radarhq.io/c/clus_existing")
	got := out.String()
	for _, want := range []string{"clus_existing", "No token Secret or Helm release was written", "Rerun `radar cloud install`", "pending cluster in Radar", "organization owner", "https://app.radarhq.io/c/clus_existing"} {
		if !strings.Contains(got, want) {
			t.Errorf("cancellation recovery missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Resume install") {
		t.Errorf("cancellation recovery still makes the manual resume path primary: %q", got)
	}
}

func TestPrintInstallSuccessUsesRenderedDeploymentName(t *testing.T) {
	var out bytes.Buffer
	printInstallSuccess(&out, "production", "https://app.radarhq.io/c/clus_123", helm.DeploymentRef{Name: "prod-radar", Namespace: "radar-prod"}, cloudCommandTarget{})
	got := out.String()
	for _, want := range []string{
		`Cluster "production" is connected to Radar`,
		"Open: https://app.radarhq.io/c/clus_123",
		"kubectl -n radar-prod rollout status deployment/prod-radar",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("success guidance missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "deploy/prod") {
		t.Fatalf("success guidance assumes the release name is the Deployment name:\n%s", got)
	}
	if strings.Contains(got, "Radar Cloud") {
		t.Fatalf("success guidance hard-codes the hosted product name:\n%s", got)
	}
}

func TestFollowUpCommandsPinSelectedContextAndKubeconfig(t *testing.T) {
	target := cloudCommandTarget{Context: "team's prod", Kubeconfig: "/tmp/kube config"}

	var success bytes.Buffer
	printInstallSuccess(&success, "production", "https://app.radarhq.io/c/clus_123",
		helm.DeploymentRef{Name: "prod-radar", Namespace: "radar-prod"}, target)
	if want := `kubectl --kubeconfig '/tmp/kube config' --context 'team'"'"'s prod' -n radar-prod rollout status deployment/prod-radar`; !strings.Contains(success.String(), want) {
		t.Fatalf("success command did not pin the selected target; want %q in:\n%s", want, success.String())
	}

	var rollback bytes.Buffer
	printAdoptionRollbackGuidance(&rollback, cloudProvisionRecovery{
		Mode: cloudinstall.ProvisionAdopt, ReleaseName: "prod", Namespace: "radar-prod", CurrentRevision: 7,
	}, "https://app.radarhq.io/c/clus_123", target)
	for _, want := range []string{
		`helm --kubeconfig '/tmp/kube config' --kube-context 'team'"'"'s prod' rollback prod 7 -n radar-prod`,
		`kubectl --kubeconfig '/tmp/kube config' --context 'team'"'"'s prod' -n radar-prod delete secret/radar-cloud-config`,
	} {
		if !strings.Contains(rollback.String(), want) {
			t.Fatalf("rollback command did not pin the selected target; want %q in:\n%s", want, rollback.String())
		}
	}
}

func TestCloudClusterURLUsesConnectOriginAndEscapesClusterID(t *testing.T) {
	got := cloudClusterURL("https://app.radarhq.io/connect/req_123?from=cli#approval", "clus/a b")
	if want := "https://app.radarhq.io/c/clus%2Fa%20b"; got != want {
		t.Fatalf("cloudClusterURL() = %q, want %q", got, want)
	}
}

func TestPrintTokenSecretConflict(t *testing.T) {
	var out bytes.Buffer
	err := fmt.Errorf("wrapped: %w", &cloudinstall.TokenSecretExistsError{Name: "radar-cloud-config", Namespace: "ops"})
	if !printTokenSecretConflict(&out, err) {
		t.Fatal("typed Secret conflict was not classified")
	}
	for _, want := range []string{"will not overwrite", "recover that installation", "corresponding Hub cluster"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("guidance missing %q:\n%s", want, out.String())
		}
	}
}

func TestTunnelConfirmationFailurePreservesExistingInstall(t *testing.T) {
	var out bytes.Buffer
	printTunnelConfirmationFailure(&out, cloud.ErrConnectConsumptionTimeout, "clus_existing", "wss://api.example/agent", helm.DeploymentRef{
		Name: "prod-radar", Namespace: "radar-prod",
	}, cloudCommandTarget{})
	got := out.String()
	for _, want := range []string{
		"Radar was provisioned", "clus_existing", "five-minute confirmation window",
		"Do not rerun", "deployment/prod-radar", "logs deployment/prod-radar",
		"outbound WSS/HTTPS access to wss://api.example/agent", "Only if you deliberately abandon",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
}

func TestTunnelConfirmationPickupExpiryDoesNotRepeatDeleteAdvice(t *testing.T) {
	var out bytes.Buffer
	err := fmt.Errorf("%w: delete pending cluster", cloud.ErrConnectPickupExpired)
	printTunnelConfirmationFailure(&out, err, "clus_existing", "wss://api.example/agent", helm.DeploymentRef{Name: "radar", Namespace: "radar"}, cloudCommandTarget{})
	got := out.String()
	if strings.Contains(got, "delete pending cluster") {
		t.Fatalf("post-install guidance repeated pre-install deletion advice:\n%s", got)
	}
	if !strings.Contains(got, "Do not rerun") {
		t.Fatalf("post-install guidance omitted recovery-first instruction:\n%s", got)
	}
}

func TestPrintFreshInstallConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "deployed",
			err:  &helm.ReleaseExistsError{Name: "radar", Namespace: "ops", Revision: 4},
			want: []string{"already deployed", "prepared fresh-install plan is stale", "--adopt-existing", "revision 4"},
		},
		{
			name: "pending",
			err:  &helm.ReleasePendingError{Name: "radar", Namespace: "ops", Status: "pending-upgrade", Revision: 5},
			want: []string{"pending-upgrade", "Wait for the current Helm operation", "helm status radar -n ops"},
		},
		{
			name: "unknown",
			err:  &helm.ReleasePendingError{Name: "radar", Namespace: "ops", Status: "unknown", Revision: 5},
			want: []string{"cannot safely determine", "helm status radar -n ops"},
		},
		{
			name: "retained history",
			err:  &helm.ReleaseHistoryError{Name: "radar", Namespace: "ops", Status: "failed", Revision: 3},
			want: []string{"retained \"failed\" history", "helm history radar -n ops", "new --release"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if !printFreshInstallConflict(&out, fmt.Errorf("wrapped: %w", tc.err), cloudCommandTarget{}) {
				t.Fatal("typed conflict was not classified")
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("guidance missing %q:\n%s", want, out.String())
				}
			}
		})
	}

	var out bytes.Buffer
	if printFreshInstallConflict(&out, errors.New("apiserver unavailable"), cloudCommandTarget{}) {
		t.Fatal("untyped error was classified as a release conflict")
	}
}
