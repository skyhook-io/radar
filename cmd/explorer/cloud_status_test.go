package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cliui"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCloudStatusHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := cloudStatus([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("cloud status --help exit code = %d", code)
	}
	if !strings.Contains(out.String(), "Usage: radar cloud status") || !strings.Contains(out.String(), "--namespace and --release") || errOut.Len() != 0 {
		t.Fatalf("help stdout = %q, stderr = %q", out.String(), errOut.String())
	}
}

func TestCloudStatusRequiresCompleteExactTarget(t *testing.T) {
	for _, args := range [][]string{{"--namespace", "radar"}, {"--release", "radar"}} {
		var out, errOut bytes.Buffer
		if code := cloudStatus(args, &out, &errOut); code != 2 {
			t.Fatalf("cloud status %v exit code = %d, want 2", args, code)
		}
		if !strings.Contains(errOut.String(), "must be passed together") {
			t.Fatalf("cloud status %v stderr = %q", args, errOut.String())
		}
	}
}

func TestDiscoveredRadarTargetsUsesExactSelection(t *testing.T) {
	selected := cloudinstall.RadarTarget{DeploymentName: "selected"}
	namespace := cloudinstall.RadarTarget{DeploymentName: "namespace"}
	clusterWide := cloudinstall.RadarTarget{DeploymentName: "cluster-wide"}
	result := cloudinstall.DiscoveryResult{
		Selected: []cloudinstall.RadarTarget{selected}, Namespace: []cloudinstall.RadarTarget{namespace}, ClusterWide: []cloudinstall.RadarTarget{clusterWide},
	}
	if got := discoveredRadarTargets(result, true); len(got) != 1 || got[0].DeploymentName != "selected" {
		t.Fatalf("exact targets = %#v", got)
	}
	if got := discoveredRadarTargets(result, false); len(got) != 2 || got[0].DeploymentName != "namespace" || got[1].DeploymentName != "cluster-wide" {
		t.Fatalf("discovered targets = %#v", got)
	}
}

func TestPrintCloudStatusConfiguredReady(t *testing.T) {
	target := cloudinstall.RadarTarget{
		Namespace: "radar", DeploymentName: "radar", ReleaseName: "radar", Chart: "radar-1.8.1",
		Ownership: cloudinstall.TargetOwnership{Classification: cloudinstall.OwnershipNativeHelm},
		Runtime: cloudinstall.DeploymentRuntime{
			Image: "ghcr.io/skyhook-io/radar:1.8.1", DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1,
			ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
			AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
			ClusterNameConfigured: true, ClusterName: "cluster-123", CloudTokenConfigured: true,
		},
	}
	var out bytes.Buffer
	if ok := printCloudStatus(&out, target); !ok {
		t.Fatal("configured ready status = false")
	}
	got := out.String()
	for _, want := range []string{
		"radar/radar", `Helm release "radar"`, "Agent: ✓ 1/1 replicas ready", "Cloud configuration: ✓ present",
		"wss://api.radarhq.io/agent", "Configured cluster reference: cluster-123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "connection token") {
		t.Fatalf("configured status exposed token detail:\n%s", got)
	}
}

type fakeAgentStatusGetter struct {
	status *cloud.AgentStatusResponse
	err    error
	token  string
}

func (f *fakeAgentStatusGetter) Get(_ context.Context, token string) (*cloud.AgentStatusResponse, error) {
	f.token = token
	return f.status, f.err
}

func TestCheckHubTunnelStatusReadsReferencedSecret(t *testing.T) {
	connectedAt := time.Date(2026, time.July, 18, 7, 30, 0, 0, time.UTC)
	target := cloudinstall.RadarTarget{Namespace: "radar", Runtime: cloudinstall.DeploymentRuntime{
		ClusterName: "prod-us-east", CloudTokenSecretName: "radar-cloud-config", CloudTokenSecretKey: "token",
	}}
	kc := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-config", Namespace: "radar"},
		Data:       map[string][]byte{"token": []byte("rhc_secret")},
	})
	client := &fakeAgentStatusGetter{status: &cloud.AgentStatusResponse{
		ClusterID: "clus_123", Status: "connected", LastConnectedAt: &connectedAt,
	}}

	got := checkHubTunnelStatusWithClient(context.Background(), kc, target, client)
	if got.verdict != hubTunnelHealthy || got.summary != "connected" || got.hubClusterID != "clus_123" || got.lastConnectedAt == nil || !got.lastConnectedAt.Equal(connectedAt) {
		t.Fatalf("result = %#v", got)
	}
	if client.token != "rhc_secret" {
		t.Fatalf("client token = %q", client.token)
	}
}

