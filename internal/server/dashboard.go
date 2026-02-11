package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/topology"
	"github.com/skyhook-io/radar/internal/traffic"
)

// DashboardResponse is the aggregated response for the home dashboard
type DashboardResponse struct {
	Cluster         DashboardCluster         `json:"cluster"`
	Health          DashboardHealth          `json:"health"`
	Problems        []DashboardProblem       `json:"problems"`
	ResourceCounts  DashboardResourceCounts  `json:"resourceCounts"`
	RecentEvents    []DashboardEvent         `json:"recentEvents"`
	RecentChanges   []DashboardChange        `json:"recentChanges"`
	TopologySummary DashboardTopologySummary `json:"topologySummary"`
	TrafficSummary  *DashboardTrafficSummary `json:"trafficSummary"`
	HelmReleases    DashboardHelmSummary     `json:"helmReleases"`
	Metrics         *DashboardMetrics        `json:"metrics"`
}

// DashboardCRDsResponse is the response for CRD counts (loaded lazily)
type DashboardCRDsResponse struct {
	TopCRDs []DashboardCRDCount `json:"topCRDs"`
}

type DashboardCluster struct {
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Connected bool   `json:"connected"`
}

type DashboardHealth struct {
	Healthy       int `json:"healthy"`
	Warning       int `json:"warning"`
	Error         int `json:"error"`
	WarningEvents int `json:"warningEvents"`
}

type DashboardProblem struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	Age        string `json:"age"`
	AgeSeconds int64  `json:"ageSeconds"` // For sorting: lower = more recent
}

type DashboardResourceCounts struct {
	Pods         ResourceCount `json:"pods"`
	Deployments  ResourceCount `json:"deployments"`
	StatefulSets WorkloadCount `json:"statefulSets"`
	DaemonSets   WorkloadCount `json:"daemonSets"`
	Services     int           `json:"services"`
	Ingresses    int           `json:"ingresses"`
	Nodes        NodeCount     `json:"nodes"`
	Namespaces   int           `json:"namespaces"`
	Jobs         JobCount      `json:"jobs"`
	CronJobs     CronJobCount  `json:"cronJobs"`
	ConfigMaps   int           `json:"configMaps"`
	Secrets      int           `json:"secrets"`
	PVCs         PVCCount      `json:"pvcs"`
	Gateways     int           `json:"gateways"`
	Routes       int           `json:"routes"`
	HelmReleases int           `json:"helmReleases"`
	Restricted   []string      `json:"restricted,omitempty"` // Resource kinds the user cannot list
}

type WorkloadCount struct {
	Total   int `json:"total"`
	Ready   int `json:"ready"`
	Unready int `json:"unready"`
}

type DashboardMetrics struct {
	CPU    *MetricSummary `json:"cpu,omitempty"`
	Memory *MetricSummary `json:"memory,omitempty"`
}

type MetricSummary struct {
	UsageMillis    int64 `json:"usageMillis"`
	RequestsMillis int64 `json:"requestsMillis"`
	CapacityMillis int64 `json:"capacityMillis"`
	UsagePercent   int   `json:"usagePercent"`
	RequestPercent int   `json:"requestPercent"`
}

type ResourceCount struct {
	Total       int `json:"total"`
	Running     int `json:"running,omitempty"`
	Pending     int `json:"pending,omitempty"`
	Failed      int `json:"failed,omitempty"`
	Succeeded   int `json:"succeeded,omitempty"`
	Available   int `json:"available,omitempty"`
	Unavailable int `json:"unavailable,omitempty"`
}

type NodeCount struct {
	Total    int `json:"total"`
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
}

type JobCount struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

type CronJobCount struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Suspended int `json:"suspended"`
}

type PVCCount struct {
	Total    int `json:"total"`
	Bound    int `json:"bound"`
	Pending  int `json:"pending"`
	Unbound  int `json:"unbound"`
}

type DashboardCRDCount struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"` // plural resource name (e.g. "rollouts")
	Group string `json:"group"`
	Count int    `json:"count"`
}

