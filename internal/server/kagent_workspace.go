package server

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/kagent"
	"github.com/skyhook-io/radar/pkg/readreason"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const kagentWorkspaceObjectLimit = 10000

var kagentWorkspaceKinds = []integration.WorkspaceKind{
	kagentAgentKind,
	{Key: "agentTemplates", Group: kagent.Group, Kind: kagent.KindAgentTemplate, Resource: "agenttemplates"},
	{Key: "harnesses", Group: kagent.Group, Kind: kagent.KindHarness, Resource: "harnesses"},
	{Key: "models", Group: kagent.Group, Kind: kagent.KindModelConfig, Resource: "modelconfigs"},
	{Key: "mcpServers", Group: kagent.Group, Kind: kagent.KindRemoteMCPServer, Resource: "remotemcpservers"},
	{Key: "egressPolicies", Group: kagent.Group, Kind: kagent.KindEgressPolicy, Resource: "egresspolicies"},
	{Key: "modelProviders", Group: kagent.Group, Kind: kagent.KindModelProviderConfig, Resource: "modelproviderconfigs"},
	{Key: "sandboxTemplates", Group: kagent.Group, Kind: kagent.KindSandboxTemplate, Resource: "sandboxtemplates"},
}

type kagentWorkspaceResponse struct {
	Context         string                              `json:"context"`
	Installed       bool                                `json:"installed"`
	Namespaces      []string                            `json:"namespaces"`
	ForcedNamespace string                              `json:"forcedNamespace,omitempty"`
	Coverage        map[string]integration.KindCoverage `json:"coverage"`
	// ReferenceCoverage includes reads outside the view filter, within the namespace pin.
	ReferenceCoverage map[string]integration.KindCoverage `json:"referenceCoverage"`
	*kagent.Workspace
	Groups []kagent.AttentionGroup `json:"groups"`
	Counts kagent.Counts           `json:"counts"`
}

// handleKagentWorkspace reads Kubernetes configuration only. In shared mode
// without sign-in it exposes what Radar's Kubernetes identity can list, with
// credential literals removed; it never connects to Substrate or reads a
// Secret/ConfigMap. Signed-in callers are checked per kind. The view filter
// narrows inventory and findings, while dependency resolution uses the caller's
// full list access within the process namespace pin. A switch invalidates the
// entire answer rather than joining objects from different connections.
func (s *Server) handleKagentWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	var cache *k8s.ResourceCache
	var discovery *k8s.ResourceDiscovery
	var dynamic *k8s.DynamicResourceCache
	var operation context.Context
	var cluster, pin string
	var forced bool
	if !k8s.CaptureClusterReads(func() {
		cache, discovery, dynamic = k8s.GetResourceCache(), k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
		operation, cluster = k8s.OperationContext(), k8s.ActiveClusterContext()
		forced, pin = k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	}) || cache == nil || operation == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	view := parseNamespaces(r.URL.Query())
	var scope []string
	if forced {
		scope = []string{}
		if pin != "" {
			scope = []string{pin}
		}
		if view != nil && !slices.Contains(view, pin) {
			s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
			return
		}
		view = scope
	}
	resp := kagentWorkspaceResponse{Context: cluster, Namespaces: append([]string{}, view...), Coverage: map[string]integration.KindCoverage{}, ReferenceCoverage: map[string]integration.KindCoverage{}}
	if forced {
		resp.ForcedNamespace = pin
	}
	budget := newSyncBudget(r.Context())
	budget.bound, budget.discovery, budget.dynamicCache, budget.readOnly = true, discovery, dynamic, true
	inv := kagent.Inventory{Now: time.Now(), Kinds: map[string]kagent.Objects{}}
	complete := map[string]bool{}
	objectCount := 0
	for _, kind := range kagentWorkspaceKinds {
		access := integration.KindAccess{State: integration.KindCoverageNotInstalled}
		selected := access
		var items []*unstructured.Unstructured
		if discovery != nil {
			if _, installed := discovery.GetGVRWithGroup(kind.Kind, kind.Group); installed {
				resp.Installed = true
				if view != nil && !slices.Equal(view, scope) {
					selected, items = s.readWorkspaceKind(r, cache, kind, view, []string{kagent.Group}, budget)
					var broader []*unstructured.Unstructured
					access, broader = s.readWorkspaceKind(r, cache, kind, scope, []string{kagent.Group}, budget)
					items = append(items, broader...)
					for _, ns := range view {
						if selected.Covers(ns) && !access.Covers(ns) {
							access.State = integration.KindCoveragePartial
							if access.Namespaces == nil {
								access.Namespaces = map[string]bool{}
							}
							access.Namespaces[ns] = true
						}
					}
				} else {
					access, items = s.readWorkspaceKind(r, cache, kind, scope, []string{kagent.Group}, budget)
					selected = access
				}
			}
		}
		byName := make(map[string]*unstructured.Unstructured, len(items))
		for _, item := range items {
			byName[item.GetNamespace()+"/"+item.GetName()] = item
		}
		items = items[:0]
		for _, item := range byName {
			items = append(items, item)
		}
		objectCount += len(items)
		if objectCount > kagentWorkspaceObjectLimit {
			s.writeError(w, http.StatusServiceUnavailable, "kagent workspace exceeds the 10000-object composition limit")
			return
		}
		if r.Context().Err() != nil || operation.Err() != nil {
			s.writeError(w, http.StatusServiceUnavailable, "kagent workspace read interrupted; try again")
			return
		}
		absentKind := access.State == integration.KindCoverageNotInstalled
		inv.Kinds[kind.Kind] = kagent.Objects{Items: items, Read: func(ns string) bool { return absentKind || access.Covers(ns) }, State: access.State,
			Complete: absentKind || access.State == integration.KindCoverageFull,
			UnreadReason: func(ns string) string {
				if forced && ns != pin {
					return string(readreason.OutsideNamespacePin)
				}
				if !s.canRead(r, kind.Group, kind.Resource, ns, "list") {
					return integration.KindCoverageDenied
				}
				if access.State == integration.KindCoverageFull {
					return integration.KindCoverageUncached
				}
				return access.State
			}}
		resp.ReferenceCoverage[kind.Key] = access.Coverage()
		resp.Coverage[kind.Key] = selected.Coverage()
		complete[kind.Kind] = absentKind || selected.State == integration.KindCoverageFull
	}

	inv.NamespaceLabels = func(ns string) (map[string]string, bool) {
		if forced && ns != pin || !s.canRead(r, "", "namespaces", "", "get") || cache.Namespaces() == nil {
			return nil, false
		}
		obj, err := cache.Namespaces().Get(ns)
		if err != nil {
			return nil, false
		}
		return obj.Labels, true
	}
	resp.Workspace = kagent.Compose(inv).In(view)
	resp.Groups = kagent.Fold(resp.Findings)
	resp.Counts = kagent.CountsOf(resp.Workspace, resp.Groups, func(kind string) bool { return complete[kind] })
	if operation.Err() != nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection changed; try again")
		return
	}
	s.writeJSON(w, resp)
}
