package insights

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Insight struct {
	Summary      Summary       `json:"summary"`
	Issues       []Issue       `json:"issues"`
	Changes      []Change      `json:"changes"`
	Plan         []PlanItem    `json:"plan"`
	History      []HistoryItem `json:"history"`
	Capabilities Capabilities  `json:"capabilities"`
	Partial      bool          `json:"partial"`
}

type Summary struct {
	Tool           string `json:"tool"`
	Kind           string `json:"kind"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Sync           string `json:"sync,omitempty"`
	Health         string `json:"health,omitempty"`
	OperationPhase string `json:"operationPhase,omitempty"`
	Source         string `json:"source,omitempty"`
	TargetRevision string `json:"targetRevision,omitempty"`
	LastRevision   string `json:"lastRevision,omitempty"`
	LastReconcile  string `json:"lastReconcile,omitempty"`
	PartialReason  string `json:"partialReason,omitempty"`
}

type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type Issue struct {
	Severity string `json:"severity"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Refs     []Ref  `json:"refs,omitempty"`
	Action   string `json:"action,omitempty"`
}

type Change struct {
	Ref         Ref    `json:"ref"`
	Category    string `json:"category"`
	Sync        string `json:"sync,omitempty"`
	Health      string `json:"health,omitempty"`
	Message     string `json:"message,omitempty"`
	HasDesired  bool   `json:"hasDesired"`
	HasLive     bool   `json:"hasLive"`
	Diff        string `json:"diff,omitempty"`
	Partial     bool   `json:"partial"`
	PartialNote string `json:"partialNote,omitempty"`
}

type PlanItem struct {
	Ref          Ref      `json:"ref"`
	Phase        string   `json:"phase,omitempty"`
	Wave         int      `json:"wave,omitempty"`
	WaveSet      bool     `json:"waveSet,omitempty"`
	Order        int      `json:"order"`
	Hook         string   `json:"hook,omitempty"`
	Relationship string   `json:"relationship,omitempty"`
	Status       string   `json:"status,omitempty"`
	BlockedBy    []Ref    `json:"blockedBy,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

type HistoryItem struct {
	ID         string `json:"id,omitempty"`
	Revision   string `json:"revision,omitempty"`
	DeployedAt string `json:"deployedAt,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Message    string `json:"message,omitempty"`
	Source     string `json:"source,omitempty"`
}