type DashboardEvent struct {
	Type           string `json:"type"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	InvolvedObject string `json:"involvedObject"`
	Namespace      string `json:"namespace"`
	Timestamp      string `json:"timestamp"`
}

type DashboardChange struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	ChangeType string `json:"changeType"`
	Summary    string `json:"summary"`
	Timestamp  string `json:"timestamp"`
}

type DashboardTopologySummary struct {
	NodeCount int `json:"nodeCount"`
	EdgeCount int `json:"edgeCount"`
}

type DashboardTrafficSummary struct {
	Source    string             `json:"source"`
	FlowCount int                `json:"flowCount"`
	TopFlows  []DashboardTopFlow `json:"topFlows"`
}

type DashboardTopFlow struct {
	Src            string  `json:"src"`
	Dst            string  `json:"dst"`
	RequestsPerSec float64 `json:"requestsPerSec,omitempty"`
	Connections    int64   `json:"connections"`
}

type DashboardHelmSummary struct {
	Total      int                    `json:"total"`
	Releases   []DashboardHelmRelease `json:"releases"`
	Restricted bool                   `json:"restricted,omitempty"` // True when user lacks permissions to list Helm releases
}

type DashboardHelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`
	ChartVersion   string `json:"chartVersion"`
	Status         string `json:"status"`
	ResourceHealth string `json:"resourceHealth,omitempty"`
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	namespaces := parseNamespaces(r.URL.Query())
	// For backward compat with single namespace string in internal functions
	namespace := ""
	if len(namespaces) == 1 {
		namespace = namespaces[0]
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	resp := DashboardResponse{}

	// Cluster info
	resp.Cluster = s.getDashboardCluster(r.Context())

	// Pod health + workload problems
	resp.Health, resp.Problems = s.getDashboardHealth(cache, namespace)

	// Resource counts
	resp.ResourceCounts = s.getDashboardResourceCounts(cache, namespace)

	// Recent warning events
	resp.RecentEvents = s.getDashboardRecentEvents(cache, namespace)

	// Count warning events for health banner
	resp.Health.WarningEvents = s.countWarningEvents(cache, namespace)

	// Recent changes from timeline
	resp.RecentChanges = s.getDashboardRecentChanges(r.Context(), namespaces)

	// Topology summary
	resp.TopologySummary = s.getDashboardTopologySummary(namespaces)

	// Traffic summary
	resp.TrafficSummary = s.getDashboardTrafficSummary(r.Context(), namespaces)

	// Helm releases summary
	resp.HelmReleases = s.getDashboardHelmSummary(namespace)

	// Cluster metrics (best-effort, nil if metrics-server unavailable)
	resp.Metrics = s.getDashboardMetrics(r.Context())

	s.writeJSON(w, resp)
}

// handleDashboardCRDs returns CRD counts - loaded lazily to keep main dashboard fast
func (s *Server) handleDashboardCRDs(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	resp := DashboardCRDsResponse{
		TopCRDs: s.getDashboardCRDCounts(r.Context(), namespace),
	}

	s.writeJSON(w, resp)
}

func (s *Server) getDashboardCluster(ctx context.Context) DashboardCluster {
	info, err := k8s.GetClusterInfo(ctx)
	if err != nil {
		return DashboardCluster{Connected: false}
	}
	return DashboardCluster{
		Name:      info.Cluster,
		Platform:  info.Platform,
		Version:   info.KubernetesVersion,
		Connected: true,
	}
}

func (s *Server) getDashboardHealth(cache *k8s.ResourceCache, namespace string) (DashboardHealth, []DashboardProblem) {
	health := DashboardHealth{}
	problems := make([]DashboardProblem, 0)

	now := time.Now()

	// Pod health
	var pods []*corev1.Pod
	var err error
	if podLister := cache.Pods(); podLister != nil {
		if namespace != "" {
			pods, err = podLister.Pods(namespace).List(labels.Everything())
		} else {
			pods, err = podLister.List(labels.Everything())
		}
	}
	if err == nil {
		for _, pod := range pods {
			status := classifyPodHealth(pod, now)
			switch status {
			case "healthy":
				health.Healthy++
			case "warning":
				health.Warning++
				if len(problems) < 20 {
					problems = append(problems, podToProblem(pod, "warning", now))
				}
			case "error":
				health.Error++
				if len(problems) < 20 {
					problems = append([]DashboardProblem{podToProblem(pod, "error", now)}, problems...)
				}
			}
		}
	}

	// Deployment problems: unavailableReplicas > 0
	if depLister := cache.Deployments(); depLister != nil {
		if namespace != "" {
			deps, _ := depLister.Deployments(namespace).List(labels.Everything())
			for _, d := range deps {
				if d.Status.UnavailableReplicas > 0 {
					ageDur := now.Sub(d.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "Deployment",
						Namespace:  d.Namespace,
						Name:       d.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, d.Status.Replicas),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		} else {
			deps, _ := depLister.List(labels.Everything())
			for _, d := range deps {
				if d.Status.UnavailableReplicas > 0 {
					ageDur := now.Sub(d.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "Deployment",
						Namespace:  d.Namespace,
						Name:       d.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, d.Status.Replicas),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		}
	}

	// StatefulSet problems: readyReplicas < replicas
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			ssets, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.ReadyReplicas < ss.Status.Replicas {
					ageDur := now.Sub(ss.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "StatefulSet",
						Namespace:  ss.Namespace,
						Name:       ss.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		} else {
			ssets, _ := ssLister.List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.ReadyReplicas < ss.Status.Replicas {
					ageDur := now.Sub(ss.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "StatefulSet",
						Namespace:  ss.Namespace,
						Name:       ss.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		}
	}

	// DaemonSet problems: numberUnavailable > 0
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			dsets, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.NumberUnavailable > 0 {
					ageDur := now.Sub(ds.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "DaemonSet",
						Namespace:  ds.Namespace,
						Name:       ds.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		} else {
			dsets, _ := dsLister.List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.NumberUnavailable > 0 {
					ageDur := now.Sub(ds.CreationTimestamp.Time)
					problems = append(problems, DashboardProblem{
						Kind:       "DaemonSet",
						Namespace:  ds.Namespace,
						Name:       ds.Name,
						Status:     "error",
						Reason:     fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
						Age:        formatAge(ageDur),
						AgeSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		}
	}

	// Node problems: Ready=False
	var nodes []*corev1.Node
	if nodeLister := cache.Nodes(); nodeLister != nil {
		nodes, _ = nodeLister.List(labels.Everything())
	}
	for _, n := range nodes {
		ready := false
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			reason := "NotReady"
			for _, cond := range n.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Message != "" {
					reason = cond.Message
					break
				}
			}
			ageDur := now.Sub(n.CreationTimestamp.Time)
			problems = append(problems, DashboardProblem{
				Kind:       "Node",
				Name:       n.Name,
				Status:     "error",
				Reason:     reason,
				Age:        formatAge(ageDur),
				AgeSeconds: int64(ageDur.Seconds()),
			})
		}
	}

	// Sort: errors first, then warnings; within each group sort by age (most recent first)
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].Status != problems[j].Status {
			return problems[i].Status == "error"
		}
		// Within same status, sort by age (lower AgeSeconds = more recent = first)
		return problems[i].AgeSeconds < problems[j].AgeSeconds
	})

	return health, problems
}

// classifyPodHealth determines if a pod is healthy, warning, or error
func classifyPodHealth(pod *corev1.Pod, now time.Time) string {
	// Succeeded pods are healthy
	if pod.Status.Phase == corev1.PodSucceeded {
		return "healthy"
	}

	// Failed pods are errors
	if pod.Status.Phase == corev1.PodFailed {
		return "error"
	}

	// Check container statuses for error conditions
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError" {
				return "error"
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return "error"
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			return "error"
		}
	}

	// Init container errors
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				return "error"
			}
		}
	}

	// Warning: pods pending for more than 5 minutes
	if pod.Status.Phase == corev1.PodPending {
		if now.Sub(pod.CreationTimestamp.Time) > 5*time.Minute {
			return "warning"
		}
		return "healthy" // recently pending is fine
	}

	// Warning: pods with high restart counts
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 3 {
			return "warning"
		}
	}

	// Running with all containers ready = healthy
	if pod.Status.Phase == corev1.PodRunning {
		return "healthy"
	}

	return "healthy"
}

