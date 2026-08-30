package context

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/hpadiag"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// ResourceSummary is the typed output for Summary-level minification.
// Typed struct ensures no extra fields can leak.
type ResourceSummary struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Status is the DISPLAY state, computed the way `kubectl get` computes its
	// STATUS column: what is true about this object right now.
	Status string `json:"status,omitempty"`
	Ready  string `json:"ready,omitempty"`
	// Issue is the diagnostic hint — a reason worth looking at. It is
	// deliberately NOT the same concept as Status and is allowed to be broader:
	// it surfaces recent history too, so a pod that OOMKilled and recovered
	// reports Status "Running" with Issue "OOMKilled". Read it as "notable,
	// worth a look", never as "currently broken" — the health level
	// (summaryContext.health, from pkg/health) is the authority on that.
	Issue string `json:"issue,omitempty"`
	Age   string `json:"age,omitempty"`
	// Terminating signals that metadata.deletionTimestamp is set on the
	// resource. AI agents need this signal to avoid suggesting
	// mutating actions that will fail (e.g. "run kubectl rollout restart"
	// on a Pod that's being torn down) and to correctly diagnose "why is
	// this stuck" scenarios where the resource is a finalizer-blocked
	// zombie. Pruned when false.
	Terminating bool `json:"terminating,omitempty"`
	// Finalizers names the keys blocking deletion when Terminating is
	// true. The owning controller is responsible for clearing each key
	// during cleanup; if it doesn't, the resource lingers as a zombie.
	// Pruned when empty.
	Finalizers []string `json:"finalizers,omitempty"`

	// Type-specific fields (only populated when relevant)
	Image     string   `json:"image,omitempty"`
	Ports     string   `json:"ports,omitempty"`
	Schedule  string   `json:"schedule,omitempty"`
	Type      string   `json:"type,omitempty"` // Service type, Secret type
	Selector  string   `json:"selector,omitempty"`
	ClusterIP string   `json:"clusterIP,omitempty"`
	Hosts     []string `json:"hosts,omitempty"`
	Restarts  int32    `json:"restarts,omitempty"`
	// LastTerminatedReason is why the most recently terminated container
	// exited (OOMKilled, Error, ...). It is HISTORY, not an active fault: a
	// pod OOMKilled an hour ago that has been Ready since is healthy, and
	// Status says so. Carried separately so an agent can tell "crashing now"
	// from "crashed once, weeks ago" — a distinction a bare restart count
	// destroys. Mirrors what `kubectl describe` shows as "Last State".
	LastTerminatedReason string `json:"lastTerminatedReason,omitempty"`
	// LastRestartedAge is how long ago that termination happened, mirroring
	// kubectl's "2 (5m ago)" RESTARTS column. Empty when nothing restarted.
	LastRestartedAge string   `json:"lastRestartedAge,omitempty"`
	Node             string   `json:"node,omitempty"`
	Strategy         string   `json:"strategy,omitempty"`
	Completions      string   `json:"completions,omitempty"`
	Duration         string   `json:"duration,omitempty"`
	Suspended        *bool    `json:"suspended,omitempty"`
	Unschedulable    *bool    `json:"unschedulable,omitempty"`
	Active           int      `json:"active,omitempty"`
	Target           string   `json:"target,omitempty"`
	MinReplicas      *int32   `json:"minReplicas,omitempty"`
	MaxReplicas      int32    `json:"maxReplicas,omitempty"`
	Current          int32    `json:"current,omitempty"`
	Desired          int32    `json:"desired,omitempty"`
	Roles            []string `json:"roles,omitempty"`
	Version          string   `json:"version,omitempty"`
	Pressures        []string `json:"pressures,omitempty"`
	Keys             []string `json:"keys,omitempty"`
	StorageClass     string   `json:"storageClass,omitempty"`
	Capacity         string   `json:"capacity,omitempty"`
	AccessModes      []string `json:"accessModes,omitempty"`
	Owner            string   `json:"owner,omitempty"`

	// SummaryContext is the per-row enrichment attached by AI-facing list
	// surfaces (REST /api/ai/resources/{kind}, MCP list_resources, search
	// hits). Populated by handlers post-minify via resourcecontext.BuildSummary;
	// nil when the caller opted out (?context=none) or when no fields apply.
	// Type is resourcecontext.ResourceSummaryContext — the field name keeps
	// the shorter "SummaryContext" form to match the wire JSON tag.
	SummaryContext *resourcecontext.ResourceSummaryContext `json:"summaryContext,omitempty"`
}

