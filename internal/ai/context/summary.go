package context

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ResourceSummary is the typed output for Summary-level minification.
// Typed struct ensures no extra fields can leak.
type ResourceSummary struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Status    string `json:"status,omitempty"`
	Ready     string `json:"ready,omitempty"`
	Issue     string `json:"issue,omitempty"`
	Age       string `json:"age,omitempty"`

	// Type-specific fields (only populated when relevant)
	Image       string   `json:"image,omitempty"`
	Ports       string   `json:"ports,omitempty"`
	Schedule    string   `json:"schedule,omitempty"`
	Type        string   `json:"type,omitempty"` // Service type, Secret type
	Selector    string   `json:"selector,omitempty"`
	ClusterIP   string   `json:"clusterIP,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`
	Restarts    int32    `json:"restarts,omitempty"`
	Node        string   `json:"node,omitempty"`
	Strategy    string   `json:"strategy,omitempty"`
	Completions string   `json:"completions,omitempty"`
	Duration    string   `json:"duration,omitempty"`
	Suspended     *bool    `json:"suspended,omitempty"`
	Unschedulable *bool    `json:"unschedulable,omitempty"`
	Active      int      `json:"active,omitempty"`
	Target      string   `json:"target,omitempty"`
	MinReplicas *int32   `json:"minReplicas,omitempty"`
	MaxReplicas int32    `json:"maxReplicas,omitempty"`
	Current     int32    `json:"current,omitempty"`
	Desired     int32    `json:"desired,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Version     string   `json:"version,omitempty"`
	Pressures   []string `json:"pressures,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	StorageClass string  `json:"storageClass,omitempty"`
	Capacity    string   `json:"capacity,omitempty"`
	AccessModes []string `json:"accessModes,omitempty"`
	Owner       string   `json:"owner,omitempty"`
}

// summarize dispatches to the appropriate per-type extractor.
func summarize(obj runtime.Object) (*ResourceSummary, error) {
	switch o := obj.(type) {
	case *corev1.Pod:
		return summarizePod(o), nil
	case *appsv1.Deployment:
		return summarizeDeployment(o), nil
	case *appsv1.StatefulSet:
		return summarizeStatefulSet(o), nil
	case *appsv1.DaemonSet:
		return summarizeDaemonSet(o), nil
	case *corev1.Service:
		return summarizeService(o), nil
	case *networkingv1.Ingress:
		return summarizeIngress(o), nil
	case *batchv1.Job:
		return summarizeJob(o), nil
	case *batchv1.CronJob:
		return summarizeCronJob(o), nil
	case *autoscalingv2.HorizontalPodAutoscaler:
		return summarizeHPA(o), nil
	case *corev1.Node:
		return summarizeNode(o), nil
	case *corev1.ConfigMap:
		return summarizeConfigMap(o), nil
	case *corev1.Secret:
		return summarizeSecret(o), nil
	case *corev1.PersistentVolumeClaim:
		return summarizePVC(o), nil
	case *appsv1.ReplicaSet:
		return summarizeReplicaSet(o), nil
	case *corev1.Namespace:
		return summarizeNamespace(o), nil
	default:
		return nil, fmt.Errorf("unsupported type for summary: %T", obj)
	}
}

func summarizePod(pod *corev1.Pod) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Pod",
		Name:      pod.Name,
		Namespace: pod.Namespace,
		Status:    string(pod.Status.Phase),
		Node:      pod.Spec.NodeName,
		Age:       age(pod.CreationTimestamp.Time),
	}

	// Image from first container
	if len(pod.Spec.Containers) > 0 {
		s.Image = pod.Spec.Containers[0].Image
	}

	// Ready count and restarts
	ready, total := int32(0), int32(len(pod.Status.ContainerStatuses))
	var restarts int32
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}
	if total > 0 {
		s.Ready = fmt.Sprintf("%d/%d", ready, total)
	}
	s.Restarts = restarts

	// Detect issue
	s.Issue = getPodIssue(pod)
	if s.Issue != "" {
		s.Status = s.Issue
	}

	return s
}

func summarizeDeployment(dep *appsv1.Deployment) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Deployment",
		Name:      dep.Name,
		Namespace: dep.Namespace,
		Ready:     fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, dep.Status.Replicas),
		Strategy:  string(dep.Spec.Strategy.Type),
		Age:       age(dep.CreationTimestamp.Time),
	}

	if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.Replicas > 0 {
		s.Status = "Running"
	} else if dep.Status.Replicas == 0 {
		s.Status = "Scaled to 0"
	} else {
		s.Status = "Progressing"
	}

	// Image from first container in template
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		s.Image = dep.Spec.Template.Spec.Containers[0].Image
	}

	return s
}

