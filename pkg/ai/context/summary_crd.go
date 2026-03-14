package context

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// summarizeUnstructured produces a Summary for CRDs and dynamic resources.
// Uses known CRD extractors where available, falls back to generic extraction.
func summarizeUnstructured(obj *unstructured.Unstructured) *ResourceSummary {
	kind := obj.GetKind()
	group := obj.GroupVersionKind().Group

	// Known CRD extractors
	switch {
	case group == "argoproj.io" && kind == "Application":
		return summarizeArgoApp(obj)
	case group == "argoproj.io" && kind == "Rollout":
		return summarizeArgoRollout(obj)
	case group == "kustomize.toolkit.fluxcd.io" && kind == "Kustomization":
		return summarizeFluxKustomization(obj)
	case group == "helm.toolkit.fluxcd.io" && kind == "HelmRelease":
		return summarizeFluxHelmRelease(obj)
	case group == "gateway.networking.k8s.io" && kind == "Gateway":
		return summarizeGateway(obj)
	case group == "karpenter.sh" && kind == "NodePool":
		return summarizeKarpenterNodePool(obj)
	case group == "karpenter.sh" && kind == "NodeClaim":
		return summarizeKarpenterNodeClaim(obj)
	case strings.Contains(group, "karpenter") && strings.HasSuffix(kind, "NodeClass"):
		return summarizeKarpenterNodeClass(obj)
	case group == "keda.sh" && kind == "ScaledObject":
		return summarizeKedaScaledObject(obj)
	case group == "keda.sh" && kind == "ScaledJob":
		return summarizeKedaScaledJob(obj)
	case group == "keda.sh" && kind == "TriggerAuthentication":
		return summarizeKedaTriggerAuthentication(obj)
	case group == "keda.sh" && kind == "ClusterTriggerAuthentication":
		return summarizeKedaTriggerAuthentication(obj)
	case group == "gateway.networking.k8s.io" && kind == "GatewayClass":
		return summarizeGatewayClass(obj)
	case group == "gateway.networking.k8s.io" && (kind == "HTTPRoute" || kind == "GRPCRoute" || kind == "TCPRoute" || kind == "TLSRoute"):
		return summarizeGatewayRoute(obj)
	case group == "projectcontour.io" && kind == "HTTPProxy":
		return summarizeContourHTTPProxy(obj)
	}

	// Generic fallback
	return summarizeGenericCRD(obj)
}

func summarizeArgoApp(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Application",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Sync status
	syncStatus, _, _ := unstructured.NestedString(obj.Object, "status", "sync", "status")
	s.Status = syncStatus

	// Health status stored as issue if not Healthy
	healthStatus, _, _ := unstructured.NestedString(obj.Object, "status", "health", "status")
	if healthStatus != "" && healthStatus != "Healthy" {
		s.Issue = healthStatus
	}

	// Repo URL as extra context
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "repoURL")
	if repo != "" {
		s.Image = repo // Reuse Image field for repo
	}

	return s
}

func summarizeArgoRollout(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Rollout",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	s.Status = phase

	// Strategy type
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "strategy", "canary"); found {
		s.Strategy = "canary"
	} else if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "strategy", "blueGreen"); found {
		s.Strategy = "blueGreen"
	}

	readyReplicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
	replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if replicas > 0 {
		s.Ready = formatInt64Pair(readyReplicas, replicas)
	}

	return s
}

func summarizeFluxKustomization(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Kustomization",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	revision, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	if revision != "" {
		s.Version = revision
	}

	return s
}

func summarizeFluxHelmRelease(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "HelmRelease",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	chart, _, _ := unstructured.NestedString(obj.Object, "spec", "chart", "spec", "chart")
	if chart != "" {
		s.Image = chart // Reuse Image field for chart name
	}

	version, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	if version != "" {
		s.Version = version
	}

	return s
}

func summarizeGateway(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Gateway",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	className, _, _ := unstructured.NestedString(obj.Object, "spec", "gatewayClassName")
	s.Type = className

	s.Status = extractReadyCondition(obj)

	return s
}

func summarizeGenericCRD(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Try status.conditions — most CRDs follow this convention
	s.Status = extractReadyCondition(obj)

	// Try status.phase as fallback
	if s.Status == "" {
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		s.Status = phase
	}

	return s
}

