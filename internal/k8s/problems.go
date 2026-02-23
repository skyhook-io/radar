package k8s

import (
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Problem is a transport-neutral cluster issue.
type Problem struct {
	Kind       string
	Namespace  string
	Name       string
	Severity   string // "error" or "warning"
	Reason     string
	Message    string
	Age        string // human-readable
	AgeSeconds int64  // for sorting
}

// DetectProblems scans workloads in cache and returns detected problems.
// Covers: Deployments, StatefulSets, DaemonSets, HPAs, CronJobs, Nodes.
// Does NOT include pods (consumers handle pod problems differently).
// namespace="" scans all namespaces.
func DetectProblems(cache *ResourceCache, namespace string) []Problem {
	var problems []Problem
	now := time.Now()

	// Deployment problems: unavailableReplicas > 0
	if depLister := cache.Deployments(); depLister != nil {
		var deps []*appsv1.Deployment
		if namespace != "" {
			deps, _ = depLister.Deployments(namespace).List(labels.Everything())
		} else {
			deps, _ = depLister.List(labels.Everything())
		}
		for _, d := range deps {
			if d.Status.UnavailableReplicas > 0 {
				ageDur := now.Sub(d.CreationTimestamp.Time)
				problems = append(problems, Problem{
					Kind:       "Deployment",
					Namespace:  d.Namespace,
					Name:       d.Name,
					Severity:   "error",
					Reason:     fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, d.Status.Replicas),
					Age:        FormatAge(ageDur),
					AgeSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	// StatefulSet problems: readyReplicas < replicas
	if ssLister := cache.StatefulSets(); ssLister != nil {
		var ssets []*appsv1.StatefulSet
		if namespace != "" {
			ssets, _ = ssLister.StatefulSets(namespace).List(labels.Everything())
		} else {
			ssets, _ = ssLister.List(labels.Everything())
		}
		for _, ss := range ssets {
			if ss.Status.ReadyReplicas < ss.Status.Replicas {
				ageDur := now.Sub(ss.CreationTimestamp.Time)
				problems = append(problems, Problem{
					Kind:       "StatefulSet",
					Namespace:  ss.Namespace,
					Name:       ss.Name,
					Severity:   "error",
					Reason:     fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
					Age:        FormatAge(ageDur),
					AgeSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	// DaemonSet problems: numberUnavailable > 0
	if dsLister := cache.DaemonSets(); dsLister != nil {
		var dsets []*appsv1.DaemonSet
		if namespace != "" {
			dsets, _ = dsLister.DaemonSets(namespace).List(labels.Everything())
		} else {
			dsets, _ = dsLister.List(labels.Everything())
		}
		for _, ds := range dsets {
			if ds.Status.NumberUnavailable > 0 {
				ageDur := now.Sub(ds.CreationTimestamp.Time)
				problems = append(problems, Problem{
					Kind:       "DaemonSet",
					Namespace:  ds.Namespace,
					Name:       ds.Name,
					Severity:   "error",
					Reason:     fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
					Age:        FormatAge(ageDur),
					AgeSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	// HPA problems
	if hpaLister := cache.HorizontalPodAutoscalers(); hpaLister != nil {
		var hpas []*autoscalingv2.HorizontalPodAutoscaler
		if namespace != "" {
			hpas, _ = hpaLister.HorizontalPodAutoscalers(namespace).List(labels.Everything())
		} else {
			hpas, _ = hpaLister.List(labels.Everything())
		}
		for _, hp := range DetectHPAProblems(hpas) {
			problems = append(problems, Problem{
				Kind:      "HorizontalPodAutoscaler",
				Namespace: hp.Namespace,
				Name:      hp.Name,
				Severity:  "warning",
				Reason:    hp.Problem,
				Message:   hp.Reason,
			})
		}
	}

	// CronJob problems
	if cjLister := cache.CronJobs(); cjLister != nil {
		var cronjobs []*batchv1.CronJob
		if namespace != "" {
			cronjobs, _ = cjLister.CronJobs(namespace).List(labels.Everything())
		} else {
			cronjobs, _ = cjLister.List(labels.Everything())
		}
		for _, cp := range DetectCronJobProblems(cronjobs) {
			problems = append(problems, Problem{
				Kind:      "CronJob",
				Namespace: cp.Namespace,
				Name:      cp.Name,
				Severity:  "warning",
				Reason:    cp.Problem,
				Message:   cp.Reason,
			})
		}
	}

	// Node problems (cluster-scoped, not filtered by namespace)
	if nodeLister := cache.Nodes(); nodeLister != nil {
		nodes, _ := nodeLister.List(labels.Everything())
		for _, np := range DetectNodeProblems(nodes) {
			ageDur := time.Duration(0)
			for _, n := range nodes {
				if n.Name == np.NodeName {
					ageDur = now.Sub(n.CreationTimestamp.Time)
					break
				}
			}
			problems = append(problems, Problem{
				Kind:       "Node",
				Name:       np.NodeName,
				Severity:   np.Severity,
				Reason:     np.Problem,
				Message:    np.Reason,
				Age:        FormatAge(ageDur),
				AgeSeconds: int64(ageDur.Seconds()),
			})
		}
	}

	return problems
}
