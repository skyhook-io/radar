package k8s

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Type aliases — canonical definitions live in pkg/k8score.
type OwnerInfo = k8score.OwnerInfo
type DiffInfo = k8score.DiffInfo
type FieldChange = k8score.FieldChange

// kindDiffFunc is the per-kind diff dispatcher signature used by diffFunctions.
type kindDiffFunc func(oldObj, newObj any) ([]FieldChange, []string)

// diffFunctions is the single source of truth for kinds with audited diff
// coverage. ComputeDiff dispatches via this map, KindHasDiffer reads its
// keys — no separate "kinds we know about" list to drift out of sync.
//
// Adding a kind here is a CONTRACT: the diff function MUST surface every
// status field a user would care about, because for kinds in this map,
// recordToTimelineStore drops update events when the diff is empty.
var diffFunctions = map[string]kindDiffFunc{
	"Deployment":              diffDeployment,
	"Pod":                     diffPod,
	"Service":                 diffService,
	"ConfigMap":               diffConfigMap,
	"Ingress":                 diffIngress,
	"ReplicaSet":              diffReplicaSet,
	"DaemonSet":               diffDaemonSet,
	"StatefulSet":             diffStatefulSet,
	"HorizontalPodAutoscaler": diffHPA,
	"Job":                     diffJob,
	"Node":                    diffNode,
	"PersistentVolumeClaim":   diffPVC,
	"Application":             diffApplication,
	"Kustomization":           diffKustomization,
	"HelmRelease":             diffFluxHelmRelease,
	"GitRepository":           func(o, n any) ([]FieldChange, []string) { return diffFluxSource(o, n, "GitRepository") },
	"OCIRepository":           func(o, n any) ([]FieldChange, []string) { return diffFluxSource(o, n, "OCIRepository") },
	"HelmRepository":          func(o, n any) ([]FieldChange, []string) { return diffFluxSource(o, n, "HelmRepository") },
	"Gateway":                 diffGateway,
	"GatewayClass":            diffGatewayClass,
	"HTTPRoute":               diffGatewayRoute,
	"GRPCRoute":               diffGatewayRoute,
	"TCPRoute":                diffGatewayRoute,
	"TLSRoute":                diffGatewayRoute,
	"ReferenceGrant":          diffReferenceGrant,
}

// ComputeDiff computes the diff between old and new objects based on kind.
// Returns nil if the kind has no audited diff function or if no meaningful
// changes were detected.
func ComputeDiff(kind string, oldObj, newObj any) *DiffInfo {
	fn, ok := diffFunctions[kind]
	if !ok {
		return nil
	}
	changes, summaryParts := fn(oldObj, newObj)
	if len(changes) == 0 {
		return nil
	}

	var summary strings.Builder
	if len(summaryParts) > 0 {
		for i, part := range summaryParts {
			if i > 0 {
				summary.WriteString(", ")
			}
			summary.WriteString(part)
		}
	}

	return &DiffInfo{
		Fields:  changes,
		Summary: summary.String(),
	}
}

// typeAssertWarnedKinds dedups one-time warnings about type-assertion failures
// inside the per-kind diff helpers. A failure means an informer for a kind in
// KindHasDiffer is wired with the wrong factory — every update for that kind
// would silently drop as "no diff." Logging once per kind keeps it diagnosable
// without spamming.
var typeAssertWarnedKinds sync.Map

// warnUnstructuredAssertFailed logs once per kind when an unstructured diff
// helper receives a non-unstructured object.
func warnUnstructuredAssertFailed(kind string, got any) {
	if _, loaded := typeAssertWarnedKinds.LoadOrStore(kind, true); loaded {
		return
	}
	log.Printf("[history] WARN: %s diff received non-unstructured object (%T) — every %s update will silently drop. Likely informer wired to wrong factory.", kind, got, kind)
}

// KindHasDiffer reports whether the given kind has audited ComputeDiff
// coverage. The no-diff drop only fires for kinds in this set.
func KindHasDiffer(kind string) bool {
	_, ok := diffFunctions[kind]
	return ok
}

// diffDeployment computes diff for Deployment resources
func diffDeployment(oldObj, newObj any) ([]FieldChange, []string) {
	oldDep, ok1 := oldObj.(*appsv1.Deployment)
	newDep, ok2 := newObj.(*appsv1.Deployment)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldDep.Spec.Replicas != nil {
		oldReplicas = *oldDep.Spec.Replicas
	}
	if newDep.Spec.Replicas != nil {
		newReplicas = *newDep.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check container images
	oldImages := getContainerImages(oldDep.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newDep.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	// Check resource limits/requests
	oldResources := getContainerResources(oldDep.Spec.Template.Spec.Containers)
	newResources := getContainerResources(newDep.Spec.Template.Spec.Containers)
	if !equalResourceMaps(oldResources, newResources) {
		changes = append(changes, FieldChange{
			Path:     "spec.template.spec.containers[*].resources",
			OldValue: oldResources,
			NewValue: newResources,
		})
		summary = append(summary, "resources changed")
	}

	// Check paused state
	if oldDep.Spec.Paused != newDep.Spec.Paused {
		changes = append(changes, FieldChange{
			Path:     "spec.paused",
			OldValue: oldDep.Spec.Paused,
			NewValue: newDep.Spec.Paused,
		})
		if newDep.Spec.Paused {
			summary = append(summary, "rollout paused")
		} else {
			summary = append(summary, "rollout resumed")
		}
	}

	// Check ready replicas (rollout progress)
	if oldDep.Status.ReadyReplicas != newDep.Status.ReadyReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.readyReplicas",
			OldValue: oldDep.Status.ReadyReplicas,
			NewValue: newDep.Status.ReadyReplicas,
		})
		summary = append(summary, fmt.Sprintf("ready: %d→%d", oldDep.Status.ReadyReplicas, newDep.Status.ReadyReplicas))
	}

	// Check updated replicas (new version rollout)
	if oldDep.Status.UpdatedReplicas != newDep.Status.UpdatedReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.updatedReplicas",
			OldValue: oldDep.Status.UpdatedReplicas,
			NewValue: newDep.Status.UpdatedReplicas,
		})
		// Only add to summary if not already showing ready replicas change
		if oldDep.Status.ReadyReplicas == newDep.Status.ReadyReplicas {
			summary = append(summary, fmt.Sprintf("updated: %d→%d", oldDep.Status.UpdatedReplicas, newDep.Status.UpdatedReplicas))
		}
	}

	// Available=False = rollout failed minAvailable check; Progressing=False =
	// rollout stalled / deadline exceeded. Replica counts alone don't reveal these.
	for _, condType := range []appsv1.DeploymentConditionType{appsv1.DeploymentAvailable, appsv1.DeploymentProgressing} {
		oldStatus := getDeploymentConditionStatus(oldDep, condType)
		newStatus := getDeploymentConditionStatus(newDep, condType)
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

// diffPod computes diff for Pod resources
func diffPod(oldObj, newObj any) ([]FieldChange, []string) {
	oldPod, ok1 := oldObj.(*corev1.Pod)
	newPod, ok2 := newObj.(*corev1.Pod)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check phase
	if oldPod.Status.Phase != newPod.Status.Phase {
		changes = append(changes, FieldChange{
			Path:     "status.phase",
			OldValue: string(oldPod.Status.Phase),
			NewValue: string(newPod.Status.Phase),
		})
		summary = append(summary, fmt.Sprintf("phase: %s→%s", oldPod.Status.Phase, newPod.Status.Phase))
	}

	// Check restart counts
	oldRestarts := getTotalRestarts(oldPod.Status.ContainerStatuses)
	newRestarts := getTotalRestarts(newPod.Status.ContainerStatuses)
	if oldRestarts != newRestarts {
		changes = append(changes, FieldChange{
			Path:     "status.containerStatuses[*].restartCount",
			OldValue: oldRestarts,
			NewValue: newRestarts,
		})
		summary = append(summary, fmt.Sprintf("restarts: %d→%d", oldRestarts, newRestarts))
	}

	// Check for OOMKilled in any container
	for _, cs := range newPod.Status.ContainerStatuses {
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			// Check if this is a new OOM (not in old status)
			var wasOOM bool
			for _, oldCS := range oldPod.Status.ContainerStatuses {
				if oldCS.Name == cs.Name && oldCS.LastTerminationState.Terminated != nil &&
					oldCS.LastTerminationState.Terminated.Reason == "OOMKilled" &&
					oldCS.LastTerminationState.Terminated.FinishedAt == cs.LastTerminationState.Terminated.FinishedAt {
					wasOOM = true
					break
				}
			}
			if !wasOOM {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("status.containerStatuses[%s].lastState", cs.Name),
					OldValue: nil,
					NewValue: "OOMKilled",
				})
				summary = append(summary, fmt.Sprintf("%s: OOMKilled", cs.Name))
			}
		}
	}

	// Check container state transitions (Running, Waiting, Terminated)
	for _, newCS := range newPod.Status.ContainerStatuses {
		for _, oldCS := range oldPod.Status.ContainerStatuses {
			if oldCS.Name != newCS.Name {
				continue
			}
			oldState := getContainerState(oldCS)
			newState := getContainerState(newCS)
			if oldState != newState && oldState != "" && newState != "" {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("status.containerStatuses[%s].state", newCS.Name),
					OldValue: oldState,
					NewValue: newState,
				})
				summary = append(summary, fmt.Sprintf("%s: %s→%s", newCS.Name, oldState, newState))
			}
		}
	}

	// Check for node assignment (scheduling)
	if oldPod.Spec.NodeName == "" && newPod.Spec.NodeName != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.nodeName",
			OldValue: "",
			NewValue: newPod.Spec.NodeName,
		})
		summary = append(summary, fmt.Sprintf("scheduled to %s", newPod.Spec.NodeName))
	}

	// Check for IP assignment
	if oldPod.Status.PodIP == "" && newPod.Status.PodIP != "" {
		changes = append(changes, FieldChange{
			Path:     "status.podIP",
			OldValue: "",
			NewValue: newPod.Status.PodIP,
		})
		summary = append(summary, fmt.Sprintf("IP: %s", newPod.Status.PodIP))
	}

	// Ephemeral containers (kubectl debug attach). Status surfaces them as a
	// new EphemeralContainerStatuses entry — invisible to phase/restart/state.
	if len(newPod.Status.EphemeralContainerStatuses) > len(oldPod.Status.EphemeralContainerStatuses) {
		changes = append(changes, FieldChange{
			Path:     "status.ephemeralContainerStatuses",
			OldValue: len(oldPod.Status.EphemeralContainerStatuses),
			NewValue: len(newPod.Status.EphemeralContainerStatuses),
		})
		summary = append(summary, fmt.Sprintf("debug container attached (%d total)", len(newPod.Status.EphemeralContainerStatuses)))
	}

	// PodReady and ContainersReady transitions. Probe failures on a Running
	// container flip these without changing container state, so we'd otherwise
	// miss them entirely.
	for _, condType := range []corev1.PodConditionType{corev1.PodReady, corev1.ContainersReady} {
		oldStatus := getPodConditionStatus(oldPod, condType)
		newStatus := getPodConditionStatus(newPod, condType)
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

func getPodConditionStatus(p *corev1.Pod, condType corev1.PodConditionType) string {
	for _, c := range p.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

// getContainerState returns a string describing the container's current state
func getContainerState(cs corev1.ContainerStatus) string {
	if cs.State.Running != nil {
		return "Running"
	}
	if cs.State.Waiting != nil {
		if cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		return "Waiting"
	}
	if cs.State.Terminated != nil {
		if cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
		return "Terminated"
	}
	return ""
}

// diffService computes diff for Service resources
func diffService(oldObj, newObj any) ([]FieldChange, []string) {
	oldSvc, ok1 := oldObj.(*corev1.Service)
	newSvc, ok2 := newObj.(*corev1.Service)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check type
	if oldSvc.Spec.Type != newSvc.Spec.Type {
		changes = append(changes, FieldChange{
			Path:     "spec.type",
			OldValue: string(oldSvc.Spec.Type),
			NewValue: string(newSvc.Spec.Type),
		})
		summary = append(summary, fmt.Sprintf("type: %s→%s", oldSvc.Spec.Type, newSvc.Spec.Type))
	}

	// Check ports
	oldPorts := getServicePorts(oldSvc.Spec.Ports)
	newPorts := getServicePorts(newSvc.Spec.Ports)
	if !equalStringSlices(oldPorts, newPorts) {
		changes = append(changes, FieldChange{
			Path:     "spec.ports",
			OldValue: oldPorts,
			NewValue: newPorts,
		})
		summary = append(summary, "ports changed")
	}

	// Check selector
	if !equalStringMaps(oldSvc.Spec.Selector, newSvc.Spec.Selector) {
		changes = append(changes, FieldChange{
			Path:     "spec.selector",
			OldValue: oldSvc.Spec.Selector,
			NewValue: newSvc.Spec.Selector,
		})
		summary = append(summary, "selector changed")
	}

	// Check LoadBalancer status (IP/hostname assignment)
	oldLBAddrs := getLBAddresses(oldSvc.Status.LoadBalancer.Ingress)
	newLBAddrs := getLBAddresses(newSvc.Status.LoadBalancer.Ingress)
	if !equalStringSlices(oldLBAddrs, newLBAddrs) {
		if len(oldLBAddrs) == 0 && len(newLBAddrs) > 0 {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: nil,
				NewValue: newLBAddrs,
			})
			summary = append(summary, fmt.Sprintf("LB ready: %s", joinStrings(newLBAddrs, ", ")))
		} else if len(newLBAddrs) == 0 && len(oldLBAddrs) > 0 {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: oldLBAddrs,
				NewValue: nil,
			})
			summary = append(summary, "LB removed")
		} else {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: oldLBAddrs,
				NewValue: newLBAddrs,
			})
			summary = append(summary, "LB addresses changed")
		}
	}

	// Check ExternalIPs
	if !equalStringSlices(oldSvc.Spec.ExternalIPs, newSvc.Spec.ExternalIPs) {
		changes = append(changes, FieldChange{
			Path:     "spec.externalIPs",
			OldValue: oldSvc.Spec.ExternalIPs,
			NewValue: newSvc.Spec.ExternalIPs,
		})
		summary = append(summary, "externalIPs changed")
	}

	return changes, summary
}