// extractReadyCondition extracts the most informative condition from status.conditions.
// Priority: Ready > Available > Synced > first condition.
func extractReadyCondition(obj *unstructured.Unstructured) string {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return ""
	}

	priority := map[string]int{"Ready": 0, "Available": 1, "Synced": 2}
	bestPriority := 999
	bestStatus := ""

	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		condStatus, _ := cond["status"].(string)

		p, known := priority[condType]
		if known && p < bestPriority {
			bestPriority = p
			if condStatus == "True" {
				bestStatus = condType
			} else {
				reason, _ := cond["reason"].(string)
				if reason != "" {
					bestStatus = reason
				} else {
					bestStatus = "Not" + condType
				}
			}
		}
	}

	// If no priority condition found, use first condition
	if bestStatus == "" {
		cond, ok := conditions[0].(map[string]any)
		if ok {
			condType, _ := cond["type"].(string)
			condStatus, _ := cond["status"].(string)
			if condStatus == "True" {
				bestStatus = condType
			} else {
				bestStatus = "Not" + condType
			}
		}
	}

	return bestStatus
}

func formatInt64Pair(a, b int64) string {
	return fmt.Sprintf("%d/%d", a, b)
}

func summarizeKarpenterNodePool(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "NodePool",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	// Disruption policy
	policy, _, _ := unstructured.NestedString(obj.Object, "spec", "disruption", "consolidationPolicy")
	if policy != "" {
		s.Strategy = policy
	}

	// Limits (CPU/memory)
	limits, found, _ := unstructured.NestedMap(obj.Object, "spec", "limits")
	if found {
		var limitParts []string
		if cpu, ok := limits["cpu"]; ok {
			limitParts = append(limitParts, fmt.Sprintf("cpu:%v", cpu))
		}
		if mem, ok := limits["memory"]; ok {
			limitParts = append(limitParts, fmt.Sprintf("mem:%v", mem))
		}
		if len(limitParts) > 0 {
			s.Capacity = strings.Join(limitParts, ",")
		}
	}

	// Requirement count
	reqs, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "requirements")
	if found && len(reqs) > 0 {
		s.Ready = fmt.Sprintf("%d requirements", len(reqs))
	}

	return s
}

func summarizeKarpenterNodeClaim(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "NodeClaim",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	// Instance type from status
	instanceType, _, _ := unstructured.NestedString(obj.Object, "status", "instanceType")
	if instanceType != "" {
		s.Type = instanceType
	}

	// Node name from status
	nodeName, _, _ := unstructured.NestedString(obj.Object, "status", "nodeName")
	if nodeName != "" {
		s.Node = nodeName
	}

	// Capacity from status
	capacity, found, _ := unstructured.NestedMap(obj.Object, "status", "capacity")
	if found {
		var capParts []string
		if cpu, ok := capacity["cpu"]; ok {
			capParts = append(capParts, fmt.Sprintf("cpu:%v", cpu))
		}
		if mem, ok := capacity["memory"]; ok {
			capParts = append(capParts, fmt.Sprintf("mem:%v", mem))
		}
		if len(capParts) > 0 {
			s.Capacity = strings.Join(capParts, ",")
		}
	}

	return s
}

func summarizeKedaScaledObject(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "ScaledObject",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Check paused annotation
	annotations := obj.GetAnnotations()
	if annotations != nil {
		if paused, ok := annotations["autoscaling.keda.sh/paused"]; ok && paused == "true" {
			s.Status = "Paused"
		}
	}
	if s.Status == "" {
		s.Status = extractReadyCondition(obj)
	}

	// Target ref
	targetKind, _, _ := unstructured.NestedString(obj.Object, "spec", "scaleTargetRef", "kind")
	targetName, _, _ := unstructured.NestedString(obj.Object, "spec", "scaleTargetRef", "name")
	if targetKind != "" && targetName != "" {
		s.Target = fmt.Sprintf("%s/%s", targetKind, targetName)
	}

	// Min/max replicas
	minReplicas, found, _ := unstructured.NestedInt64(obj.Object, "spec", "minReplicaCount")
	if found {
		min32 := int32(minReplicas)
		s.MinReplicas = &min32
	}
	maxReplicas, found, _ := unstructured.NestedInt64(obj.Object, "spec", "maxReplicaCount")
	if found {
		s.MaxReplicas = int32(maxReplicas)
	}

	// Trigger types
	triggers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "triggers")
	if found && len(triggers) > 0 {
		s.Ready = fmt.Sprintf("triggers: %s", extractTriggerTypes(triggers))
	}

	return s
}