func podToProblem(pod *corev1.Pod, severity string, now time.Time) DashboardProblem {
	reason := ""
	message := ""

	// Find the most relevant issue
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			reason = cs.State.Waiting.Reason
			message = cs.State.Waiting.Message
			break
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			reason = cs.State.Terminated.Reason
			message = cs.State.Terminated.Message
			break
		}
		if cs.RestartCount > 3 {
			reason = fmt.Sprintf("RestartCount: %d", cs.RestartCount)
			break
		}
	}

	if reason == "" && pod.Status.Phase == corev1.PodPending {
		reason = "Pending"
		for _, cond := range pod.Status.Conditions {
			if cond.Status == corev1.ConditionFalse && cond.Message != "" {
				message = cond.Message
				break
			}
		}
	}

	if reason == "" && pod.Status.Phase == corev1.PodFailed {
		reason = "Failed"
		if pod.Status.Message != "" {
			message = pod.Status.Message
		}
	}

	ageDur := now.Sub(pod.CreationTimestamp.Time)

	return DashboardProblem{
		Kind:       "Pod",
		Namespace:  pod.Namespace,
		Name:       pod.Name,
		Status:     severity,
		Reason:     reason,
		Message:    truncate(message, 200),
		Age:        formatAge(ageDur),
		AgeSeconds: int64(ageDur.Seconds()),
	}
}

