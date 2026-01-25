package k8s

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/skyhook-io/skyhook-explorer/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// Type aliases - these types are defined in the timeline package
type OwnerInfo = timeline.OwnerInfo
type DiffInfo = timeline.DiffInfo
type FieldChange = timeline.FieldChange

// ComputeDiff computes the diff between old and new objects based on kind
// Returns nil if no meaningful changes detected or kind not supported
func ComputeDiff(kind string, oldObj, newObj any) *DiffInfo {
	var changes []FieldChange
	var summaryParts []string

	switch kind {
	case "Deployment":
		changes, summaryParts = diffDeployment(oldObj, newObj)
	case "Pod":
		changes, summaryParts = diffPod(oldObj, newObj)
	case "Service":
		changes, summaryParts = diffService(oldObj, newObj)
	case "ConfigMap":
		changes, summaryParts = diffConfigMap(oldObj, newObj)
	case "Ingress":
		changes, summaryParts = diffIngress(oldObj, newObj)
	case "ReplicaSet":
		changes, summaryParts = diffReplicaSet(oldObj, newObj)
	case "DaemonSet":
		changes, summaryParts = diffDaemonSet(oldObj, newObj)
	case "StatefulSet":
		changes, summaryParts = diffStatefulSet(oldObj, newObj)
	case "HorizontalPodAutoscaler":
		changes, summaryParts = diffHPA(oldObj, newObj)
	case "Job":
		changes, summaryParts = diffJob(oldObj, newObj)
	case "Node":
		changes, summaryParts = diffNode(oldObj, newObj)
	case "PersistentVolumeClaim":
		changes, summaryParts = diffPVC(oldObj, newObj)
	default:
		return nil
	}

	if len(changes) == 0 {
		return nil
	}

	summary := ""
	if len(summaryParts) > 0 {
		for i, part := range summaryParts {
			if i > 0 {
				summary += ", "
			}
			summary += part
		}
	}

	return &DiffInfo{
		Fields:  changes,
		Summary: summary,
	}
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

	return changes, summary
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

	// Check completion
	if oldJob.Status.CompletionTime == nil && newJob.Status.CompletionTime != nil {
		changes = append(changes, FieldChange{
			Path:     "status.completionTime",
			OldValue: nil,
			NewValue: newJob.Status.CompletionTime.Time,
		})
		summary = append(summary, "completed")
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

	// Check Ready condition
	oldReady := getNodeConditionStatus(oldNode, corev1.NodeReady)
	newReady := getNodeConditionStatus(newNode, corev1.NodeReady)
	if oldReady != newReady {
		changes = append(changes, FieldChange{
			Path:     "status.conditions[Ready]",
			OldValue: oldReady,
			NewValue: newReady,
		})
		summary = append(summary, fmt.Sprintf("Ready: %s→%s", oldReady, newReady))
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

	return changes, summary
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
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += sep + parts[i]
	}
	return result
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