func summarizeKedaScaledJob(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "ScaledJob",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	// Job target
	targetName, _, _ := unstructured.NestedString(obj.Object, "spec", "jobTargetRef", "name")
	if targetName != "" {
		s.Target = targetName
	}

	// Max replicas
	maxReplicas, found, _ := unstructured.NestedInt64(obj.Object, "spec", "maxReplicaCount")
	if found {
		s.MaxReplicas = int32(maxReplicas)
	}

	// Scaling strategy
	strategy, _, _ := unstructured.NestedString(obj.Object, "spec", "scalingStrategy", "strategy")
	if strategy != "" {
		s.Strategy = strategy
	}

	// Trigger types
	triggers, tfound, _ := unstructured.NestedSlice(obj.Object, "spec", "triggers")
	if tfound && len(triggers) > 0 {
		s.Ready = fmt.Sprintf("triggers: %s", extractTriggerTypes(triggers))
	}

	return s
}

func summarizeKedaTriggerAuthentication(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	var sources []string
	secretRefs, found, _ := unstructured.NestedSlice(obj.Object, "spec", "secretTargetRef")
	if found && len(secretRefs) > 0 {
		sources = append(sources, fmt.Sprintf("%d secrets", len(secretRefs)))
	}
	envVars, found, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	if found && len(envVars) > 0 {
		sources = append(sources, fmt.Sprintf("%d env", len(envVars)))
	}
	_, found, _ = unstructured.NestedMap(obj.Object, "spec", "hashiCorpVault")
	if found {
		sources = append(sources, "vault")
	}
	_, found, _ = unstructured.NestedMap(obj.Object, "spec", "azureKeyVault")
	if found {
		sources = append(sources, "azure-kv")
	}
	_, found, _ = unstructured.NestedMap(obj.Object, "spec", "awsSecretManager")
	if found {
		sources = append(sources, "aws-sm")
	}

	if len(sources) > 0 {
		s.Ready = strings.Join(sources, ", ")
	}

	return s
}

// extractTriggerTypes returns a deduplicated comma-separated list of trigger types from a triggers slice.
func extractTriggerTypes(triggers []any) string {
	seen := map[string]bool{}
	var types []string
	for _, t := range triggers {
		trigger, ok := t.(map[string]any)
		if !ok {
			continue
		}
		trigType, _ := trigger["type"].(string)
		if trigType != "" && !seen[trigType] {
			seen[trigType] = true
			types = append(types, trigType)
		}
	}
	return strings.Join(types, ", ")
}

func summarizeKarpenterNodeClass(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind: obj.GetKind(),
		Name: obj.GetName(),
		Age:  age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	// IAM role
	role, _, _ := unstructured.NestedString(obj.Object, "spec", "role")
	if role != "" {
		s.Type = role
	}

	// AMI selector
	amiTerms, found, _ := unstructured.NestedSlice(obj.Object, "spec", "amiSelectorTerms")
	if found && len(amiTerms) > 0 {
		if term, ok := amiTerms[0].(map[string]any); ok {
			if alias, ok := term["alias"].(string); ok {
				s.Image = alias
			} else if id, ok := term["id"].(string); ok {
				s.Image = id
			}
		}
	}

	// Volume info
	blockDevices, found, _ := unstructured.NestedSlice(obj.Object, "spec", "blockDeviceMappings")
	if found && len(blockDevices) > 0 {
		if bd, ok := blockDevices[0].(map[string]any); ok {
			if ebs, ok := bd["ebs"].(map[string]any); ok {
				var volParts []string
				if volType, ok := ebs["volumeType"].(string); ok {
					volParts = append(volParts, volType)
				}
				if volSize, ok := ebs["volumeSize"].(string); ok {
					volParts = append(volParts, volSize)
				}
				if len(volParts) > 0 {
					s.Capacity = strings.Join(volParts, " ")
				}
			}
		}
	}

	return s
}