// getLBAddresses extracts IP/hostname addresses from LoadBalancer ingress
func getLBAddresses(ingress []corev1.LoadBalancerIngress) []string {
	var addrs []string
	for _, ing := range ingress {
		if ing.IP != "" {
			addrs = append(addrs, ing.IP)
		} else if ing.Hostname != "" {
			addrs = append(addrs, ing.Hostname)
		}
	}
	return addrs
}

// diffConfigMap computes diff for ConfigMap resources
func diffConfigMap(oldObj, newObj any) ([]FieldChange, []string) {
	oldCM, ok1 := oldObj.(*corev1.ConfigMap)
	newCM, ok2 := newObj.(*corev1.ConfigMap)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check data keys (not values for security)
	oldKeys := getMapKeys(oldCM.Data)
	newKeys := getMapKeys(newCM.Data)

	addedKeys := diffStringSlices(newKeys, oldKeys)
	removedKeys := diffStringSlices(oldKeys, newKeys)
	modifiedKeys := getModifiedKeys(oldCM.Data, newCM.Data)

	if len(addedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (added keys)",
			OldValue: nil,
			NewValue: addedKeys,
		})
		summary = append(summary, fmt.Sprintf("added keys: %v", addedKeys))
	}
	if len(removedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (removed keys)",
			OldValue: removedKeys,
			NewValue: nil,
		})
		summary = append(summary, fmt.Sprintf("removed keys: %v", removedKeys))
	}
	if len(modifiedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (modified keys)",
			OldValue: modifiedKeys,
			NewValue: modifiedKeys,
		})
		summary = append(summary, fmt.Sprintf("modified keys: %v", modifiedKeys))
	}

	// binaryData (separate field for non-UTF-8 payloads). Same key-only semantic.
	oldBinKeys := getBinaryMapKeys(oldCM.BinaryData)
	newBinKeys := getBinaryMapKeys(newCM.BinaryData)
	addedBin := diffStringSlices(newBinKeys, oldBinKeys)
	removedBin := diffStringSlices(oldBinKeys, newBinKeys)
	if len(addedBin) > 0 {
		changes = append(changes, FieldChange{Path: "binaryData (added keys)", OldValue: nil, NewValue: addedBin})
		summary = append(summary, fmt.Sprintf("added binaryData keys: %v", addedBin))
	}
	if len(removedBin) > 0 {
		changes = append(changes, FieldChange{Path: "binaryData (removed keys)", OldValue: removedBin, NewValue: nil})
		summary = append(summary, fmt.Sprintf("removed binaryData keys: %v", removedBin))
	}

	// Immutable flag flips are user-meaningful (locks the CM until recreated).
	oldImmut := oldCM.Immutable != nil && *oldCM.Immutable
	newImmut := newCM.Immutable != nil && *newCM.Immutable
	if oldImmut != newImmut {
		changes = append(changes, FieldChange{Path: "immutable", OldValue: oldImmut, NewValue: newImmut})
		if newImmut {
			summary = append(summary, "marked immutable")
		} else {
			summary = append(summary, "immutable cleared")
		}
	}

	return changes, summary
}

// diffIngress computes diff for Ingress resources
func diffIngress(oldObj, newObj any) ([]FieldChange, []string) {
	oldIng, ok1 := oldObj.(*networkingv1.Ingress)
	newIng, ok2 := newObj.(*networkingv1.Ingress)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check rules count
	if len(oldIng.Spec.Rules) != len(newIng.Spec.Rules) {
		changes = append(changes, FieldChange{
			Path:     "spec.rules",
			OldValue: len(oldIng.Spec.Rules),
			NewValue: len(newIng.Spec.Rules),
		})
		summary = append(summary, fmt.Sprintf("rules: %d→%d", len(oldIng.Spec.Rules), len(newIng.Spec.Rules)))
	}

	// Check TLS
	oldTLS := len(oldIng.Spec.TLS)
	newTLS := len(newIng.Spec.TLS)
	if oldTLS != newTLS {
		changes = append(changes, FieldChange{
			Path:     "spec.tls",
			OldValue: oldTLS,
			NewValue: newTLS,
		})
		summary = append(summary, fmt.Sprintf("tls: %d→%d", oldTLS, newTLS))
	}

	// Check LoadBalancer status (address assignment)
	oldLBAddrs := getIngressLBAddresses(oldIng.Status.LoadBalancer.Ingress)
	newLBAddrs := getIngressLBAddresses(newIng.Status.LoadBalancer.Ingress)
	if !equalStringSlices(oldLBAddrs, newLBAddrs) {
		if len(oldLBAddrs) == 0 && len(newLBAddrs) > 0 {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: nil,
				NewValue: newLBAddrs,
			})
			summary = append(summary, fmt.Sprintf("LB ready: %s", joinStrings(newLBAddrs, ", ")))
		} else if len(newLBAddrs) == 0 && len(oldLBAddrs) > 0 {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: oldLBAddrs,
				NewValue: nil,
			})
			summary = append(summary, "LB removed")
		} else {
			changes = append(changes, FieldChange{
				Path:     "status.loadBalancer.ingress",
				OldValue: oldLBAddrs,
				NewValue: newLBAddrs,
			})
			summary = append(summary, "LB addresses changed")
		}
	}

	// Check hosts
	oldHosts := getIngressHosts(oldIng.Spec.Rules)
	newHosts := getIngressHosts(newIng.Spec.Rules)
	if !equalStringSlices(oldHosts, newHosts) {
		changes = append(changes, FieldChange{
			Path:     "spec.rules[*].host",
			OldValue: oldHosts,
			NewValue: newHosts,
		})
		summary = append(summary, "hosts changed")
	}

	return changes, summary
}

