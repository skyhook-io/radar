package tree

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func nodeFromTopology(n topology.Node, ref ResourceRef, role NodeRole, tool Tool, sync, health string) Node {
	data := map[string]any{}
	for k, v := range n.Data {
		data[k] = v
	}
	data["namespace"] = ref.Namespace
	data["group"] = ref.Group
	info := infoFromTopology(n)
	if health == "" {
		health = healthFromTopology(n.Status)
	}
	return Node{
		ID:             nodeID(ref),
		Ref:            ref,
		Role:           role,
		Tool:           tool,
		Sync:           sync,
		Health:         health,
		TopologyStatus: string(n.Status),
		Info:           info,
		Data:           data,
	}
}

func syntheticNode(ref ResourceRef, role NodeRole, tool Tool, sync, health string) Node {
	return Node{
		ID:             nodeID(ref),
		Ref:            ref,
		Role:           role,
		Tool:           tool,
		Sync:           sync,
		Health:         health,
		TopologyStatus: healthToTopology(health),
		Data:           map[string]any{"namespace": ref.Namespace, "group": ref.Group},
	}
}

func enrichNodeFromObject(node Node, obj *unstructured.Unstructured) Node {
	if obj == nil {
		return node
	}
	if node.Data == nil {
		node.Data = map[string]any{}
	}
	node.Ref.UID = string(obj.GetUID())
	createdAt := obj.GetCreationTimestamp()
	if !createdAt.IsZero() {
		node.Data["createdAt"] = createdAt.Format(time.RFC3339)
	}
	// Lifecycle: surface deletionTimestamp + finalizers on every node so the
	// frontend graph renderer can paint a Terminating treatment on managed
	// resources whose own controllers are mid-cleanup. Without these fields,
	// a graph showing five nodes "OutOfSync · Degraded" would obscure that
	// they're all being torn down — same false-signal class we removed from
	// the title row badges. Both fields are emitted only when set so the
	// payload doesn't grow for healthy resources.
	if dt := obj.GetDeletionTimestamp(); dt != nil && !dt.IsZero() {
		node.Data["deletionTimestamp"] = dt.UTC().Format(time.RFC3339)
		if fin := obj.GetFinalizers(); len(fin) > 0 {
			node.Data["finalizers"] = fin
		}
	}
	// Tag nodes that are themselves GitOps detail-page CRs (Argo
	// Application/ApplicationSet/AppProject, Flux Kustomization/HelmRelease/
	// GitRepository/etc). The frontend uses these tags to render the node
	// with a "this is a portal" treatment and route a click to its own
	// GitOps detail page rather than the standard resource drawer. Doing
	// the classification here (one place, sees the live object) avoids
	// duplicating the kind list across the package + the frontend.
	if tool, kind := classifyGitOpsKind(obj); tool != "" {
		node.Data["gitopsTool"] = tool
		node.Data["gitopsKind"] = kind
	}
	node.Data["labels"] = obj.GetLabels()
	node.Data["annotations"] = obj.GetAnnotations()
	if wave := obj.GetAnnotations()["argocd.argoproj.io/sync-wave"]; wave != "" {
		node.Data["syncWave"] = wave
	}
	if hook := obj.GetAnnotations()["argocd.argoproj.io/hook"]; hook != "" {
		node.Data["hook"] = hook
	}
	// "revision" is set by tool with the most authoritative source winning:
	// Argo (status.sync.revision) → Flux Kustomization (status.lastAppliedRevision)
	// → HelmRelease release number (status.lastReleaseRevision). The fields
	// don't co-occur in practice (each kind sets one), so the later overwrites
	// are no-ops; the order encodes intent if a future kind exposes multiple.
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "sync", "revision"); ok && rev != "" {
		node.Data["revision"] = truncateRevision(rev)
	}
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "operationState", "syncResult", "revision"); ok && rev != "" {
		node.Data["lastSyncRevision"] = truncateRevision(rev)
	}
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision"); ok && rev != "" {
		node.Data["revision"] = truncateRevision(rev)
	}
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "lastAttemptedRevision"); ok && rev != "" {
		node.Data["attemptedRevision"] = truncateRevision(rev)
	}
	if rev, ok, _ := unstructured.NestedInt64(obj.Object, "status", "lastReleaseRevision"); ok && rev > 0 {
		node.Data["revision"] = fmt.Sprintf("rev:%d", rev)
	}
	if ts, ok, _ := unstructured.NestedString(obj.Object, "status", "lastHandledReconcileAt"); ok && ts != "" {
		node.Data["lastReconciledAt"] = ts
	}
	if ts, ok, _ := unstructured.NestedString(obj.Object, "status", "reconciledAt"); ok && ts != "" {
		node.Data["lastReconciledAt"] = ts
	}
	return node
}

func refFromTopologyNode(n topology.Node) ResourceRef {
	ns, _ := n.Data["namespace"].(string)
	group, _ := n.Data["group"].(string)
	return ResourceRef{Group: group, Kind: string(n.Kind), Namespace: ns, Name: n.Name}
}

func infoFromTopology(n topology.Node) []InfoItem {
	switch string(n.Kind) {
	case "Deployment", "Rollout", "StatefulSet", "DaemonSet", "ReplicaSet":
		if summary, ok := n.Data["statusSummary"].(string); ok && summary != "" {
			return []InfoItem{{Name: "Status", Value: summary}}
		}
		return []InfoItem{{Name: "Ready", Value: fmt.Sprintf("%v/%v", n.Data["readyReplicas"], n.Data["totalReplicas"])}}
	case "Pod":
		if phase, ok := n.Data["phase"].(string); ok && phase != "" {
			return []InfoItem{{Name: "Phase", Value: phase}}
		}
	case "Service":
		if typ, ok := n.Data["type"].(string); ok && typ != "" {
			if port, ok := n.Data["port"]; ok {
				return []InfoItem{{Name: "Service", Value: fmt.Sprintf("%s :%v", typ, port)}}
			}
			return []InfoItem{{Name: "Service", Value: typ}}
		}
	case "Ingress":
		if host, ok := n.Data["hostname"].(string); ok && host != "" {
			return []InfoItem{{Name: "Host", Value: host}}
		}
	case "ConfigMap", "Secret":
		if keys, ok := n.Data["keys"]; ok {
			return []InfoItem{{Name: "Keys", Value: fmt.Sprintf("%v keys", keys)}}
		}
	}
	return nil
}