func TestCheckHubTunnelStatusUnavailableWithoutSecretReference(t *testing.T) {
	target := cloudinstall.RadarTarget{Namespace: "radar", Runtime: cloudinstall.DeploymentRuntime{ClusterName: "clus_123"}}
	client := &fakeAgentStatusGetter{}
	got := checkHubTunnelStatusWithClient(context.Background(), fake.NewSimpleClientset(), target, client)
	if got.verdict != hubTunnelUnavailable || !strings.Contains(got.summary, "not sourced from a Kubernetes Secret") || !strings.Contains(got.next, "cloud.existingSecret") {
		t.Fatalf("result = %#v", got)
	}
	if client.token != "" {
		t.Fatal("client called without a Secret reference")
	}
}

func TestCheckHubTunnelStatusFailsForBrokenSecretReference(t *testing.T) {
	target := cloudinstall.RadarTarget{Namespace: "radar", Runtime: cloudinstall.DeploymentRuntime{
		CloudTokenSecretName: "radar-cloud-config", CloudTokenSecretKey: "token",
	}}
	for _, tc := range []struct {
		name string
		kc   *fake.Clientset
		want string
	}{
		{name: "missing Secret", kc: fake.NewSimpleClientset(), want: "connection Secret was not found"},
		{name: "empty token", kc: fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-config", Namespace: "radar"},
			Data:       map[string][]byte{"token": {}},
		}), want: "connection Secret has no usable token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeAgentStatusGetter{}
			got := checkHubTunnelStatusWithClient(context.Background(), tc.kc, target, client)
			if got.verdict != hubTunnelUnhealthy || !strings.Contains(got.summary, tc.want) || got.next == "" {
				t.Fatalf("result = %#v", got)
			}
			if client.token != "" {
				t.Fatal("client called with a broken Secret reference")
			}
		})
	}
}

func TestEvaluateHubTunnelStatusHealthContract(t *testing.T) {
	connectedAt := time.Date(2026, time.July, 18, 7, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		status  *cloud.AgentStatusResponse
		err     error
		verdict hubTunnelVerdict
		want    string
	}{
		{name: "disconnected", status: &cloud.AgentStatusResponse{ClusterID: "clus_123", Status: "disconnected", LastConnectedAt: &connectedAt}, verdict: hubTunnelUnhealthy, want: "disconnected"},
		{name: "never connected", status: &cloud.AgentStatusResponse{ClusterID: "clus_123", Status: "never_connected"}, verdict: hubTunnelUnhealthy, want: "never connected"},
		{name: "rejected", err: cloud.ErrAgentStatusUnauthorized, verdict: hubTunnelUnhealthy, want: "rejected the connection token"},
		{name: "status endpoint not found", err: cloud.ErrAgentStatusEndpointNotFound, verdict: hubTunnelUnavailable, want: "status endpoint was not found"},
		{name: "network unavailable", err: errors.New("dial timeout"), verdict: hubTunnelUnavailable, want: "Hub status request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateHubTunnelStatus(tc.status, tc.err)
			actual := strings.Join([]string{got.summary, got.detail, got.next}, "\n")
			if got.verdict != tc.verdict || !strings.Contains(actual, tc.want) {
				t.Fatalf("result = %#v", got)
			}
			if tc.name == "disconnected" && (got.lastConnectedAt == nil || !got.lastConnectedAt.Equal(connectedAt)) {
				t.Fatalf("disconnected timestamp = %#v", got.lastConnectedAt)
			}
		})
	}
}