// summarize dispatches to the appropriate per-type extractor and then
// fills in lifecycle fields (Terminating, Finalizers) shared across all
// kinds. Filling these once at the dispatch boundary avoids touching
// every per-type summarizer; all K8s resource types implement
// metav1.Object, so the cast is universal.
func summarize(obj runtime.Object) (*ResourceSummary, error) {
	// Typed-nil-through-interface trap: a (*v1.Pod)(nil) assigned to
	// runtime.Object compares != nil but calling methods panics. Catch
	// it once at the boundary so per-type summarizers and
	// applyLifecycleFields can assume non-nil concrete values.
	if isNilObject(obj) {
		return &ResourceSummary{Kind: "Unknown"}, nil
	}
	var s *ResourceSummary
	switch o := obj.(type) {
	case *corev1.Pod:
		s = summarizePod(o)
	case *appsv1.Deployment:
		s = summarizeDeployment(o)
	case *appsv1.StatefulSet:
		s = summarizeStatefulSet(o)
	case *appsv1.DaemonSet:
		s = summarizeDaemonSet(o)
	case *corev1.Service:
		s = summarizeService(o)
	case *networkingv1.Ingress:
		s = summarizeIngress(o)
	case *batchv1.Job:
		s = summarizeJob(o)
	case *batchv1.CronJob:
		s = summarizeCronJob(o)
	case *autoscalingv2.HorizontalPodAutoscaler:
		s = summarizeHPA(o)
	case *corev1.Node:
		s = summarizeNode(o)
	case *corev1.ConfigMap:
		s = summarizeConfigMap(o)
	case *corev1.Secret:
		s = summarizeSecret(o)
	case *corev1.PersistentVolumeClaim:
		s = summarizePVC(o)
	case *appsv1.ReplicaSet:
		s = summarizeReplicaSet(o)
	case *corev1.Namespace:
		s = summarizeNamespace(o)
	default:
		// Generic fallback is better than erroring — a single unsupported
		// kind would otherwise break the whole MCP list_resources response.
		// Add an explicit case above when richer per-kind output is worth
		// maintaining.
		s = summarizeGeneric(obj)
	}
	applyLifecycleFields(s, obj)
	return s, nil
}

// summarizeGeneric is the default summarizer for kinds without a hand-written case.
func summarizeGeneric(obj runtime.Object) *ResourceSummary {
	if isNilObject(obj) {
		return &ResourceSummary{Kind: "Unknown"}
	}
	s := &ResourceSummary{}
	if kinder, ok := obj.(interface{ GetObjectKind() schema.ObjectKind }); ok {
		s.Kind = kinder.GetObjectKind().GroupVersionKind().Kind
	}
	if s.Kind == "" {
		// TypeMeta isn't populated on informer-cached objects. Fall back to
		// the Go type name with the package qualifier stripped (e.g.
		// "*v1.NetworkPolicy" → "NetworkPolicy").
		typeName := fmt.Sprintf("%T", obj)
		if i := strings.LastIndex(typeName, "."); i >= 0 {
			typeName = typeName[i+1:]
		}
		s.Kind = typeName
	}
	mo, ok := obj.(interface {
		GetName() string
		GetNamespace() string
		GetCreationTimestamp() metav1.Time
	})
	if !ok {
		return s
	}
	s.Name = mo.GetName()
	s.Namespace = mo.GetNamespace()
	s.Age = age(mo.GetCreationTimestamp().Time)
	return s
}

