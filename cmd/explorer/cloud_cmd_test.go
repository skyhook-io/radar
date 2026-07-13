package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
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
		"Existing installation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
}

func TestPostApprovalRecoveryGuidanceDoesNotRecommendImmediateRetry(t *testing.T) {
	var out bytes.Buffer
	printPostApprovalRecoveryGuidance(&out, "clus_existing", "prod", "radar-prod", helm.DeploymentRef{
		Name: "prod-radar", Namespace: "radar-prod",
	})
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

func TestCloudConnectHelpDoesNotAdvertisePreviewAsAvailable(t *testing.T) {
	var out bytes.Buffer
	if code := cloudConnect([]string{"--help"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "preview mode is not available yet") || !strings.Contains(out.String(), "radar cloud install") {
		t.Fatalf("help omitted availability guidance:\n%s", out.String())
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

func TestPrintInstallSuccessUsesRenderedDeploymentName(t *testing.T) {
	var out bytes.Buffer
	printInstallSuccess(&out, "production", "https://app.radarhq.io/c/clus_123", helm.DeploymentRef{Name: "prod-radar", Namespace: "radar-prod"})
	got := out.String()
	for _, want := range []string{
		`Cluster "production" installed and connected`,
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
	})
	got := out.String()
	for _, want := range []string{
		"Radar was installed", "clus_existing", "five-minute confirmation window",
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
	printTunnelConfirmationFailure(&out, err, "clus_existing", "wss://api.example/agent", helm.DeploymentRef{Name: "radar", Namespace: "radar"})
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
			want: []string{"already deployed", "Existing installation", "revision 4"},
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
			if !printFreshInstallConflict(&out, fmt.Errorf("wrapped: %w", tc.err)) {
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
	if printFreshInstallConflict(&out, errors.New("apiserver unavailable")) {
		t.Fatal("untyped error was classified as a release conflict")
	}
}