func TestPrintHubTunnelStatusKeepsBottomLineReadableAndExpertIdentityWhenNeeded(t *testing.T) {
	connectedAt := time.Date(2026, time.July, 18, 7, 30, 0, 0, time.UTC)
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{ClusterName: "prod-us-east"}}
	result := hubTunnelResult{
		verdict: hubTunnelHealthy, summary: "connected", hubClusterID: "clus_123", lastConnectedAt: &connectedAt,
	}
	var out bytes.Buffer
	printHubTunnelStatus(&out, target, result)
	got := out.String()
	for _, want := range []string{
		"\nCloud connection: ✓ connected\n", "Hub cluster ID: clus_123", "Connected since: 2026-07-18T07:30:00Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q:\n%s", want, got)
		}
	}

	out.Reset()
	target.Runtime.ClusterName = "clus_123"
	printHubTunnelStatus(&out, target, result)
	if strings.Contains(out.String(), "Hub cluster ID:") {
		t.Fatalf("matching configured and authenticated cluster IDs were repeated:\n%s", out.String())
	}
}

func TestCloudStatusHeadlineSynthesizesHumanVerdict(t *testing.T) {
	healthyRuntime := cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "clus_123", CloudTokenConfigured: true,
	}
	for _, tc := range []struct {
		name    string
		targets []cloudinstall.RadarTarget
		err     error
		tunnel  hubTunnelResult
		want    string
	}{
		{name: "not installed", want: "not installed"},
		{name: "inconclusive discovery", err: errors.New("forbidden"), want: "inconclusive"},
		{name: "multiple", targets: []cloudinstall.RadarTarget{{}, {}}, want: "Multiple Radar installations"},
		{name: "connected", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: "healthy and connected"},
		{name: "connected with partial discovery", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, err: errors.New("forbidden"), tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: "Discovery was incomplete"},
		{name: "disconnected", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "disconnected"}, want: "disconnected from the Hub"},
		{name: "rejected token", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "failed — the Hub rejected the connection token"}, want: "rejected its connection token"},
		{name: "missing Secret", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "failed — the connection Secret was not found"}, want: "connection Secret is missing"},
		{name: "empty token", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "failed — the connection Secret has no usable token"}, want: "no usable token"},
		{name: "unverified", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, want: "could not be verified"},
		{name: "not configured", targets: []cloudinstall.RadarTarget{{Runtime: cloudinstall.DeploymentRuntime{DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true}}}, want: "not configured for Cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudStatusHeadline(tc.targets, tc.err, tc.tunnel); !strings.Contains(got, tc.want) {
				t.Fatalf("headline = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestCloudStatusToneDistinguishesAttentionFromConfirmedFailure(t *testing.T) {
	healthyRuntime := cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "clus_123", CloudTokenConfigured: true,
	}
	attentionRuntime := healthyRuntime
	attentionRuntime.StatusObserved = false
	failureRuntime := healthyRuntime
	failureRuntime.DesiredReplicas = 0
	failureRuntime.TotalReplicas = 0
	failureRuntime.UpdatedReplicas = 0
	failureRuntime.ReadyReplicas = 0
	failureRuntime.AvailableReplicas = 0

	for _, tc := range []struct {
		name    string
		targets []cloudinstall.RadarTarget
		err     error
		tunnel  hubTunnelResult
		want    cliui.Tone
	}{
		{name: "not installed", want: cliui.Attention},
		{name: "healthy connected", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: cliui.Success},
		{name: "rollout pending", targets: []cloudinstall.RadarTarget{{Runtime: attentionRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: cliui.Attention},
		{name: "scaled to zero", targets: []cloudinstall.RadarTarget{{Runtime: failureRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: cliui.Failure},
		{name: "never connected", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: hubTunnelNeverConnectedSummary}, want: cliui.Attention},
		{name: "disconnected", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, tunnel: hubTunnelResult{verdict: hubTunnelUnhealthy, summary: "disconnected"}, want: cliui.Failure},
		{name: "Hub unverified", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, want: cliui.Attention},
		{name: "partial discovery", targets: []cloudinstall.RadarTarget{{Runtime: healthyRuntime}}, err: errors.New("forbidden"), tunnel: hubTunnelResult{verdict: hubTunnelHealthy}, want: cliui.Attention},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudStatusTone(tc.targets, tc.err, tc.tunnel); got != tc.want {
				t.Fatalf("tone = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCloudStatusStylingIsSemanticAndLeavesCopyableValuesPlain(t *testing.T) {
	runtime := cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "clus_123", CloudTokenConfigured: true,
	}
	target := cloudinstall.RadarTarget{Namespace: "radar", DeploymentName: "radar", Runtime: runtime}
	style := cliui.Styler{Enabled: true}

	headline := formatCloudStatusHeadline(style, []cloudinstall.RadarTarget{target}, nil, hubTunnelResult{verdict: hubTunnelHealthy})
	if !strings.Contains(headline, cliui.Bold+"Status:"+cliui.Reset) || !strings.Contains(headline, cliui.Green+"✓"+cliui.Reset) {
		t.Fatalf("styled headline = %q", headline)
	}

	var out bytes.Buffer
	printCloudStatusWithStyle(&out, target, style)
	printHubTunnelStatusWithStyle(&out, target, hubTunnelResult{verdict: hubTunnelHealthy, summary: "connected"}, style)
	got := out.String()
	if strings.Count(got, cliui.Green+"✓"+cliui.Reset) < 3 {
		t.Fatalf("styled state output = %q", got)
	}
	for _, plain := range []string{"1/1 replicas ready", "present", "connected"} {
		if strings.Contains(got, cliui.Green+plain) {
			t.Fatalf("state explanation %q was colored instead of remaining readable: %q", plain, got)
		}
	}
	if !strings.Contains(got, "Hub: wss://api.radarhq.io/agent\n") || strings.Contains(got, "Hub: "+cliui.Cyan) {
		t.Fatalf("Hub URL was styled instead of remaining copyable: %q", got)
	}

	out.Reset()
	printCloudStatus(&out, target)
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("buffered output contains ANSI sequences: %q", out.String())
	}
}

func TestCheckHubTunnelStatusRejectedTokenNamesCredentialAndRecovery(t *testing.T) {
	target := cloudinstall.RadarTarget{Namespace: "radar", Runtime: cloudinstall.DeploymentRuntime{
		CloudTokenSecretName: "radar-cloud-config", CloudTokenSecretKey: "token",
	}}
	kc := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-config", Namespace: "radar"},
		Data:       map[string][]byte{"token": []byte("rhc_rejected")},
	})
	client := &fakeAgentStatusGetter{err: cloud.ErrAgentStatusUnauthorized}

	got := checkHubTunnelStatusWithClient(context.Background(), kc, target, client)
	if got.verdict != hubTunnelUnhealthy || !strings.Contains(got.summary, "rejected the connection token") ||
		!strings.Contains(got.detail, `Kubernetes Secret "radar/radar-cloud-config", key "token"`) ||
		!strings.Contains(got.next, "generate a new cluster token in the Hub") {
		t.Fatalf("result = %#v", got)
	}
}

func TestCheckHubTunnelStatusExplainsUnconfiguredAndIncompleteInstallations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime cloudinstall.DeploymentRuntime
		want    string
	}{
		{name: "not configured", want: "not configured"},
		{name: "incomplete", runtime: cloudinstall.DeploymentRuntime{AlreadyCloud: true}, want: "configuration is incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkHubTunnelStatus(context.Background(), fake.NewSimpleClientset(), cloudinstall.RadarTarget{Runtime: tc.runtime})
			if !strings.Contains(got.summary, tc.want) || got.next == "" {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestCheckHubTunnelStatusFailsForInvalidHubURL(t *testing.T) {
	runtime := cloudinstall.DeploymentRuntime{
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "://invalid",
		ClusterNameConfigured: true, ClusterName: "clus_123", CloudTokenConfigured: true,
	}

	got := checkHubTunnelStatus(context.Background(), fake.NewSimpleClientset(), cloudinstall.RadarTarget{Runtime: runtime})
	if got.verdict != hubTunnelUnhealthy || !strings.Contains(got.summary, "configured Hub URL is invalid") || got.next == "" {
		t.Fatalf("result = %#v", got)
	}
	if headline := cloudTargetStatusHeadline(cloudinstall.RadarTarget{Runtime: runtime}, got); !strings.Contains(headline, "configured Hub URL is invalid") {
		t.Fatalf("headline = %q", headline)
	}
}

func TestPrintCloudStatusIncompleteOrUnreadyFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target cloudinstall.RadarTarget
		want   string
	}{
		{
			name: "not configured",
			target: cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
				DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
			}},
			want: "not configured",
		},
		{
			name: "missing token",
			target: cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
				DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
				AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
				CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
				ClusterNameConfigured: true, ClusterName: "cluster-123",
			}},
			want: "missing connection token",
		},
		{
			name: "unready",
			target: cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
				DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, StatusObserved: true,
				AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true,
				ClusterNameConfigured: true, CloudTokenConfigured: true,
			}},
			want: "0/1 ready",
		},
		{
			name: "scaled to zero",
			target: cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
				StatusObserved: true, AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
				CloudURLConfigured: true, ClusterNameConfigured: true, CloudTokenConfigured: true,
			}},
			want: "scaled to zero",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if ok := printCloudStatus(&out, tc.target); ok {
				t.Fatal("status = true")
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("status missing %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestPrintCloudStatusAcceptsSourcedCloudMode(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true, AlreadyCloud: true,
		CloudModeConfigured: true, CloudModeUnresolved: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "cluster-123", CloudTokenConfigured: true,
	}}
	var out bytes.Buffer
	if ok := printCloudStatus(&out, target); !ok {
		t.Fatalf("sourced Cloud mode status = false:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "referenced value (value not inspected)") {
		t.Fatalf("sourced Cloud mode uncertainty not disclosed:\n%s", out.String())
	}
}