func (s *Server) getDashboardResourceCounts(cache *k8s.ResourceCache, namespace string) DashboardResourceCounts {
	counts := DashboardResourceCounts{}
	var restricted []string

	// Pods
	var pods []*corev1.Pod
	if podLister := cache.Pods(); podLister != nil {
		if namespace != "" {
			pods, _ = podLister.Pods(namespace).List(labels.Everything())
		} else {
			pods, _ = podLister.List(labels.Everything())
		}
	} else {
		restricted = append(restricted, "pods")
	}
	counts.Pods.Total = len(pods)
	for _, pod := range pods {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			counts.Pods.Running++
		case corev1.PodPending:
			counts.Pods.Pending++
		case corev1.PodFailed:
			counts.Pods.Failed++
		case corev1.PodSucceeded:
			counts.Pods.Succeeded++
		}
	}

	// Deployments
	if depLister := cache.Deployments(); depLister != nil {
		if namespace != "" {
			deps, _ := depLister.Deployments(namespace).List(labels.Everything())
			counts.Deployments.Total = len(deps)
			for _, d := range deps {
				if d.Status.AvailableReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
					counts.Deployments.Available++
				} else if d.Status.Replicas > 0 {
					counts.Deployments.Unavailable++
				}
			}
		} else {
			deps, _ := depLister.List(labels.Everything())
			counts.Deployments.Total = len(deps)
			for _, d := range deps {
				if d.Status.AvailableReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
					counts.Deployments.Available++
				} else if d.Status.Replicas > 0 {
					counts.Deployments.Unavailable++
				}
			}
		}
	} else {
		restricted = append(restricted, "deployments")
	}

	// StatefulSets (only count those with replicas > 0)
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			ssets, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.Replicas == 0 {
					continue
				}
				counts.StatefulSets.Total++
				if ss.Status.ReadyReplicas == ss.Status.Replicas {
					counts.StatefulSets.Ready++
				} else {
					counts.StatefulSets.Unready++
				}
			}
		} else {
			ssets, _ := ssLister.List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.Replicas == 0 {
					continue
				}
				counts.StatefulSets.Total++
				if ss.Status.ReadyReplicas == ss.Status.Replicas {
					counts.StatefulSets.Ready++
				} else {
					counts.StatefulSets.Unready++
				}
			}
		}
	} else {
		restricted = append(restricted, "statefulsets")
	}

	// DaemonSets (only count those with desired > 0)
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			dsets, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.DesiredNumberScheduled == 0 {
					continue
				}
				counts.DaemonSets.Total++
				if ds.Status.NumberUnavailable == 0 {
					counts.DaemonSets.Ready++
				} else {
					counts.DaemonSets.Unready++
				}
			}
		} else {
			dsets, _ := dsLister.List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.DesiredNumberScheduled == 0 {
					continue
				}
				counts.DaemonSets.Total++
				if ds.Status.NumberUnavailable == 0 {
					counts.DaemonSets.Ready++
				} else {
					counts.DaemonSets.Unready++
				}
			}
		}
	} else {
		restricted = append(restricted, "daemonsets")
	}

	// Services
	if svcLister := cache.Services(); svcLister != nil {
		if namespace != "" {
			svcs, _ := svcLister.Services(namespace).List(labels.Everything())
			counts.Services = len(svcs)
		} else {
			svcs, _ := svcLister.List(labels.Everything())
			counts.Services = len(svcs)
		}
	} else {
		restricted = append(restricted, "services")
	}

	// Ingresses
	if ingLister := cache.Ingresses(); ingLister != nil {
		if namespace != "" {
			ings, _ := ingLister.Ingresses(namespace).List(labels.Everything())
			counts.Ingresses = len(ings)
		} else {
			ings, _ := ingLister.List(labels.Everything())
			counts.Ingresses = len(ings)
		}
	} else {
		restricted = append(restricted, "ingresses")
	}

	// Gateways and routes (via dynamic cache)
	dynamicCache := k8s.GetDynamicResourceCache()
	resourceDiscovery := k8s.GetResourceDiscovery()
	if dynamicCache != nil && resourceDiscovery != nil {
		if gwGVR, ok := resourceDiscovery.GetGVR("Gateway"); ok {
			gateways, err := dynamicCache.List(gwGVR, namespace)
			if err != nil {
				log.Printf("WARNING [dashboard] Failed to count Gateways: %v", err)
			} else {
				counts.Gateways = len(gateways)
			}
		}
		for _, routeKind := range []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute"} {
			if rGVR, ok := resourceDiscovery.GetGVR(routeKind); ok {
				routes, err := dynamicCache.List(rGVR, namespace)
				if err != nil {
					log.Printf("WARNING [dashboard] Failed to count %s: %v", routeKind, err)
				} else {
					counts.Routes += len(routes)
				}
			}
		}
	}

	// Nodes (cluster-scoped, not filtered by namespace)
	if nodeLister := cache.Nodes(); nodeLister != nil {
		nodeList, _ := nodeLister.List(labels.Everything())
		counts.Nodes.Total = len(nodeList)
		for _, n := range nodeList {
			ready := false
			for _, cond := range n.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if ready {
				counts.Nodes.Ready++
			} else {
				counts.Nodes.NotReady++
			}
		}
	} else {
		restricted = append(restricted, "nodes")
	}

	// Namespaces (cluster-scoped)
	if nsLister := cache.Namespaces(); nsLister != nil {
		nss, _ := nsLister.List(labels.Everything())
		counts.Namespaces = len(nss)
	}

	// Jobs
	if jobLister := cache.Jobs(); jobLister != nil {
		if namespace != "" {
			jobList, _ := jobLister.Jobs(namespace).List(labels.Everything())
			counts.Jobs.Total = len(jobList)
			for _, j := range jobList {
				if j.Status.Active > 0 {
					counts.Jobs.Active++
				}
				counts.Jobs.Succeeded += int(j.Status.Succeeded)
				counts.Jobs.Failed += int(j.Status.Failed)
			}
		} else {
			jobList, _ := jobLister.List(labels.Everything())
			counts.Jobs.Total = len(jobList)
			for _, j := range jobList {
				if j.Status.Active > 0 {
					counts.Jobs.Active++
				}
				counts.Jobs.Succeeded += int(j.Status.Succeeded)
				counts.Jobs.Failed += int(j.Status.Failed)
			}
		}
	} else {
		restricted = append(restricted, "jobs")
	}

	// CronJobs
	if cjLister := cache.CronJobs(); cjLister != nil {
		if namespace != "" {
			cronJobs, _ := cjLister.CronJobs(namespace).List(labels.Everything())
			counts.CronJobs.Total = len(cronJobs)
			for _, cj := range cronJobs {
				if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
					counts.CronJobs.Suspended++
				} else if len(cj.Status.Active) > 0 {
					counts.CronJobs.Active++
				}
			}
		} else {
			cronJobs, _ := cjLister.List(labels.Everything())
			counts.CronJobs.Total = len(cronJobs)
			for _, cj := range cronJobs {
				if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
					counts.CronJobs.Suspended++
				} else if len(cj.Status.Active) > 0 {
					counts.CronJobs.Active++
				}
			}
		}
	} else {
		restricted = append(restricted, "cronjobs")
	}

	// ConfigMaps
	if cmLister := cache.ConfigMaps(); cmLister != nil {
		if namespace != "" {
			cms, _ := cmLister.ConfigMaps(namespace).List(labels.Everything())
			counts.ConfigMaps = len(cms)
		} else {
			cms, _ := cmLister.List(labels.Everything())
			counts.ConfigMaps = len(cms)
		}
	}

	// Secrets (may be nil if RBAC doesn't allow listing secrets)
	if secretsLister := cache.Secrets(); secretsLister != nil {
		if namespace != "" {
			secrets, _ := secretsLister.Secrets(namespace).List(labels.Everything())
			counts.Secrets = len(secrets)
		} else {
			secrets, _ := secretsLister.List(labels.Everything())
			counts.Secrets = len(secrets)
		}
	}

	// PVCs
	if pvcLister := cache.PersistentVolumeClaims(); pvcLister != nil {
		if namespace != "" {
			pvcs, _ := pvcLister.PersistentVolumeClaims(namespace).List(labels.Everything())
			counts.PVCs.Total = len(pvcs)
			for _, pvc := range pvcs {
				switch pvc.Status.Phase {
				case corev1.ClaimBound:
					counts.PVCs.Bound++
				case corev1.ClaimPending:
					counts.PVCs.Pending++
				default:
					counts.PVCs.Unbound++
				}
			}
		} else {
			pvcs, _ := pvcLister.List(labels.Everything())
			counts.PVCs.Total = len(pvcs)
			for _, pvc := range pvcs {
				switch pvc.Status.Phase {
				case corev1.ClaimBound:
					counts.PVCs.Bound++
				case corev1.ClaimPending:
					counts.PVCs.Pending++
				default:
					counts.PVCs.Unbound++
				}
			}
		}
	}

	// Helm releases count
	helmClient := helm.GetClient()
	if helmClient != nil {
		releases, err := helmClient.ListReleases(namespace)
		if err == nil {
			counts.HelmReleases = len(releases)
		}
	}

	counts.Restricted = restricted
	return counts
}

