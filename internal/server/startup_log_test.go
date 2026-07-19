package server

import (
	"bytes"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestFormatStartupLogSummaryLoopback(t *testing.T) {
	lines := formatStartupLogSummary(startupLogSummary{
		listenAddress:        DefaultListenAddress,
		port:                 9280,
		mcpEnabled:           true,
		aiAgent:              "claude",
		showRemoteAccessHint: true,
		contextName:          "kind-radar",
		kubeconfigPath:       "/tmp/kubeconfig",
		kubeconfig: k8s.KubeconfigSummary{
			Mode:               "single",
			FileCount:          1,
			ContextCount:       24,
			ExecPluginsPresent: []string{"aws", "gke-gcloud-auth-plugin", "kubelogin"},
		},
	}, false)
	got := strings.Join(lines, "\n")

	for _, want := range []string{
		"Mode:        local",
		"URL:         http://localhost:9280",
		"Access:      LOCAL ONLY (127.0.0.1)",
		"Auth:        disabled",
		"Cluster:     kind-radar",
		"Kubeconfig:  /tmp/kubeconfig · 24 contexts · 3 exec plugins",
		"MCP:         enabled at /mcp",
		"AI diagnose: enabled via claude",
		"Remote:      use --listen-address=0.0.0.0 with authentication and network controls",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("non-color summary contains ANSI escapes:\n%q", got)
	}
}

func TestFormatStartupLogSummaryUnauthenticatedWildcard(t *testing.T) {
	got := strings.Join(formatStartupLogSummary(startupLogSummary{
		listenAddress: AllInterfacesAddress,
		port:          9280,
	}, true), "\n")

	for _, want := range []string{
		"Listener:    0.0.0.0:9280",
		"NETWORK-EXPOSED",
		"DISABLED",
		"WARNING: Remote clients can access Radar without authentication.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup summary missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, startupANSIAmberBold) {
		t.Fatalf("color summary does not highlight the exposed unauthenticated state:\n%q", got)
	}
}

func TestFormatStartupLogSummaryCloudListener(t *testing.T) {
	got := strings.Join(formatStartupLogSummary(startupLogSummary{
		listenAddress: AllInterfacesAddress,
		port:          9280,
		authMode:      "proxy",
		cloudMode:     true,
		kubeconfig:    k8s.KubeconfigSummary{Mode: "in-cluster"},
	}, false), "\n")

	for _, want := range []string{
		"Mode:        Radar Cloud",
		"Access:      HEALTH CHECK ONLY (application traffic uses the Cloud tunnel)",
		"Auth:        proxy (Cloud tunnel identity)",
		"Kubeconfig:  in-cluster service account",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Remote clients can access Radar") || strings.Contains(got, "ensure the ingress strips") {
		t.Fatalf("Cloud health listener emitted an inapplicable warning:\n%s", got)
	}
}

func TestFormatStartupLogSummaryProxyWarning(t *testing.T) {
	got := strings.Join(formatStartupLogSummary(startupLogSummary{
		listenAddress:     AllInterfacesAddress,
		port:              9280,
		authMode:          "proxy",
		proxyUserHeader:   "X-Forwarded-User",
		proxyGroupsHeader: "X-Forwarded-Groups",
	}, false), "\n")

	if strings.Contains(got, "without authentication") {
		t.Fatalf("authenticated proxy listener emitted unauthenticated warning:\n%s", got)
	}
	if !strings.Contains(got, "ensure the ingress strips client-supplied identity headers") {
		t.Fatalf("proxy trust warning missing:\n%s", got)
	}
}

func TestFormatStartupLogSummarySanitizesLogFields(t *testing.T) {
	got := strings.Join(formatStartupLogSummary(startupLogSummary{
		listenAddress:     AllInterfacesAddress,
		port:              9280,
		authMode:          "proxy",
		proxyUserHeader:   "X-User\nFORGED user log",
		proxyGroupsHeader: "X-Groups\rFORGED groups log",
		aiAgent:           "agent\nFORGED agent log",
		contextName:       "cluster\nFORGED context log",
		kubeconfigPath:    "/tmp/config\rFORGED path log",
		kubeconfig:        k8s.KubeconfigSummary{Mode: "single"},
	}, false), "\n")

	for _, forged := range []string{
		"\nFORGED user log",
		"\rFORGED groups log",
		"\nFORGED agent log",
		"\nFORGED context log",
		"\rFORGED path log",
	} {
		if strings.Contains(got, forged) {
			t.Fatalf("startup summary contains unsanitized log field %q:\n%s", forged, got)
		}
	}
}

func TestStartupLogColorDisabledForNonTerminal(t *testing.T) {
	if startupLogColorEnabled(&bytes.Buffer{}) {
		t.Fatal("startupLogColorEnabled(buffer) = true, want false")
	}
}