func TestPrintCloudStatusDisclosesReferencedHubAndClusterValues(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true, AlreadyCloud: true,
		CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true, CloudURLUnresolved: true,
		ClusterNameConfigured: true, ClusterNameUnresolved: true, CloudTokenConfigured: true,
	}}
	var out bytes.Buffer
	if ok := printCloudStatus(&out, target); !ok {
		t.Fatalf("referenced Cloud configuration status = false:\n%s", out.String())
	}
	for _, want := range []string{"Hub: configured from a referenced value", "Configured cluster reference: sourced from a referenced value"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, out.String())
		}
	}
}

func TestMissingCloudConfigurationRejectsEmptyLiteralValues(t *testing.T) {
	runtime := cloudinstall.DeploymentRuntime{
		CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true,
		ClusterNameConfigured: true, CloudTokenConfigured: true,
	}
	got := strings.Join(missingCloudConfiguration(runtime), ", ")
	if !strings.Contains(got, "Hub URL") || !strings.Contains(got, "cluster ID") {
		t.Fatalf("missing configuration = %q", got)
	}
}

func TestPrintCloudStatusRequiresObservedReadyDeployment(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, ReadyReplicas: 3, AlreadyCloud: true,
		CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true,
		ClusterNameConfigured: true, CloudTokenConfigured: true,
	}}
	var out bytes.Buffer
	if ok := printCloudStatus(&out, target); ok {
		t.Fatalf("stale rollout status = true:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "rollout status pending") {
		t.Fatalf("status = %q", out.String())
	}
}