func summarizeGatewayClass(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind: "GatewayClass",
		Name: obj.GetName(),
		Age:  age(obj.GetCreationTimestamp().Time),
	}

	// Accepted condition
	s.Status = extractReadyCondition(obj)

	// Controller name
	controllerName, _, _ := unstructured.NestedString(obj.Object, "spec", "controllerName")
	if controllerName != "" {
		s.Type = controllerName
	}

	return s
}

func summarizeGatewayRoute(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Gateway API routes use status.parents[].conditions, not status.conditions
	parents, found, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
	if found && len(parents) > 0 {
		allAccepted := true
		anyRejected := false
		checkedAny := false
		for _, p := range parents {
			pMap, ok := p.(map[string]any)
			if !ok {
				continue
			}
			conds, _, _ := unstructured.NestedSlice(pMap, "conditions")
			for _, c := range conds {
				cond, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if cond["type"] == "Accepted" {
					checkedAny = true
					if cond["status"] != "True" {
						allAccepted = false
						if cond["status"] == "False" {
							anyRejected = true
						}
					}
				}
			}
		}
		if !checkedAny {
			// No Accepted conditions found — can't determine status
		} else if allAccepted {
			s.Status = "Accepted"
		} else if anyRejected {
			s.Status = "NotAccepted"
		} else {
			s.Status = "Pending"
		}
	}

	// Parent gateway names
	parentRefs, found, _ := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	if found {
		var names []string
		for _, ref := range parentRefs {
			refMap, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			name, _ := refMap["name"].(string)
			if name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			s.Type = strings.Join(names, ", ")
		}
	}

	// Hostnames
	hostnames, found, _ := unstructured.NestedStringSlice(obj.Object, "spec", "hostnames")
	if found && len(hostnames) > 0 {
		s.Ready = strings.Join(hostnames, ", ")
	}

	// Rules count as strategy field
	rules, found, _ := unstructured.NestedSlice(obj.Object, "spec", "rules")
	if found && len(rules) > 0 {
		s.Strategy = fmt.Sprintf("%d rules", len(rules))
	}

	return s
}

func summarizeContourHTTPProxy(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "HTTPProxy",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Status from currentStatus field
	currentStatus, _, _ := unstructured.NestedString(obj.Object, "status", "currentStatus")
	if currentStatus != "" {
		s.Status = currentStatus
	} else {
		// Contour uses "Valid" condition, not "Ready"
		s.Status = extractConditionByType(obj, "Valid")
	}

	// FQDN
	fqdn, _, _ := unstructured.NestedString(obj.Object, "spec", "virtualhost", "fqdn")
	if fqdn != "" {
		s.Type = fqdn
	}

	// Route and service counts
	routes, found, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	svcCount := 0
	if found {
		for _, r := range routes {
			if rm, ok := r.(map[string]any); ok {
				if svcs, _, _ := unstructured.NestedSlice(rm, "services"); svcs != nil {
					svcCount += len(svcs)
				}
			}
		}
	}
	// Also count tcpproxy services
	tcpSvcs, tcpFound, _ := unstructured.NestedSlice(obj.Object, "spec", "tcpproxy", "services")
	if tcpFound {
		svcCount += len(tcpSvcs)
	}
	if len(routes) > 0 || svcCount > 0 {
		s.Strategy = fmt.Sprintf("%d routes, %d services", len(routes), svcCount)
	}

	// Includes count
	includes, found, _ := unstructured.NestedSlice(obj.Object, "spec", "includes")
	if found && len(includes) > 0 {
		s.Ready = fmt.Sprintf("%d includes", len(includes))
	}

	return s
}

// extractConditionByType extracts the status of a specific condition type from a K8s resource.
func extractConditionByType(obj *unstructured.Unstructured, conditionType string) string {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return ""
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		if condType != conditionType {
			continue
		}
		condStatus, _ := cond["status"].(string)
		if condStatus == "True" {
			return conditionType
		}
		reason, _ := cond["reason"].(string)
		if reason != "" {
			return reason
		}
		return "Not" + conditionType
	}
	return ""
}
