package tree

import (
	"fmt"
	"net/url"
	"sort"
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
	node.Data["labels"] = obj.GetLabels()
	node.Data["annotations"] = obj.GetAnnotations()
	if wave := obj.GetAnnotations()["argocd.argoproj.io/sync-wave"]; wave != "" {
		node.Data["syncWave"] = wave
	}
	if hook := obj.GetAnnotations()["argocd.argoproj.io/hook"]; hook != "" {
		node.Data["hook"] = hook
	}
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
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
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
		"Deployment": 11, "StatefulSet": 11, "DaemonSet": 11,
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

func groupLeafSiblings(rootID string, nodes []Node, edges []Edge) ([]Node, []Edge) {
	children := map[string][]string{}
	parent := map[string]string{}
	for _, e := range edges {
		children[e.Source] = append(children[e.Source], e.Target)
		parent[e.Target] = e.Source
	}
	leavesByParentKind := map[string][]Node{}
	nodeByID := map[string]Node{}
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	for _, n := range nodes {
		if n.ID == rootID || len(children[n.ID]) > 0 {
			continue
		}
		p := parent[n.ID]
		if p == "" {
			continue
		}
		key := p + "|" + n.Ref.Kind
		leavesByParentKind[key] = append(leavesByParentKind[key], n)
	}

	remove := map[string]bool{}
	var additions []Node
	var edgeAdditions []Edge
	for key, group := range leavesByParentKind {
		if len(group) < groupThreshold {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		p := parts[0]
		kind := parts[1]
		sort.Slice(group, func(i, j int) bool { return refKey(group[i].Ref) < refKey(group[j].Ref) })
		groupID := p + "/group/" + kind
		var ids []string
		for _, n := range group {
			remove[n.ID] = true
			ids = append(ids, n.ID)
		}
		ref := ResourceRef{Kind: kind, Name: fmt.Sprintf("%d %ss", len(group), kind)}
		additions = append(additions, Node{
			ID:             groupID,
			Ref:            ref,
			Role:           RoleGroup,
			Tool:           group[0].Tool,
			TopologyStatus: "unknown",
			GroupedNodeIDs: ids,
			Count:          len(group),
			Data:           map[string]any{"groupedKind": kind},
		})
		edgeAdditions = append(edgeAdditions, Edge{Source: p, Target: groupID, Type: "owns"})
	}
	if len(remove) == 0 {
		return nodes, edges
	}
	filteredNodes := make([]Node, 0, len(nodes)-len(remove)+len(additions))
	for _, n := range nodes {
		if !remove[n.ID] {
			filteredNodes = append(filteredNodes, n)
		}
	}
	filteredNodes = append(filteredNodes, additions...)

	filteredEdges := make([]Edge, 0, len(edges)-len(remove)+len(edgeAdditions))
	for _, e := range edges {
		if !remove[e.Source] && !remove[e.Target] {
			filteredEdges = append(filteredEdges, e)
		}
	}
	filteredEdges = append(filteredEdges, edgeAdditions...)
	return filteredNodes, filteredEdges
}