func TestPrintCloudStatusRejectsStuckRolloutMaskedByOldReadyPod(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 2, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true, CloudURLConfigured: true,
		ClusterNameConfigured: true, CloudTokenConfigured: true,
	}}
	var out bytes.Buffer
	if ok := printCloudStatus(&out, target); ok {
		t.Fatalf("stuck rollout status = true:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "rollout incomplete") || !strings.Contains(out.String(), "1 old replica still running") {
		t.Fatalf("status did not expose stuck rollout:\n%s", out.String())
	}
}

func TestPrintCloudDiscoveryStatusDoesNotClaimAbsenceAfterPartialScan(t *testing.T) {
	var out, errOut bytes.Buffer
	code := printCloudDiscoveryStatus(&out, &errOut, nil, errors.New("forbidden"), false, "radar", "radar")
	if code != 1 || !strings.Contains(out.String(), "none found in namespaces this account can inspect") || !strings.Contains(out.String(), "could not be ruled out") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "cloud install") {
		t.Fatalf("partial discovery recommended a new install: %q", out.String())
	}
}

func TestPrintCloudDiscoveryStatusHealthyTargetStillFailsAfterPartialScan(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "cluster-123", CloudTokenConfigured: true,
	}}
	var out, errOut bytes.Buffer
	code := printCloudDiscoveryStatus(&out, &errOut, []cloudinstall.RadarTarget{target}, errors.New("forbidden"), false, "radar", "radar")
	if code != 1 || !strings.Contains(out.String(), "Discovery: ! incomplete") || !strings.Contains(errOut.String(), "could not inspect all visible namespaces") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestPrintCloudDiscoveryStatusHealthyExactTargetSucceeds(t *testing.T) {
	target := cloudinstall.RadarTarget{Runtime: cloudinstall.DeploymentRuntime{
		DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, StatusObserved: true,
		AlreadyCloud: true, CloudModeConfigured: true, CloudMode: true,
		CloudURLConfigured: true, CloudURL: "wss://api.radarhq.io/agent",
		ClusterNameConfigured: true, ClusterName: "cluster-123", CloudTokenConfigured: true,
	}}
	var out, errOut bytes.Buffer
	if code := printCloudDiscoveryStatus(&out, &errOut, []cloudinstall.RadarTarget{target}, nil, true, "radar", "radar"); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestPrintCloudDiscoveryStatusExactTargetDescribesDeploymentLookup(t *testing.T) {
	var out, errOut bytes.Buffer
	code := printCloudDiscoveryStatus(&out, &errOut, nil, nil, true, "radar-system", "radar-prod")
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `Radar Deployment with release label "radar-prod" in namespace "radar-system"`) {
		t.Fatalf("status misdescribed exact lookup: %q", out.String())
	}
	if strings.Contains(out.String(), "Checked Helm release") {
		t.Fatalf("status claimed to inspect Helm storage: %q", out.String())
	}
}

