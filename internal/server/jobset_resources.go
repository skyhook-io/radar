package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	resourcehelper "k8s.io/component-helpers/resource"
)

type jobSetMemberQuery struct {
	Role, Search, State, Selected string
}

func jobSetMemberQueryFromRequest(r *http.Request) jobSetMemberQuery {
	q := r.URL.Query()
	return jobSetMemberQuery{Role: q.Get("role"), Search: q.Get("search"), State: q.Get("state"), Selected: q.Get("selected")}
}

type jobSetPod struct {
	Pod *corev1.Pod
	Job *batchv1.Job
}

func jobSetOwnedPods(root *unstructured.Unstructured, jobs []*batchv1.Job, pods []*corev1.Pod) []jobSetPod {
	owned := make(map[types.UID]*batchv1.Job)
	for _, job := range jobs {
		if job != nil && jobSetControls(root, job) && job.UID != "" {
			owned[job.UID] = job
		}
	}
	result := []jobSetPod{}
	for _, pod := range pods {
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "Job" || owner.APIVersion != "batch/v1" {
			continue
		}
		job := owned[owner.UID]
		if job == nil || owner.Name != job.Name || pod.Namespace != job.Namespace {
			continue
		}
		result = append(result, jobSetPod{Pod: pod, Job: job})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Pod.Name < result[j].Pod.Name })
	return result
}

func loadJobSetPods(ctx context.Context, namespace, name string) (*unstructured.Unstructured, []*batchv1.Job, []jobSetPod, *workloadError) {
	root, jobs, err := loadJobSetJobs(ctx, namespace, name)
	if err != nil {
		return nil, nil, nil, err
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		return nil, nil, nil, &workloadError{http.StatusServiceUnavailable, "Pod cache unavailable"}
	}
	pods, listErr := cache.Pods().Pods(namespace).List(labels.Everything())
	if listErr != nil {
		return nil, nil, nil, &workloadError{http.StatusInternalServerError, listErr.Error()}
	}
	return root, jobs, jobSetOwnedPods(root, jobs, pods), nil
}

func (s *Server) authorizeJobSetEvidence(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if !s.requireConnected(w) {
		return false
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return false
	}
	for _, target := range []struct{ group, resource, verb string }{
		{"jobset.x-k8s.io", "jobsets", "get"}, {"batch", "jobs", "list"}, {"", "pods", "list"},
	} {
		if !s.canRead(r, target.group, target.resource, namespace, target.verb) {
			s.writeError(w, http.StatusForbidden, "no access to "+target.resource+" in namespace "+namespace)
			return false
		}
	}
	return true
}

type jobSetUsage struct {
	RunningPods      int                 `json:"runningPods"`
	ReportingPods    int                 `json:"reportingPods"`
	StalePods        int                 `json:"stalePods"`
	CPU              *int64              `json:"cpu"`
	Memory           *int64              `json:"memory"`
	CPURequest       int64               `json:"cpuRequest"`
	MemoryRequest    int64               `json:"memoryRequest"`
	ObservedAt       string              `json:"observedAt,omitempty"`
	ExtendedRequests corev1.ResourceList `json:"extendedRequests,omitempty"`
}

type jobSetResourcesResponse struct {
	UID         types.UID               `json:"uid"`
	Total       jobSetUsage             `json:"total"`
	Members     map[string]*jobSetUsage `json:"members"`
	Source      string                  `json:"source"`
	Unavailable string                  `json:"unavailable,omitempty"`
}

func (s *Server) handleJobSetResources(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeJobSetEvidence(w, r, namespace) {
		return
	}
	root, jobs, pods, err := loadJobSetPods(r.Context(), namespace, name)
	if err != nil {
		if err.statusCode == http.StatusInternalServerError {
			log.Printf("[jobset] Failed to load Pods for resource snapshot %s/%s: %v", namespace, name, err)
		}
		s.writeWorkloadError(w, err)
		return
	}
	metrics := map[string]k8score.PodMetrics{}
	unavailable := ""
	gvr, discovered := k8s.ResolveMetricsGVR("pods")
	client := s.getDynamicClientForRequest(r)
	if !discovered {
		unavailable = "The Kubernetes resource metrics API is unavailable."
	} else if client == nil {
		unavailable = "The cluster metrics connection is unavailable."
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		list, listErr := client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			if apierrors.IsForbidden(listErr) {
				unavailable = "Resource metrics are not readable with your current permissions."
			} else {
				unavailable = "Resource metrics could not be read. Retry to refresh the snapshot."
				log.Printf("[jobset] Failed to list resource metrics in %s: %v", namespace, listErr)
			}
		} else {
			for _, item := range list.Items {
				var metric k8score.PodMetrics
				if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &metric) == nil && metric.Metadata.Namespace == namespace {
					metrics[metric.Metadata.Name] = metric
				}
			}
		}
	}
	result := buildJobSetResources(root, jobs, pods, metrics, jobSetMemberQueryFromRequest(r), time.Now())
	result.Unavailable = unavailable
	s.writeJSON(w, result)
}