func (s *Server) getDashboardRecentEvents(cache *k8s.ResourceCache, namespace string) []DashboardEvent {
	eventLister := cache.Events()
	if eventLister == nil {
		return []DashboardEvent{}
	}
	var events []*corev1.Event
	var err error
	if namespace != "" {
		events, err = eventLister.Events(namespace).List(labels.Everything())
	} else {
		events, err = eventLister.List(labels.Everything())
	}
	if err != nil || len(events) == 0 {
		return []DashboardEvent{}
	}

	// Filter to Warning events only and sort by last timestamp desc
	var warnings []*corev1.Event
	for _, e := range events {
		if e.Type == "Warning" {
			warnings = append(warnings, e)
		}
	}

	sort.Slice(warnings, func(i, j int) bool {
		ti := warnings[i].LastTimestamp.Time
		tj := warnings[j].LastTimestamp.Time
		if ti.IsZero() {
			ti = warnings[i].CreationTimestamp.Time
		}
		if tj.IsZero() {
			tj = warnings[j].CreationTimestamp.Time
		}
		return ti.After(tj)
	})

	// Take top 5
	limit := 5
	if len(warnings) < limit {
		limit = len(warnings)
	}

	result := make([]DashboardEvent, 0, limit)
	for _, e := range warnings[:limit] {
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.CreationTimestamp.Time
		}
		result = append(result, DashboardEvent{
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        truncate(e.Message, 200),
			InvolvedObject: fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Namespace:      e.Namespace,
			Timestamp:      ts.Format(time.RFC3339),
		})
	}

	return result
}