// getIngressLBAddresses extracts IP/hostname addresses from Ingress LoadBalancer status
func getIngressLBAddresses(ingress []networkingv1.IngressLoadBalancerIngress) []string {
	var addrs []string
	for _, ing := range ingress {
		if ing.IP != "" {
			addrs = append(addrs, ing.IP)
		} else if ing.Hostname != "" {
			addrs = append(addrs, ing.Hostname)
		}
	}
	return addrs
}

// getIngressHosts extracts hosts from Ingress rules
func getIngressHosts(rules []networkingv1.IngressRule) []string {
	var hosts []string
	for _, rule := range rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	return hosts
}

// diffReplicaSet computes diff for ReplicaSet resources
func diffReplicaSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldRS, ok1 := oldObj.(*appsv1.ReplicaSet)
	newRS, ok2 := newObj.(*appsv1.ReplicaSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldRS.Spec.Replicas != nil {
		oldReplicas = *oldRS.Spec.Replicas
	}
	if newRS.Spec.Replicas != nil {
		newReplicas = *newRS.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check ready replicas
	if oldRS.Status.ReadyReplicas != newRS.Status.ReadyReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.readyReplicas",
			OldValue: oldRS.Status.ReadyReplicas,
			NewValue: newRS.Status.ReadyReplicas,
		})
		summary = append(summary, fmt.Sprintf("ready: %d→%d", oldRS.Status.ReadyReplicas, newRS.Status.ReadyReplicas))
	}

	if oldRS.Status.AvailableReplicas != newRS.Status.AvailableReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.availableReplicas",
			OldValue: oldRS.Status.AvailableReplicas,
			NewValue: newRS.Status.AvailableReplicas,
		})
		if oldRS.Status.ReadyReplicas == newRS.Status.ReadyReplicas {
			summary = append(summary, fmt.Sprintf("available: %d→%d", oldRS.Status.AvailableReplicas, newRS.Status.AvailableReplicas))
		}
	}

	// ReplicaFailure=True surfaces pod-create failures (quota, image pull, scheduling).
	oldRF := getReplicaSetConditionStatus(oldRS, appsv1.ReplicaSetReplicaFailure)
	newRF := getReplicaSetConditionStatus(newRS, appsv1.ReplicaSetReplicaFailure)
	if oldRF != newRF && (oldRF != "" || newRF != "") {
		changes = append(changes, FieldChange{
			Path:     "status.conditions[ReplicaFailure]",
			OldValue: oldRF,
			NewValue: newRF,
		})
		summary = append(summary, fmt.Sprintf("ReplicaFailure: %s→%s", oldRF, newRF))
	}

	return changes, summary
}

// diffDaemonSet computes diff for DaemonSet resources
func diffDaemonSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldDS, ok1 := oldObj.(*appsv1.DaemonSet)
	newDS, ok2 := newObj.(*appsv1.DaemonSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check container images
	oldImages := getContainerImages(oldDS.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newDS.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	// Check desired/ready
	if oldDS.Status.DesiredNumberScheduled != newDS.Status.DesiredNumberScheduled {
		changes = append(changes, FieldChange{
			Path:     "status.desiredNumberScheduled",
			OldValue: oldDS.Status.DesiredNumberScheduled,
			NewValue: newDS.Status.DesiredNumberScheduled,
		})
		summary = append(summary, fmt.Sprintf("desired: %d→%d", oldDS.Status.DesiredNumberScheduled, newDS.Status.DesiredNumberScheduled))
	}

	// Check ready pods
	if oldDS.Status.NumberReady != newDS.Status.NumberReady {
		changes = append(changes, FieldChange{
			Path:     "status.numberReady",
			OldValue: oldDS.Status.NumberReady,
			NewValue: newDS.Status.NumberReady,
		})
		summary = append(summary, fmt.Sprintf("ready: %d→%d", oldDS.Status.NumberReady, newDS.Status.NumberReady))
	}

	// Check updated pods (rollout progress)
	if oldDS.Status.UpdatedNumberScheduled != newDS.Status.UpdatedNumberScheduled {
		changes = append(changes, FieldChange{
			Path:     "status.updatedNumberScheduled",
			OldValue: oldDS.Status.UpdatedNumberScheduled,
			NewValue: newDS.Status.UpdatedNumberScheduled,
		})
		summary = append(summary, fmt.Sprintf("updated: %d→%d nodes", oldDS.Status.UpdatedNumberScheduled, newDS.Status.UpdatedNumberScheduled))
	}

	// Check unavailable
	if oldDS.Status.NumberUnavailable != newDS.Status.NumberUnavailable {
		changes = append(changes, FieldChange{
			Path:     "status.numberUnavailable",
			OldValue: oldDS.Status.NumberUnavailable,
			NewValue: newDS.Status.NumberUnavailable,
		})
		if newDS.Status.NumberUnavailable > 0 {
			summary = append(summary, fmt.Sprintf("unavailable: %d", newDS.Status.NumberUnavailable))
		}
	}

	// NumberMisscheduled = pods running on nodes the selector now excludes
	// (e.g. taint added). Real signal that a tolerations/selector change took effect.
	if oldDS.Status.NumberMisscheduled != newDS.Status.NumberMisscheduled {
		changes = append(changes, FieldChange{
			Path:     "status.numberMisscheduled",
			OldValue: oldDS.Status.NumberMisscheduled,
			NewValue: newDS.Status.NumberMisscheduled,
		})
		if newDS.Status.NumberMisscheduled > 0 {
			summary = append(summary, fmt.Sprintf("misscheduled: %d", newDS.Status.NumberMisscheduled))
		}
	}

	return changes, summary
}

// diffStatefulSet computes diff for StatefulSet resources
func diffStatefulSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldSTS, ok1 := oldObj.(*appsv1.StatefulSet)
	newSTS, ok2 := newObj.(*appsv1.StatefulSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas (spec)
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldSTS.Spec.Replicas != nil {
		oldReplicas = *oldSTS.Spec.Replicas
	}
	if newSTS.Spec.Replicas != nil {
		newReplicas = *newSTS.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check container images
	oldImages := getContainerImages(oldSTS.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newSTS.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	// Check ready replicas
	if oldSTS.Status.ReadyReplicas != newSTS.Status.ReadyReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.readyReplicas",
			OldValue: oldSTS.Status.ReadyReplicas,
			NewValue: newSTS.Status.ReadyReplicas,
		})
		summary = append(summary, fmt.Sprintf("ready: %d→%d", oldSTS.Status.ReadyReplicas, newSTS.Status.ReadyReplicas))
	}

	// Check updated replicas (rolling update progress)
	if oldSTS.Status.UpdatedReplicas != newSTS.Status.UpdatedReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.updatedReplicas",
			OldValue: oldSTS.Status.UpdatedReplicas,
			NewValue: newSTS.Status.UpdatedReplicas,
		})
		if oldSTS.Status.ReadyReplicas == newSTS.Status.ReadyReplicas {
			summary = append(summary, fmt.Sprintf("updated: %d→%d", oldSTS.Status.UpdatedReplicas, newSTS.Status.UpdatedReplicas))
		}
	}

	// Check current revision vs update revision
	if oldSTS.Status.CurrentRevision != newSTS.Status.CurrentRevision {
		changes = append(changes, FieldChange{
			Path:     "status.currentRevision",
			OldValue: oldSTS.Status.CurrentRevision,
			NewValue: newSTS.Status.CurrentRevision,
		})
		summary = append(summary, "revision updated")
	}

	if oldSTS.Status.AvailableReplicas != newSTS.Status.AvailableReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.availableReplicas",
			OldValue: oldSTS.Status.AvailableReplicas,
			NewValue: newSTS.Status.AvailableReplicas,
		})
		if oldSTS.Status.ReadyReplicas == newSTS.Status.ReadyReplicas {
			summary = append(summary, fmt.Sprintf("available: %d→%d", oldSTS.Status.AvailableReplicas, newSTS.Status.AvailableReplicas))
		}
	}

	return changes, summary
}

