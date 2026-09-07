package server

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	startupANSIReset     = "\x1b[0m"
	startupANSIBold      = "\x1b[1m"
	startupANSICyan      = "\x1b[36m"
	startupANSIAmberBold = "\x1b[1;33m"
)

type startupLogSummary struct {
	listenAddress        string
	port                 int
	basePath             string
	authMode             string
	proxyUserHeader      string
	proxyGroupsHeader    string
	mcpEnabled           bool
	aiAgent              string
	cloudMode            bool
	showRemoteAccessHint bool
	contextName          string
	kubeconfigPath       string
	kubeconfig           k8s.KubeconfigSummary
}

func (s *Server) logStartupSummaryBlock() {
	aiAgent := ""
	if s.aiDiagnoser != nil {
		aiAgent = s.aiDiagnoser.DefaultAgent()
	}

	summary := startupLogSummary{
		listenAddress:        s.listenAddress,
		port:                 s.ActualPort(),
		basePath:             s.basePath,
		authMode:             s.authConfig.Mode,
		proxyUserHeader:      s.authConfig.UserHeader,
		proxyGroupsHeader:    s.authConfig.GroupsHeader,
		mcpEnabled:           s.mcpHandler != nil,
		aiAgent:              aiAgent,
		cloudMode:            cloud.Mode(),
		showRemoteAccessHint: s.remoteAccessHint,
		contextName:          k8s.GetContextName(),
		kubeconfigPath:       k8s.GetKubeconfigPath(),
		kubeconfig:           k8s.GetKubeconfigSummary(),
	}

	color := startupLogColorEnabled(log.Writer())
	for _, line := range formatStartupLogSummary(summary, color) {
		log.Print(line)
	}
}

// startupURLPath renders the URL path suffix for the startup block: a
// normalized base path gets a trailing slash so the printed URL lands on the
// app directly instead of bouncing through the root redirect.
func startupURLPath(basePath string) string {
	if basePath == "" {
		return ""
	}
	return basePath + "/"
}

func formatStartupLogSummary(summary startupLogSummary, color bool) []string {
	paint := func(code, value string) string {
		if !color {
			return value
		}
		return code + value + startupANSIReset
	}
	row := func(label, value string) string {
		key := label + ":"
		if len(key) >= 13 {
			return key + " " + value
		}
		return fmt.Sprintf("%-13s%s", key, value)
	}

	lines := []string{paint(startupANSIBold, "── Radar startup ─────────────────────────────────────────")}
	if summary.cloudMode {
		lines = append(lines, row("Mode", "Radar Cloud"))
	} else {
		lines = append(lines, row("Mode", "local"))
	}

	loopback := cloud.IsLoopbackHostname(summary.listenAddress)
	if loopback {
		lines = append(lines,
			row("URL", fmt.Sprintf("http://localhost:%d%s", summary.port, startupURLPath(summary.basePath))),
			row("Access", paint(startupANSICyan, "LOCAL ONLY")+" ("+summary.listenAddress+")"),
		)
	} else {
		lines = append(lines, row("Listener", fmt.Sprintf("%s:%d", summary.listenAddress, summary.port)))
		if summary.cloudMode {
			lines = append(lines, row("Access", paint(startupANSICyan, "HEALTH CHECK ONLY")+" (application traffic uses the Cloud tunnel)"))
		} else {
			lines = append(lines, row("Access", paint(startupANSIAmberBold, "NETWORK-EXPOSED")+" ("+summary.listenAddress+")"))
		}
	}

	authMode := strings.ToLower(summary.authMode)
	if authMode == "" || authMode == "none" {
		authValue := "disabled"
		if !loopback && !summary.cloudMode {
			authValue = paint(startupANSIAmberBold, "DISABLED")
		}
		lines = append(lines, row("Auth", authValue))
	} else if summary.cloudMode && authMode == "proxy" {
		lines = append(lines, row("Auth", "proxy (Cloud tunnel identity)"))
	} else if authMode == "oidc" {
		lines = append(lines, row("Auth", "OIDC"))
	} else {
		lines = append(lines, row("Auth", authMode))
	}

	if summary.contextName != "" {
		lines = append(lines, row("Cluster", summary.contextName))
	}
	if kubeconfig := formatStartupKubeconfig(summary.kubeconfigPath, summary.kubeconfig); kubeconfig != "" {
		lines = append(lines, row("Kubeconfig", kubeconfig))
	}

	if summary.mcpEnabled {
		lines = append(lines, row("MCP", "enabled at /mcp"))
	} else {
		lines = append(lines, row("MCP", "disabled"))
	}
	if summary.aiAgent != "" {
		lines = append(lines, row("AI investigations", "enabled via "+summary.aiAgent))
	}

	if loopback && summary.showRemoteAccessHint {
		lines = append(lines, row("Remote", "use --listen-address=0.0.0.0 with authentication and network controls"))
	}
	if shouldWarnUnauthenticatedListener(summary.listenAddress, authMode != "" && authMode != "none") && !summary.cloudMode {
		lines = append(lines, paint(startupANSIAmberBold, "WARNING: Remote clients can access Radar without authentication."))
	}
	if authMode == "proxy" && !summary.cloudMode {
		lines = append(lines, paint(startupANSIAmberBold, fmt.Sprintf(
			"WARNING: Proxy auth trusts %s and %s; ensure the ingress strips client-supplied identity headers.",
			summary.proxyUserHeader, summary.proxyGroupsHeader)))
	}

	lines = append(lines, "──────────────────────────────────────────────────────────")
	for i := range lines {
		lines[i] = sanitizeForLog(lines[i])
	}
	return lines
}

func formatStartupKubeconfig(path string, summary k8s.KubeconfigSummary) string {
	if summary.Mode == "" {
		return ""
	}
	if summary.Mode == "in-cluster" {
		return "in-cluster service account"
	}

	source := path
	if source == "" {
		switch summary.Mode {
		case "multi-env":
			source = "KUBECONFIG"
		case "multi-dir":
			source = "configured directories"
		case "multi-source":
			if summary.DirectoryFileCount == 0 && summary.FileCount > 1 {
				source = "configured sources"
			} else {
				source = "configured file + directories"
			}
		default:
			source = summary.Mode
		}
	}

	parts := []string{source}
	if summary.Mode == "multi-source" && (summary.DirectoryFileCount > 0 || summary.FileCount <= 1) {
		directoryNoun := "files"
		if summary.DirectoryFileCount == 1 {
			directoryNoun = "file"
		}
		parts = append(parts, fmt.Sprintf("1 primary file + %d directory %s",
			summary.DirectoryFileCount, directoryNoun))
	} else if summary.FileCount > 1 {
		parts = append(parts, fmt.Sprintf("%d files", summary.FileCount))
	}
	if summary.ContextCount > 0 {
		parts = append(parts, fmt.Sprintf("%d contexts", summary.ContextCount))
	}
	execPlugins := len(summary.ExecPluginsPresent) + len(summary.ExecPluginsMissing)
	if execPlugins > 0 {
		parts = append(parts, fmt.Sprintf("%d exec plugins", execPlugins))
	}
	return strings.Join(parts, " · ")
}

func startupLogColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