func TestCloudOwnershipLabelGitOps(t *testing.T) {
	target := cloudinstall.RadarTarget{Ownership: cloudinstall.TargetOwnership{
		Classification: cloudinstall.OwnershipGitOpsVerified,
		Controllers: []cloudinstall.ControllerCandidate{
			{Ref: subject.Ref{Kind: "HelmRelease", Namespace: "flux-system", Name: "old-radar"}, Verification: cloudinstall.ControllerStale},
			{Ref: subject.Ref{Kind: "Application", Namespace: "argocd", Name: "radar"}, Verification: cloudinstall.ControllerVerified},
		},
	}}
	if got := cloudOwnershipLabel(target); got != "GitOps (Application argocd/radar)" {
		t.Fatalf("ownership = %q", got)
	}
}

func TestCloudOwnershipLabelUnmanaged(t *testing.T) {
	target := cloudinstall.RadarTarget{Ownership: cloudinstall.TargetOwnership{Classification: cloudinstall.OwnershipGeneric}}
	if got := cloudOwnershipLabel(target); got != "unmanaged" {
		t.Fatalf("ownership = %q", got)
	}
}

func TestCloudOwnershipLabelUncertainGitOpsStates(t *testing.T) {
	for classification, want := range map[cloudinstall.OwnershipClassification]string{
		cloudinstall.OwnershipGitOpsSuspected:  "suspected",
		cloudinstall.OwnershipGitOpsUnreadable: "not readable",
		cloudinstall.OwnershipGitOpsStale:      "stale",
	} {
		target := cloudinstall.RadarTarget{Ownership: cloudinstall.TargetOwnership{Classification: classification}}
		if got := cloudOwnershipLabel(target); !strings.Contains(got, want) {
			t.Errorf("ownership %q label = %q, want %q", classification, got, want)
		}
	}
}