func summarizeStatefulSet(sts *appsv1.StatefulSet) *ResourceSummary {
	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}

	s := &ResourceSummary{
		Kind:      "StatefulSet",
		Name:      sts.Name,
		Namespace: sts.Namespace,
		Ready:     fmt.Sprintf("%d/%d", sts.Status.ReadyReplicas, replicas),
		Age:       age(sts.CreationTimestamp.Time),
	}

	if sts.Status.ReadyReplicas == replicas && replicas > 0 {
		s.Status = "Running"
	} else if replicas == 0 {
		s.Status = "Scaled to 0"
	} else {
		s.Status = "Progressing"
	}

	if len(sts.Spec.Template.Spec.Containers) > 0 {
		s.Image = sts.Spec.Template.Spec.Containers[0].Image
	}

	return s
}

func summarizeDaemonSet(ds *appsv1.DaemonSet) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "DaemonSet",
		Name:      ds.Name,
		Namespace: ds.Namespace,
		Ready:     fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled),
		Age:       age(ds.CreationTimestamp.Time),
	}

	if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 {
		s.Status = "Running"
	} else {
		s.Status = "Progressing"
	}

	if len(ds.Spec.Template.Spec.Containers) > 0 {
		s.Image = ds.Spec.Template.Spec.Containers[0].Image
	}

	return s
}

func summarizeService(svc *corev1.Service) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Service",
		Name:      svc.Name,
		Namespace: svc.Namespace,
		Type:      string(svc.Spec.Type),
		ClusterIP: svc.Spec.ClusterIP,
		Age:       age(svc.CreationTimestamp.Time),
	}

	// Format ports
	var ports []string
	for _, p := range svc.Spec.Ports {
		ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
	}
	if len(ports) > 0 {
		s.Ports = strings.Join(ports, ", ")
	}

	// Format selector
	if len(svc.Spec.Selector) > 0 {
		var pairs []string
		for k, v := range svc.Spec.Selector {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
		}
		s.Selector = strings.Join(pairs, ",")
	}

	return s
}

func summarizeIngress(ing *networkingv1.Ingress) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Ingress",
		Name:      ing.Name,
		Namespace: ing.Namespace,
		Age:       age(ing.CreationTimestamp.Time),
	}

	if ing.Spec.IngressClassName != nil {
		s.Type = *ing.Spec.IngressClassName
	} else if v, ok := ing.Annotations["kubernetes.io/ingress.class"]; ok {
		s.Type = v
	}

	hostSet := make(map[string]bool)
	for _, rule := range ing.Spec.Rules {
		if rule.Host != "" {
			hostSet[rule.Host] = true
		}
	}
	for h := range hostSet {
		s.Hosts = append(s.Hosts, h)
	}

	return s
}

func summarizeJob(job *batchv1.Job) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Job",
		Name:      job.Name,
		Namespace: job.Namespace,
		Age:       age(job.CreationTimestamp.Time),
	}

	// Status from conditions
	s.Status = "Running"
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			s.Status = "Complete"
		} else if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			s.Status = "Failed"
		}
	}

	// Completions
	target := int32(1)
	if job.Spec.Completions != nil {
		target = *job.Spec.Completions
	}
	s.Completions = fmt.Sprintf("%d/%d", job.Status.Succeeded, target)

	// Duration
	if job.Status.StartTime != nil && job.Status.CompletionTime != nil {
		d := job.Status.CompletionTime.Sub(job.Status.StartTime.Time)
		s.Duration = formatDuration(d)
	}

	return s
}

func summarizeCronJob(cj *batchv1.CronJob) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "CronJob",
		Name:      cj.Name,
		Namespace: cj.Namespace,
		Schedule:  cj.Spec.Schedule,
		Active:    len(cj.Status.Active),
		Age:       age(cj.CreationTimestamp.Time),
	}

	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		s.Suspended = cj.Spec.Suspend
	} else {
		if cj.Status.LastScheduleTime != nil && time.Since(cj.Status.LastScheduleTime.Time) > 24*time.Hour {
			s.Issue = "no recent runs"
		} else if cj.Status.LastScheduleTime == nil && time.Since(cj.CreationTimestamp.Time) > 24*time.Hour {
			s.Issue = "never scheduled"
		}
	}

	return s
}

func summarizeHPA(hpa *autoscalingv2.HorizontalPodAutoscaler) *ResourceSummary {
	s := &ResourceSummary{
		Kind:        "HorizontalPodAutoscaler",
		Name:        hpa.Name,
		Namespace:   hpa.Namespace,
		Target:      fmt.Sprintf("%s/%s", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name),
		MaxReplicas: hpa.Spec.MaxReplicas,
		Current:     hpa.Status.CurrentReplicas,
		Desired:     hpa.Status.DesiredReplicas,
		Age:         age(hpa.CreationTimestamp.Time),
	}

	if hpa.Spec.MinReplicas != nil {
		s.MinReplicas = hpa.Spec.MinReplicas
	}

	if hpa.Spec.MaxReplicas > 0 && hpa.Status.CurrentReplicas >= hpa.Spec.MaxReplicas && hpa.Status.DesiredReplicas >= hpa.Spec.MaxReplicas {
		s.Issue = "maxed"
	}

	return s
}