// diffHPA computes diff for HorizontalPodAutoscaler resources
func diffHPA(oldObj, newObj any) ([]FieldChange, []string) {
	oldHPA, ok1 := oldObj.(*autoscalingv2.HorizontalPodAutoscaler)
	newHPA, ok2 := newObj.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check min replicas
	oldMin := int32(1)
	newMin := int32(1)
	if oldHPA.Spec.MinReplicas != nil {
		oldMin = *oldHPA.Spec.MinReplicas
	}
	if newHPA.Spec.MinReplicas != nil {
		newMin = *newHPA.Spec.MinReplicas
	}
	if oldMin != newMin {
		changes = append(changes, FieldChange{
			Path:     "spec.minReplicas",
			OldValue: oldMin,
			NewValue: newMin,
		})
		summary = append(summary, fmt.Sprintf("minReplicas: %d→%d", oldMin, newMin))
	}

	// Check max replicas
	if oldHPA.Spec.MaxReplicas != newHPA.Spec.MaxReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.maxReplicas",
			OldValue: oldHPA.Spec.MaxReplicas,
			NewValue: newHPA.Spec.MaxReplicas,
		})
		summary = append(summary, fmt.Sprintf("maxReplicas: %d→%d", oldHPA.Spec.MaxReplicas, newHPA.Spec.MaxReplicas))
	}

	// Check current replicas (scaling event)
	if oldHPA.Status.CurrentReplicas != newHPA.Status.CurrentReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.currentReplicas",
			OldValue: oldHPA.Status.CurrentReplicas,
			NewValue: newHPA.Status.CurrentReplicas,
		})
		direction := "scaled up"
		if newHPA.Status.CurrentReplicas < oldHPA.Status.CurrentReplicas {
			direction = "scaled down"
		}
		summary = append(summary, fmt.Sprintf("%s: %d→%d", direction, oldHPA.Status.CurrentReplicas, newHPA.Status.CurrentReplicas))
	}

	// Check desired replicas (scaling decision)
	if oldHPA.Status.DesiredReplicas != newHPA.Status.DesiredReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.desiredReplicas",
			OldValue: oldHPA.Status.DesiredReplicas,
			NewValue: newHPA.Status.DesiredReplicas,
		})
		if oldHPA.Status.CurrentReplicas == newHPA.Status.CurrentReplicas {
			// Only show desired if current didn't change (otherwise it's redundant)
			summary = append(summary, fmt.Sprintf("target: %d→%d replicas", oldHPA.Status.DesiredReplicas, newHPA.Status.DesiredReplicas))
		}
	}

	// Conditions: ScalingActive=False means HPA can't fetch metrics. AbleToScale=False
	// means it's hit a cooldown / spec error. ScalingLimited=True means the policy
	// capped the decision. All three are silent failures without this.
	for _, condType := range []autoscalingv2.HorizontalPodAutoscalerConditionType{
		autoscalingv2.ScalingActive, autoscalingv2.AbleToScale, autoscalingv2.ScalingLimited,
	} {
		oldStatus := getHPAConditionStatus(oldHPA, condType)
		newStatus := getHPAConditionStatus(newHPA, condType)
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

// diffJob computes diff for Job resources
func diffJob(oldObj, newObj any) ([]FieldChange, []string) {
	oldJob, ok1 := oldObj.(*batchv1.Job)
	newJob, ok2 := newObj.(*batchv1.Job)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check active pods
	if oldJob.Status.Active != newJob.Status.Active {
		changes = append(changes, FieldChange{
			Path:     "status.active",
			OldValue: oldJob.Status.Active,
			NewValue: newJob.Status.Active,
		})
		summary = append(summary, fmt.Sprintf("active: %d→%d", oldJob.Status.Active, newJob.Status.Active))
	}

	// Check succeeded pods
	if oldJob.Status.Succeeded != newJob.Status.Succeeded {
		changes = append(changes, FieldChange{
			Path:     "status.succeeded",
			OldValue: oldJob.Status.Succeeded,
			NewValue: newJob.Status.Succeeded,
		})
		summary = append(summary, fmt.Sprintf("succeeded: %d→%d", oldJob.Status.Succeeded, newJob.Status.Succeeded))
	}

	// Check failed pods
	if oldJob.Status.Failed != newJob.Status.Failed {
		changes = append(changes, FieldChange{
			Path:     "status.failed",
			OldValue: oldJob.Status.Failed,
			NewValue: newJob.Status.Failed,
		})
		summary = append(summary, fmt.Sprintf("failed: %d→%d", oldJob.Status.Failed, newJob.Status.Failed))
	}

	// Check terminal conditions. CompletionTime alone misses Failed jobs
	// (which never set CompletionTime) and the FailureTarget signal.
	jobCondSummary := map[batchv1.JobConditionType]string{
		batchv1.JobComplete:       "completed",
		batchv1.JobFailed:         "failed",
		batchv1.JobFailureTarget:  "failure target",
		batchv1.JobSuspended:      "suspended",
	}
	for _, condType := range []batchv1.JobConditionType{batchv1.JobComplete, batchv1.JobFailed, batchv1.JobSuspended, batchv1.JobFailureTarget} {
		oldStatus := getJobConditionStatus(oldJob, condType)
		newStatus := getJobConditionStatus(newJob, condType)
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			label := jobCondSummary[condType]
			switch newStatus {
			case "True":
				summary = append(summary, label)
			case "False":
				if oldStatus == "True" {
					summary = append(summary, "no longer "+label)
				} else {
					summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
				}
			default:
				summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
			}
		}
	}

	// First scheduling — startTime fills in when the controller picks up the job.
	if oldJob.Status.StartTime == nil && newJob.Status.StartTime != nil {
		changes = append(changes, FieldChange{
			Path:     "status.startTime",
			OldValue: nil,
			NewValue: newJob.Status.StartTime.Time,
		})
		summary = append(summary, "started")
	}

	// Check suspended
	oldSuspended := oldJob.Spec.Suspend != nil && *oldJob.Spec.Suspend
	newSuspended := newJob.Spec.Suspend != nil && *newJob.Spec.Suspend
	if oldSuspended != newSuspended {
		changes = append(changes, FieldChange{
			Path:     "spec.suspend",
			OldValue: oldSuspended,
			NewValue: newSuspended,
		})
		if newSuspended {
			summary = append(summary, "suspended")
		} else {
			summary = append(summary, "resumed")
		}
	}

	return changes, summary
}

// diffNode computes diff for Node resources
func diffNode(oldObj, newObj any) ([]FieldChange, []string) {
	oldNode, ok1 := oldObj.(*corev1.Node)
	newNode, ok2 := newObj.(*corev1.Node)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check unschedulable (cordon/uncordon)
	if oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable {
		changes = append(changes, FieldChange{
			Path:     "spec.unschedulable",
			OldValue: oldNode.Spec.Unschedulable,
			NewValue: newNode.Spec.Unschedulable,
		})
		if newNode.Spec.Unschedulable {
			summary = append(summary, "cordoned")
		} else {
			summary = append(summary, "uncordoned")
		}
	}

	// Check taints
	oldTaints := getTaintKeys(oldNode.Spec.Taints)
	newTaints := getTaintKeys(newNode.Spec.Taints)
	if !equalStringSlices(oldTaints, newTaints) {
		changes = append(changes, FieldChange{
			Path:     "spec.taints",
			OldValue: oldTaints,
			NewValue: newTaints,
		})
		added := diffStringSlices(newTaints, oldTaints)
		removed := diffStringSlices(oldTaints, newTaints)
		if len(added) > 0 {
			summary = append(summary, fmt.Sprintf("taints added: %v", added))
		}
		if len(removed) > 0 {
			summary = append(summary, fmt.Sprintf("taints removed: %v", removed))
		}
	}

	// Check pressure + ready conditions. MemoryPressure / DiskPressure /
	// PIDPressure flips signal imminent eviction or scheduling failures —
	// just as actionable as Ready, and previously missed entirely.
	for _, condType := range []corev1.NodeConditionType{
		corev1.NodeReady, corev1.NodeMemoryPressure, corev1.NodeDiskPressure,
		corev1.NodePIDPressure, corev1.NodeNetworkUnavailable,
	} {
		oldStatus := getNodeConditionStatus(oldNode, condType)
		newStatus := getNodeConditionStatus(newNode, condType)
		if oldStatus != newStatus {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	// Kubelet / kernel upgrades — captured by version flips on Node.Status.NodeInfo.
	if oldNode.Status.NodeInfo.KubeletVersion != newNode.Status.NodeInfo.KubeletVersion {
		changes = append(changes, FieldChange{
			Path:     "status.nodeInfo.kubeletVersion",
			OldValue: oldNode.Status.NodeInfo.KubeletVersion,
			NewValue: newNode.Status.NodeInfo.KubeletVersion,
		})
		summary = append(summary, fmt.Sprintf("kubelet: %s→%s", oldNode.Status.NodeInfo.KubeletVersion, newNode.Status.NodeInfo.KubeletVersion))
	}
	if oldNode.Status.NodeInfo.KernelVersion != newNode.Status.NodeInfo.KernelVersion {
		changes = append(changes, FieldChange{
			Path:     "status.nodeInfo.kernelVersion",
			OldValue: oldNode.Status.NodeInfo.KernelVersion,
			NewValue: newNode.Status.NodeInfo.KernelVersion,
		})
		summary = append(summary, "kernel upgraded")
	}

	// Allocatable capacity (cpu / memory / pods). Reduction during draining or
	// kubelet --reserved tuning is operator-relevant; expansion during hot-add
	// likewise. Capacity is the underlying physical; Allocatable is what the
	// scheduler sees and what changes more often, so we diff that one.
	for _, res := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourcePods} {
		oldVal := oldNode.Status.Allocatable[res]
		newVal := newNode.Status.Allocatable[res]
		if oldVal.Cmp(newVal) != 0 {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.allocatable.%s", res),
				OldValue: oldVal.String(),
				NewValue: newVal.String(),
			})
			summary = append(summary, fmt.Sprintf("allocatable %s: %s→%s", res, oldVal.String(), newVal.String()))
		}
	}

	return changes, summary
}

