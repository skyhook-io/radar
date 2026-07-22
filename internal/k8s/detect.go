package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/skyhook-io/radar/pkg/health"
)

const probeFailureWindow = 10 * time.Minute

const livenessProbeFailedReason = "LivenessProbeFailed"

const terminatingWarningAfter = 10 * time.Minute
const terminatingCriticalAfter = 30 * time.Minute

// Detection is a transport-neutral raw operational finding emitted by the
// detector layer — a failing Deployment, a crashlooping pod, a dangling
// reference, a degraded Argo app. It carries NO classification, grouping, or
// ranking; those are the issues layer's job.
//
// detect.go and its sibling detectors (health.go, detect_missing_refs.go,
// detect_scheduling.go, detect_capi.go, detect_gitops.go) ARE that detector
// layer: each reads the live cache and returns []Detection. internal/issues is
// the layer ABOVE them —
// it classifies each Detection into a symptom Category, resolves its Subject,
// and folds replica fan-out into the public grouped Issue model; the home
// dashboard and MCP also consume Detections directly. So this is the bottom
// tier of the pipeline (detectors → classify/group → render) — the generalized
// successor to the v0 standalone "problems" feature, NOT a parallel surface to
// issues.
type Detection struct {
	Kind            string
	Namespace       string
	Name            string
	Group           string // API group for CRD disambiguation (e.g., "cluster.x-k8s.io")
	Severity        string // "critical", "high", "medium", "warning", or "info"
	Reason          string
	Message         string
	RawMessage      string
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
	// OwnerKind + OwnerName name the topmost stable controller of a Pod
	// problem (Pod→Deployment, not the intermediate ReplicaSet), resolved
	// via topOwnerForPod when the Pod is detected. Empty for non-Pod and
	// standalone-pod problems — those are their own subject. Lets the
	// issues layer group member pods under one workload without re-walking
	// ownerReferences.
	OwnerGroup string
	OwnerKind  string
	OwnerName  string
	// Fingerprint is an optional STABLE cause key for detectors where one
	// subject+category can have multiple distinct causes that must NOT collapse
	// into one issue (e.g. a workload missing both a ConfigMap and a Secret —
	// both are missing_config_ref). It feeds the issue ID discriminator so each
	// cause is its own row. MUST be stable across polls (don't use a flapping
	// reason or a count); empty means "fold by category" (the common case).
	Fingerprint string
	// IssueTiming + IssueTimingBasis carry best-effort timing evidence derived at
	// detection time, where typed K8s objects are in hand. Empty = no confident
	// signal; the issues layer copies them to the Issue and the API omits them
	// (omitempty). Do NOT set these unless the signal is clean; absent timing
	// is honest while a wrong one misleads agents.
	//
	// IssueTiming values:
	//   "started_at_resource_creation"        — failing state began during creation/first reconciliation.
	//   "started_after_resource_was_healthy"  — a meaningful healthy window preceded the failing state.
	// Basis values: "condition" | "owner_condition" | "pod_creation" | "deletion" | "phase" | "spec".
	IssueTiming      string
	IssueTimingBasis string
	// Cause / Action / Remediation* carry parsed domain diagnosis so the issues
	// stream can surface a plain-English cause + next step when a detector has
	// enough evidence. Empty for detectors without a parser. RemediationKind
	// names a structured one-click fix (e.g. "create-namespace") and
	// RemediationTarget the resource it acts on.
	Cause             string
	Action            string
	RemediationKind   string
	RemediationTarget string
	// OperationRetryCount / Stuck describe a controller operation that keeps
	// failing: the retry count parsed from a GitOps controller's message and
	// whether it has crossed the "no longer transient" threshold. Named
	// "operation" to distinguish from pod RestartCount above. Stuck is also set
	// for self-perpetuating states like a stuck-drift loop.
	OperationRetryCount int
	Stuck               bool
}

// podOwnerKindName resolves a Pod's topmost stable controller for issue
// grouping (Pod→Deployment, not ReplicaSet), returning empty strings for
// standalone pods. Thin wrapper over topOwnerForPod so the pod
// problem-emission sites stay terse.
func podOwnerKindName(cache *ResourceCache, pod *corev1.Pod) (group, kind, name string) {
	if to := topOwnerForPodResolved(cache, pod); to != nil {
		return to.Group, to.Kind, to.Name
	}
	return "", "", ""
}

