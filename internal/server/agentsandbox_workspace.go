package server

import (
	"net/http"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/agentsandbox"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

var (
	agentSandboxSandboxesKind = integration.WorkspaceKind{Key: "sandboxes", Group: agentsandbox.Group, Kind: "Sandbox", Resource: "sandboxes"}
	agentSandboxClaimsKind    = integration.WorkspaceKind{Key: "claims", Group: agentsandbox.ExtensionsGroup, Kind: "SandboxClaim", Resource: "sandboxclaims"}
	agentSandboxPoolsKind     = integration.WorkspaceKind{Key: "warmPools", Group: agentsandbox.ExtensionsGroup, Kind: "SandboxWarmPool", Resource: "sandboxwarmpools"}
	agentSandboxTemplatesKind = integration.WorkspaceKind{Key: "templates", Group: agentsandbox.ExtensionsGroup, Kind: "SandboxTemplate", Resource: "sandboxtemplates"}
	agentSandboxKinds         = []integration.WorkspaceKind{agentSandboxSandboxesKind, agentSandboxClaimsKind, agentSandboxPoolsKind, agentSandboxTemplatesKind}
)

// AgentSandboxWorkspaceResponse is GET /api/agentsandbox/workspace.
type AgentSandboxWorkspaceResponse struct {
	Context string `json:"context"`
	// Installed: discovery serves at least one Agent Sandbox kind.
	Installed bool `json:"installed"`
	// Namespaces the request asked for, after the --namespace-scope clamp;
	// empty for every namespace the caller may read.
	Namespaces      []string                            `json:"namespaces"`
	ForcedNamespace string                              `json:"forcedNamespace,omitempty"`
	Coverage        map[string]integration.KindCoverage `json:"coverage"`
	agentsandbox.Summary
}

// handleAgentSandboxWorkspace serves GET /api/agentsandbox/workspace?namespaces=:
// the Agent Sandbox Overview, its Needs attention rows and the sidebar's
// badges, computed from what the caller can read.
//
// The namespace filter applies: Sandboxes, their claims, pools and templates
// are application objects in the namespaces that use them, and every join is
// within a namespace. Each kind is read as the caller through
// readWorkspaceKind, so a reader of these kinds alone gets them without Pod
// access, and a kind read in part makes every answer resting on it unread
// rather than absent. The controller Deployment is read where the release
// manifest installs it, outside the filter, and only with the caller's own
// access to it.
func (s *Server) handleAgentSandboxWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := parseNamespaces(r.URL.Query())
	forced := ""
	if k8s.ForceNamespaceScope {
		forced = k8s.GetNamespaceScopeTarget()
		switch {
		case forced == "":
			namespaces = []string{}
		case namespaces == nil || slices.Contains(namespaces, forced):
			namespaces = []string{forced}
		default:
			s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
			return
		}
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	resp := AgentSandboxWorkspaceResponse{
		Context:    k8s.ActiveClusterContext(),
		Namespaces: append([]string{}, namespaces...), ForcedNamespace: forced,
		Coverage: map[string]integration.KindCoverage{},
	}
	if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		for _, k := range agentSandboxKinds {
			if _, ok := discovery.GetGVRWithGroup(k.Kind, k.Group); ok {
				resp.Installed = true
			}
		}
	}

	reads := map[string]agentsandbox.Objects{}
	budget := newSyncBudget(r.Context())
	// Summarize keeps nothing of the objects past the request and never
	// mutates them, so the reads need no copies.
	budget.readOnly = true
	for _, k := range agentSandboxKinds {
		access := integration.KindAccess{State: integration.KindCoverageNotInstalled}
		var objects agentsandbox.Objects
		if resp.Installed {
			var items []*unstructured.Unstructured
			access, items = s.readWorkspaceKind(r, cache, k, namespaces, []string{k.Group}, budget)
			objects = agentsandbox.Objects{
				Items:    items,
				Read:     access.Covers,
				Usable:   access.State == integration.KindCoverageFull || access.State == integration.KindCoveragePartial,
				Complete: access.State == integration.KindCoverageFull,
			}
		}
		if access.State == integration.KindCoverageNotInstalled {
			objects = agentsandbox.Objects{Read: func(string) bool { return true }, Complete: true}
		}
		resp.Coverage[k.Key] = access.Coverage()
		reads[k.Key] = objects
	}
	controller := agentsandbox.Controller{State: agentsandbox.ControllerNotInstalled, Namespace: agentsandbox.ControllerNamespace, Name: agentsandbox.ControllerName}
	if resp.Installed {
		controller = s.agentSandboxController(r, cache)
	}
	resp.Summary = agentsandbox.Summarize(agentsandbox.Input{
		Now:        time.Now().UTC(),
		Sandboxes:  reads[agentSandboxSandboxesKind.Key],
		Claims:     reads[agentSandboxClaimsKind.Key],
		WarmPools:  reads[agentSandboxPoolsKind.Key],
		Templates:  reads[agentSandboxTemplatesKind.Key],
		Controller: controller,
	})
	s.writeJSON(w, resp)
}

// agentSandboxController reads the controller Deployment as the caller.
// Anything short of reading it where it should be is unread, never stopped.
func (s *Server) agentSandboxController(r *http.Request, cache *k8s.ResourceCache) agentsandbox.Controller {
	ns, name := agentsandbox.ControllerNamespace, agentsandbox.ControllerName
	if outsideNamespacePin(ns) != nil {
		return agentsandbox.ControllerNotRead(readreason.OutsideNamespacePin)
	}
	if !s.canRead(r, "apps", "deployments", ns, "get") {
		return agentsandbox.ControllerNotRead(readreason.UserForbidden)
	}
	if code := controllerReadGap(cache.AssessKindRead("deployments", []string{ns}), ns); code != "" || cache.Deployments() == nil {
		if code == "" {
			code = readreason.RadarKindNotCollected
		}
		return agentsandbox.ControllerNotRead(code)
	}
	d, err := cache.Deployments().Deployments(ns).Get(name)
	switch {
	case apierrors.IsNotFound(err):
		return agentsandbox.ControllerMissing()
	case err != nil:
		return agentsandbox.ControllerNotRead(readreason.ReadError)
	}
	return agentsandbox.ReadController(d)
}

// controllerReadGap is why Radar's cache can't answer for Deployments in ns
// now, or "" when it can.
func controllerReadGap(a k8score.KindReadAssessment, ns string) readreason.Code {
	switch {
	case a.Readiness == k8score.KindUnavailable:
		return readreason.RadarKindNotCollected
	case slices.Contains(a.Read, ns):
		return ""
	case slices.Contains(a.Uncovered, ns):
		return readreason.RadarNamespaceNotCollected
	case slices.Contains(a.Failed, ns):
		return readreason.KindSyncTimedOut
	}
	return readreason.KindSyncPending
}