// diffPVC computes diff for PersistentVolumeClaim resources
func diffPVC(oldObj, newObj any) ([]FieldChange, []string) {
	oldPVC, ok1 := oldObj.(*corev1.PersistentVolumeClaim)
	newPVC, ok2 := newObj.(*corev1.PersistentVolumeClaim)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check phase
	if oldPVC.Status.Phase != newPVC.Status.Phase {
		changes = append(changes, FieldChange{
			Path:     "status.phase",
			OldValue: string(oldPVC.Status.Phase),
			NewValue: string(newPVC.Status.Phase),
		})
		summary = append(summary, fmt.Sprintf("phase: %s→%s", oldPVC.Status.Phase, newPVC.Status.Phase))
	}

	// Check volume binding
	if oldPVC.Spec.VolumeName == "" && newPVC.Spec.VolumeName != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.volumeName",
			OldValue: "",
			NewValue: newPVC.Spec.VolumeName,
		})
		summary = append(summary, fmt.Sprintf("bound to %s", newPVC.Spec.VolumeName))
	}

	// Check capacity change (resize)
	oldCap := oldPVC.Status.Capacity[corev1.ResourceStorage]
	newCap := newPVC.Status.Capacity[corev1.ResourceStorage]
	if !oldCap.IsZero() && !newCap.IsZero() && oldCap.Cmp(newCap) != 0 {
		changes = append(changes, FieldChange{
			Path:     "status.capacity.storage",
			OldValue: oldCap.String(),
			NewValue: newCap.String(),
		})
		summary = append(summary, fmt.Sprintf("capacity: %s→%s", oldCap.String(), newCap.String()))
	}

	// Resize lifecycle conditions — Resizing=True signals an in-flight expansion;
	// FileSystemResizePending=True signals the volume needs a pod restart to grow.
	for _, condType := range []corev1.PersistentVolumeClaimConditionType{
		corev1.PersistentVolumeClaimResizing,
		corev1.PersistentVolumeClaimFileSystemResizePending,
	} {
		oldStatus := getPVCConditionStatus(oldPVC, condType)
		newStatus := getPVCConditionStatus(newPVC, condType)
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

// diffApplication computes diff for ArgoCD Application resources (CRD)
func diffApplication(oldObj, newObj any) ([]FieldChange, []string) {
	oldApp, ok1 := oldObj.(*unstructured.Unstructured)
	newApp, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("Application", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Extract status fields
	oldStatus, _, _ := unstructured.NestedMap(oldApp.Object, "status")
	newStatus, _, _ := unstructured.NestedMap(newApp.Object, "status")

	// Check sync status
	oldSync, _, _ := unstructured.NestedString(oldStatus, "sync", "status")
	newSync, _, _ := unstructured.NestedString(newStatus, "sync", "status")
	if oldSync != newSync && oldSync != "" && newSync != "" {
		changes = append(changes, FieldChange{
			Path:     "status.sync.status",
			OldValue: oldSync,
			NewValue: newSync,
		})
		summary = append(summary, fmt.Sprintf("sync: %s→%s", oldSync, newSync))
	}

	// Check health status
	oldHealth, _, _ := unstructured.NestedString(oldStatus, "health", "status")
	newHealth, _, _ := unstructured.NestedString(newStatus, "health", "status")
	if oldHealth != newHealth && oldHealth != "" && newHealth != "" {
		changes = append(changes, FieldChange{
			Path:     "status.health.status",
			OldValue: oldHealth,
			NewValue: newHealth,
		})
		summary = append(summary, fmt.Sprintf("health: %s→%s", oldHealth, newHealth))
	}

	// Check revision (Git commit SHA)
	oldRevision, _, _ := unstructured.NestedString(oldStatus, "sync", "revision")
	newRevision, _, _ := unstructured.NestedString(newStatus, "sync", "revision")
	if oldRevision != newRevision && newRevision != "" {
		changes = append(changes, FieldChange{
			Path:     "status.sync.revision",
			OldValue: truncateRevision(oldRevision),
			NewValue: truncateRevision(newRevision),
		})
		if oldRevision == "" {
			summary = append(summary, fmt.Sprintf("revision: %s", truncateRevision(newRevision)))
		} else {
			summary = append(summary, fmt.Sprintf("revision: %s→%s", truncateRevision(oldRevision), truncateRevision(newRevision)))
		}
	}

	// Check operation state (sync operation started/completed)
	oldOp, oldOpExists, _ := unstructured.NestedMap(oldStatus, "operationState")
	newOp, newOpExists, _ := unstructured.NestedMap(newStatus, "operationState")

	// Operation started
	if !oldOpExists && newOpExists {
		opPhase, _, _ := unstructured.NestedString(newOp, "phase")
		opMessage, _, _ := unstructured.NestedString(newOp, "message")
		changes = append(changes, FieldChange{
			Path:     "status.operationState.phase",
			OldValue: nil,
			NewValue: opPhase,
		})
		if opPhase == "Running" {
			summary = append(summary, "sync started")
		} else if opPhase != "" {
			summary = append(summary, fmt.Sprintf("operation: %s", opPhase))
		}
		if opMessage != "" && opPhase == "Failed" {
			summary = append(summary, fmt.Sprintf("error: %s", truncateMessage(opMessage)))
		}
	}

	// Operation phase changed
	if oldOpExists && newOpExists {
		oldPhase, _, _ := unstructured.NestedString(oldOp, "phase")
		newPhase, _, _ := unstructured.NestedString(newOp, "phase")
		if oldPhase != newPhase && newPhase != "" {
			changes = append(changes, FieldChange{
				Path:     "status.operationState.phase",
				OldValue: oldPhase,
				NewValue: newPhase,
			})
			switch newPhase {
			case "Succeeded":
				summary = append(summary, "sync completed")
			case "Failed":
				opMessage, _, _ := unstructured.NestedString(newOp, "message")
				summary = append(summary, "sync failed")
				if opMessage != "" {
					summary = append(summary, fmt.Sprintf("error: %s", truncateMessage(opMessage)))
				}
			case "Running":
				summary = append(summary, "sync in progress")
			default:
				summary = append(summary, fmt.Sprintf("operation: %s", newPhase))
			}
		}
	}

	// Operation completed (removed from status)
	if oldOpExists && !newOpExists {
		oldPhase, _, _ := unstructured.NestedString(oldOp, "phase")
		if oldPhase == "Running" {
			changes = append(changes, FieldChange{
				Path:     "status.operationState",
				OldValue: "Running",
				NewValue: nil,
			})
			summary = append(summary, "operation cleared")
		}
	}

	// Check sync policy changes (auto-sync enabled/disabled)
	oldAutoSync, oldAutoExists, _ := unstructured.NestedMap(oldApp.Object, "spec", "syncPolicy", "automated")
	newAutoSync, newAutoExists, _ := unstructured.NestedMap(newApp.Object, "spec", "syncPolicy", "automated")

	if !oldAutoExists && newAutoExists {
		changes = append(changes, FieldChange{
			Path:     "spec.syncPolicy.automated",
			OldValue: nil,
			NewValue: newAutoSync,
		})
		summary = append(summary, "auto-sync enabled")
	} else if oldAutoExists && !newAutoExists {
		changes = append(changes, FieldChange{
			Path:     "spec.syncPolicy.automated",
			OldValue: oldAutoSync,
			NewValue: nil,
		})
		summary = append(summary, "auto-sync disabled")
	}

	// Check target revision change
	oldTargetRev, _, _ := unstructured.NestedString(oldApp.Object, "spec", "source", "targetRevision")
	newTargetRev, _, _ := unstructured.NestedString(newApp.Object, "spec", "source", "targetRevision")
	if oldTargetRev != newTargetRev && newTargetRev != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.source.targetRevision",
			OldValue: oldTargetRev,
			NewValue: newTargetRev,
		})
		summary = append(summary, fmt.Sprintf("target: %s→%s", oldTargetRev, newTargetRev))
	}

	// Check destination namespace change
	oldDestNS, _, _ := unstructured.NestedString(oldApp.Object, "spec", "destination", "namespace")
	newDestNS, _, _ := unstructured.NestedString(newApp.Object, "spec", "destination", "namespace")
	if oldDestNS != newDestNS && newDestNS != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.destination.namespace",
			OldValue: oldDestNS,
			NewValue: newDestNS,
		})
		summary = append(summary, fmt.Sprintf("destination ns: %s→%s", oldDestNS, newDestNS))
	}

	// status.conditions — Argo surfaces non-standard signals here:
	// SyncError, ComparisonError, OrphanedResourceWarning, ExcludedResourceWarning,
	// SharedResourceWarning, RepeatedResourceWarning. These flip on operator
	// config issues that don't show in sync.status / health.status.
	oldAppConds := getConditionMap(oldStatus, "conditions")
	newAppConds := getConditionMap(newStatus, "conditions")
	for condType, newCond := range newAppConds {
		if oldAppConds[condType] != newCond {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldAppConds[condType],
				NewValue: newCond,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldAppConds[condType], newCond))
		}
	}
	for condType, oldCond := range oldAppConds {
		if _, present := newAppConds[condType]; !present {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions[%s]", condType),
				OldValue: oldCond,
				NewValue: nil,
			})
			summary = append(summary, fmt.Sprintf("%s cleared", condType))
		}
	}

	// Image rolls — an already-Synced+Healthy app can roll new images via auto-sync
	// without flipping sync.status or health.status. Without this, those updates
	// drop as no-diff and the timeline misses the actual deploy event.
	oldImages, _, _ := unstructured.NestedStringSlice(oldStatus, "summary", "images")
	newImages, _, _ := unstructured.NestedStringSlice(newStatus, "summary", "images")
	if !equalStringSlices(oldImages, newImages) {
		added := diffStringSlices(newImages, oldImages)
		removed := diffStringSlices(oldImages, newImages)
		changes = append(changes, FieldChange{
			Path:     "status.summary.images",
			OldValue: oldImages,
			NewValue: newImages,
		})
		switch {
		case len(added) > 0 && len(removed) == 0:
			summary = append(summary, fmt.Sprintf("images +%d", len(added)))
		case len(removed) > 0 && len(added) == 0:
			summary = append(summary, fmt.Sprintf("images -%d", len(removed)))
		default:
			summary = append(summary, "images changed")
		}
	}

	return changes, summary
}