// DetectProblems scans workloads in cache and returns detected problems.
// Covers: Deployments, StatefulSets, DaemonSets, HPAs, CronJobs, Nodes.
// Does NOT include pods (consumers handle pod problems differently).
// namespace="" scans all namespaces.
func DetectProblems(cache *ResourceCache, namespace string) []Detection {
	var problems []Detection
	now := time.Now()
	problems = append(problems, detectConfigProblems(cache, namespace, now)...)

	if namespace == "" {
		if nsLister := cache.Namespaces(); nsLister != nil {
			namespaces, _ := nsLister.List(labels.Everything())
			for _, ns := range namespaces {
				if det, ok := namespaceTerminatingProblem(ns, now); ok {
					problems = append(problems, det)
				}
			}
		}
	}

	if cmLister := cache.ConfigMaps(); cmLister != nil {
		var configMaps []*corev1.ConfigMap
		if namespace != "" {
			configMaps, _ = cmLister.ConfigMaps(namespace).List(labels.Everything())
		} else {
			configMaps, _ = cmLister.List(labels.Everything())
		}
		for _, cm := range configMaps {
			if det, ok := terminatingProblem("ConfigMap", "", cm, now); ok {
				problems = append(problems, det)
			}
		}
	}

	if secretLister := cache.Secrets(); secretLister != nil {
		var secrets []*corev1.Secret
		if namespace != "" {
			secrets, _ = secretLister.Secrets(namespace).List(labels.Everything())
		} else {
			secrets, _ = secretLister.List(labels.Everything())
		}
		for _, secret := range secrets {
			if det, ok := terminatingProblem("Secret", "", secret, now); ok {
				problems = append(problems, det)
			}
		}
	}

	podsByNamespace := listPodsByNamespace(cache, namespace)

	// Deployment problems: unavailableReplicas > 0
	if depLister := cache.Deployments(); depLister != nil {
		var deps []*appsv1.Deployment
		if namespace != "" {
			deps, _ = depLister.Deployments(namespace).List(labels.Everything())
		} else {
			deps, _ = depLister.List(labels.Everything())
		}
		for _, d := range deps {
			if det, ok := terminatingProblem("Deployment", "apps", d, now); ok {
				problems = append(problems, det)
				continue
			}
			replicaFailure := deploymentReplicaFailure(d)
			stuck := deploymentProgressDeadlineExceeded(d)
			if replicaFailure != nil {
				ageDur := now.Sub(d.CreationTimestamp.Time)
				durDur := ageDur
				if !replicaFailure.LastTransitionTime.IsZero() {
					durDur = now.Sub(replicaFailure.LastTransitionTime.Time)
				}
				timingR := IssueTimingFromConditionLTT(replicaFailure.LastTransitionTime.Time, d.CreationTimestamp.Time, "condition")
				if deploymentNeverHealthy(d) {
					timingR = IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: "condition"}
				}
				problems = append(problems, Detection{
					Kind:             "Deployment",
					Namespace:        d.Namespace,
					Name:             d.Name,
					Group:            "apps",
					Severity:         "critical",
					Reason:           "ReplicaFailure",
					Action:           "Check the ReplicaSet's events — pod creation is being rejected (often a quota, admission webhook, or PodSecurity rule).",
					Message:          replicaFailure.Message,
					Fingerprint:      "deployment:replica-failure",
					Age:              FormatAge(ageDur),
					AgeSeconds:       int64(ageDur.Seconds()),
					Duration:         FormatAge(durDur),
					DurationSeconds:  int64(durDur.Seconds()),
					IssueTiming:      timingR.IssueTiming,
					IssueTimingBasis: timingR.Basis,
				})
			}

			if d.Status.UnavailableReplicas > 0 && stuck == nil && replicaFailure == nil {
				ageDur := now.Sub(d.CreationTimestamp.Time)
				durDur := ageDur // fallback to creation time
				var availCondLTT time.Time
				for _, cond := range d.Status.Conditions {
					if cond.Type == appsv1.DeploymentAvailable && cond.Status == "False" && !cond.LastTransitionTime.IsZero() {
						durDur = now.Sub(cond.LastTransitionTime.Time)
						availCondLTT = cond.LastTransitionTime.Time
						break
					}
				}
				timingR := IssueTimingFromConditionLTT(availCondLTT, d.CreationTimestamp.Time, "condition")
				// Report available/DESIRED: spec.replicas is the authoritative goal
				// (nil defaults to 1; a scale-down's terminating pods inflate
				// status.replicas above the target). schedDesiredReplicas encodes
				// the nil→1 default.
				desired := schedDesiredReplicas(d.Spec.Replicas)
				problems = append(problems, Detection{
					Kind:             "Deployment",
					Namespace:        d.Namespace,
					Name:             d.Name,
					Group:            "apps",
					Severity:         "critical",
					Reason:           fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, desired),
					Age:              FormatAge(ageDur),
					AgeSeconds:       int64(ageDur.Seconds()),
					Duration:         FormatAge(durDur),
					DurationSeconds:  int64(durDur.Seconds()),
					IssueTiming:      timingR.IssueTiming,
					IssueTimingBasis: timingR.Basis,
				})
			}

			if stuck != nil && replicaFailure == nil {
				durDur := now.Sub(d.CreationTimestamp.Time)
				if !stuck.LastTransitionTime.IsZero() {
					durDur = now.Sub(stuck.LastTransitionTime.Time)
				}
				message := stuck.Message
				if detail := rolloutRWOVolumeDetail(cache, d); detail != "" {
					if message != "" {
						message += " " + detail
					} else {
						message = detail
					}
				}
				// Progressing's LTT re-triggers on every rollout retry, so on a
				// never-healthy Deployment it reads as a recent transition and the
				// classifier would claim a healthy window that never existed. The
				// Available condition is the authority on "was it ever healthy":
				// Available=False whose LTT sits at creation means it never went
				// True — override to at-creation before trusting Progressing.
				timingR := IssueTimingFromConditionLTT(stuck.LastTransitionTime.Time, d.CreationTimestamp.Time, "condition")
				if deploymentNeverHealthy(d) {
					timingR = IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: "condition"}
				} else if timingR.IssueTiming == "" && d.Status.ObservedGeneration == 1 {
					// observedGeneration==1 rescues only the no-verdict case for
					// first-ever rollouts (slow clusters can exceed
					// progressDeadlineSeconds on gen 1). It must not override a
					// timestamp-backed verdict: generation only bumps on spec
					// changes, so a gen-1 Deployment that ran healthy for months
					// and then broke (image gone, node loss) is still gen 1.
					timingR = IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: "spec"}
				}
				problems = append(problems, Detection{
					Kind:             "Deployment",
					Namespace:        d.Namespace,
					Name:             d.Name,
					Group:            "apps",
					Severity:         "critical",
					Reason:           "Rollout stuck",
					Action:           "Inspect the new pods (image, readiness probe, resources) — the rollout is waiting on them to become ready.",
					Message:          message,
					Age:              FormatAge(now.Sub(d.CreationTimestamp.Time)),
					AgeSeconds:       int64(now.Sub(d.CreationTimestamp.Time).Seconds()),
					Duration:         FormatAge(durDur),
					DurationSeconds:  int64(durDur.Seconds()),
					IssueTiming:      timingR.IssueTiming,
					IssueTimingBasis: timingR.Basis,
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
			if det, ok := terminatingProblem("StatefulSet", "apps", ss, now); ok {
				problems = append(problems, det)
				continue
			}
			// status.replicas counts pods the controller has created so far; a
			// partitioned/ordered rollout wedged on an early ordinal (bad image)
			// can have ReadyReplicas == Replicas while spec.replicas is never
			// reached. Compare against the desired count so that stall surfaces.
			desired := ss.Status.Replicas
			if ss.Spec.Replicas != nil && *ss.Spec.Replicas > desired {
				desired = *ss.Spec.Replicas
			}
			if ss.Status.ReadyReplicas < desired {
				ageDur := now.Sub(ss.CreationTimestamp.Time)
				problems = append(problems, Detection{
					Kind:            "StatefulSet",
					Namespace:       ss.Namespace,
					Name:            ss.Name,
					Group:           "apps",
					Severity:        "critical",
					Reason:          fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, desired),
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
			if det, ok := terminatingProblem("DaemonSet", "apps", ds, now); ok {
				problems = append(problems, det)
				continue
			}
			ageDur := now.Sub(ds.CreationTimestamp.Time)
			appendDSProblem := func(reason, severity, fingerprint string) {
				problems = append(problems, Detection{
					Kind:            "DaemonSet",
					Namespace:       ds.Namespace,
					Name:            ds.Name,
					Group:           "apps",
					Severity:        severity,
					Reason:          reason,
					Fingerprint:     fingerprint,
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
			if ds.Status.NumberMisscheduled > 0 {
				appendDSProblem(fmt.Sprintf("%d misscheduled", ds.Status.NumberMisscheduled), "high", "daemonset:misscheduled")
			}
			if ds.Status.DesiredNumberScheduled > ds.Status.CurrentNumberScheduled {
				appendDSProblem(fmt.Sprintf("%d not scheduled", ds.Status.DesiredNumberScheduled-ds.Status.CurrentNumberScheduled), "critical", "daemonset:not-scheduled")
			} else if ds.Status.NumberUnavailable > 0 {
				severity := "critical"
				if daemonSetUnavailableIsByDesignUnschedulable(cache, ds, podsByNamespace) {
					severity = "high"
				}
				appendDSProblem(fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable), severity, "daemonset:unavailable")
			}
		}
	}

	probeFailures := latestProbeFailures(cache, namespace, now)
	pvcPendingFailures := latestPVCPendingFailures(cache, namespace)

	// Pod problems: high-signal container waiting/terminated states, old
	// Pending pods, and restart-heavy pods. These are useful direct pointers
	// even when a controller-level problem also exists.
	for _, pods := range podsByNamespace {
		for _, pod := range pods {
			if det, ok := terminatingProblem("Pod", "", pod, now); ok {
				problems = append(problems, det)
				continue
			}
			healthStr := health.Pod(pod, now).LegacyString()
			earlyProbeTargetProblem, hasEarlyProbeTargetProblem := activeProbeTargetProblem(pod, "")
			if healthStr == "healthy" && !hasEarlyProbeTargetProblem {
				continue
			}
			// Unschedulable pods are owned by the scheduling source, which
			// names the offending constraint instead of a bare "Pending".
			if health.IsPodUnschedulable(pod) {
				continue
			}
			ageDur := now.Sub(pod.CreationTimestamp.Time)
			severity := "high"
			if healthStr == "error" {
				severity = "critical"
			}
			restartCount, lastTermReason := health.PodRestartContext(pod)
			ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
			reason := health.PodProblemReason(pod, now)
			message := health.PodProblemMessage(pod)
			if pf, ok := probeFailures[pod.Namespace+"/"+pod.Name]; ok && shouldUseProbeFailure(pod, reason, lastTermReason, pf.reason, now) {
				reason = pf.reason
				message = pf.message
			}
			fingerprint := ""
			if inv, ok := activeProbeTargetProblem(pod, reason); ok {
				reason = inv.reason
				message = inv.message
				fingerprint = inv.fingerprint
			} else if hasEarlyProbeTargetProblem {
				reason = earlyProbeTargetProblem.reason
				message = earlyProbeTargetProblem.message
				fingerprint = earlyProbeTargetProblem.fingerprint
			} else if init, ok := stalledInitContainerProblem(pod, now); ok {
				reason = init.reason
				message = init.message
				fingerprint = init.fingerprint
			}
			cause, action := oomLimitDiagnosis(cache, pod, reason, lastTermReason, now)
			if cause == "" {
				if reason == crashLoopReason {
					cause, action = health.PodCrashLoopDiagnosis(pod, now)
				} else {
					cause, action = imagePullDiagnosis(reason, message)
				}
			}
			// IssueTiming: classify whether this pod has been failing since the Deployment
			// was first created (started_at_resource_creation) or broke after a period of
			// healthy operation (started_after_resource_was_healthy).
			//
			// The central hazard: restart-driven readiness cycling (CrashLoopBackOff,
			// OOM loops, liveness kills) flips the owner's Available condition
			// True↔False on every crash, so its lastTransitionTime stops marking
			// when the problem started. Every branch below is chosen to be
			// flap-immune; when no flap-immune evidence exists, timing is omitted —
			// an absent answer is honest, a flap-derived one actively misleads.
			//
			//  1. Never-healthy proof: Available=False whose LTT sits at creation
			//     never went True — it cannot have flapped. At-creation, any age.
			//  2. Young original rollout (<15min, pod or its RS created with the
			//     Deployment): creation-timestamp proximity. The 15-minute cap
			//     keeps month-old original pods that crash later (memory leak)
			//     from being misread as deploy-time faults.
			//  3. Surge-rollout regression: the pod belongs to a NEW ReplicaSet
			//     while Available is still True (old RS pods serving) — the
			//     workload was demonstrably healthy and this rollout brought the
			//     failure. failingSince = RS creation. Available=False new-RS
			//     cases are deliberately NOT classified: a broken→broken rollout
			//     (never-healthy workload, failed fix attempt) is
			//     indistinguishable from a regression there.
			//  4. Restart-cycling pods (restartCount ≥ 3) past those branches:
			//     omit — the Available LTT is flap-poisoned.
			//  5. Otherwise (old deployment, stable pods — e.g. ImagePullBackOff
			//     pods that never start, so readiness never cycles): the
			//     Available=False LTT is trustworthy; classify from it.
			//
			// Basis "pod_creation" documents creation-timestamp evidence (pod or
			// ReplicaSet vs Deployment); "owner_condition" documents owner
			// condition LTT evidence.
			const maxDeployAgeForProximityIssueTiming = 15 * time.Minute
			const establishedRestartLoop = 3
			var podIssueTiming IssueTimingResult
			if ownerKind == "Deployment" && ownerName != "" {
				if depLister := cache.Deployments(); depLister != nil {
					if dep, err := depLister.Deployments(pod.Namespace).Get(ownerName); err == nil {
						depAge := now.Sub(dep.CreationTimestamp.Time)
						podAge := now.Sub(pod.CreationTimestamp.Time)
						podCreatedWithDeploy := depAge > 0 && (depAge-podAge) < 60*time.Second
						// The owning ReplicaSet anchors rollout identity: backoff
						// recreations stay in the original RS, while a mid-life
						// rollout or scale-up creates a NEW RS.
						var ownRS *appsv1.ReplicaSet
						if rsLister := cache.ReplicaSets(); rsLister != nil {
							for _, ref := range pod.OwnerReferences {
								if ref.Kind != "ReplicaSet" {
									continue
								}
								if rs, rsErr := rsLister.ReplicaSets(pod.Namespace).Get(ref.Name); rsErr == nil {
									ownRS = rs
								}
								break
							}
						}
						rsCreatedWithDeploy := ownRS != nil && ownRS.CreationTimestamp.Time.Sub(dep.CreationTimestamp.Time) < 60*time.Second
						rsIsNewRollout := ownRS != nil && !rsCreatedWithDeploy
						var availCond *appsv1.DeploymentCondition
						for i := range dep.Status.Conditions {
							if dep.Status.Conditions[i].Type == appsv1.DeploymentAvailable {
								availCond = &dep.Status.Conditions[i]
								break
							}
						}
						switch {
						case deploymentNeverHealthy(dep):
							podIssueTiming = IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: "owner_condition"}
						case depAge <= maxDeployAgeForProximityIssueTiming && (podCreatedWithDeploy || rsCreatedWithDeploy):
							// Original pod uses its own creation as the failingSince
							// proxy; a backoff replacement (recreated minutes later in
							// the original RS) backdates to Deployment creation so
							// healthyFor≈0 lands in the at-creation slop.
							failingSince := pod.CreationTimestamp.Time
							if !podCreatedWithDeploy {
								failingSince = dep.CreationTimestamp.Time
							}
							podIssueTiming = IssueTimingFromConditionLTT(failingSince, dep.CreationTimestamp.Time, "pod_creation")
						case rsIsNewRollout && availCond != nil && availCond.Status == corev1.ConditionTrue:
							podIssueTiming = IssueTimingFromConditionLTT(ownRS.CreationTimestamp.Time, dep.CreationTimestamp.Time, "pod_creation")
						case restartCount >= establishedRestartLoop:
							// flap-poisoned Available LTT — omit
						case availCond != nil && availCond.Status == corev1.ConditionFalse && !availCond.LastTransitionTime.IsZero():
							podIssueTiming = IssueTimingFromConditionLTT(availCond.LastTransitionTime.Time, dep.CreationTimestamp.Time, "owner_condition")
						}
					}
				}
			}
			problems = append(problems, Detection{
				Kind:                 "Pod",
				Namespace:            pod.Namespace,
				Name:                 pod.Name,
				Severity:             severity,
				Reason:               reason,
				Message:              message,
				Fingerprint:          fingerprint,
				Age:                  FormatAge(ageDur),
				AgeSeconds:           int64(ageDur.Seconds()),
				Duration:             FormatAge(ageDur),
				DurationSeconds:      int64(ageDur.Seconds()),
				RestartCount:         restartCount,
				LastTerminatedReason: lastTermReason,
				OwnerGroup:           ownerGroup,
				OwnerKind:            ownerKind,
				OwnerName:            ownerName,
				IssueTiming:          podIssueTiming.IssueTiming,
				IssueTimingBasis:     podIssueTiming.Basis,
				Cause:                cause,
				Action:               action,
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
			if det, ok := terminatingProblem("Service", "", svc, now); ok {
				problems = append(problems, det)
				continue
			}
			ageDur := now.Sub(svc.CreationTimestamp.Time)
			if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && len(svc.Status.LoadBalancer.Ingress) == 0 && ageDur > 5*time.Minute {
				problems = append(problems, Detection{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "high",
					Reason:          "LoadBalancer pending",
					Action:          "Check the cloud load-balancer controller and provider quota / subnet annotations.",
					Message:         "Service is type LoadBalancer but has no assigned external address",
					Fingerprint:     "svc:loadbalancer-pending",
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
			if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
				continue
			}
			selected := podsMatchingService(svc, podsByNamespace[svc.Namespace])
			if len(selected) == 0 {
				// Warning, not critical: a Service with zero matching pods
				// is often intentional (scaled-to-zero workload, dormant
				// staging environment, just-deployed Service waiting for
				// its workload to apply). The "0/N selected but 0 ready"
				// case below stays critical — that's a real routing break
				// because the workload is up but unhealthy.
				reason := "Selector matches no pods"
				message := selectorMessage(svc.Spec.Selector)
				// Distinguish the deliberate scale-to-0 case (managed-prometheus
				// components disabled, antrea on Autopilot, dormant staging) from
				// a genuinely orphaned selector. Both stay warning, but an honest
				// reason keeps the row from reading as a routing fault.
				if scaledToZeroBackingWorkload(cache, svc) {
					reason = "Backing workload scaled to 0"
					message = "selector matches a Deployment/StatefulSet that is intentionally scaled to 0 replicas"
				} else if selectorMatchesSucceededPod(svc, podsByNamespace[svc.Namespace]) {
					// The selector DOES match pods — they're just completed Job pods,
					// which aren't routable endpoints. "matches no pods" would be a
					// false lead for an operator who can see them with kubectl.
					reason = "Selector matches only completed pods"
					message = "selector matches finished Job pods, which are not routable endpoints"
				}
				problems = append(problems, Detection{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "warning",
					Reason:          reason,
					Message:         message,
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
				// Mid scale-to-zero (1→0) the terminating old pod still matches
				// the selector (selected>0) but isn't ready — that's the
				// deliberate scale-down, not a routing break. Same benign case
				// as the zero-selected branch above, just caught a poll earlier.
				if scaledToZeroBackingWorkload(cache, svc) {
					problems = append(problems, Detection{
						Kind:            "Service",
						Namespace:       svc.Namespace,
						Name:            svc.Name,
						Severity:        "warning",
						Reason:          "Backing workload scaled to 0",
						Message:         "selector matches a Deployment/StatefulSet that is intentionally scaled to 0 replicas",
						Age:             FormatAge(ageDur),
						AgeSeconds:      int64(ageDur.Seconds()),
						Duration:        FormatAge(ageDur),
						DurationSeconds: int64(ageDur.Seconds()),
					})
					continue
				}
				problems = append(problems, Detection{
					Kind:      "Service",
					Namespace: svc.Namespace,
					Name:      svc.Name,
					Severity:  "critical",
					Reason:    fmt.Sprintf("0/%d selected pods ready", len(selected)),
					// A Service can be BOTH no-ready-endpoints AND have an
					// unresolved named targetPort — distinct fixes (the workload
					// vs the Service port spec). Stable per-cause fingerprints
					// (not the flapping replica count) keep them as two issues.
					Fingerprint:     "svc:no-ready-endpoints",
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
			if missing := unresolvedNamedTargetPorts(svc, selected); len(missing) > 0 {
				problems = append(problems, Detection{
					Kind:            "Service",
					Namespace:       svc.Namespace,
					Name:            svc.Name,
					Severity:        "high",
					Reason:          fmt.Sprintf("Unresolved named targetPort: %s", strings.Join(missing, ", ")),
					Message:         "No selected pod declares a container port with this name",
					Fingerprint:     "svc:unresolved-targetport",
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
			ageDur := resourceAge(now, hpas, hp.Namespace, hp.Name)
			// IssueTiming for cannot-scale only: use ScalingActive condition LTT.
			// "maxed" carries no issue_timing — hitting the replica ceiling is a
			// capacity concern that doesn't cleanly map to
			// started_at_resource_creation vs started_after_resource_was_healthy.
			var hpaIssueTiming IssueTimingResult
			var hpaCondDur time.Duration
			if hp.Problem == "cannot-scale" {
				for _, hpa := range hpas {
					if hpa.Name != hp.Name || hpa.Namespace != hp.Namespace {
						continue
					}
					for _, cond := range hpa.Status.Conditions {
						if cond.Type == autoscalingv2.ScalingActive && cond.Status == corev1.ConditionFalse && !cond.LastTransitionTime.IsZero() {
							hpaCondDur = now.Sub(cond.LastTransitionTime.Time)
							hpaIssueTiming = IssueTimingFromConditionLTT(cond.LastTransitionTime.Time, hpa.CreationTimestamp.Time, "condition")
							break
						}
					}
					break
				}
			}
			// Use the condition LTT for Duration when available so first_seen
			// stays coherent with issue_timing instead of falling back to resource age.
			hpaDur := hpaCondDur
			if hpaDur == 0 {
				hpaDur = ageDur
			}
			problems = append(problems, Detection{
				Kind:      "HorizontalPodAutoscaler",
				Namespace: hp.Namespace,
				Name:      hp.Name,
				Group:     "autoscaling",
				Severity:  severity,
				Reason:    hp.Problem,
				Message:   hp.Reason,
				Cause:     hp.Cause,
				Action:    hp.Action,
				// One HPA can be BOTH maxed and unable-to-scale at once — distinct
				// problems with distinct fixes. Fingerprint on the problem kind so
				// they don't collapse into one hpa_limited_or_failed row.
				Fingerprint:      "hpa:" + hp.Problem,
				Age:              FormatAge(ageDur),
				AgeSeconds:       int64(ageDur.Seconds()),
				Duration:         FormatAge(hpaDur),
				DurationSeconds:  int64(hpaDur.Seconds()),
				IssueTiming:      hpaIssueTiming.IssueTiming,
				IssueTimingBasis: hpaIssueTiming.Basis,
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
			ageDur := resourceAge(now, cronjobs, cp.Namespace, cp.Name)
			problems = append(problems, Detection{
				Kind:       "CronJob",
				Namespace:  cp.Namespace,
				Name:       cp.Name,
				Group:      "batch",
				Severity:   "medium",
				Reason:     cp.Problem,
				Message:    cp.Reason,
				Age:        FormatAge(ageDur),
				AgeSeconds: int64(ageDur.Seconds()),
			})
		}
	}

	// PDBs that require every selected pod to stay healthy allow zero voluntary
	// evictions even when the workload is fully healthy. That is not an outage,
	// but it blocks node drains and upgrades; name the policy footgun from
	// status instead of leaving agents to infer it during a failed drain.
	if pdbLister := cache.PodDisruptionBudgets(); pdbLister != nil {
		var pdbs []*policyv1.PodDisruptionBudget
		if namespace != "" {
			pdbs, _ = pdbLister.PodDisruptionBudgets(namespace).List(labels.Everything())
		} else {
			pdbs, _ = pdbLister.List(labels.Everything())
		}
		for _, pdb := range pdbs {
			if det, ok := terminatingProblem("PodDisruptionBudget", "policy", pdb, now); ok {
				problems = append(problems, det)
				continue
			}
			if !pdbStructurallyBlocksEvictions(pdb) {
				continue
			}
			ageDur := now.Sub(pdb.CreationTimestamp.Time)
			durDur := ageDur
			for _, cond := range pdb.Status.Conditions {
				if cond.Type == policyv1.DisruptionAllowedCondition && cond.Status == metav1.ConditionFalse && !cond.LastTransitionTime.IsZero() {
					durDur = now.Sub(cond.LastTransitionTime.Time)
					break
				}
			}
			problems = append(problems, Detection{
				Kind:            "PodDisruptionBudget",
				Namespace:       pdb.Namespace,
				Name:            pdb.Name,
				Group:           "policy",
				Severity:        "high",
				Reason:          "Voluntary evictions blocked",
				Action:          "Relax the PDB (minAvailable / maxUnavailable) or add replicas so drains and node upgrades can proceed.",
				Message:         pdbBlocksEvictionsMessage(pdb),
				Fingerprint:     "pdb:zero-disruptions",
				Age:             FormatAge(ageDur),
				AgeSeconds:      int64(ageDur.Seconds()),
				Duration:        FormatAge(durDur),
				DurationSeconds: int64(durDur.Seconds()),
			})
		}
	}

	// Node problems (cluster-scoped, not filtered by namespace)
	if nodeLister := cache.Nodes(); nodeLister != nil {
		nodes, _ := nodeLister.List(labels.Everything())
		for _, np := range DetectNodeProblems(nodes) {
			ageDur := time.Duration(0)
			var nodeIssueTiming IssueTimingResult
			var nodeCondDur time.Duration
			for _, n := range nodes {
				if n.Name != np.NodeName {
					continue
				}
				ageDur = now.Sub(n.CreationTimestamp.Time)
				// Map each problem type to its own condition so issue_timing and
				// DurationSeconds reflect the right LTT:
				//   NotReady      → NodeReady condition
				//   *Pressure     → matching pressure condition
				//   Cordoned      → no condition, omit issue_timing
				var targetCondType corev1.NodeConditionType
				switch np.Problem {
				case "NotReady":
					targetCondType = corev1.NodeReady
				case "MemoryPressure":
					targetCondType = corev1.NodeMemoryPressure
				case "DiskPressure":
					targetCondType = corev1.NodeDiskPressure
				case "PIDPressure":
					targetCondType = corev1.NodePIDPressure
				}
				if targetCondType != "" {
					for _, cond := range n.Status.Conditions {
						if cond.Type != targetCondType || cond.LastTransitionTime.IsZero() {
							continue
						}
						// Ready=False or pressure=True both mean the condition is active.
						if targetCondType == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
							continue
						}
						if targetCondType != corev1.NodeReady && cond.Status != corev1.ConditionTrue {
							continue
						}
						nodeCondDur = now.Sub(cond.LastTransitionTime.Time)
						nodeIssueTiming = IssueTimingFromConditionLTT(cond.LastTransitionTime.Time, n.CreationTimestamp.Time, "condition")
						break
					}
				}
				break
			}
			nodeDur := nodeCondDur
			if nodeDur == 0 {
				nodeDur = ageDur
			}
			problems = append(problems, Detection{
				Kind:             "Node",
				Name:             np.NodeName,
				Severity:         np.Severity,
				Reason:           np.Problem,
				Message:          np.Reason,
				Age:              FormatAge(ageDur),
				AgeSeconds:       int64(ageDur.Seconds()),
				Duration:         FormatAge(nodeDur),
				DurationSeconds:  int64(nodeDur.Seconds()),
				IssueTiming:      nodeIssueTiming.IssueTiming,
				IssueTimingBasis: nodeIssueTiming.Basis,
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
			if det, ok := terminatingProblem("PersistentVolumeClaim", "", pvc, now); ok {
				problems = append(problems, det)
				continue
			}
			ageDur := now.Sub(pvc.CreationTimestamp.Time)
			if pvc.Status.Phase == corev1.ClaimLost {
				problems = append(problems, Detection{
					Kind:            "PersistentVolumeClaim",
					Namespace:       pvc.Namespace,
					Name:            pvc.Name,
					Severity:        "critical",
					Reason:          "Lost",
					Message:         "PVC has lost its bound volume",
					Action:          "The bound PersistentVolume is gone — restore it from backup or recreate the PVC (data may be unrecoverable).",
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
					// IssueTiming omitted: Phase=Lost happened after the PVC existed, but
					// there is no condition LTT or event timestamp available at this
					// site. first_seen would show resource age, inconsistent with an
					// started_after_resource_was_healthy classification.
				})
				continue
			}
			if resize := pvcResizeProblem(pvc); resize.reason != "" {
				// Timing requires a real condition LTT — without one, Duration
				// falls back to resource age and first_seen would contradict the
				// label (same rule as the Lost/Failed sites). With one, run the
				// classifier like every other site: a resize error moments after
				// PVC creation is at-creation/gray, not a post-healthy regression.
				var resizeIssueTiming IssueTimingResult
				if !resize.lastTransitionTime.IsZero() {
					resizeIssueTiming = IssueTimingFromConditionLTT(resize.lastTransitionTime.Time, pvc.CreationTimestamp.Time, "condition")
				}
				problems = append(problems, Detection{
					Kind:             "PersistentVolumeClaim",
					Namespace:        pvc.Namespace,
					Name:             pvc.Name,
					Severity:         resize.severity,
					Reason:           resize.reason,
					Message:          resize.message,
					Fingerprint:      "pvc:resize:" + resize.reason,
					Age:              FormatAge(ageDur),
					AgeSeconds:       int64(ageDur.Seconds()),
					Duration:         FormatAge(resize.duration(now, ageDur)),
					DurationSeconds:  int64(resize.duration(now, ageDur).Seconds()),
					IssueTiming:      resizeIssueTiming.IssueTiming,
					IssueTimingBasis: resizeIssueTiming.Basis,
				})
			}
			if pvc.Status.Phase == corev1.ClaimPending {
				// A WaitForFirstConsumer PVC is Pending BY DESIGN until a pod that
				// mounts it is scheduled — dormant/scaled-to-zero/orphaned volumes
				// sit here forever and are not a fault. If a consumer is genuinely
				// stuck, that pod surfaces as unschedulable via the scheduling
				// source, so suppress the PVC row to avoid flagging every awaiting-
				// consumer volume.
				if pvcAwaitsFirstConsumer(cache, pvc) {
					continue
				}
				if ageDur > 5*time.Minute {
					message := "PVC is unbound — no volume has been provisioned"
					var cause, action string
					if failure, ok := pvcPendingFailures[pvc.Namespace+"/"+pvc.Name]; ok {
						message = failure.message
						cause = failure.cause
						action = failure.action
					}
					problems = append(problems, Detection{
						Kind:            "PersistentVolumeClaim",
						Namespace:       pvc.Namespace,
						Name:            pvc.Name,
						Severity:        "high",
						Reason:          "Pending",
						Message:         message,
						Age:             FormatAge(ageDur),
						AgeSeconds:      int64(ageDur.Seconds()),
						Duration:        FormatAge(ageDur),
						DurationSeconds: int64(ageDur.Seconds()),
						Cause:           cause,
						Action:          action,
						// Phase=Pending since creation means the PVC has never been bound —
						// present since creation (wrong StorageClass, missing or failing provisioner).
						IssueTiming:      "started_at_resource_creation",
						IssueTimingBasis: "phase",
					})
				}
			}
		}
	}

	if pvLister := cache.PersistentVolumes(); pvLister != nil && namespace == "" {
		pvs, _ := pvLister.List(labels.Everything())
		for _, pv := range pvs {
			if det, ok := terminatingProblem("PersistentVolume", "", pv, now); ok {
				problems = append(problems, det)
				continue
			}
			if pv.Status.Phase != corev1.VolumeFailed {
				continue
			}
			ageDur := now.Sub(pv.CreationTimestamp.Time)
			problems = append(problems, Detection{
				Kind:            "PersistentVolume",
				Name:            pv.Name,
				Severity:        "critical",
				Reason:          "Failed",
				Message:         pv.Status.Message,
				Action:          "Check the volume's status message and the storage backend / CSI driver events; replace the PV only after confirming the data and its reclaim policy.",
				Age:             FormatAge(ageDur),
				AgeSeconds:      int64(ageDur.Seconds()),
				Duration:        FormatAge(ageDur),
				DurationSeconds: int64(ageDur.Seconds()),
				// IssueTiming omitted: Phase=Failed happened after the PV existed, but
				// no condition LTT or event timestamp is available here; first_seen
				// would show resource age, contradicting started_after_resource_was_healthy.
			})
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
			if det, ok := terminatingProblem("Job", "batch", job, now); ok {
				problems = append(problems, det)
				continue
			}
			ageDur := now.Sub(job.CreationTimestamp.Time)
			// A failed Job stays its OWN subject — deliberately NOT rolled up to a
			// CronJob owner. The Job's pods resolve their top owner to the CronJob
			// (skipping the Job), so stamping the CronJob here would move job_failed
			// off the Job and fold it away, leaving a failing Job's own detail page
			// with no Operational Issues. Keeping the Job as the subject preserves
			// that drill-down; standalone Jobs already fold with their pods (whose
			// top owner is the Job).
			if cond := failedJobCondition(job); cond != nil {
				durDur := ageDur
				if !cond.LastTransitionTime.IsZero() {
					durDur = now.Sub(cond.LastTransitionTime.Time)
				}
				reason := cond.Reason
				if reason == "" {
					reason = "Failed"
				}
				problems = append(problems, Detection{
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
					problems = append(problems, Detection{
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

	// Multi-replica Deployment sharing a ReadWriteOnce volume: only one node can
	// attach an RWO PVC, so a Deployment wanting >1 replica can never bring up
	// the surplus — they sit unschedulable / multi-attach-failed. A config-level
	// root cause we can name from spec, independent of whether the symptom has
	// fired yet. StatefulSets are exempt (volumeClaimTemplates give each replica
	// its own PVC); DaemonSets are one-per-node by design.
	if depLister := cache.Deployments(); depLister != nil {
		var deps []*appsv1.Deployment
		if namespace != "" {
			deps, _ = depLister.Deployments(namespace).List(labels.Everything())
		} else {
			deps, _ = depLister.List(labels.Everything())
		}
		for _, d := range deps {
			if schedDesiredReplicas(d.Spec.Replicas) < 2 {
				continue
			}
			ageDur := now.Sub(d.CreationTimestamp.Time)
			for _, pvcName := range sharedRWOVolumeConflicts(cache, d) {
				problems = append(problems, Detection{
					Kind:      "Deployment",
					Namespace: d.Namespace,
					Name:      d.Name,
					Group:     "apps",
					Severity:  "high",
					Reason:    "ReadWriteOnce volume shared across replicas",
					Message: fmt.Sprintf("Deployment wants %d replicas but mounts ReadWriteOnce PVC %q — only one node can attach it, so the other replicas can't start. Use a ReadWriteMany volume, switch to a StatefulSet with volumeClaimTemplates (a volume per replica), or reduce to 1 replica.",
						schedDesiredReplicas(d.Spec.Replicas), pvcName),
					// One Deployment can share multiple distinct RWO PVCs; fingerprint
					// per PVC so each is its own row rather than collapsing into one.
					Fingerprint:     "pvc-rwo-multireplica:" + pvcName,
					Age:             FormatAge(ageDur),
					AgeSeconds:      int64(ageDur.Seconds()),
					Duration:        FormatAge(ageDur),
					DurationSeconds: int64(ageDur.Seconds()),
				})
			}
		}
	}

	return problems
}

type probeFailure struct {
	reason  string
	message string
	at      time.Time
}

type pvcPendingFailure struct {
	message string
	cause   string
	action  string
	at      time.Time
}

type podSpecificProblem struct {
	reason      string
	message     string
	fingerprint string
}

func activeProbeTargetProblem(pod *corev1.Pod, currentReason string) (podSpecificProblem, bool) {
	for _, c := range pod.Spec.Containers {
		if currentReason == readinessProbeFailedReason || podCurrentlyNotReady(pod) {
			if port, ok := missingNamedProbePort(c, c.ReadinessProbe); ok {
				return podSpecificProblem{
					reason:      readinessProbeInvalidReason,
					message:     fmt.Sprintf("readiness probe for container %q references named port %q, but the container declares no port with that name", c.Name, port),
					fingerprint: "probe:readiness:" + c.Name + ":" + port,
				}, true
			}
		}
		if currentReason == livenessProbeFailedReason || currentReason == crashLoopReason || currentReason == highRestartReason {
			if port, ok := missingNamedProbePort(c, c.LivenessProbe); ok {
				return podSpecificProblem{
					reason:      livenessProbeInvalidReason,
					message:     fmt.Sprintf("liveness probe for container %q references named port %q, but the container declares no port with that name", c.Name, port),
					fingerprint: "probe:liveness:" + c.Name + ":" + port,
				}, true
			}
		}
	}
	return podSpecificProblem{}, false
}

func podCurrentlyNotReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if (cond.Type == corev1.PodReady || cond.Type == corev1.ContainersReady) && cond.Status == corev1.ConditionFalse {
			return true
		}
	}
	return false
}

func missingNamedProbePort(c corev1.Container, probe *corev1.Probe) (string, bool) {
	if probe == nil {
		return "", false
	}
	var port intstr.IntOrString
	switch {
	case probe.HTTPGet != nil:
		port = probe.HTTPGet.Port
	case probe.TCPSocket != nil:
		port = probe.TCPSocket.Port
	default:
		return "", false
	}
	if port.Type != intstr.String || port.StrVal == "" {
		return "", false
	}
	for _, declared := range c.Ports {
		if declared.Name == port.StrVal {
			return "", false
		}
	}
	return port.StrVal, true
}

func stalledInitContainerProblem(pod *corev1.Pod, now time.Time) (podSpecificProblem, bool) {
	if pod.Status.Phase != corev1.PodPending {
		return podSpecificProblem{}, false
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Running == nil || cs.State.Running.StartedAt.IsZero() {
			continue
		}
		if now.Sub(cs.State.Running.StartedAt.Time) < 5*time.Minute {
			continue
		}
		msg := fmt.Sprintf("init container %q has been running for %s and is blocking the pod from starting", cs.Name, FormatAge(now.Sub(cs.State.Running.StartedAt.Time)))
		if detail := initContainerSpecSummary(pod, cs.Name); detail != "" {
			msg += "; " + detail
		}
		return podSpecificProblem{
			reason:      initContainerStalledReason,
			message:     msg,
			fingerprint: "init-stalled:" + cs.Name,
		}, true
	}
	return podSpecificProblem{}, false
}

func initContainerSpecSummary(pod *corev1.Pod, name string) string {
	for _, c := range pod.Spec.InitContainers {
		if c.Name != name {
			continue
		}
		parts := make([]string, 0, 2)
		if c.Image != "" {
			parts = append(parts, "image "+c.Image)
		}
		if len(c.Command) > 0 {
			parts = append(parts, "command "+strings.Join(c.Command, " "))
		} else if len(c.Args) > 0 {
			parts = append(parts, "args "+strings.Join(c.Args, " "))
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func imagePullDiagnosis(reason, message string) (cause, action string) {
	if !isImagePullReason(reason) {
		return "", ""
	}
	ref := imageRefFromMessage(message)
	lower := strings.ToLower(message)

	switch {
	case reason == "InvalidImageName" || strings.Contains(lower, "invalid reference format"):
		return imageCause("Image reference is invalid", ref),
			"Fix the image reference in the pod spec; check registry, repository, tag, and digest syntax."
	case containsAny(lower, "unauthorized", "forbidden", "denied", "authentication required", "pull access denied") ||
		containsStatusCode(lower, "401") || containsStatusCode(lower, "403"):
		return imageCause("Not authorized to pull image", ref),
			"Check imagePullSecrets, the pod service account, registry permissions for this namespace, and whether the repository/tag exists."
	case containsAny(lower, "toomanyrequests", "too many requests", "rate limit"):
		return imageCause("Registry rate-limited", ref),
			"Use an authenticated pull secret, reduce pull frequency, or mirror/cache the image."
	case containsAny(lower, "no such host", "i/o timeout", "timeout", "connection refused", "dial tcp"):
		return imageCause("Registry unreachable", ref),
			"Check node egress, DNS, proxy/firewall rules, and the registry endpoint."
	case containsAny(lower, "not found", "manifest unknown", "no such image", "no such manifest"):
		return imageCause("Image not found", ref),
			"Verify the repository and tag, then update the image reference or publish the missing image."
	default:
		return "", ""
	}
}

func isImagePullReason(reason string) bool {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ImageInspectError":
		return true
	default:
		return false
	}
}

func imageRefFromMessage(message string) string {
	const marker = `image "`
	start := strings.Index(message, marker)
	if start < 0 {
		return ""
	}
	rest := message[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func imageCause(base, ref string) string {
	if ref == "" {
		return base
	}
	return base + ": " + ref
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func containsStatusCode(s, code string) bool {
	for start := strings.Index(s, code); start >= 0; {
		end := start + len(code)
		beforeOK := start == 0 || !isASCIIAlnum(s[start-1])
		afterOK := end == len(s) || !isASCIIAlnum(s[end])
		if beforeOK && afterOK {
			return true
		}
		next := strings.Index(s[end:], code)
		if next < 0 {
			return false
		}
		start = end + next
	}
	return false
}

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func latestPVCPendingFailures(cache *ResourceCache, namespace string) map[string]pvcPendingFailure {
	out := map[string]pvcPendingFailure{}
	if cache == nil || cache.Events() == nil {
		return out
	}
	var events []*corev1.Event
	if namespace != "" {
		events, _ = cache.Events().Events(namespace).List(labels.Everything())
	} else {
		events, _ = cache.Events().List(labels.Everything())
	}
	for _, e := range events {
		if e.Type != corev1.EventTypeWarning || e.InvolvedObject.Kind != "PersistentVolumeClaim" {
			continue
		}
		failure, ok := pvcPendingFailureFromEvent(e)
		if !ok {
			continue
		}
		if failure.at.IsZero() {
			continue
		}
		key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
		if cur, exists := out[key]; exists && !failure.at.After(cur.at) {
			continue
		}
		out[key] = failure
	}
	return out
}

func pvcPendingFailureFromEvent(e *corev1.Event) (pvcPendingFailure, bool) {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = e.Reason
	}
	f := pvcPendingFailure{message: message, at: eventLastTime(e)}
	switch e.Reason {
	case "ProvisioningFailed":
		f.cause = "Storage provisioner failed to create a volume."
		f.action = "Check the StorageClass/provisioner and CSI controller events or logs; fix the provisioner error, quota, credentials, or backend volume settings."
	case "FailedBinding":
		f.cause = "PVC could not bind to a PersistentVolume."
		f.action = "Check matching PersistentVolumes, StorageClass topology and volumeBindingMode, and any consuming pod scheduling constraints."
	default:
		return pvcPendingFailure{}, false
	}
	return f, true
}

func latestProbeFailures(cache *ResourceCache, namespace string, now time.Time) map[string]probeFailure {
	out := map[string]probeFailure{}
	if cache == nil || cache.Events() == nil {
		return out
	}
	var events []*corev1.Event
	if namespace != "" {
		events, _ = cache.Events().Events(namespace).List(labels.Everything())
	} else {
		events, _ = cache.Events().List(labels.Everything())
	}
	for _, e := range events {
		if e.InvolvedObject.Kind != "Pod" {
			continue
		}
		reason, ok := classifyProbeFailureEvent(e.Reason, e.Message)
		if !ok {
			continue
		}
		t := eventLastTime(e)
		if t.IsZero() || now.Sub(t) > probeFailureWindow {
			continue
		}
		key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
		if cur, exists := out[key]; exists && !t.After(cur.at) {
			continue
		}
		out[key] = probeFailure{reason: reason, message: strings.TrimSpace(e.Message), at: t}
	}
	return out
}

func classifyProbeFailureEvent(reason, msg string) (string, bool) {
	if reason != "Unhealthy" {
		return "", false
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "liveness probe failed"):
		return livenessProbeFailedReason, true
	case strings.Contains(lower, "readiness probe failed"):
		return readinessProbeFailedReason, true
	default:
		return "", false
	}
}

func shouldUseProbeFailure(pod *corev1.Pod, currentReason, lastTerminatedReason, probeReason string, now time.Time) bool {
	switch probeReason {
	case readinessProbeFailedReason:
		return currentReason == readinessProbeFailedReason || health.PodHasReadinessProbeFailure(pod, now)
	case livenessProbeFailedReason:
		if lastTerminatedReason == "OOMKilled" {
			return false
		}
		switch currentReason {
		case "CrashLoopBackOff", "Error", "Failed", "Running", "Pending", "Unknown", "":
			return true
		}
		return false
	default:
		return false
	}
}

func terminatingProblem(kind, group string, obj metav1.Object, now time.Time) (Detection, bool) {
	if obj.GetDeletionTimestamp() == nil {
		return Detection{}, false
	}
	duration := now.Sub(obj.GetDeletionTimestamp().Time)
	if duration < terminatingWarningAfter {
		return Detection{}, false
	}
	severity := "high"
	if duration >= terminatingCriticalAfter {
		severity = "critical"
	}
	msg := "Resource is still present after deletion started"
	if finalizers := obj.GetFinalizers(); len(finalizers) > 0 {
		msg = "Waiting on finalizers: " + strings.Join(finalizers, ", ")
	}
	// The stuck-termination issue began when deletion was requested — run the
	// classifier against the deletionTimestamp like any other timing site. A
	// resource deleted right after creation classifies at-creation; one that
	// existed for a real window before deletion classifies post-healthy. A
	// hardcoded post-healthy label would overstate the evidence for
	// create-then-delete churn.
	timingR := IssueTimingFromConditionLTT(obj.GetDeletionTimestamp().Time, obj.GetCreationTimestamp().Time, "deletion")
	return Detection{
		Kind:             kind,
		Group:            group,
		Namespace:        obj.GetNamespace(),
		Name:             obj.GetName(),
		Severity:         severity,
		Reason:           "Terminating stuck",
		Action:           "Check for finalizers holding the object (metadata.finalizers) and whether their controller is running.",
		Message:          msg,
		Fingerprint:      "lifecycle:terminating",
		Age:              FormatAge(now.Sub(obj.GetCreationTimestamp().Time)),
		AgeSeconds:       int64(now.Sub(obj.GetCreationTimestamp().Time).Seconds()),
		Duration:         FormatAge(duration),
		DurationSeconds:  int64(duration.Seconds()),
		IssueTiming:      timingR.IssueTiming,
		IssueTimingBasis: timingR.Basis,
	}, true
}

func namespaceTerminatingProblem(ns *corev1.Namespace, now time.Time) (Detection, bool) {
	if ns == nil || ns.DeletionTimestamp == nil {
		return Detection{}, false
	}
	if ns.Status.Phase != "" && ns.Status.Phase != corev1.NamespaceTerminating {
		return Detection{}, false
	}
	det, ok := terminatingProblem("Namespace", "", ns, now)
	if !ok {
		return Detection{}, false
	}
	det.Reason = "Namespace terminating stuck"
	det.Fingerprint = "lifecycle:namespace-terminating"
	if msg := namespaceTerminationConditionMessage(ns); msg != "" {
		det.Message = msg
	}
	return det, true
}

func namespaceTerminationConditionMessage(ns *corev1.Namespace) string {
	var parts []string
	for _, cond := range ns.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		part := string(cond.Type)
		if cond.Reason != "" {
			part += ": " + cond.Reason
		}
		if cond.Message != "" {
			part += " - " + cond.Message
		}
		parts = append(parts, part)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func pdbStructurallyBlocksEvictions(pdb *policyv1.PodDisruptionBudget) bool {
	if pdb == nil || pdb.DeletionTimestamp != nil {
		return false
	}
	if pdb.Generation > 0 && pdb.Status.ObservedGeneration > 0 && pdb.Status.ObservedGeneration < pdb.Generation {
		return false
	}
	status := pdb.Status
	return status.ExpectedPods > 0 &&
		status.DisruptionsAllowed == 0 &&
		status.CurrentHealthy >= status.ExpectedPods &&
		status.DesiredHealthy >= status.ExpectedPods
}

func pdbBlocksEvictionsMessage(pdb *policyv1.PodDisruptionBudget) string {
	return fmt.Sprintf("PDB selects %d healthy pod(s) and allows 0 voluntary disruptions; node drains and upgrades cannot evict these pods. Set maxUnavailable to at least 1, lower minAvailable, or scale the workload above the required healthy count.",
		pdb.Status.ExpectedPods)
}

// sharedRWOVolumeConflicts returns the names of PVCs the Deployment's pod
// template MOUNTS whose bound access modes permit only single-node attach
// (ReadWriteOnce / ReadWriteOncePod, no ReadWriteMany). Pending PVCs are skipped
// here because the binding/provisioning failure is the actionable blocker until a
// real volume can attach.
func sharedRWOVolumeConflicts(cache *ResourceCache, d *appsv1.Deployment) []string {
	pvcl := cache.PersistentVolumeClaims()
	if pvcl == nil {
		return nil
	}
	spec := d.Spec.Template.Spec
	mounted := mountedVolumeNames(spec)
	var out []string
	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim == nil || !mounted[v.Name] {
			continue
		}
		pvc, err := pvcl.PersistentVolumeClaims(d.Namespace).Get(v.PersistentVolumeClaim.ClaimName)
		if err != nil || pvc == nil {
			continue
		}
		if pvc.Status.Phase == corev1.ClaimBound && pvcSingleNodeAccessOnly(pvc) {
			out = append(out, pvc.Name)
		}
	}
	sort.Strings(out)
	return out
}

func rolloutRWOVolumeDetail(cache *ResourceCache, d *appsv1.Deployment) string {
	if d == nil || d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return ""
	}
	pvcs := sharedRWOVolumeConflicts(cache, d)
	if len(pvcs) == 0 {
		return ""
	}
	return fmt.Sprintf("This Deployment mounts ReadWriteOnce PVC %q and uses RollingUpdate; a surge pod can be blocked while the old pod still holds the volume. Use strategy: Recreate, a ReadWriteMany volume, or per-replica volumes.",
		pvcs[0])
}

// mountedVolumeNames is the set of pod-template volume names actually mounted by
// a container (main or init). A defined-but-unmounted volume can't cause an
// attach conflict, so the detector ignores it.
func mountedVolumeNames(spec corev1.PodSpec) map[string]bool {
	out := map[string]bool{}
	add := func(containers []corev1.Container) {
		for _, c := range containers {
			for _, m := range c.VolumeMounts {
				out[m.Name] = true
			}
		}
	}
	add(spec.InitContainers)
	add(spec.Containers)
	return out
}

// pvcSingleNodeAccessOnly reports whether a PVC's effective access modes permit
// attaching on only one node at a time (ReadWriteOnce / ReadWriteOncePod and NOT
// ReadWriteMany). Prefers the bound status modes; falls back to the requested
// spec modes. Empty/unknown modes → false (don't guess).
func pvcSingleNodeAccessOnly(pvc *corev1.PersistentVolumeClaim) bool {
	modes := pvc.Status.AccessModes
	if len(modes) == 0 {
		modes = pvc.Spec.AccessModes
	}
	restrictive := false
	for _, m := range modes {
		if m == corev1.ReadWriteMany {
			return false
		}
		if m == corev1.ReadWriteOnce || m == corev1.ReadWriteOncePod {
			restrictive = true
		}
	}
	return restrictive
}

type pvcResizeDetection struct {
	reason             string
	message            string
	severity           string
	lastTransitionTime metav1.Time
}

func (d pvcResizeDetection) duration(now time.Time, fallback time.Duration) time.Duration {
	if !d.lastTransitionTime.IsZero() {
		return now.Sub(d.lastTransitionTime.Time)
	}
	return fallback
}

func pvcResizeProblem(pvc *corev1.PersistentVolumeClaim) pvcResizeDetection {
	for _, cond := range pvc.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch string(cond.Type) {
		case "ControllerResizeError", "ModifyVolumeError":
			return pvcResizeDetection{
				reason:             string(cond.Type),
				message:            cond.Message,
				severity:           "critical",
				lastTransitionTime: cond.LastTransitionTime,
			}
		case "NodeResizeError":
			return pvcResizeDetection{
				reason:             string(cond.Type),
				message:            cond.Message,
				severity:           "critical",
				lastTransitionTime: cond.LastTransitionTime,
			}
		}
	}
	return pvcResizeDetection{}
}

// resourceAge returns now-creationTimestamp for the item in items matching
// namespace/name, or 0 if absent. Used to stamp a stable AgeSeconds on
// detections from per-kind detectors (HPA/CronJob) that don't track how long
// the problem has persisted — without it, the issues layer would derive
// FirstSeen=now on every compose and a chronic issue would sort as fresh.
func resourceAge[T metav1.Object](now time.Time, items []T, namespace, name string) time.Duration {
	for _, it := range items {
		if it.GetNamespace() == namespace && it.GetName() == name {
			return now.Sub(it.GetCreationTimestamp().Time)
		}
	}
	return 0
}

// pvcAwaitsFirstConsumer reports whether a Pending PVC is bound to a
// WaitForFirstConsumer StorageClass — in which case Pending is the EXPECTED
// state until a consuming pod is scheduled, not a fault. Resolves the PVC's
// explicit StorageClass, falling back to the cluster default. Unknown SC →
// false (can't prove benign, so let the caller flag it).
func pvcAwaitsFirstConsumer(cache *ResourceCache, pvc *corev1.PersistentVolumeClaim) bool {
	scl := cache.StorageClasses()
	if scl == nil {
		return false
	}
	var sc *storagev1.StorageClass
	if name := pvc.Spec.StorageClassName; name != nil && *name != "" {
		sc, _ = scl.Get(*name)
	} else {
		all, _ := scl.List(labels.Everything())
		for _, c := range all {
			if c.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
				sc = c
				break
			}
		}
	}
	return sc != nil && sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
}

func daemonSetUnavailableIsByDesignUnschedulable(cache *ResourceCache, ds *appsv1.DaemonSet, podsByNamespace map[string][]*corev1.Pod) bool {
	byDesignTargets := map[string]bool{}
	for _, pod := range podsByNamespace[ds.Namespace] {
		_, ownerKind, ownerName := podOwnerKindName(cache, pod)
		if ownerKind != "DaemonSet" || ownerName != ds.Name {
			continue
		}
		if daemonSetPodIsAssignedButUnavailable(pod) {
			return false
		}
		cond := podScheduledCondition(pod)
		if cond == nil || cond.Status != corev1.ConditionFalse || cond.Reason != corev1.PodReasonUnschedulable {
			continue
		}
		_, reasons := parseSchedulerMessage(cond.Message)
		if len(reasons) == 0 || !allDaemonSetByDesignPlacementReasons(reasons) {
			return false
		}
		targetNode := daemonSetPodTargetNode(pod)
		if targetNode == "" {
			continue
		}
		byDesignTargets[targetNode] = true
	}
	return int32(len(byDesignTargets)) >= ds.Status.NumberUnavailable
}

func daemonSetPodIsAssignedButUnavailable(pod *corev1.Pod) bool {
	if pod == nil || pod.Spec.NodeName == "" {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if (cond.Type == corev1.PodReady || cond.Type == corev1.ContainersReady) && cond.Status == corev1.ConditionFalse {
			return true
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready || status.State.Waiting != nil || status.State.Terminated != nil {
			return true
		}
	}
	return pod.Status.Phase == corev1.PodFailed
}

func daemonSetPodTargetNode(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	if pod.Spec.NodeName != "" {
		return pod.Spec.NodeName
	}
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return ""
	}
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		return ""
	}
	for _, term := range required.NodeSelectorTerms {
		for _, field := range term.MatchFields {
			if field.Key == "metadata.name" && field.Operator == corev1.NodeSelectorOpIn && len(field.Values) == 1 {
				return field.Values[0]
			}
		}
	}
	return ""
}

func allDaemonSetByDesignPlacementReasons(reasons []SchedulingReason) bool {
	for _, reason := range reasons {
		switch reason.Class {
		case SchedNodeAffinitySelector:
			continue
		case SchedUntoleratedTaint:
			if reason.TaintKey != "" && !isNodeLifecycleTaint(reason.TaintKey) {
				continue
			}
		}
		return false
	}
	return len(reasons) > 0
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
		// Succeeded pods are never endpoints — Kubernetes excludes them from
		// EndpointSlices. A Service whose selector happens to overlap completed
		// Job pods (common when a chart shares app labels) must not be counted as
		// "0/N ready" because of those benign, finished pods. Failed pods are
		// deliberately kept: a Service backed only by Failed pods is a real
		// no-ready-endpoints outage, not a benign empty selector.
		if pod.Status.Phase == corev1.PodSucceeded {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			out = append(out, pod)
		}
	}
	return out
}

// selectorMatchesSucceededPod reports whether the Service's selector matches at
// least one Succeeded pod. Used only on the zero-live-endpoints branch to tell a
// genuinely-orphaned selector from one matching only completed Job pods, so the
// message stays accurate.
func selectorMatchesSucceededPod(svc *corev1.Service, pods []*corev1.Pod) bool {
	if svc == nil || len(svc.Spec.Selector) == 0 {
		return false
	}
	selector := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodSucceeded && selector.Matches(labels.Set(pod.Labels)) {
			return true
		}
	}
	return false
}

// scaledToZeroBackingWorkload reports whether the Service's selector matches a
// Deployment or StatefulSet that is intentionally scaled to 0 replicas. Such a
// Service has no endpoints by design (a disabled managed component, a dormant
// environment), which is a different — benign — state than a selector that
// matches nothing in the cluster. Only called on the rare zero-endpoint branch,
// so the per-Service workload scan is not a hot path.
func scaledToZeroBackingWorkload(cache *ResourceCache, svc *corev1.Service) bool {
	if cache == nil || len(svc.Spec.Selector) == 0 {
		return false
	}
	sel := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
	if dl := cache.Deployments(); dl != nil {
		deps, _ := dl.Deployments(svc.Namespace).List(labels.Everything())
		for _, d := range deps {
			if d.Spec.Replicas != nil && *d.Spec.Replicas == 0 && sel.Matches(labels.Set(d.Spec.Template.Labels)) {
				return true
			}
		}
	}
	if sl := cache.StatefulSets(); sl != nil {
		stss, _ := sl.StatefulSets(svc.Namespace).List(labels.Everything())
		for _, s := range stss {
			if s.Spec.Replicas != nil && *s.Spec.Replicas == 0 && sel.Matches(labels.Set(s.Spec.Template.Labels)) {
				return true
			}
		}
	}
	return false
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

func deploymentReplicaFailure(dep *appsv1.Deployment) *appsv1.DeploymentCondition {
	if dep == nil {
		return nil
	}
	for i := range dep.Status.Conditions {
		cond := &dep.Status.Conditions[i]
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			return cond
		}
	}
	return nil
}

// deploymentNeverHealthy reports whether the Deployment's Available condition
// proves it was never healthy: Available=False with a lastTransitionTime still
// sitting at creation means it never once went True. This is flap-immune
// evidence — a condition that never transitioned can't have had its LTT reset —
// and outranks Progressing/ReplicaFailure timestamps, which re-trigger on
// retries. The 30s slop mirrors the classifier's condition-propagation slop.
func deploymentNeverHealthy(dep *appsv1.Deployment) bool {
	if dep == nil {
		return false
	}
	for i := range dep.Status.Conditions {
		cond := &dep.Status.Conditions[i]
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionFalse && !cond.LastTransitionTime.IsZero() {
			return cond.LastTransitionTime.Time.Sub(dep.CreationTimestamp.Time) < 30*time.Second
		}
	}
	return false
}

func deploymentProgressDeadlineExceeded(dep *appsv1.Deployment) *appsv1.DeploymentCondition {
	if dep == nil {
		return nil
	}
	for i := range dep.Status.Conditions {
		cond := &dep.Status.Conditions[i]
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
			return cond
		}
	}
	return nil
}

// DetectCAPIProblems scans Cluster API resources for problems.
// Checks both status.phase and the rich condition system (Ready, InfrastructureReady,
// ControlPlaneReady, BootstrapReady, NodeHealthy, TopologyReconciled).
// Returns nil if CAPI is not installed in the cluster.