func (s *Server) getDashboardRecentChanges(ctx context.Context, namespaces []string) []DashboardChange {
	store := timeline.GetStore()
	if store == nil {
		return []DashboardChange{}
	}

	opts := timeline.QueryOptions{
		Namespaces:   namespaces,
		Since:        time.Now().Add(-1 * time.Hour),
		Limit:        5,
		FilterPreset: "workloads",
	}

	events, err := store.Query(ctx, opts)
	if err != nil || len(events) == 0 {
		return []DashboardChange{}
	}

	result := make([]DashboardChange, 0, len(events))
	for _, e := range events {
		summary := ""
		if e.Diff != nil && e.Diff.Summary != "" {
			summary = e.Diff.Summary
		} else if e.Message != "" {
			summary = truncate(e.Message, 100)
		}

		result = append(result, DashboardChange{
			Kind:       e.Kind,
			Namespace:  e.Namespace,
			Name:       e.Name,
			ChangeType: string(e.EventType),
			Summary:    summary,
			Timestamp:  e.Timestamp.Format(time.RFC3339),
		})
	}

	return result
}

func (s *Server) getDashboardTopologySummary(namespaces []string) DashboardTopologySummary {
	// Use cached topology only when no namespace filter is active,
	// since the cached topology's namespace scope may not match the request.
	if len(namespaces) == 0 {
		if cachedTopo := s.broadcaster.GetCachedTopology(); cachedTopo != nil {
			return DashboardTopologySummary{
				NodeCount: len(cachedTopo.Nodes),
				EdgeCount: len(cachedTopo.Edges),
			}
		}
	}

	// Build topology with the requested namespace filter
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	builder := topology.NewBuilder()
	topo, err := builder.Build(opts)
	if err != nil {
		return DashboardTopologySummary{}
	}

	return DashboardTopologySummary{
		NodeCount: len(topo.Nodes),
		EdgeCount: len(topo.Edges),
	}
}

func (s *Server) getDashboardTrafficSummary(ctx context.Context, namespaces []string) *DashboardTrafficSummary {
	manager := traffic.GetManager()
	if manager == nil {
		return nil
	}

	sourceName := manager.GetActiveSourceName()
	if sourceName == "" {
		return nil
	}

	opts := traffic.DefaultFlowOptions()
	// Traffic only supports single namespace filter for now
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}

	response, err := manager.GetFlows(ctx, opts)
	if err != nil || len(response.Flows) == 0 {
		return &DashboardTrafficSummary{
			Source:    sourceName,
			FlowCount: 0,
			TopFlows:  []DashboardTopFlow{},
		}
	}

	// Aggregate flows
	aggregated := traffic.AggregateFlows(response.Flows)

	// Sort by connection count
	sort.Slice(aggregated, func(i, j int) bool {
		return aggregated[i].Connections > aggregated[j].Connections
	})

	topFlows := make([]DashboardTopFlow, 0, 3)
	limit := 3
	if len(aggregated) < limit {
		limit = len(aggregated)
	}
	for _, f := range aggregated[:limit] {
		srcName := f.Source.Name
		if f.Source.Workload != "" {
			srcName = f.Source.Workload
		}
		dstName := f.Destination.Name
		if f.Destination.Workload != "" {
			dstName = f.Destination.Workload
		}
		topFlows = append(topFlows, DashboardTopFlow{
			Src:         srcName,
			Dst:         dstName,
			Connections: f.Connections,
		})
	}

	return &DashboardTrafficSummary{
		Source:    sourceName,
		FlowCount: len(aggregated),
		TopFlows:  topFlows,
	}
}

