package k8s

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Problem is a transport-neutral cluster issue.
type Problem struct {
	Kind            string
	Namespace       string
	Name            string
	Group           string // API group for CRD disambiguation (e.g., "cluster.x-k8s.io")
	Severity        string // "critical", "high", or "medium"
	Reason          string
	Message         string
	Age             string // human-readable
	AgeSeconds      int64  // for sorting
	Duration        string // how long the problem has persisted
	DurationSeconds int64
	// RestartCount + LastTerminatedReason are populated for Pod problems where
	// the kubelet has recorded crash data. Together they answer the two
	// questions an agent needs about a CrashLoopBackOff in one read:
	// chronic-vs-acute (RestartCount: 2 vs 2000) and what kind of failure
	// (Reason: OOMKilled / Error / Completed — disambiguates memory pressure
	// from app bug from misconfigured-as-long-running). Zero / empty values
	// mean either non-Pod problem or no crash data on this Pod yet.
	RestartCount         int32
	LastTerminatedReason string
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
				durDur := ageDur // fallback to creation time
				for _, cond := range d.Status.Conditions {
					if cond.Type == appsv1.DeploymentAvailable && cond.Status == "False" && !cond.LastTransitionTime.IsZero() {
						durDur = now.Sub(cond.LastTransitionTime.Time)
						break
					}
				}
				problems = append(problems, Problem{
					Kind:            "Deployment",
					Namespace:       d.Namespace,
					Name:            d.Name,
					Group:           "apps",
					Severity:        "critical",
					Reason:          fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, d.Status.Replicas),
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(durDur),
					DurationSeconds: int64(durDur.Seconds()),
				})
			}
			// Stuck rollout: ProgressDeadlineExceeded
			for _, cond := range d.Status.Conditions {
				if cond.Type == appsv1.DeploymentProgressing && cond.Status == "False" && cond.Reason == "ProgressDeadlineExceeded" {
					durDur := now.Sub(d.CreationTimestamp.Time)
					if !cond.LastTransitionTime.IsZero() {
						durDur = now.Sub(cond.LastTransitionTime.Time)
					}
					problems = append(problems, Problem{
						Kind:            "Deployment",
						Namespace:       d.Namespace,
						Name:            d.Name,
						Group:           "apps",
						Severity:        "critical",
						Reason:          "Rollout stuck",
						Message:         cond.Message,
						Age:             FormatAge(now.Sub(d.CreationTimestamp.Time)),
						AgeSeconds:      int64(now.Sub(d.CreationTimestamp.Time).Seconds()),
						Duration:        FormatAge(durDur),
						DurationSeconds: int64(durDur.Seconds()),
					})
					break
				}
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
					Kind:            "StatefulSet",
					Namespace:       ss.Namespace,
					Name:            ss.Name,
					Group:           "apps",
					Severity:        "critical",
					Reason:          fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
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
					Kind:            "DaemonSet",
					Namespace:       ds.Namespace,
					Name:            ds.Name,
					Group:           "apps",
					Severity:        "critical",
					Reason:          fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	podsByNamespace := listPodsByNamespace(cache, namespace)

	// Pod problems: high-signal container waiting/terminated states, old
	// Pending pods, and restart-heavy pods. These are useful direct pointers
	// even when a controller-level problem also exists.
	for _, pods := range podsByNamespace {
		for _, pod := range pods {
			health := ClassifyPodHealth(pod, now)
			if health == "healthy" {
				continue
			}
			ageDur := now.Sub(pod.CreationTimestamp.Time)
			severity := "high"
			if health == "error" {
				severity = "critical"
			}
			restartCount, lastTermReason := PodRestartContext(pod)
			problems = append(problems, Problem{
				Kind:                 "Pod",
				Namespace:            pod.Namespace,
				Name:                 pod.Name,
				Severity:             severity,
				Reason:               PodProblemReason(pod),
				Age:                  FormatAge(ageDur),
				AgeSeconds:           int64(ageDur.Seconds()),
				Duration:             FormatAge(ageDur),
				DurationSeconds:      int64(ageDur.Seconds()),
				RestartCount:         restartCount,
				LastTerminatedReason: lastTermReason,
			})
		}
	}

	// Service problems: routing health that workload .status often misses.
	// EndpointSlice would be the strongest source for realized backend state,
	// but the typed cache intentionally does not watch noisy endpoint resources
	// today. Use selector -> Pod readiness here, and keep the targetPort check
	// conservative: only named targetPorts are flagged as unresolved.
	if svcLister := cache.Services(); svcLister != nil {
		var services []*corev1.Service
		if namespace != "" {
			services, _ = svcLister.Services(namespace).List(labels.Everything())
		} else {
			services, _ = svcLister.List(labels.Everything())
		}
		for _, svc := range services {
			if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
				continue
			}
			selected := podsMatchingService(svc, podsByNamespace[svc.Namespace])
			ageDur := now.Sub(svc.CreationTimestamp.Time)
			if len(selected) == 0 {
				// Warning, not critical: a Service with zero matching pods
				// is often intentional (scaled-to-zero workload, dormant
				// staging environment, just-deployed Service waiting for
				// its workload to apply). The "0/N selected but 0 ready"
				// case below stays critical — that's a real routing break
				// because the workload is up but unhealthy.
				problems = append(problems, Problem{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "warning",
					Reason:          "Selector matches no pods",
					Message:         selectorMessage(svc.Spec.Selector),
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
				continue
			}
			ready := 0
			for _, pod := range selected {
				if isPodReadyForProblem(pod) {
					ready++
				}
			}
			if ready == 0 {
				problems = append(problems, Problem{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "critical",
					Reason:          fmt.Sprintf("0/%d selected pods ready", len(selected)),
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
			if missing := unresolvedNamedTargetPorts(svc, selected); len(missing) > 0 {
				problems = append(problems, Problem{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "high",
					Reason:          fmt.Sprintf("Unresolved named targetPort: %s", strings.Join(missing, ", ")),
					Message:         "No selected pod declares a container port with this name",
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
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
			// "cannot-scale" is critical (HPA inert; workload's scaling
			// guarantees silently broken). "maxed" stays medium (HPA is
			// working; signal is that the ceiling was hit, which may or
			// may not be a problem depending on intent).
			severity := "medium"
			if hp.Problem == "cannot-scale" {
				severity = "critical"
			}
			problems = append(problems, Problem{
				Kind:      "HorizontalPodAutoscaler",
				Namespace: hp.Namespace,
				Name:      hp.Name,
				Group:     "autoscaling",
				Severity:  severity,
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
				Group:     "batch",
				Severity:  "medium",
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

	// PVC problems: stuck in Pending phase or Lost bound volume.
	if pvcLister := cache.PersistentVolumeClaims(); pvcLister != nil {
		var pvcs []*corev1.PersistentVolumeClaim
		if namespace != "" {
			pvcs, _ = pvcLister.PersistentVolumeClaims(namespace).List(labels.Everything())
		} else {
			pvcs, _ = pvcLister.List(labels.Everything())
		}
		for _, pvc := range pvcs {
			ageDur := now.Sub(pvc.CreationTimestamp.Time)
			if pvc.Status.Phase == corev1.ClaimLost {
				problems = append(problems, Problem{
					Kind:            "PersistentVolumeClaim",
					Namespace:       pvc.Namespace,
					Name:            pvc.Name,
					Severity:        "critical",
					Reason:          "Lost",
					Message:         "PVC has lost its bound volume",
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
				continue
			}
			if pvc.Status.Phase == corev1.ClaimPending {
				if ageDur > 5*time.Minute {
					problems = append(problems, Problem{
						Kind:            "PersistentVolumeClaim",
						Namespace:       pvc.Namespace,
						Name:            pvc.Name,
						Severity:        "high",
						Reason:          "Pending",
						Age:             FormatAge(ageDur),
						AgeSeconds:      int64(ageDur.Seconds()),
						Duration:        FormatAge(ageDur),
						DurationSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		}
	}

	// Job problems: stuck active (running > 1h with no completions)
	if jobLister := cache.Jobs(); jobLister != nil {
		var jobs []*batchv1.Job
		if namespace != "" {
			jobs, _ = jobLister.Jobs(namespace).List(labels.Everything())
		} else {
			jobs, _ = jobLister.List(labels.Everything())
		}
		for _, job := range jobs {
			ageDur := now.Sub(job.CreationTimestamp.Time)
			if cond := failedJobCondition(job); cond != nil {
				durDur := ageDur
				if !cond.LastTransitionTime.IsZero() {
					durDur = now.Sub(cond.LastTransitionTime.Time)
				}
				reason := cond.Reason
				if reason == "" {
					reason = "Failed"
				}
				problems = append(problems, Problem{
					Kind:            "Job",
					Namespace:       job.Namespace,
					Name:            job.Name,
					Group:           "batch",
					Severity:        "critical",
					Reason:          reason,
					Message:         cond.Message,
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(durDur),
					DurationSeconds: int64(durDur.Seconds()),
				})
				continue
			}
			if job.Status.Active > 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 {
				if ageDur > time.Hour {
					problems = append(problems, Problem{
						Kind:            "Job",
						Namespace:       job.Namespace,
						Name:            job.Name,
						Group:           "batch",
						Severity:        "high",
						Reason:          fmt.Sprintf("Running for %s with no completions", FormatAge(ageDur)),
						Age:             FormatAge(ageDur),
						AgeSeconds:      int64(ageDur.Seconds()),
						Duration:        FormatAge(ageDur),
						DurationSeconds: int64(ageDur.Seconds()),
					})
				}
			}
		}
	}

	return problems
}

func listPodsByNamespace(cache *ResourceCache, namespace string) map[string][]*corev1.Pod {
	out := make(map[string][]*corev1.Pod)
	if cache == nil || cache.Pods() == nil {
		return out
	}
	var pods []*corev1.Pod
	if namespace != "" {
		pods, _ = cache.Pods().Pods(namespace).List(labels.Everything())
	} else {
		pods, _ = cache.Pods().List(labels.Everything())
	}
	for _, pod := range pods {
		out[pod.Namespace] = append(out[pod.Namespace], pod)
	}
	return out
}

func podsMatchingService(svc *corev1.Service, pods []*corev1.Pod) []*corev1.Pod {
	if svc == nil || len(svc.Spec.Selector) == 0 {
		return nil
	}
	selector := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
	out := make([]*corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if selector.Matches(labels.Set(pod.Labels)) {
			out = append(out, pod)
		}
	}
	return out
}

func isPodReadyForProblem(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func unresolvedNamedTargetPorts(svc *corev1.Service, pods []*corev1.Pod) []string {
	if svc == nil || len(pods) == 0 {
		return nil
	}
	declared := make(map[string]bool)
	for _, pod := range pods {
		for _, container := range pod.Spec.InitContainers {
			addNamedContainerPorts(declared, container.Ports)
		}
		for _, container := range pod.Spec.Containers {
			addNamedContainerPorts(declared, container.Ports)
		}
	}
	var missing []string
	seen := make(map[string]bool)
	for _, port := range svc.Spec.Ports {
		if port.TargetPort.Type != intstr.String || port.TargetPort.StrVal == "" {
			continue
		}
		name := port.TargetPort.StrVal
		if declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	return missing
}

func addNamedContainerPorts(dst map[string]bool, ports []corev1.ContainerPort) {
	for _, port := range ports {
		if port.Name != "" {
			dst[port.Name] = true
		}
	}
}

func selectorMessage(selector map[string]string) string {
	if len(selector) == 0 {
		return ""
	}
	parts := make([]string, 0, len(selector))
	for k, v := range selector {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return "selector: " + strings.Join(parts, ", ")
}

func failedJobCondition(job *batchv1.Job) *batchv1.JobCondition {
	if job == nil {
		return nil
	}
	for i := range job.Status.Conditions {
		cond := &job.Status.Conditions[i]
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return cond
		}
	}
	return nil
}

// DetectCAPIProblems scans Cluster API resources for problems.
// Checks both status.phase and the rich condition system (Ready, InfrastructureReady,
// ControlPlaneReady, BootstrapReady, NodeHealthy, TopologyReconciled).
// Returns nil if CAPI is not installed in the cluster.
func DetectCAPIProblems(dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Problem {
	if dynamicCache == nil || discovery == nil {
		return nil
	}

	var problems []Problem
	now := time.Now()

	// Helper: list CAPI resources by kind
	listCAPI := func(kind, group string) []*unstructured.Unstructured {
		if group != "" {
			gvr, ok := discovery.GetGVRWithGroup(kind, group)
			if !ok {
				return nil // CRD not installed — expected
			}
			resources, err := dynamicCache.List(gvr, namespace)
			if err != nil {
				log.Printf("[capi-problems] Failed to list %s (%s): %v", kind, group, err)
				return nil
			}
			return resources
		}
		gvr, ok := discovery.GetGVR(kind)
		if !ok {
			return nil // CRD not installed — expected
		}
		resources, err := dynamicCache.List(gvr, namespace)
		if err != nil {
			log.Printf("[capi-problems] Failed to list %s: %v", kind, err)
			return nil
		}
		return resources
	}

	// Helper: find a False condition and return its reason + message.
	// Checks v1beta2 conditions first (status.v1beta2.conditions), then v1beta1 (status.conditions).
	findFalseCondition := func(obj *unstructured.Unstructured, condTypes ...string) (condType, reason, message string, since time.Duration, found bool) {
		condSlices := [][]any{}
		if v1b2, ok, _ := unstructured.NestedSlice(obj.Object, "status", "v1beta2", "conditions"); ok {
			condSlices = append(condSlices, v1b2)
		}
		if v1b1, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
			condSlices = append(condSlices, v1b1)
		}
		for _, conditions := range condSlices {
			for _, c := range conditions {
				cond, ok := c.(map[string]any)
				if !ok {
					continue
				}
				ct, _ := cond["type"].(string)
				status, _ := cond["status"].(string)
				if status != "False" {
					continue
				}
				// Check if this condition type is in our watch list
				for _, wanted := range condTypes {
					if ct == wanted {
						r, _ := cond["reason"].(string)
						m, _ := cond["message"].(string)
						var dur time.Duration
						if ts, _ := cond["lastTransitionTime"].(string); ts != "" {
							if t, err := time.Parse(time.RFC3339, ts); err == nil {
								dur = now.Sub(t)
							}
						}
						return ct, r, m, dur, true
					}
				}
			}
		}
		return "", "", "", 0, false
	}

	const capiGroup = "cluster.x-k8s.io"
	const capiCPGroup = "controlplane.cluster.x-k8s.io"

	// -----------------------------------------------------------------------
	// CAPI Cluster problems
	// -----------------------------------------------------------------------
	for _, cl := range listCAPI("Cluster", capiGroup) {
		ageDur := now.Sub(cl.GetCreationTimestamp().Time)

		// Phase-based: Failed
		phase, _, _ := unstructured.NestedString(cl.Object, "status", "phase")
		if strings.EqualFold(phase, "failed") {
			problems = append(problems, Problem{
				Kind: "Cluster", Namespace: cl.GetNamespace(), Name: cl.GetName(), Group: capiGroup,
				Severity: "critical", Reason: "Cluster in Failed phase",
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(ageDur), DurationSeconds: int64(ageDur.Seconds()),
			})
			continue // don't double-report conditions
		}

		// Condition-based: InfrastructureReady, ControlPlaneReady, Ready, TopologyReconciled
		if ct, reason, msg, dur, ok := findFalseCondition(cl,
			"Ready", "InfrastructureReady", "ControlPlaneReady", "TopologyReconciled",
		); ok {
			severity := "high"
			if ct == "InfrastructureReady" || ct == "ControlPlaneReady" {
				severity = "critical"
			}
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			d := dur
			if d == 0 {
				d = ageDur
			}
			problems = append(problems, Problem{
				Kind: "Cluster", Namespace: cl.GetNamespace(), Name: cl.GetName(), Group: capiGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(d), DurationSeconds: int64(d.Seconds()),
			})
		}
	}

	// -----------------------------------------------------------------------
	// CAPI Machine problems
	// -----------------------------------------------------------------------
	for _, m := range listCAPI("Machine", "cluster.x-k8s.io") {
		ageDur := now.Sub(m.GetCreationTimestamp().Time)
		phase, _, _ := unstructured.NestedString(m.Object, "status", "phase")

		// Phase-based: Failed
		if strings.EqualFold(phase, "failed") {
			// Include the condition message for richer context
			_, _, msg, _, _ := findFalseCondition(m, "Ready", "InfrastructureReady", "BootstrapReady")
			problems = append(problems, Problem{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: "critical", Reason: "Machine in Failed phase", Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(ageDur), DurationSeconds: int64(ageDur.Seconds()),
			})
			continue
		}

		// Phase-based: stuck Provisioning > 10m
		if strings.EqualFold(phase, "provisioning") && ageDur > 10*time.Minute {
			_, reason, msg, _, _ := findFalseCondition(m, "InfrastructureReady", "BootstrapReady")
			displayReason := fmt.Sprintf("Stuck provisioning for %s", FormatAge(ageDur))
			if reason != "" {
				displayReason += " (" + reason + ")"
			}
			problems = append(problems, Problem{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: "high", Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(ageDur), DurationSeconds: int64(ageDur.Seconds()),
			})
			continue
		}

		// Condition-based: BootstrapReady=False, NodeHealthy=False, InfrastructureReady=False
		// (catches problems that phase alone misses, e.g. Running phase but NodeHealthy=False)
		if ct, reason, msg, dur, ok := findFalseCondition(m,
			"BootstrapReady", "NodeHealthy", "InfrastructureReady",
		); ok {
			severity := "high"
			if ct == "BootstrapReady" {
				severity = "critical"
			}
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			d := dur
			if d == 0 {
				d = ageDur
			}
			problems = append(problems, Problem{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(d), DurationSeconds: int64(d.Seconds()),
			})
		}
	}

	// -----------------------------------------------------------------------
	// CAPI MachineDeployment problems: ready < desired for > 5m
	// -----------------------------------------------------------------------
	for _, md := range listCAPI("MachineDeployment", "") {
		desired, _, _ := unstructured.NestedInt64(md.Object, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(md.Object, "status", "readyReplicas")
		if desired > 0 && ready < desired {
			ageDur := now.Sub(md.GetCreationTimestamp().Time)
			if ageDur > 5*time.Minute {
				_, reason, msg, _, _ := findFalseCondition(md, "Ready", "Available")
				displayReason := fmt.Sprintf("%d/%d machines ready", ready, desired)
				if reason != "" {
					displayReason += " (" + reason + ")"
				}
				problems = append(problems, Problem{
					Kind: "MachineDeployment", Namespace: md.GetNamespace(), Name: md.GetName(), Group: capiGroup,
					Severity: "high", Reason: displayReason, Message: msg,
					Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
					Duration: FormatAge(ageDur), DurationSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	// -----------------------------------------------------------------------
	// CAPI KubeadmControlPlane problems: Ready=False or replicas mismatch
	// -----------------------------------------------------------------------
	for _, kcp := range listCAPI("KubeadmControlPlane", "") {
		ageDur := now.Sub(kcp.GetCreationTimestamp().Time)
		desired, _, _ := unstructured.NestedInt64(kcp.Object, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(kcp.Object, "status", "readyReplicas")

		if ct, reason, msg, dur, ok := findFalseCondition(kcp,
			"Ready", "Available", "CertificatesAvailable", "MachinesReady",
		); ok {
			severity := "critical"
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			if desired > 0 && ready < desired {
				displayReason = fmt.Sprintf("%d/%d CP replicas ready, %s", ready, desired, displayReason)
			}
			d := dur
			if d == 0 {
				d = ageDur
			}
			problems = append(problems, Problem{
				Kind: "KubeadmControlPlane", Namespace: kcp.GetNamespace(), Name: kcp.GetName(), Group: capiCPGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(d), DurationSeconds: int64(d.Seconds()),
			})
		}
	}

	// -----------------------------------------------------------------------
	// CAPI MachineHealthCheck: actively remediating
	// -----------------------------------------------------------------------
	for _, mhc := range listCAPI("MachineHealthCheck", "") {
		expected, _, _ := unstructured.NestedInt64(mhc.Object, "status", "expectedMachines")
		healthy, _, _ := unstructured.NestedInt64(mhc.Object, "status", "currentHealthy")
		if expected > 0 && healthy < expected {
			ageDur := now.Sub(mhc.GetCreationTimestamp().Time)
			problems = append(problems, Problem{
				Kind: "MachineHealthCheck", Namespace: mhc.GetNamespace(), Name: mhc.GetName(), Group: capiGroup,
				Severity:        "high",
				Reason:          fmt.Sprintf("Remediating: %d/%d healthy", healthy, expected),
				Age:             FormatAge(ageDur),
				AgeSeconds:      int64(ageDur.Seconds()),
				Duration:        FormatAge(ageDur),
				DurationSeconds: int64(ageDur.Seconds()),
			})
		}
	}

	return problems
}