func summarizeNode(node *corev1.Node) *ResourceSummary {
	s := &ResourceSummary{
		Kind:    "Node",
		Name:    node.Name,
		Version: node.Status.NodeInfo.KubeletVersion,
		Age:     age(node.CreationTimestamp.Time),
	}

	// Roles from labels
	for label := range node.Labels {
		const prefix = "node-role.kubernetes.io/"
		if strings.HasPrefix(label, prefix) {
			s.Roles = append(s.Roles, label[len(prefix):])
		}
	}

	// Status and pressure conditions
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				s.Status = "Ready"
			} else {
				s.Status = "NotReady"
				s.Issue = c.Reason
			}
		}
		if c.Status == corev1.ConditionTrue {
			switch c.Type {
			case corev1.NodeMemoryPressure:
				s.Pressures = append(s.Pressures, "MemoryPressure")
			case corev1.NodeDiskPressure:
				s.Pressures = append(s.Pressures, "DiskPressure")
			case corev1.NodePIDPressure:
				s.Pressures = append(s.Pressures, "PIDPressure")
			}
		}
	}

	// Cordoned/unschedulable status
	if node.Spec.Unschedulable {
		unschedulable := true
		s.Unschedulable = &unschedulable
		if s.Status != "" {
			s.Status += ",SchedulingDisabled"
		} else {
			s.Status = "SchedulingDisabled"
		}
	}

	return s
}

func summarizeConfigMap(cm *corev1.ConfigMap) *ResourceSummary {
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	return &ResourceSummary{
		Kind:      "ConfigMap",
		Name:      cm.Name,
		Namespace: cm.Namespace,
		Keys:      keys,
		Age:       age(cm.CreationTimestamp.Time),
	}
}

func summarizeSecret(secret *corev1.Secret) *ResourceSummary {
	keys := make([]string, 0, len(secret.Data)+len(secret.StringData))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	for k := range secret.StringData {
		keys = append(keys, k)
	}
	return &ResourceSummary{
		Kind:      "Secret",
		Name:      secret.Name,
		Namespace: secret.Namespace,
		Type:      string(secret.Type),
		Keys:      keys,
		Age:       age(secret.CreationTimestamp.Time),
	}
}

func summarizePVC(pvc *corev1.PersistentVolumeClaim) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "PersistentVolumeClaim",
		Name:      pvc.Name,
		Namespace: pvc.Namespace,
		Status:    string(pvc.Status.Phase),
		Age:       age(pvc.CreationTimestamp.Time),
	}

	if pvc.Spec.StorageClassName != nil {
		s.StorageClass = *pvc.Spec.StorageClassName
	}
	if cap, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		s.Capacity = cap.String()
	}
	for _, m := range pvc.Spec.AccessModes {
		s.AccessModes = append(s.AccessModes, string(m))
	}

	return s
}

func summarizeReplicaSet(rs *appsv1.ReplicaSet) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "ReplicaSet",
		Name:      rs.Name,
		Namespace: rs.Namespace,
		Ready:     fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, rs.Status.Replicas),
		Age:       age(rs.CreationTimestamp.Time),
	}

	if len(rs.Spec.Template.Spec.Containers) > 0 {
		s.Image = rs.Spec.Template.Spec.Containers[0].Image
	}

	if len(rs.OwnerReferences) > 0 {
		s.Owner = fmt.Sprintf("%s/%s", rs.OwnerReferences[0].Kind, rs.OwnerReferences[0].Name)
	}

	return s
}

func summarizeNamespace(ns *corev1.Namespace) *ResourceSummary {
	return &ResourceSummary{
		Kind:   "Namespace",
		Name:   ns.Name,
		Status: string(ns.Status.Phase),
		Age:    age(ns.CreationTimestamp.Time),
	}
}

// getPodIssue extracts the primary issue from a pod's status.
func getPodIssue(pod *corev1.Pod) string {
	// Check container statuses for waiting reasons
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" && cs.State.Terminated.Reason != "Completed" {
			return cs.State.Terminated.Reason
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason != "" && cs.LastTerminationState.Terminated.Reason != "Completed" {
			return cs.LastTerminationState.Terminated.Reason
		}
	}
	// Check init container statuses
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" && cs.State.Terminated.Reason != "Completed" {
			return cs.State.Terminated.Reason
		}
	}
	return ""
}

// age formats a duration since the given time as a human-readable string.
func age(created time.Time) string {
	if created.IsZero() {
		return ""
	}
	d := time.Since(created)
	return formatDuration(d)
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