func (s *Server) getDashboardHelmSummary(namespace string) DashboardHelmSummary {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return DashboardHelmSummary{Releases: []DashboardHelmRelease{}}
	}

	releases, err := helmClient.ListReleases(namespace)
	if err != nil {
		if helm.IsForbiddenError(err) {
			return DashboardHelmSummary{Releases: []DashboardHelmRelease{}, Restricted: true}
		}
		return DashboardHelmSummary{Releases: []DashboardHelmRelease{}}
	}

	result := DashboardHelmSummary{
		Total: len(releases),
	}

	// Take top 6 releases
	limit := 6
	if len(releases) < limit {
		limit = len(releases)
	}

	result.Releases = make([]DashboardHelmRelease, 0, limit)
	for _, r := range releases[:limit] {
		result.Releases = append(result.Releases, DashboardHelmRelease{
			Name:           r.Name,
			Namespace:      r.Namespace,
			Chart:          r.Chart,
			ChartVersion:   r.ChartVersion,
			Status:         r.Status,
			ResourceHealth: r.ResourceHealth,
		})
	}

	return result
}

func (s *Server) countWarningEvents(cache *k8s.ResourceCache, namespace string) int {
	eventLister := cache.Events()
	if eventLister == nil {
		return 0
	}
	var events []*corev1.Event
	if namespace != "" {
		events, _ = eventLister.Events(namespace).List(labels.Everything())
	} else {
		events, _ = eventLister.List(labels.Everything())
	}
	count := 0
	for _, e := range events {
		if e.Type == "Warning" {
			count++
		}
	}
	return count
}

func (s *Server) getDashboardMetrics(ctx context.Context) *DashboardMetrics {
	client := k8s.GetClient()
	if client == nil {
		return nil
	}

	// Query metrics-server via raw REST to avoid adding k8s.io/metrics dependency.
	// GET /apis/metrics.k8s.io/v1beta1/nodes
	data, err := client.RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").
		DoRaw(ctx)
	if err != nil {
		// metrics-server not installed or not accessible — that's fine
		return nil
	}

	var nodeMetricsList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &nodeMetricsList); err != nil {
		log.Printf("Failed to parse node metrics: %v", err)
		return nil
	}

	if len(nodeMetricsList.Items) == 0 {
		return nil
	}

	// Get node capacity from the cache
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	nodeLister := cache.Nodes()
	if nodeLister == nil {
		return nil
	}
	nodes, _ := nodeLister.List(labels.Everything())
	if len(nodes) == 0 {
		return nil
	}

	// Sum capacity across all nodes
	var cpuCapacityMillis int64
	var memCapacityBytes int64
	for _, n := range nodes {
		cpuCapacityMillis += n.Status.Capacity.Cpu().MilliValue()
		memCapacityBytes += n.Status.Capacity.Memory().Value()
	}

	// Sum usage across all nodes
	var cpuUsageMillis int64
	var memUsageBytes int64
	for _, item := range nodeMetricsList.Items {
		cpuUsageMillis += parseCPUToMillis(item.Usage.CPU)
		memUsageBytes += parseMemoryToBytes(item.Usage.Memory)
	}

	// Sum requests across all pods
	var cpuRequestsMillis int64
	var memRequestsBytes int64
	var metricPods []*corev1.Pod
	if podLister := cache.Pods(); podLister != nil {
		metricPods, _ = podLister.List(labels.Everything())
	}
	for _, pod := range metricPods {
		// Skip completed/failed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if container.Resources.Requests != nil {
				if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
					cpuRequestsMillis += cpu.MilliValue()
				}
				if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
					memRequestsBytes += mem.Value()
				}
			}
		}
	}

	metrics := &DashboardMetrics{}
	if cpuCapacityMillis > 0 {
		metrics.CPU = &MetricSummary{
			UsageMillis:    cpuUsageMillis,
			RequestsMillis: cpuRequestsMillis,
			CapacityMillis: cpuCapacityMillis,
			UsagePercent:   int(cpuUsageMillis * 100 / cpuCapacityMillis),
			RequestPercent: int(cpuRequestsMillis * 100 / cpuCapacityMillis),
		}
	}
	if memCapacityBytes > 0 {
		// Convert bytes to MiB for the "millis" fields (repurposed as MiB)
		memUsageMiB := memUsageBytes / (1024 * 1024)
		memRequestsMiB := memRequestsBytes / (1024 * 1024)
		memCapacityMiB := memCapacityBytes / (1024 * 1024)
		metrics.Memory = &MetricSummary{
			UsageMillis:    memUsageMiB,
			RequestsMillis: memRequestsMiB,
			CapacityMillis: memCapacityMiB,
			UsagePercent:   int(memUsageMiB * 100 / memCapacityMiB),
			RequestPercent: int(memRequestsMiB * 100 / memCapacityMiB),
		}
	}

	return metrics
}