func buildJobSetResources(root *unstructured.Unstructured, jobs []*batchv1.Job, pods []jobSetPod, metrics map[string]k8score.PodMetrics, query jobSetMemberQuery, now time.Time) jobSetResourcesResponse {
	result := jobSetResourcesResponse{UID: root.GetUID(), Source: "metrics.k8s.io", Members: map[string]*jobSetUsage{}}
	collection := jobSetMemberRuns(root, jobs, query)
	for _, run := range collection.Runs {
		result.Members[run.Name] = &jobSetUsage{}
	}
	for _, owned := range pods {
		pod := owned.Pod
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		extended := corev1.ResourceList{}
		for name, quantity := range resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{}) {
			if strings.Contains(string(name), "/") && !strings.HasPrefix(string(name), "kubernetes.io/") {
				extended[name] = quantity
			}
		}
		usage := jobSetUsage{ExtendedRequests: extended}
		if pod.Status.Phase == corev1.PodRunning {
			usage.RunningPods = 1
			cpu, memory, at, state := currentPodUsage(pod, metrics[pod.Name], now)
			if state == "stale" {
				usage.StalePods = 1
			}
			if state == "available" {
				usage.ReportingPods = 1
				usage.CPU, usage.Memory = &cpu, &memory
				usage.ObservedAt = at.Format(time.RFC3339Nano)
				requests := k8s.SumRunningContainerResources(pod)
				usage.CPURequest, usage.MemoryRequest = requests.CPURequest, requests.MemoryRequest
			}
		}
		addJobSetUsage(&result.Total, usage)
		if member := result.Members[owned.Job.Name]; member != nil {
			addJobSetUsage(member, usage)
		}
	}
	return result
}

func currentPodUsage(pod *corev1.Pod, metric k8score.PodMetrics, now time.Time) (int64, int64, time.Time, string) {
	at, err := time.Parse(time.RFC3339Nano, metric.Timestamp)
	if err != nil {
		return 0, 0, time.Time{}, "missing"
	}
	window, err := time.ParseDuration(metric.Window)
	if err != nil || window < 0 || at.Add(-window).Before(pod.CreationTimestamp.Time) || at.After(now.Add(30*time.Second)) {
		return 0, 0, at, "missing"
	}
	if now.Sub(at) > 2*time.Minute {
		return 0, 0, at, "stale"
	}
	wanted := map[string]bool{}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, status := range statuses {
			if status.State.Running != nil {
				wanted[status.Name] = true
			}
		}
	}
	if len(wanted) == 0 {
		return 0, 0, at, "missing"
	}
	var cpu, memory int64
	for _, c := range metric.Containers {
		if !wanted[c.Name] {
			continue
		}
		cq, ce := resource.ParseQuantity(c.Usage.CPU)
		mq, me := resource.ParseQuantity(c.Usage.Memory)
		if ce != nil || me != nil || cq.Sign() < 0 || mq.Sign() < 0 {
			return 0, 0, at, "missing"
		}
		cpu += cq.ScaledValue(resource.Nano)
		memory += mq.Value()
		delete(wanted, c.Name)
	}
	if len(wanted) != 0 {
		return 0, 0, at, "missing"
	}
	return cpu, memory, at, "available"
}

func addJobSetUsage(total *jobSetUsage, value jobSetUsage) {
	total.RunningPods += value.RunningPods
	total.ReportingPods += value.ReportingPods
	total.StalePods += value.StalePods
	if value.CPU != nil {
		if total.CPU == nil {
			total.CPU, total.Memory = new(int64), new(int64)
		}
		*total.CPU += *value.CPU
		*total.Memory += *value.Memory
	}
	total.CPURequest += value.CPURequest
	total.MemoryRequest += value.MemoryRequest
	valueTime, _ := time.Parse(time.RFC3339Nano, value.ObservedAt)
	totalTime, _ := time.Parse(time.RFC3339Nano, total.ObservedAt)
	if !valueTime.IsZero() && (totalTime.IsZero() || valueTime.Before(totalTime)) {
		total.ObservedAt = value.ObservedAt
	}
	if len(value.ExtendedRequests) > 0 && total.ExtendedRequests == nil {
		total.ExtendedRequests = corev1.ResourceList{}
	}
	for name, quantity := range value.ExtendedRequests {
		old := total.ExtendedRequests[name]
		old.Add(quantity)
		total.ExtendedRequests[name] = old
	}
}

func jobSetSourceLabel(owned jobSetPod) string {
	role := owned.Job.Labels[replicatedJobNameLabel]
	if role == "" {
		role = "Unreported role"
	}
	if index := owned.Job.Labels[jobSetJobIndexLabel]; index != "" {
		role += " #" + index
	}
	return fmt.Sprintf("%s · %s", role, owned.Pod.Name)
}