// isNilObject handles Go's typed-nil-in-interface trap: a (*v1.Pod)(nil)
// assigned to a runtime.Object interface compares != nil but calling
// methods on it panics. Reflection is the only way to detect this case.
func isNilObject(obj runtime.Object) bool {
	if obj == nil {
		return true
	}
	v := reflect.ValueOf(obj)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// applyLifecycleFields populates Terminating + Finalizers on a
// ResourceSummary by reading metadata via the metav1.Object interface
// that every K8s typed object implements. No-ops on a nil summary or
// a non-metav1 object so it's safe to call from any caller.
func applyLifecycleFields(s *ResourceSummary, obj runtime.Object) {
	if s == nil {
		return
	}
	mo, ok := obj.(interface {
		GetDeletionTimestamp() *metav1.Time
		GetFinalizers() []string
	})
	if !ok {
		return
	}
	if dt := mo.GetDeletionTimestamp(); dt != nil && !dt.IsZero() {
		s.Terminating = true
		s.Finalizers = mo.GetFinalizers()
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

	// STATUS / READY / RESTARTS come from the kubectl-equivalent computation.
	// Status is a DISPLAY string describing the pod's current state; Issue
	// below is the separate diagnostic signal. Keeping them apart matters:
	// routing display through getPodIssue would turn every display refinement
	// (Completed, SchedulingGated) into a serialized "issue", which is a
	// different, load-bearing concept.
	d := computePodDisplay(pod)
	s.Status = d.Status
	s.Ready = d.Ready
	s.Restarts = d.Restarts
	// Termination history, deliberately NOT folded into Status: a pod
	// OOMKilled an hour ago that has been Ready since is healthy now, and
	// Status says "Running". The history is still worth carrying — a bare
	// restart count can't distinguish "crashing right now" from "crashed once,
	// weeks ago". This mirrors kubectl, which keeps STATUS current and shows
	// the reason under "Last State" in describe.
	// Only when something actually restarted, which is the rule kubectl applies
	// to its own RESTARTS column ("7 (16h ago)" for a non-zero count, a bare
	// "0" otherwise). Termination history without a restart would describe a
	// container that ended and stayed ended — the pod's current state already
	// says that, so the history adds nothing.
	//
	// A 4,355-resource sweep across five clusters found no row violating this,
	// so the guard is an invariant made explicit rather than a fix for observed
	// breakage. Note "Completed" IS legitimate history here: redis-master-0 in
	// that sweep had 7 restarts each ending cleanly, which kubectl reports too —
	// a service quietly exiting and being restarted is worth seeing.
	if d.Restarts > 0 {
		s.LastTerminatedReason = d.LastTerminatedBy
		if !d.LastRestartedAt.IsZero() {
			s.LastRestartedAge = age(d.LastRestartedAt)
		}
	}

	// Diagnostic issue — independent of the display status above.
	s.Issue = getPodIssue(pod)

	return s
}

func summarizeDeployment(dep *appsv1.Deployment) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Deployment",
		Name:      dep.Name,
		Namespace: dep.Namespace,
		Ready:     fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, specReplicas(dep.Spec.Replicas)),
		Strategy:  string(dep.Spec.Strategy.Type),
		Age:       age(dep.CreationTimestamp.Time),
	}

	// Denominate by SPEC, not status. status.Replicas is what the controller
	// has created so far; on a 3->10 scale-up it still reads 3 until the
	// controller catches up, so a status-denominated row shows "3/3 Running"
	// for a workload that is 7 replicas short of its target. kubectl uses
	// spec.Replicas for exactly this reason.
	desired := specReplicas(dep.Spec.Replicas)
	switch {
	case desired == 0:
		s.Status = "Scaled to 0"
	case dep.Status.ReadyReplicas == desired:
		s.Status = "Running"
	default:
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

	// Status from conditions. Terminal conditions win over spec intent: a Job
	// that failed and was then suspended is still a failure, so Complete /
	// Failed are checked before Suspended — mirroring health.jobVerdict
	// (pkg/health/workload.go:63) so the two surfaces agree.
	s.Status = "Running"
	terminal := false
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			s.Status, terminal = "Complete", true
		} else if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			s.Status, terminal = "Failed", true
			// Preserve WHY it failed (BackoffLimitExceeded / DeadlineExceeded);
			// "Failed" alone sends a reader back to the API for the reason.
			if c.Reason != "" {
				s.Issue = c.Reason
			}
		}
	}

	// Suspension is operator intent, not a fault — surfaced as its own field
	// rather than folded into Status. CronJob already did this
	// (s.Suspended below); Job did not, so a deliberately paused Job read as
	// "Running" here while health.jobVerdict correctly called it Suspended.
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		s.Suspended = job.Spec.Suspend
		if !terminal {
			s.Status = "Suspended"
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
		// Staleness is relative to the schedule's cadence, not a flat day: a
		// weekly job that ran on schedule 25h ago is healthy, not stale.
		threshold := cronsched.StaleThreshold(cj.Spec.Schedule)
		if cj.Status.LastScheduleTime != nil && time.Since(cj.Status.LastScheduleTime.Time) > threshold {
			s.Issue = "no recent runs"
		} else if cj.Status.LastScheduleTime == nil && time.Since(cj.CreationTimestamp.Time) > threshold {
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

	if diagnosis := hpadiag.Analyze(hpa); diagnosis != nil {
		switch diagnosis.State {
		case hpadiag.StateLimitedMax:
			s.Issue = "maxed"
		case hpadiag.StateMetricsUnavailable, hpadiag.StateUnableToScale:
			s.Issue = diagnosis.Summary
		}
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
		// Spec-denominated, same reasoning as summarizeDeployment.
		Ready: fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, specReplicas(rs.Spec.Replicas)),
		Age:   age(rs.CreationTimestamp.Time),
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

// podDisplay is the `kubectl get pods` view of a pod — its STATUS, READY and
// RESTARTS columns. The three are computed together because upstream derives
// all of them from a single walk over the container statuses; splitting them
// would duplicate that walk and let them drift apart.
//
// WHY THIS EXISTS: pod.Status.Phase alone is nearly useless as a status. It has
// five values, and a Job pod whose main container finished while a sidecar
// keeps running reports phase Running forever. Radar used to emit the bare
// phase here, so such a pod read "Running" with no hint anything was wrong,
// while `kubectl get pods` read "NotReady". This is a deliberate port of
// upstream printPod (k8s.io/kubernetes/pkg/printers/internalversion).
//
// Two deliberate divergences from upstream, both retaining strictly more
// information than upstream does:
//   - No Terminating override. kubectl replaces STATUS wholesale with
//     "Terminating" when deletionTimestamp is set; radar carries Terminating
//     and Finalizers as dedicated fields, so overriding would DISCARD the
//     underlying state during every routine rollout.
//   - No NodeUnreachable -> "Unknown" branch. It is gated on a legacy
//     pod.Status.Reason that modern kubelets rarely set; radar's health
//     classifier keys on phase Unknown instead (pkg/health/pod.go:75).
type podDisplay struct {
	Status           string
	Ready            string
	Restarts         int32
	LastRestartedAt  time.Time
	LastTerminatedBy string
}

// isRestartableInitContainer reports whether an init container is a native
// (KEP-753) sidecar — restartPolicy: Always — which runs alongside the main
// containers rather than gating them.
func isRestartableInitContainer(c *corev1.Container) bool {
	return c != nil && c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

func computePodDisplay(pod *corev1.Pod) podDisplay {
	var d podDisplay

	// Native sidecars count toward the READY denominator: they are real
	// containers the pod depends on, not setup steps that finish.
	totalContainers := len(pod.Spec.Containers)
	initSpecs := make(map[string]*corev1.Container, len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		initSpecs[c.Name] = c
		if isRestartableInitContainer(c) {
			totalContainers++
		}
	}

	reason := string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason // Evicted, NodeAffinity, ...
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Reason == corev1.PodReasonSchedulingGated {
			reason = corev1.PodReasonSchedulingGated
		}
	}

	readyContainers := 0
	var restarts, sidecarRestarts int32
	var lastRestart, lastSidecarRestart time.Time
	var lastReason, lastSidecarReason string

	noteTermination := func(t *corev1.ContainerStateTerminated, at *time.Time, who *string) {
		if t == nil {
			return
		}
		// A kubelet always stamps FinishedAt, but an unstamped termination
		// still tells us WHY the container died — keep the reason and simply
		// report no age for it, rather than dropping the fact entirely.
		if t.FinishedAt.IsZero() {
			if *who == "" {
				*who = t.Reason
			}
			return
		}
		if at.IsZero() || t.FinishedAt.Time.After(*at) {
			*at, *who = t.FinishedAt.Time, t.Reason
		}
	}

	initializing := false
	for i := range pod.Status.InitContainerStatuses {
		cs := pod.Status.InitContainerStatuses[i]
		spec := initSpecs[cs.Name]
		restarts += cs.RestartCount
		noteTermination(cs.LastTerminationState.Terminated, &lastRestart, &lastReason)
		if isRestartableInitContainer(spec) {
			sidecarRestarts += cs.RestartCount
			noteTermination(cs.LastTerminationState.Terminated, &lastSidecarRestart, &lastSidecarReason)
		}

		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0:
			continue // a completed init step: move on
		case isRestartableInitContainer(spec) && cs.Started != nil && *cs.Started:
			if cs.Ready {
				readyContainers++
			}
			continue // native sidecar: running alongside, doesn't gate init
		case cs.State.Terminated != nil:
			switch {
			case cs.State.Terminated.Reason != "":
				reason = "Init:" + cs.State.Terminated.Reason
			case cs.State.Terminated.Signal != 0:
				reason = fmt.Sprintf("Init:Signal:%d", cs.State.Terminated.Signal)
			default:
				reason = fmt.Sprintf("Init:ExitCode:%d", cs.State.Terminated.ExitCode)
			}
			initializing = true
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + cs.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
			initializing = true
		}
		break // upstream stops at the first init container that isn't done
	}

	if !initializing || isPodInitialized(pod) {
		// Init restarts stop counting once initialization is done; only native
		// sidecars keep contributing, matching upstream.
		restarts, lastRestart, lastReason = sidecarRestarts, lastSidecarRestart, lastSidecarReason
		hasRunning := false
		errorReason := ""
		// Reverse order is upstream's: the FIRST container in spec order wins
		// the reason, because later iterations overwrite earlier ones.
		for i := len(pod.Status.ContainerStatuses) - 1; i >= 0; i-- {
			cs := pod.Status.ContainerStatuses[i]
			restarts += cs.RestartCount
			noteTermination(cs.LastTerminationState.Terminated, &lastRestart, &lastReason)

			switch {
			case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
				reason = cs.State.Waiting.Reason
			case cs.State.Terminated != nil:
				switch {
				case cs.State.Terminated.Reason != "":
					reason = cs.State.Terminated.Reason
				case cs.State.Terminated.Signal != 0:
					reason = fmt.Sprintf("Signal:%d", cs.State.Terminated.Signal)
				default:
					reason = fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode)
				}
				if cs.State.Terminated.ExitCode != 0 {
					errorReason = reason
				}
			case cs.Ready && cs.State.Running != nil:
				hasRunning = true
				readyContainers++
			}
		}

		// THE CASE THAT MATTERS: a container reported Completed while another
		// is still running. If the pod is Ready it is genuinely fine; if not,
		// something finished and left the pod wedged — a Job whose sidecar
		// never exits is the canonical example.
		if reason == "Completed" {
			switch {
			case hasRunning && hasPodReadyCondition(pod):
				reason = "Running"
			case errorReason != "":
				reason = errorReason
			case hasRunning:
				reason = "NotReady"
			}
		}
	}

	d.Status = reason
	d.Restarts = restarts
	d.LastRestartedAt = lastRestart
	d.LastTerminatedBy = lastReason
	if totalContainers > 0 {
		d.Ready = fmt.Sprintf("%d/%d", readyContainers, totalContainers)
	}
	return d
}

// specReplicas resolves a workload's desired replica count. A nil pointer
// means "unset", which Kubernetes defaults to 1 — not 0.
func specReplicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func hasPodReadyCondition(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func isPodInitialized(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodInitialized && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
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