// parseCPUToMillis parses CPU quantity strings like "250m", "1", "500n"
func parseCPUToMillis(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "n") {
		// nanocores
		s = strings.TrimSuffix(s, "n")
		var val int64
		fmt.Sscanf(s, "%d", &val)
		return val / 1000000
	}
	if strings.HasSuffix(s, "m") {
		s = strings.TrimSuffix(s, "m")
		var val int64
		fmt.Sscanf(s, "%d", &val)
		return val
	}
	// Plain number = cores
	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val * 1000
}

// parseMemoryToBytes parses memory quantity strings like "1024Ki", "256Mi", "1Gi"
func parseMemoryToBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "Ki") {
		s = strings.TrimSuffix(s, "Ki")
		var val int64
		fmt.Sscanf(s, "%d", &val)
		return val * 1024
	}
	if strings.HasSuffix(s, "Mi") {
		s = strings.TrimSuffix(s, "Mi")
		var val int64
		fmt.Sscanf(s, "%d", &val)
		return val * 1024 * 1024
	}
	if strings.HasSuffix(s, "Gi") {
		s = strings.TrimSuffix(s, "Gi")
		var val int64
		fmt.Sscanf(s, "%d", &val)
		return val * 1024 * 1024 * 1024
	}
	// Plain bytes
	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val
}

// Helper functions

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// getDashboardCRDCounts returns counts of CRD instances in the cluster.
func (s *Server) getDashboardCRDCounts(_ context.Context, namespace string) []DashboardCRDCount {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return []DashboardCRDCount{}
	}

	resources, err := discovery.GetAPIResources()
	if err != nil {
		return []DashboardCRDCount{}
	}

	// Filter to CRDs only, deduplicating by Group+Kind (different versions of same CRD)
	seen := make(map[string]bool)
	var crds []k8s.APIResource
	for _, r := range resources {
		if r.IsCRD {
			key := r.Group + "/" + r.Kind
			if !seen[key] {
				seen[key] = true
				crds = append(crds, r)
			}
		}
	}
	if len(crds) == 0 {
		return []DashboardCRDCount{}
	}

	dynamicCache := k8s.GetDynamicResourceCache()
	if dynamicCache == nil {
		return []DashboardCRDCount{}
	}

	type result struct {
		kind  string
		name  string
		group string
		count int
	}

	results := make([]result, len(crds))
	var wg sync.WaitGroup

	for i, crd := range crds {
		wg.Add(1)
		go func(idx int, r k8s.APIResource) {
			defer wg.Done()

			gvr, ok := discovery.GetGVR(r.Kind)
			if !ok {
				return
			}

			// Only count CRDs that are already synced in cache
			// Skip unsynced CRDs to avoid slow API calls that trigger throttling
			if !dynamicCache.IsSynced(gvr) {
				return
			}

			items, err := dynamicCache.List(gvr, namespace)
			if err != nil {
				return
			}

			results[idx] = result{kind: r.Kind, name: r.Name, group: r.Group, count: len(items)}
		}(i, crd)
	}

	wg.Wait()

	// Filter out zero-count and sort by count descending
	counts := make([]DashboardCRDCount, 0)
	for _, r := range results {
		if r.count > 0 {
			counts = append(counts, DashboardCRDCount{
				Kind:  r.kind,
				Name:  r.name,
				Group: r.group,
				Count: r.count,
			})
		}
	}

	sort.Slice(counts, func(i, j int) bool {
		return counts[i].Count > counts[j].Count
	})

	if len(counts) > 8 {
		counts = counts[:8]
	}

	return counts
}