type Capabilities struct {
	Sync              bool     `json:"sync"`
	Refresh           bool     `json:"refresh"`
	Terminate         bool     `json:"terminate"`
	Suspend           bool     `json:"suspend"`
	Resume            bool     `json:"resume"`
	SyncWithSource    bool     `json:"syncWithSource"`
	SelectiveSync     bool     `json:"selectiveSync"`
	Rollback          bool     `json:"rollback"`
	UnsupportedReason string   `json:"unsupportedReason,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

func Build(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree) Insight {
	tool := detectTool(root)
	out := Insight{
		Summary:      buildSummary(root, tool),
		Issues:       buildIssues(root, resourceTree, tool),
		Changes:      buildChanges(root, resourceTree, tool),
		Plan:         buildPlan(root, resourceTree, tool),
		History:      buildHistory(root, tool),
		Capabilities: buildCapabilities(root, tool),
		Partial:      true,
	}
	out.Summary.PartialReason = "Radar can inspect controller status and live resources; desired manifest diff is not available from this endpoint yet."
	return out
}

func detectTool(root *unstructured.Unstructured) string {
	if root == nil {
		return ""
	}
	if strings.EqualFold(root.GetKind(), "Application") || strings.Contains(root.GetAPIVersion(), "argoproj.io/") {
		return "argocd"
	}
	return "fluxcd"
}

func buildSummary(root *unstructured.Unstructured, tool string) Summary {
	s := Summary{
		Tool:      tool,
		Kind:      root.GetKind(),
		Namespace: root.GetNamespace(),
		Name:      root.GetName(),
	}
	if tool == "argocd" {
		s.Sync, _, _ = unstructured.NestedString(root.Object, "status", "sync", "status")
		s.Health, _, _ = unstructured.NestedString(root.Object, "status", "health", "status")
		s.OperationPhase, _, _ = unstructured.NestedString(root.Object, "status", "operationState", "phase")
		s.TargetRevision, _, _ = unstructured.NestedString(root.Object, "status", "sync", "revision")
		s.LastRevision, _, _ = unstructured.NestedString(root.Object, "status", "operationState", "syncResult", "revision")
		s.LastReconcile, _, _ = unstructured.NestedString(root.Object, "status", "reconciledAt")
		source, _, _ := unstructured.NestedMap(root.Object, "spec", "source")
		if len(source) == 0 {
			sources, _, _ := unstructured.NestedSlice(root.Object, "spec", "sources")
			if len(sources) > 0 {
				source, _ = sources[0].(map[string]any)
			}
		}
		s.Source = joinNonEmpty(stringValue(source["repoURL"]), stringValue(source["path"]), stringValue(source["chart"]))
		return s
	}
	status := fluxStatus(root)
	s.Sync = status.sync
	s.Health = status.health
	s.TargetRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAttemptedRevision")
	s.LastRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAppliedRevision")
	s.LastReconcile, _, _ = unstructured.NestedString(root.Object, "status", "lastHandledReconcileAt")
	if s.LastReconcile == "" {
		s.LastReconcile = newestConditionTime(root)
	}
	if ref, ok := nestedRef(root, "spec", "sourceRef"); ok {
		s.Source = ref.Kind + "/" + ref.Name
	} else if ref, ok := nestedRef(root, "spec", "chart", "spec", "sourceRef"); ok {
		s.Source = ref.Kind + "/" + ref.Name
	}
	return s
}

func buildIssues(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string) []Issue {
	var out []Issue
	if tool == "argocd" {
		if phase, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "phase"); phase == "Failed" || phase == "Error" {
			msg, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "message")
			out = append(out, Issue{Severity: "critical", Scope: "operation", Reason: phase, Message: fallback(msg, "Last sync operation failed"), Action: "Open Activity for operation details."})
		} else if phase == "Running" {
			out = append(out, Issue{Severity: "info", Scope: "operation", Reason: "Running", Message: "A sync operation is currently running.", Action: "Wait for completion or terminate if it is stuck."})
		}
		for _, change := range argoResourceChanges(root) {
			if change.Health == "Degraded" || change.Health == "Missing" {
				out = append(out, Issue{Severity: "critical", Scope: "resource", Reason: change.Health, Message: fmt.Sprintf("%s %s is %s", change.Ref.Kind, change.Ref.Name, change.Health), Refs: []Ref{change.Ref}, Action: "Open the resource drawer for events, logs, and YAML."})
			} else if change.Sync == "OutOfSync" {
				out = append(out, Issue{Severity: "warning", Scope: "resource", Reason: "OutOfSync", Message: fmt.Sprintf("%s %s is out of sync", change.Ref.Kind, change.Ref.Name), Refs: []Ref{change.Ref}, Action: "Review Changes or run sync."})
			}
		}
	} else {
		for _, c := range conditions(root) {
			if c.status == "False" && (c.typ == "Ready" || c.typ == "Healthy" || c.typ == "Released" || c.typ == "TestSuccess") {
				out = append(out, Issue{Severity: "critical", Scope: "condition", Reason: fallback(c.reason, c.typ), Message: fallback(c.message, c.typ+" is false"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Stalled" {
				out = append(out, Issue{Severity: "critical", Scope: "condition", Reason: fallback(c.reason, "Stalled"), Message: fallback(c.message, "Reconciliation is stalled"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Reconciling" {
				out = append(out, Issue{Severity: "info", Scope: "condition", Reason: fallback(c.reason, "Reconciling"), Message: fallback(c.message, "Reconciliation is in progress")})
			}
		}
	}
	if resourceTree != nil && resourceTree.Summary.Degraded > 0 && len(out) == 0 {
		out = append(out, Issue{Severity: "warning", Scope: "tree", Reason: "DegradedResources", Message: fmt.Sprintf("%d managed resources are degraded", resourceTree.Summary.Degraded), Action: "Use the graph or Resources tab to inspect affected resources."})
	}
	sort.SliceStable(out, func(i, j int) bool { return severityRank(out[i].Severity) < severityRank(out[j].Severity) })
	return out
}

func buildChanges(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string) []Change {
	if tool == "argocd" {
		return argoResourceChanges(root)
	}
	if resourceTree == nil {
		return nil
	}
	var out []Change
	for _, n := range resourceTree.Nodes {
		if n.Role == gitopstree.RoleRoot || n.Role == gitopstree.RoleGroup {
			continue
		}
		category := "Synced"
		partial := true
		note := "Flux inventory confirms this resource is managed; desired manifest content is not available in Radar yet."
		if n.Health == "Degraded" || n.Health == "Missing" {
			category = n.Health
		} else if n.Sync == "OutOfSync" {
			category = "OutOfSync"
		}
		out = append(out, Change{
			Ref:         refFromTree(n.Ref),
			Category:    category,
			Sync:        n.Sync,
			Health:      firstNonEmpty(n.Health, n.TopologyStatus),
			HasLive:     n.Ref.UID != "",
			HasDesired:  false,
			Partial:     partial,
			PartialNote: note,
		})
	}
	sortChanges(out)
	return out
}

func argoResourceChanges(root *unstructured.Unstructured) []Change {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "resources")
	out := make([]Change, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref := Ref{
			Group:     stringValue(m["group"]),
			Kind:      stringValue(m["kind"]),
			Namespace: stringValue(m["namespace"]),
			Name:      stringValue(m["name"]),
		}
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		health := ""
		if hm, ok := m["health"].(map[string]any); ok {
			health = stringValue(hm["status"])
		}
		sync := stringValue(m["status"])
		category := firstNonEmpty(sync, health, "Unknown")
		if health == "Degraded" || health == "Missing" {
			category = health
		} else if sync == "Synced" && (health == "" || health == "Healthy") {
			category = "Synced"
		}
		out = append(out, Change{
			Ref:         ref,
			Category:    category,
			Sync:        sync,
			Health:      health,
			Message:     nestedMessage(m["health"]),
			HasDesired:  false,
			HasLive:     true,
			Partial:     true,
			PartialNote: "Argo reports resource status here; desired manifest content is not available in Radar yet.",
		})
	}
	sortChanges(out)
	return out
}

func buildPlan(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string) []PlanItem {
	if resourceTree == nil {
		return nil
	}
	items := make([]PlanItem, 0, len(resourceTree.Nodes))
	for _, n := range resourceTree.Nodes {
		if n.Role == gitopstree.RoleGroup {
			continue
		}
		item := PlanItem{
			Ref:          refFromTree(n.Ref),
			Order:        len(items) + 1,
			Hook:         stringData(n.Data, "hook"),
			Relationship: stringData(n.Data, "relationship"),
			Status:       joinNonEmpty(n.Sync, n.Health, n.TopologyStatus),
		}
		if wave, ok := parseWave(stringData(n.Data, "syncWave")); ok {
			item.Wave = wave
			item.WaveSet = true
		}
		item.Phase = firstNonEmpty(stringData(n.Data, "syncPhase"), phaseFromHook(item.Hook))
		if tool == "fluxcd" && item.Relationship == "" {
			if n.Role == gitopstree.RoleRoot {
				item.Relationship = "root"
			} else {
				item.Relationship = "managed"
			}
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if tool == "argocd" {
			if phaseRank(items[i].Phase) != phaseRank(items[j].Phase) {
				return phaseRank(items[i].Phase) < phaseRank(items[j].Phase)
			}
			if items[i].Wave != items[j].Wave {
				return items[i].Wave < items[j].Wave
			}
		}
		if kindRank(items[i].Ref.Kind) != kindRank(items[j].Ref.Kind) {
			return kindRank(items[i].Ref.Kind) < kindRank(items[j].Ref.Kind)
		}
		return items[i].Ref.Name < items[j].Ref.Name
	})
	for i := range items {
		items[i].Order = i + 1
	}
	return items
}

func buildHistory(root *unstructured.Unstructured, tool string) []HistoryItem {
	if tool == "argocd" {
		raw, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
		out := make([]HistoryItem, 0, len(raw)+1)
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := ""
			if v, ok := m["id"].(int64); ok {
				id = strconv.FormatInt(v, 10)
			} else if v, ok := m["id"].(float64); ok {
				id = strconv.Itoa(int(v))
			}
			source := ""
			if sm, ok := m["source"].(map[string]any); ok {
				source = joinNonEmpty(stringValue(sm["repoURL"]), stringValue(sm["path"]), stringValue(sm["chart"]))
			}
			out = append(out, HistoryItem{ID: id, Revision: stringValue(m["revision"]), DeployedAt: stringValue(m["deployedAt"]), Source: source})
		}
		if op, ok, _ := unstructured.NestedMap(root.Object, "status", "operationState"); ok {
			out = append(out, HistoryItem{
				Phase:      stringValue(op["phase"]),
				Message:    stringValue(op["message"]),
				DeployedAt: stringValue(op["finishedAt"]),
				Revision:   nestedString(op, "syncResult", "revision"),
			})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].DeployedAt > out[j].DeployedAt })
		return out
	}
	var out []HistoryItem
	for _, c := range conditions(root) {
		out = append(out, HistoryItem{
			ID:         c.typ,
			Phase:      joinNonEmpty(c.status, c.reason),
			Message:    c.message,
			DeployedAt: c.lastTransitionTime,
			Revision:   firstNonEmpty(nestedString(root.Object, "status", "lastAppliedRevision"), nestedString(root.Object, "status", "lastAttemptedRevision")),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeployedAt > out[j].DeployedAt })
	return out
}

func buildCapabilities(root *unstructured.Unstructured, tool string) Capabilities {
	if tool == "argocd" {
		hasHistory := false
		raw, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok && stringValue(m["revision"]) != "" {
				hasHistory = true
				break
			}
		}
		return Capabilities{Sync: true, Refresh: true, Terminate: true, Suspend: true, Resume: true, SelectiveSync: true, Rollback: hasHistory, Warnings: []string{"Selective sync skips hooks and is not equivalent to a full application sync."}}
	}
	syncWithSource := root.GetKind() == "Kustomization" || root.GetKind() == "HelmRelease"
	return Capabilities{Sync: true, Suspend: true, Resume: true, SyncWithSource: syncWithSource, UnsupportedReason: "Flux reconciles through source/workload controllers; per-resource selective sync and generic rollback are not exposed by Radar."}
}

type condition struct {
	typ                string
	status             string
	reason             string
	message            string
	lastTransitionTime string
}

func conditions(root *unstructured.Unstructured) []condition {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	out := make([]condition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, condition{
			typ:                stringValue(m["type"]),
			status:             stringValue(m["status"]),
			reason:             stringValue(m["reason"]),
			message:            stringValue(m["message"]),
			lastTransitionTime: stringValue(m["lastTransitionTime"]),
		})
	}
	return out
}

type fluxState struct {
	sync   string
	health string
}

func fluxStatus(root *unstructured.Unstructured) fluxState {
	if suspended, _, _ := unstructured.NestedBool(root.Object, "spec", "suspend"); suspended {
		return fluxState{sync: "Unknown", health: "Suspended"}
	}
	ready := ""
	reconciling := false
	stalled := false
	for _, c := range conditions(root) {
		if c.typ == "Ready" {
			ready = c.status
		}
		if c.typ == "Reconciling" && c.status == "True" {
			reconciling = true
		}
		if c.typ == "Stalled" && c.status == "True" {
			stalled = true
		}
	}
	if reconciling {
		return fluxState{sync: "Reconciling", health: "Progressing"}
	}
	if stalled {
		return fluxState{sync: "OutOfSync", health: "Degraded"}
	}
	if ready == "True" {
		return fluxState{sync: "Synced", health: "Healthy"}
	}
	if ready == "False" {
		return fluxState{sync: "OutOfSync", health: "Degraded"}
	}
	return fluxState{sync: "Unknown", health: "Unknown"}
}

func nestedRef(root *unstructured.Unstructured, fields ...string) (Ref, bool) {
	m, ok, _ := unstructured.NestedMap(root.Object, fields...)
	if !ok {
		return Ref{}, false
	}
	name := stringValue(m["name"])
	kind := stringValue(m["kind"])
	if name == "" || kind == "" {
		return Ref{}, false
	}
	return Ref{Group: groupFromAPIVersion(stringValue(m["apiVersion"])), Kind: kind, Namespace: firstNonEmpty(stringValue(m["namespace"]), root.GetNamespace()), Name: name}, true
}

func refFromTree(ref gitopstree.ResourceRef) Ref {
	return Ref{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

func sortChanges(out []Change) {
	sort.SliceStable(out, func(i, j int) bool {
		if changeRank(out[i].Category) != changeRank(out[j].Category) {
			return changeRank(out[i].Category) < changeRank(out[j].Category)
		}
		if out[i].Ref.Kind != out[j].Ref.Kind {
			return out[i].Ref.Kind < out[j].Ref.Kind
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
}

func changeRank(category string) int {
	switch category {
	case "Degraded", "Missing":
		return 0
	case "OutOfSync":
		return 1
	case "Progressing", "Reconciling":
		return 2
	case "Unknown":
		return 3
	default:
		return 4
	}
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func phaseRank(phase string) int {
	switch phase {
	case "PreSync":
		return 0
	case "", "Sync":
		return 1
	case "PostSync":
		return 2
	case "SyncFail":
		return 3
	case "PostDelete":
		return 4
	default:
		return 5
	}
}

func kindRank(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	case "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return 2
	case "Secret", "ConfigMap":
		return 3
	case "Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		return 4
	default:
		return 5
	}
}

func phaseFromHook(hook string) string {
	if hook == "" || hook == "Skip" {
		return ""
	}
	return hook
}

func fluxActionForReason(reason string) string {
	switch reason {
	case "DependencyNotReady":
		return "Inspect the dependency chain in the graph."
	case "ArtifactFailed":
		return "Inspect the Flux source and reconcile it."
	case "BuildFailed":
		return "Check the source path and rendered manifests."
	case "HealthCheckFailed":
		return "Open unhealthy managed resources for events and status."
	case "InstallFailed", "UpgradeFailed", "TestFailed":
		return "Inspect HelmRelease conditions and controller events."
	default:
		return "Review conditions and reconcile after fixing the source of failure."
	}
}

func parseWave(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	i, err := strconv.Atoi(value)
	return i, err == nil
}

func newestConditionTime(root *unstructured.Unstructured) string {
	newest := ""
	for _, c := range conditions(root) {
		if c.lastTransitionTime > newest {
			newest = c.lastTransitionTime
		}
	}
	return newest
}

func stringData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	return stringValue(data[key])
}

func nestedString(v any, fields ...string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for i, field := range fields {
		if i == len(fields)-1 {
			return stringValue(m[field])
		}
		m, ok = m[field].(map[string]any)
		if !ok {
			return ""
		}
	}
	return ""
}

func nestedMessage(v any) string {
	if m, ok := v.(map[string]any); ok {
		return stringValue(m["message"])
	}
	return ""
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fallback(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func joinNonEmpty(values ...string) string {
	var parts []string
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

func groupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || apiVersion == "v1" {
		return ""
	}
	if before, _, ok := strings.Cut(apiVersion, "/"); ok {
		return before
	}
	return apiVersion
}