func nodeID(ref ResourceRef) string {
	return refKey(ref)
}

func refKey(ref ResourceRef) string {
	return strings.Join([]string{
		url.QueryEscape(ref.Group),
		url.QueryEscape(ref.Kind),
		url.QueryEscape(ref.Namespace),
		url.QueryEscape(ref.Name),
	}, "/")
}

func edgeKey(source, target string) string {
	return source + "->" + target
}

func mergeData(node Node, data map[string]any) Node {
	if len(data) == 0 {
		return node
	}
	if node.Data == nil {
		node.Data = map[string]any{}
	}
	for k, v := range data {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		node.Data[k] = v
	}
	return node
}

func apiGroup(obj *unstructured.Unstructured) string {
	apiVersion := obj.GetAPIVersion()
	if strings.Contains(apiVersion, "/") {
		return strings.SplitN(apiVersion, "/", 2)[0]
	}
	return ""
}

func healthToTopology(health string) string {
	switch health {
	case "Healthy":
		return "healthy"
	case "Degraded", "Missing":
		return "unhealthy"
	case "Progressing", "Suspended":
		return "degraded"
	default:
		return "unknown"
	}
}

func healthFromTopology(status topology.HealthStatus) string {
	switch status {
	case topology.StatusHealthy:
		return "Healthy"
	case topology.StatusDegraded:
		return "Progressing"
	case topology.StatusUnhealthy:
		return "Degraded"
	default:
		return "Unknown"
	}
}

func truncateRevision(rev string) string {
	if i := strings.LastIndex(rev, ":"); i >= 0 && i < len(rev)-1 {
		rev = rev[i+1:]
	}
	if i := strings.LastIndex(rev, "@"); i >= 0 && i < len(rev)-1 {
		rev = rev[i+1:]
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func rolePriority(role NodeRole) int {
	switch role {
	case RoleRoot:
		return 0
	case RoleDeclared:
		return 1
	case RoleGenerated:
		return 2
	case RoleGroup:
		return 3
	default:
		return 4
	}
}

func kindPriority(kind string) int {
	priorities := map[string]int{
		"Namespace": 0, "AppProject": 1, "ServiceAccount": 2,
		"Secret": 3, "SealedSecret": 3, "ConfigMap": 4,
		"CustomResourceDefinition": 5,
		"ClusterRole":              6, "ClusterRoleBinding": 7, "Role": 8, "RoleBinding": 9,
		"Service":    10,
		"Deployment": 11, "Rollout": 11, "StatefulSet": 11, "DaemonSet": 11,
		"ReplicaSet": 12, "Pod": 13,
		"Ingress": 14, "Gateway": 14, "HTTPRoute": 15,
	}
	if p, ok := priorities[kind]; ok {
		return p
	}
	return 20
}

func summarize(nodes []Node) Summary {
	var s Summary
	for _, n := range nodes {
		switch n.Role {
		case RoleDeclared:
			s.Declared++
		case RoleGenerated:
			s.Generated++
		case RoleGroup:
			s.Grouped += n.Count
		}
		if n.Health == "Degraded" || n.Health == "Missing" {
			s.Degraded++
		}
		if n.Sync == "OutOfSync" {
			s.OutOfSync++
		}
	}
	return s
}

// classifyGitOpsKind returns (tool, kind) when the object is itself a
// GitOps detail-page CR, or ("", "") otherwise. The kind list is the
// authoritative source for "this node is a portal to its own GitOps
// detail view" — keeping it here means the frontend doesn't need to
// know which CRDs to special-case.
//
// The check uses the object's apiVersion (group prefix) rather than
// kind alone because some kinds collide across groups (e.g. core Service
// vs Knative Service). For nested cases — Argo app-of-apps Applications,
// Flux Kustomizations applying further Kustomizations — these tags
// drive the "→ Open" affordance + lineage breadcrumb on the child page.
func classifyGitOpsKind(obj *unstructured.Unstructured) (tool, kind string) {
	if obj == nil {
		return "", ""
	}
	api := obj.GetAPIVersion()
	k := obj.GetKind()
	switch {
	case strings.HasPrefix(api, "argoproj.io/"):
		switch k {
		case "Application", "ApplicationSet", "AppProject":
			return "argocd", k
		}
	case strings.HasPrefix(api, "kustomize.toolkit.fluxcd.io/"):
		if k == "Kustomization" {
			return "fluxcd", k
		}
	case strings.HasPrefix(api, "helm.toolkit.fluxcd.io/"):
		if k == "HelmRelease" {
			return "fluxcd", k
		}
	}
	// Flux source CRs (GitRepository/HelmRepository/OCIRepository/Bucket/HelmChart)
	// are deliberately NOT classified as portals. They have no managed-resource
	// tree of their own — they're config objects (URL, ref, interval, auth)
	// consumed by Kustomizations/HelmReleases. Routing clicks to a GitOps detail
	// page yielded a degenerate single-node view with no navigation value. The
	// standard resource drawer surfaces their spec/status (lastFetchedRevision,
	// conditions) more usefully.
	return "", ""
}