// truncateRevision truncates a git revision to first 7 chars (short SHA)
func truncateRevision(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// truncateMessage truncates a message to a reasonable display length
func truncateMessage(msg string) string {
	if len(msg) > 50 {
		return msg[:47] + "..."
	}
	return msg
}

// diffKustomization computes diff for FluxCD Kustomization resources (CRD)
func diffKustomization(oldObj, newObj any) ([]FieldChange, []string) {
	oldKs, ok1 := oldObj.(*unstructured.Unstructured)
	newKs, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("Kustomization", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Extract status fields
	oldStatus, _, _ := unstructured.NestedMap(oldKs.Object, "status")
	newStatus, _, _ := unstructured.NestedMap(newKs.Object, "status")

	// Check ready condition
	oldReady := getFluxConditionStatus(oldStatus, "Ready")
	newReady := getFluxConditionStatus(newStatus, "Ready")
	if oldReady != newReady && oldReady != "" && newReady != "" {
		changes = append(changes, FieldChange{
			Path:     "status.conditions[Ready]",
			OldValue: oldReady,
			NewValue: newReady,
		})
		summary = append(summary, fmt.Sprintf("ready: %s→%s", oldReady, newReady))
	}

	// Check reconciling condition (sync in progress)
	oldReconciling := getFluxConditionStatus(oldStatus, "Reconciling")
	newReconciling := getFluxConditionStatus(newStatus, "Reconciling")
	if oldReconciling != newReconciling {
		if newReconciling == "True" {
			summary = append(summary, "reconciling")
		} else if oldReconciling == "True" && newReconciling == "False" {
			summary = append(summary, "reconcile completed")
		}
	}

	// Stalled=True means Flux gave up retrying — terminal failure that Ready alone hides.
	oldStalled := getFluxConditionStatus(oldStatus, "Stalled")
	newStalled := getFluxConditionStatus(newStatus, "Stalled")
	if oldStalled != newStalled && (oldStalled != "" || newStalled != "") {
		changes = append(changes, FieldChange{Path: "status.conditions[Stalled]", OldValue: oldStalled, NewValue: newStalled})
		summary = append(summary, fmt.Sprintf("stalled: %s→%s", oldStalled, newStalled))
	}

	// Check last applied revision
	oldRevision, _, _ := unstructured.NestedString(oldStatus, "lastAppliedRevision")
	newRevision, _, _ := unstructured.NestedString(newStatus, "lastAppliedRevision")
	if oldRevision != newRevision && newRevision != "" {
		changes = append(changes, FieldChange{
			Path:     "status.lastAppliedRevision",
			OldValue: truncateRevision(oldRevision),
			NewValue: truncateRevision(newRevision),
		})
		if oldRevision == "" {
			summary = append(summary, fmt.Sprintf("revision: %s", truncateRevision(newRevision)))
		} else {
			summary = append(summary, fmt.Sprintf("revision: %s→%s", truncateRevision(oldRevision), truncateRevision(newRevision)))
		}
	}

	// Check inventory count (number of managed resources)
	oldInventory, _, _ := unstructured.NestedSlice(oldStatus, "inventory", "entries")
	newInventory, _, _ := unstructured.NestedSlice(newStatus, "inventory", "entries")
	if len(oldInventory) != len(newInventory) {
		changes = append(changes, FieldChange{
			Path:     "status.inventory.entries",
			OldValue: len(oldInventory),
			NewValue: len(newInventory),
		})
		summary = append(summary, fmt.Sprintf("resources: %d→%d", len(oldInventory), len(newInventory)))
	}

	// Check suspended state
	oldSuspend, _, _ := unstructured.NestedBool(oldKs.Object, "spec", "suspend")
	newSuspend, _, _ := unstructured.NestedBool(newKs.Object, "spec", "suspend")
	if oldSuspend != newSuspend {
		changes = append(changes, FieldChange{
			Path:     "spec.suspend",
			OldValue: oldSuspend,
			NewValue: newSuspend,
		})
		if newSuspend {
			summary = append(summary, "suspended")
		} else {
			summary = append(summary, "resumed")
		}
	}

	// Check source reference change
	oldSourceRef, _, _ := unstructured.NestedString(oldKs.Object, "spec", "sourceRef", "name")
	newSourceRef, _, _ := unstructured.NestedString(newKs.Object, "spec", "sourceRef", "name")
	if oldSourceRef != newSourceRef && newSourceRef != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.sourceRef.name",
			OldValue: oldSourceRef,
			NewValue: newSourceRef,
		})
		summary = append(summary, fmt.Sprintf("source: %s→%s", oldSourceRef, newSourceRef))
	}

	// Check path change
	oldPath, _, _ := unstructured.NestedString(oldKs.Object, "spec", "path")
	newPath, _, _ := unstructured.NestedString(newKs.Object, "spec", "path")
	if oldPath != newPath && newPath != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.path",
			OldValue: oldPath,
			NewValue: newPath,
		})
		summary = append(summary, fmt.Sprintf("path: %s→%s", oldPath, newPath))
	}

	return changes, summary
}

// diffFluxHelmRelease computes diff for FluxCD HelmRelease resources (CRD)
func diffFluxHelmRelease(oldObj, newObj any) ([]FieldChange, []string) {
	oldHR, ok1 := oldObj.(*unstructured.Unstructured)
	newHR, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("HelmRelease", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Extract status fields
	oldStatus, _, _ := unstructured.NestedMap(oldHR.Object, "status")
	newStatus, _, _ := unstructured.NestedMap(newHR.Object, "status")

	// Check ready condition
	oldReady := getFluxConditionStatus(oldStatus, "Ready")
	newReady := getFluxConditionStatus(newStatus, "Ready")
	if oldReady != newReady && oldReady != "" && newReady != "" {
		changes = append(changes, FieldChange{
			Path:     "status.conditions[Ready]",
			OldValue: oldReady,
			NewValue: newReady,
		})
		summary = append(summary, fmt.Sprintf("ready: %s→%s", oldReady, newReady))
	}

	// Released / Stalled conditions — Released=False is the canonical Helm install
	// failure signal; Stalled=True is the give-up state that Ready alone obscures.
	for _, condType := range []string{"Released", "Stalled"} {
		oldCond := getFluxConditionStatus(oldStatus, condType)
		newCond := getFluxConditionStatus(newStatus, condType)
		if oldCond != newCond && (oldCond != "" || newCond != "") {
			changes = append(changes, FieldChange{Path: fmt.Sprintf("status.conditions[%s]", condType), OldValue: oldCond, NewValue: newCond})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldCond, newCond))
		}
	}

	// Check last applied revision (Helm chart version)
	oldRevision, _, _ := unstructured.NestedInt64(oldStatus, "lastAppliedRevision")
	newRevision, _, _ := unstructured.NestedInt64(newStatus, "lastAppliedRevision")
	if oldRevision != newRevision && newRevision != 0 {
		changes = append(changes, FieldChange{
			Path:     "status.lastAppliedRevision",
			OldValue: oldRevision,
			NewValue: newRevision,
		})
		summary = append(summary, fmt.Sprintf("revision: %d→%d", oldRevision, newRevision))
	}

	// Check last released revision
	oldReleased, _, _ := unstructured.NestedInt64(oldStatus, "lastReleaseRevision")
	newReleased, _, _ := unstructured.NestedInt64(newStatus, "lastReleaseRevision")
	if oldReleased != newReleased && newReleased != 0 {
		changes = append(changes, FieldChange{
			Path:     "status.lastReleaseRevision",
			OldValue: oldReleased,
			NewValue: newReleased,
		})
		if oldRevision == newRevision { // Only show if not already showing applied revision
			summary = append(summary, fmt.Sprintf("released: rev %d→%d", oldReleased, newReleased))
		}
	}

	// Check suspended state
	oldSuspend, _, _ := unstructured.NestedBool(oldHR.Object, "spec", "suspend")
	newSuspend, _, _ := unstructured.NestedBool(newHR.Object, "spec", "suspend")
	if oldSuspend != newSuspend {
		changes = append(changes, FieldChange{
			Path:     "spec.suspend",
			OldValue: oldSuspend,
			NewValue: newSuspend,
		})
		if newSuspend {
			summary = append(summary, "suspended")
		} else {
			summary = append(summary, "resumed")
		}
	}

	// Check chart version change
	oldChartVersion, _, _ := unstructured.NestedString(oldHR.Object, "spec", "chart", "spec", "version")
	newChartVersion, _, _ := unstructured.NestedString(newHR.Object, "spec", "chart", "spec", "version")
	if oldChartVersion != newChartVersion && newChartVersion != "" {
		changes = append(changes, FieldChange{
			Path:     "spec.chart.spec.version",
			OldValue: oldChartVersion,
			NewValue: newChartVersion,
		})
		summary = append(summary, fmt.Sprintf("chart: %s→%s", oldChartVersion, newChartVersion))
	}

	// Check upgrade/install failures
	oldFailures, _, _ := unstructured.NestedInt64(oldStatus, "installFailures")
	newFailures, _, _ := unstructured.NestedInt64(newStatus, "installFailures")
	if newFailures > oldFailures {
		changes = append(changes, FieldChange{
			Path:     "status.installFailures",
			OldValue: oldFailures,
			NewValue: newFailures,
		})
		summary = append(summary, fmt.Sprintf("install failures: %d", newFailures))
	}

	oldUpgradeFailures, _, _ := unstructured.NestedInt64(oldStatus, "upgradeFailures")
	newUpgradeFailures, _, _ := unstructured.NestedInt64(newStatus, "upgradeFailures")
	if newUpgradeFailures > oldUpgradeFailures {
		changes = append(changes, FieldChange{
			Path:     "status.upgradeFailures",
			OldValue: oldUpgradeFailures,
			NewValue: newUpgradeFailures,
		})
		summary = append(summary, fmt.Sprintf("upgrade failures: %d", newUpgradeFailures))
	}

	return changes, summary
}

// diffFluxSource computes diff for FluxCD source resources (GitRepository, OCIRepository, HelmRepository)
func diffFluxSource(oldObj, newObj any, kind string) ([]FieldChange, []string) {
	oldSrc, ok1 := oldObj.(*unstructured.Unstructured)
	newSrc, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed(kind, oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Extract status fields
	oldStatus, _, _ := unstructured.NestedMap(oldSrc.Object, "status")
	newStatus, _, _ := unstructured.NestedMap(newSrc.Object, "status")

	// Check ready condition
	oldReady := getFluxConditionStatus(oldStatus, "Ready")
	newReady := getFluxConditionStatus(newStatus, "Ready")
	if oldReady != newReady && oldReady != "" && newReady != "" {
		changes = append(changes, FieldChange{
			Path:     "status.conditions[Ready]",
			OldValue: oldReady,
			NewValue: newReady,
		})
		summary = append(summary, fmt.Sprintf("ready: %s→%s", oldReady, newReady))
	}

	// Stalled / FetchFailed — fetch errors flip these without flipping Ready
	// the same way (Stalled implies give-up; FetchFailed is the upstream signal).
	for _, condType := range []string{"Stalled", "FetchFailed"} {
		oldCond := getFluxConditionStatus(oldStatus, condType)
		newCond := getFluxConditionStatus(newStatus, condType)
		if oldCond != newCond && (oldCond != "" || newCond != "") {
			changes = append(changes, FieldChange{Path: fmt.Sprintf("status.conditions[%s]", condType), OldValue: oldCond, NewValue: newCond})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldCond, newCond))
		}
	}

	// Check artifact revision (commit SHA or chart version)
	oldArtifactRev, _, _ := unstructured.NestedString(oldStatus, "artifact", "revision")
	newArtifactRev, _, _ := unstructured.NestedString(newStatus, "artifact", "revision")
	if oldArtifactRev != newArtifactRev && newArtifactRev != "" {
		changes = append(changes, FieldChange{
			Path:     "status.artifact.revision",
			OldValue: truncateSourceRevision(oldArtifactRev),
			NewValue: truncateSourceRevision(newArtifactRev),
		})
		if oldArtifactRev == "" {
			summary = append(summary, fmt.Sprintf("artifact: %s", truncateSourceRevision(newArtifactRev)))
		} else {
			summary = append(summary, fmt.Sprintf("artifact: %s→%s", truncateSourceRevision(oldArtifactRev), truncateSourceRevision(newArtifactRev)))
		}
	}

	// Check suspended state
	oldSuspend, _, _ := unstructured.NestedBool(oldSrc.Object, "spec", "suspend")
	newSuspend, _, _ := unstructured.NestedBool(newSrc.Object, "spec", "suspend")
	if oldSuspend != newSuspend {
		changes = append(changes, FieldChange{
			Path:     "spec.suspend",
			OldValue: oldSuspend,
			NewValue: newSuspend,
		})
		if newSuspend {
			summary = append(summary, "suspended")
		} else {
			summary = append(summary, "resumed")
		}
	}

	// GitRepository specific: check URL or ref change
	if kind == "GitRepository" {
		oldURL, _, _ := unstructured.NestedString(oldSrc.Object, "spec", "url")
		newURL, _, _ := unstructured.NestedString(newSrc.Object, "spec", "url")
		if oldURL != newURL && newURL != "" {
			changes = append(changes, FieldChange{
				Path:     "spec.url",
				OldValue: oldURL,
				NewValue: newURL,
			})
			summary = append(summary, "url changed")
		}

		oldRef, _, _ := unstructured.NestedString(oldSrc.Object, "spec", "ref", "branch")
		newRef, _, _ := unstructured.NestedString(newSrc.Object, "spec", "ref", "branch")
		if oldRef != newRef && newRef != "" {
			changes = append(changes, FieldChange{
				Path:     "spec.ref.branch",
				OldValue: oldRef,
				NewValue: newRef,
			})
			summary = append(summary, fmt.Sprintf("branch: %s→%s", oldRef, newRef))
		}
	}

	// HelmRepository specific: check URL change
	if kind == "HelmRepository" {
		oldURL, _, _ := unstructured.NestedString(oldSrc.Object, "spec", "url")
		newURL, _, _ := unstructured.NestedString(newSrc.Object, "spec", "url")
		if oldURL != newURL && newURL != "" {
			changes = append(changes, FieldChange{
				Path:     "spec.url",
				OldValue: oldURL,
				NewValue: newURL,
			})
			summary = append(summary, "url changed")
		}
	}

	return changes, summary
}

// getFluxConditionStatus extracts the status of a named condition from Flux status
func getFluxConditionStatus(status map[string]any, conditionType string) string {
	conditions, ok, _ := unstructured.NestedSlice(status, "conditions")
	if !ok {
		return ""
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == conditionType {
			if status, ok := cond["status"].(string); ok {
				return status
			}
		}
	}
	return ""
}

// truncateSourceRevision truncates a source revision (may include branch@sha format)
func truncateSourceRevision(rev string) string {
	// Handle "branch@sha:commit" format from FluxCD
	if before, after, ok := strings.Cut(rev, "@"); ok {
		branch := before
		rest := after
		// Truncate the SHA part
		if colonIdx := strings.Index(rest, ":"); colonIdx != -1 {
			sha := rest[colonIdx+1:]
			if len(sha) > 7 {
				sha = sha[:7]
			}
			return branch + "@" + sha
		}
		if len(rest) > 7 {
			rest = rest[:7]
		}
		return branch + "@" + rest
	}
	// Simple revision - just truncate
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// getTaintKeys extracts taint keys from a list of taints
func getTaintKeys(taints []corev1.Taint) []string {
	keys := make([]string, len(taints))
	for i, t := range taints {
		keys[i] = t.Key
	}
	return keys
}

// getNodeConditionStatus gets the status of a specific node condition
func getNodeConditionStatus(node *corev1.Node, condType corev1.NodeConditionType) string {
	for _, cond := range node.Status.Conditions {
		if cond.Type == condType {
			return string(cond.Status)
		}
	}
	return "Unknown"
}

// Per-kind condition lookups. Each Conditions field on the typed K8s structs
// has a different element type, so we can't share one generic helper without
// reflection — and the per-kind helpers stay short.

func getDeploymentConditionStatus(d *appsv1.Deployment, condType appsv1.DeploymentConditionType) string {
	for _, c := range d.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

func getReplicaSetConditionStatus(rs *appsv1.ReplicaSet, condType appsv1.ReplicaSetConditionType) string {
	for _, c := range rs.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

func getJobConditionStatus(j *batchv1.Job, condType batchv1.JobConditionType) string {
	for _, c := range j.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

func getHPAConditionStatus(h *autoscalingv2.HorizontalPodAutoscaler, condType autoscalingv2.HorizontalPodAutoscalerConditionType) string {
	for _, c := range h.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

func getPVCConditionStatus(pvc *corev1.PersistentVolumeClaim, condType corev1.PersistentVolumeClaimConditionType) string {
	for _, c := range pvc.Status.Conditions {
		if c.Type == condType {
			return string(c.Status)
		}
	}
	return ""
}

// Helper functions

func getContainerImages(containers []corev1.Container) map[string]string {
	images := make(map[string]string)
	for _, c := range containers {
		images[c.Name] = c.Image
	}
	return images
}

func getContainerResources(containers []corev1.Container) map[string]any {
	resources := make(map[string]any)
	for _, c := range containers {
		resources[c.Name] = map[string]any{
			"limits":   c.Resources.Limits,
			"requests": c.Resources.Requests,
		}
	}
	return resources
}

func getTotalRestarts(statuses []corev1.ContainerStatus) int32 {
	var total int32
	for _, s := range statuses {
		total += s.RestartCount
	}
	return total
}

func getServicePorts(ports []corev1.ServicePort) []string {
	result := make([]string, len(ports))
	for i, p := range ports {
		result[i] = fmt.Sprintf("%s/%d→%d", p.Protocol, p.Port, p.TargetPort.IntVal)
	}
	return result
}

func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func getBinaryMapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func getModifiedKeys(old, new map[string]string) []string {
	var modified []string
	for k, oldV := range old {
		if newV, ok := new[k]; ok && oldV != newV {
			modified = append(modified, k)
		}
	}
	return modified
}

func diffStringSlices(a, b []string) []string {
	bMap := make(map[string]bool)
	for _, s := range b {
		bMap[s] = true
	}
	var diff []string
	for _, s := range a {
		if !bMap[s] {
			diff = append(diff, s)
		}
	}
	return diff
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func equalResourceMaps(a, b map[string]any) bool {
	// Simple comparison - could be more sophisticated
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

func truncateImage(image string) string {
	// Show just the tag or digest if image is long
	if len(image) > 40 {
		// Try to find tag
		for i := len(image) - 1; i >= 0; i-- {
			if image[i] == ':' || image[i] == '@' {
				return "..." + image[i:]
			}
		}
		return image[:37] + "..."
	}
	return image
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		result.WriteString(sep + parts[i])
	}
	return result.String()
}

// extractPrimaryIssue extracts the primary issue from a diff summary string
// Returns the most significant issue (OOMKilled, CrashLoopBackOff, etc.) or empty string
func extractPrimaryIssue(summary string) string {
	if summary == "" {
		return ""
	}

	// Priority order of issues to detect
	priorityIssues := []string{
		"OOMKilled",
		"CrashLoopBackOff",
		"ImagePullBackOff",
		"ErrImagePull",
		"CreateContainerConfigError",
		"CreateContainerError",
		"InvalidImageName",
		"RunContainerError",
		"PreStartHookError",
		"PostStartHookError",
		"Unschedulable",
		"FailedScheduling",
		"FailedMount",
		"NodeNotReady",
		"Evicted",
	}

	for _, issue := range priorityIssues {
		if strings.Contains(summary, issue) {
			return issue
		}
	}

	return ""
}

// diffGateway computes diff for Gateway API Gateway resources (unstructured)
func diffGateway(oldObj, newObj any) ([]FieldChange, []string) {
	oldGW, ok1 := oldObj.(*unstructured.Unstructured)
	newGW, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("Gateway", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check listener count
	oldListeners, _, _ := unstructured.NestedSlice(oldGW.Object, "spec", "listeners")
	newListeners, _, _ := unstructured.NestedSlice(newGW.Object, "spec", "listeners")
	if len(oldListeners) != len(newListeners) {
		changes = append(changes, FieldChange{
			Path:     "spec.listeners",
			OldValue: fmt.Sprintf("%d listeners", len(oldListeners)),
			NewValue: fmt.Sprintf("%d listeners", len(newListeners)),
		})
		summary = append(summary, fmt.Sprintf("listeners: %d→%d", len(oldListeners), len(newListeners)))
	}

	// Check addresses
	oldAddrs, _, _ := unstructured.NestedSlice(oldGW.Object, "status", "addresses")
	newAddrs, _, _ := unstructured.NestedSlice(newGW.Object, "status", "addresses")
	if len(oldAddrs) != len(newAddrs) {
		changes = append(changes, FieldChange{
			Path:     "status.addresses",
			OldValue: fmt.Sprintf("%d addresses", len(oldAddrs)),
			NewValue: fmt.Sprintf("%d addresses", len(newAddrs)),
		})
		summary = append(summary, fmt.Sprintf("addresses: %d→%d", len(oldAddrs), len(newAddrs)))
	}

	// Check conditions (Accepted, Programmed)
	oldConditions := getConditionMap(oldGW.Object, "status", "conditions")
	newConditions := getConditionMap(newGW.Object, "status", "conditions")
	for _, condType := range []string{"Accepted", "Programmed"} {
		oldStatus := oldConditions[condType]
		newStatus := newConditions[condType]
		if oldStatus != newStatus && oldStatus != "" && newStatus != "" {
			changes = append(changes, FieldChange{
				Path:     fmt.Sprintf("status.conditions.%s", condType),
				OldValue: oldStatus,
				NewValue: newStatus,
			})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

// diffGatewayRoute computes diff for Gateway API route resources (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
func diffGatewayRoute(oldObj, newObj any) ([]FieldChange, []string) {
	oldRoute, ok1 := oldObj.(*unstructured.Unstructured)
	newRoute, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("GatewayRoute", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check hostnames
	oldHostnames, _, _ := unstructured.NestedStringSlice(oldRoute.Object, "spec", "hostnames")
	newHostnames, _, _ := unstructured.NestedStringSlice(newRoute.Object, "spec", "hostnames")
	if strings.Join(oldHostnames, ",") != strings.Join(newHostnames, ",") {
		changes = append(changes, FieldChange{
			Path:     "spec.hostnames",
			OldValue: strings.Join(oldHostnames, ", "),
			NewValue: strings.Join(newHostnames, ", "),
		})
		summary = append(summary, fmt.Sprintf("hostnames: %s→%s", strings.Join(oldHostnames, ","), strings.Join(newHostnames, ",")))
	}

	// Check rules count
	oldRules, _, _ := unstructured.NestedSlice(oldRoute.Object, "spec", "rules")
	newRules, _, _ := unstructured.NestedSlice(newRoute.Object, "spec", "rules")
	if len(oldRules) != len(newRules) {
		changes = append(changes, FieldChange{
			Path:     "spec.rules",
			OldValue: fmt.Sprintf("%d rules", len(oldRules)),
			NewValue: fmt.Sprintf("%d rules", len(newRules)),
		})
		summary = append(summary, fmt.Sprintf("rules: %d→%d", len(oldRules), len(newRules)))
	}

	// Per-parent per-condition diff. Counting "accepted parents" misses
	// flips on Programmed / ResolvedRefs (which are how a route signals
	// "config invalid" or "backend missing") — those leave Accepted=True
	// while the route is functionally broken.
	oldParents, _, _ := unstructured.NestedSlice(oldRoute.Object, "status", "parents")
	newParents, _, _ := unstructured.NestedSlice(newRoute.Object, "status", "parents")
	if len(oldParents) != len(newParents) {
		changes = append(changes, FieldChange{
			Path:     "status.parents",
			OldValue: fmt.Sprintf("%d parents", len(oldParents)),
			NewValue: fmt.Sprintf("%d parents", len(newParents)),
		})
		summary = append(summary, fmt.Sprintf("parents: %d→%d", len(oldParents), len(newParents)))
	}
	oldByParent := indexParentConditions(oldParents)
	newByParent := indexParentConditions(newParents)

	// Walk the union of parent keys so we catch parents that disappeared from
	// the new snapshot too — a parent vanishing is a real signal even when
	// total parent count is unchanged (one removed + one added).
	parentKeys := make(map[string]struct{}, len(oldByParent)+len(newByParent))
	for k := range oldByParent {
		parentKeys[k] = struct{}{}
	}
	for k := range newByParent {
		parentKeys[k] = struct{}{}
	}
	for parentKey := range parentKeys {
		oldConds := oldByParent[parentKey]
		newConds := newByParent[parentKey]
		for _, condType := range []string{"Accepted", "ResolvedRefs", "Programmed"} {
			oldStatus, oldHas := oldConds[condType]
			newStatus, newHas := newConds[condType]
			if !oldHas && !newHas {
				continue
			}
			if oldStatus != newStatus {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("status.parents[%s].conditions[%s]", parentKey, condType),
					OldValue: oldStatus,
					NewValue: newStatus,
				})
				summary = append(summary, fmt.Sprintf("%s/%s: %s→%s", parentKey, condType, oldStatus, newStatus))
			}
		}
	}

	return changes, summary
}

// diffGatewayClass computes diff for Gateway-API GatewayClass resources.
// GatewayClass is a cluster-scoped declaration that controllers reconcile;
// status updates fire on every reconcile and are noise except when the
// Accepted/SupportedVersion conditions flip or the controller name changes.
func diffGatewayClass(oldObj, newObj any) ([]FieldChange, []string) {
	oldGC, ok1 := oldObj.(*unstructured.Unstructured)
	newGC, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("GatewayClass", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Controller name change (rebinding to a different implementation).
	oldCtrl, _, _ := unstructured.NestedString(oldGC.Object, "spec", "controllerName")
	newCtrl, _, _ := unstructured.NestedString(newGC.Object, "spec", "controllerName")
	if oldCtrl != newCtrl && (oldCtrl != "" || newCtrl != "") {
		changes = append(changes, FieldChange{Path: "spec.controllerName", OldValue: oldCtrl, NewValue: newCtrl})
		summary = append(summary, fmt.Sprintf("controller: %s→%s", oldCtrl, newCtrl))
	}

	// Accepted / SupportedVersion conditions.
	oldConditions := getConditionMap(oldGC.Object, "status", "conditions")
	newConditions := getConditionMap(newGC.Object, "status", "conditions")
	for _, condType := range []string{"Accepted", "SupportedVersion"} {
		oldStatus := oldConditions[condType]
		newStatus := newConditions[condType]
		if oldStatus != newStatus && (oldStatus != "" || newStatus != "") {
			changes = append(changes, FieldChange{Path: fmt.Sprintf("status.conditions.%s", condType), OldValue: oldStatus, NewValue: newStatus})
			summary = append(summary, fmt.Sprintf("%s: %s→%s", condType, oldStatus, newStatus))
		}
	}

	return changes, summary
}

// diffReferenceGrant computes diff for Gateway-API ReferenceGrant resources.
// ReferenceGrant is cross-namespace permission. The interesting state lives
// entirely in spec.from / spec.to — everything else is reconcile noise.
func diffReferenceGrant(oldObj, newObj any) ([]FieldChange, []string) {
	oldRG, ok1 := oldObj.(*unstructured.Unstructured)
	newRG, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		warnUnstructuredAssertFailed("ReferenceGrant", oldObj)
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	oldFrom, _, _ := unstructured.NestedSlice(oldRG.Object, "spec", "from")
	newFrom, _, _ := unstructured.NestedSlice(newRG.Object, "spec", "from")
	if len(oldFrom) != len(newFrom) {
		changes = append(changes, FieldChange{Path: "spec.from", OldValue: len(oldFrom), NewValue: len(newFrom)})
		summary = append(summary, fmt.Sprintf("from: %d→%d", len(oldFrom), len(newFrom)))
	}

	oldTo, _, _ := unstructured.NestedSlice(oldRG.Object, "spec", "to")
	newTo, _, _ := unstructured.NestedSlice(newRG.Object, "spec", "to")
	if len(oldTo) != len(newTo) {
		changes = append(changes, FieldChange{Path: "spec.to", OldValue: len(oldTo), NewValue: len(newTo)})
		summary = append(summary, fmt.Sprintf("to: %d→%d", len(oldTo), len(newTo)))
	}

	return changes, summary
}

// indexParentConditions extracts {parentKey -> {conditionType -> status}} from
// a Gateway-API route's status.parents. The parent key is
// "<group>/<kind>/<ns>/<name>/<sectionName>/<port>" — Gateway API permits a
// route to attach to the same Gateway twice via different listeners
// disambiguated by sectionName / port, so omitting them collapses distinct
// per-listener conditions into one bucket and silently loses flips on the
// second listener.
func indexParentConditions(parents []any) map[string]map[string]string {
	out := make(map[string]map[string]string, len(parents))
	for _, p := range parents {
		pMap, ok := p.(map[string]any)
		if !ok {
			continue
		}
		group, _, _ := unstructured.NestedString(pMap, "parentRef", "group")
		kind, _, _ := unstructured.NestedString(pMap, "parentRef", "kind")
		ns, _, _ := unstructured.NestedString(pMap, "parentRef", "namespace")
		name, _, _ := unstructured.NestedString(pMap, "parentRef", "name")
		sectionName, _, _ := unstructured.NestedString(pMap, "parentRef", "sectionName")
		port, _, _ := unstructured.NestedInt64(pMap, "parentRef", "port")
		key := fmt.Sprintf("%s/%s/%s/%s/%s/%d", group, kind, ns, name, sectionName, port)
		conds := make(map[string]string)
		conditions, _, _ := unstructured.NestedSlice(pMap, "conditions")
		for _, c := range conditions {
			cMap, ok := c.(map[string]any)
			if !ok {
				continue
			}
			t, _ := cMap["type"].(string)
			s, _ := cMap["status"].(string)
			if t != "" {
				conds[t] = s
			}
		}
		out[key] = conds
	}
	return out
}

// getConditionMap extracts a map of condition type -> status from nested conditions
func getConditionMap(obj map[string]any, path ...string) map[string]string {
	result := make(map[string]string)
	conditions, _, _ := unstructured.NestedSlice(obj, path...)
	for _, c := range conditions {
		cMap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cMap["type"].(string)
		condStatus, _ := cMap["status"].(string)
		if condType != "" {
			result[condType] = condStatus
		}
	}
	return result
}

