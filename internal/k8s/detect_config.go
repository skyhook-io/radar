package k8s

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/logsafe"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

func detectConfigProblems(cache *ResourceCache, namespace string, now time.Time) []Detection {
	var out []Detection
	if namespace == "" {
		out = append(out, detectSuspiciousCoreDNS(cache, now)...)
	}
	out = append(out, detectEnvServiceRefs(cache, namespace, now)...)
	out = append(out, detectDuplicateEnvVars(cache, namespace, now)...)
	return out
}

// DetectSuspiciousCoreDNS returns conservative CoreDNS Corefile findings for
// callers that are already in a DNS diagnostic context. General namespaced
// issue sweeps keep this out of their main list to avoid repeating one
// cluster-scoped finding for every namespace.
func DetectSuspiciousCoreDNS(cache *ResourceCache, now time.Time) []Detection {
	return detectSuspiciousCoreDNS(cache, now)
}

func detectSuspiciousCoreDNS(cache *ResourceCache, now time.Time) []Detection {
	if cache == nil || cache.ConfigMaps() == nil {
		return nil
	}
	cms, err := cache.ConfigMaps().ConfigMaps("kube-system").List(labels.Everything())
	if err != nil {
		logConfigListError("ConfigMap", "kube-system", err)
		return nil
	}
	var out []Detection
	for _, cm := range cms {
		if cm == nil || !strings.Contains(strings.ToLower(cm.Name), "coredns") {
			continue
		}
		corefile := cm.Data["Corefile"]
		if corefile == "" {
			continue
		}
		reason, ok := suspiciousCoreDNSReason(corefile)
		if !ok {
			continue
		}
		age := ageSeconds(now, cm.CreationTimestamp.Time)
		out = append(out, Detection{
			Kind:        "ConfigMap",
			Namespace:   cm.Namespace,
			Name:        cm.Name,
			Severity:    "warning",
			Reason:      reason,
			Message:     "CoreDNS Corefile contains a rule that can override Kubernetes service DNS responses.",
			Age:         FormatAge(time.Duration(age) * time.Second),
			AgeSeconds:  age,
			Fingerprint: "coredns:service-dns-override",
		})
	}
	return out
}

func suspiciousCoreDNSReason(corefile string) (string, bool) {
	low := strings.ToLower(corefile)
	touchesServiceDNS := strings.Contains(low, "svc.cluster.local") || strings.Contains(low, ".svc")
	if !touchesServiceDNS {
		return "", false
	}
	if strings.Contains(low, "template") && strings.Contains(low, "nxdomain") {
		return "CoreDNS NXDOMAIN override", true
	}
	if strings.Contains(low, "rewrite") {
		return "CoreDNS service DNS rewrite", true
	}
	return "", false
}

type envServiceRef struct {
	namespace string
	name      string
	host      string
	port      int32
	display   string
}

type envServiceWorkload struct {
	group     string
	kind      string
	namespace string
	name      string
	created   time.Time
	spec      corev1.PodSpec
	selector  *metav1.LabelSelector
	degraded  bool
}

// EnvServiceRefCheck is a conservative validation result for an environment
// variable that names a Service host:port.
type EnvServiceRefCheck struct {
	WorkloadGroup    string
	WorkloadKind     string
	Namespace        string
	WorkloadName     string
	Container        string
	EnvName          string
	Value            string
	ServiceNamespace string
	ServiceName      string
	ReferencedPort   int32
	Status           string
	ServicePorts     []string
	Message          string
	AgeSeconds       int64
}

type DuplicateEnvVarOccurrence struct {
	Position int
	Value    string
}

type DuplicateEnvVarCheck struct {
	WorkloadGroup     string
	WorkloadKind      string
	Namespace         string
	WorkloadName      string
	Container         string
	EnvName           string
	Occurrences       []DuplicateEnvVarOccurrence
	LastDeclaredValue string
	Message           string
	AgeSeconds        int64
}

const maxDuplicateEnvVarMessageOccurrences = 5

func detectDuplicateEnvVars(cache *ResourceCache, namespace string, now time.Time) []Detection {
	checks := findDuplicateEnvVarChecks(envServiceWorkloads(cache, namespace), now)
	out := make([]Detection, 0, len(checks))
	for _, check := range checks {
		out = append(out, Detection{
			Kind:        check.WorkloadKind,
			Group:       check.WorkloadGroup,
			Namespace:   check.Namespace,
			Name:        check.WorkloadName,
			Severity:    "warning",
			Reason:      "DuplicateEnvVar",
			Message:     check.Message,
			Age:         FormatAge(time.Duration(check.AgeSeconds) * time.Second),
			AgeSeconds:  check.AgeSeconds,
			Fingerprint: FormatDuplicateEnvVarFingerprint(check.Namespace, check.WorkloadName, check.Container, check.EnvName),
		})
	}
	return out
}

// FindDuplicateEnvVarsForObject returns duplicate environment-variable facts
// for a single workload object.
func FindDuplicateEnvVarsForObject(obj runtime.Object) []DuplicateEnvVarCheck {
	wl, ok := envServiceWorkloadForObject(obj)
	if !ok {
		return nil
	}
	return findDuplicateEnvVarChecks([]envServiceWorkload{wl}, time.Now())
}

func findDuplicateEnvVarChecks(workloads []envServiceWorkload, now time.Time) []DuplicateEnvVarCheck {
	var out []DuplicateEnvVarCheck
	for _, wl := range workloads {
		containers := make([]corev1.Container, 0, len(wl.spec.InitContainers)+len(wl.spec.Containers))
		containers = append(containers, wl.spec.InitContainers...)
		containers = append(containers, wl.spec.Containers...)
		for _, container := range containers {
			byName := make(map[string][]DuplicateEnvVarOccurrence, len(container.Env))
			for i, env := range container.Env {
				byName[env.Name] = append(byName[env.Name], DuplicateEnvVarOccurrence{
					Position: i + 1,
					Value:    envVarDisplayValue(env),
				})
			}
			for envName, occurrences := range byName {
				if len(occurrences) < 2 {
					continue
				}
				last := occurrences[len(occurrences)-1]
				out = append(out, DuplicateEnvVarCheck{
					WorkloadGroup:     wl.group,
					WorkloadKind:      wl.kind,
					Namespace:         wl.namespace,
					WorkloadName:      wl.name,
					Container:         container.Name,
					EnvName:           envName,
					Occurrences:       occurrences,
					LastDeclaredValue: last.Value,
					Message:           duplicateEnvVarMessage(container.Name, envName, occurrences),
					AgeSeconds:        ageSeconds(now, wl.created),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].WorkloadKind != out[j].WorkloadKind {
			return out[i].WorkloadKind < out[j].WorkloadKind
		}
		if out[i].WorkloadName != out[j].WorkloadName {
			return out[i].WorkloadName < out[j].WorkloadName
		}
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		return out[i].EnvName < out[j].EnvName
	})
	return out
}

func duplicateEnvVarMessage(container, envName string, occurrences []DuplicateEnvVarOccurrence) string {
	displayed := occurrences
	if len(displayed) > maxDuplicateEnvVarMessageOccurrences {
		displayed = displayed[:maxDuplicateEnvVarMessageOccurrences]
	}
	values := make([]string, 0, len(displayed)+1)
	for _, occurrence := range displayed {
		values = append(values, fmt.Sprintf("%d=%q", occurrence.Position, occurrence.Value))
	}
	if omitted := len(occurrences) - len(displayed); omitted > 0 {
		values = append(values, fmt.Sprintf("... and %d more", omitted))
	}
	last := occurrences[len(occurrences)-1]
	return fmt.Sprintf("Container %s defines env %s %d times at positions/values %s; the last definition %q typically takes effect.", container, envName, len(occurrences), strings.Join(values, ", "), last.Value)
}

func detectEnvServiceRefs(cache *ResourceCache, namespace string, now time.Time) []Detection {
	var out []Detection
	for _, check := range findEnvServiceRefChecks(cache, envServiceWorkloads(cache, namespace), now) {
		if !envServiceRefHasCausalEvidence(cache, check) {
			continue
		}
		out = append(out, Detection{
			Kind:        check.WorkloadKind,
			Group:       check.WorkloadGroup,
			Namespace:   check.Namespace,
			Name:        check.WorkloadName,
			Severity:    envServiceRefSeverity(check.Status),
			Reason:      envServiceRefReason(check.Status),
			Message:     check.Message,
			Age:         FormatAge(time.Duration(check.AgeSeconds) * time.Second),
			AgeSeconds:  check.AgeSeconds,
			Fingerprint: fmt.Sprintf("env-service-ref:%s:%s:%s:%s:%s:%d", check.Status, check.Container, check.EnvName, check.ServiceNamespace, check.ServiceName, check.ReferencedPort),
		})
	}
	return out
}

// FindEnvServiceRefChecks returns env-to-Service validation facts for workloads
// in namespace, including informational facts that are not promoted to Issues.
func FindEnvServiceRefChecks(cache *ResourceCache, namespace string) []EnvServiceRefCheck {
	return findEnvServiceRefChecks(cache, envServiceWorkloads(cache, namespace), time.Now())
}

// FindEnvServiceRefChecksForObject validates env-to-Service references for a
// single workload object, for resource-context diagnostic enrichment.
func FindEnvServiceRefChecksForObject(cache *ResourceCache, obj runtime.Object) []EnvServiceRefCheck {
	wl, ok := envServiceWorkloadForObject(obj)
	if !ok {
		return nil
	}
	return findEnvServiceRefChecks(cache, []envServiceWorkload{wl}, time.Now())
}

func findEnvServiceRefChecks(cache *ResourceCache, workloads []envServiceWorkload, now time.Time) []EnvServiceRefCheck {
	if cache == nil || cache.Services() == nil {
		return nil
	}
	var nodeHosts map[string]bool
	nodeHostsLoaded := false
	isNodeHost := func(host string) bool {
		if !nodeHostsLoaded {
			nodeHosts = envServiceNodeHosts(cache)
			nodeHostsLoaded = true
		}
		return nodeHosts[strings.ToLower(host)]
	}
	var out []EnvServiceRefCheck
	for _, wl := range workloads {
		containers := make([]corev1.Container, 0, len(wl.spec.InitContainers)+len(wl.spec.Containers))
		containers = append(containers, wl.spec.InitContainers...)
		containers = append(containers, wl.spec.Containers...)
		for _, c := range containers {
			portByPrefix := containerPortIndex(c.Env)
			for _, env := range c.Env {
				if !serviceRefEnvName(env.Name) || env.Value == "" {
					continue
				}
				value := env.Value
				// Split _HOST + _PORT pattern: FLAGD_HOST=flagd + FLAGD_PORT=8013
				// parseEnvServiceRef requires host:port, so synthesize when the host
				// value has no port of its own and a matching _PORT sibling exists.
				// Skip when the value already carries a port/scheme (contains ':'),
				// otherwise we'd produce host:port:port and drop a valid reference.
				if strings.HasSuffix(strings.ToUpper(env.Name), "_HOST") && !strings.Contains(value, ":") {
					if port, ok := portByPrefix[hostEnvPrefix(env.Name)]; ok {
						value = value + ":" + port
					}
				}
				ref, ok := parseEnvServiceRef(value, wl.namespace)
				if !ok {
					continue
				}
				if isNodeHost(ref.host) {
					continue
				}
				age := ageSeconds(now, wl.created)
				check := EnvServiceRefCheck{
					WorkloadGroup:    wl.group,
					WorkloadKind:     wl.kind,
					Namespace:        wl.namespace,
					WorkloadName:     wl.name,
					Container:        c.Name,
					EnvName:          env.Name,
					Value:            ref.display,
					ServiceNamespace: ref.namespace,
					ServiceName:      ref.name,
					ReferencedPort:   ref.port,
					AgeSeconds:       age,
				}
				if ref.namespace != wl.namespace {
					check.Status = "cross_namespace_unverified"
					check.Message = fmt.Sprintf("Env %s in container %s points to %s outside workload namespace %s; target Service was not verified.", env.Name, c.Name, ref.display, wl.namespace)
					out = append(out, check)
					continue
				}
				svc, err := cache.Services().Services(ref.namespace).Get(ref.name)
				if err != nil {
					if !apierrors.IsNotFound(err) {
						check.Status = "lookup_error"
						check.Message = fmt.Sprintf("Env %s in container %s points to %s, but Radar could not verify Service/%s in namespace %s: %v.", env.Name, c.Name, ref.display, ref.name, ref.namespace, err)
						out = append(out, check)
						continue
					}
					check.Status = "missing_service"
					check.Message = fmt.Sprintf("Env %s in container %s points to %s, but Service/%s does not exist in namespace %s.", env.Name, c.Name, ref.display, ref.name, ref.namespace)
					out = append(out, check)
					continue
				}
				if svc == nil {
					check.Status = "lookup_error"
					check.Message = fmt.Sprintf("Env %s in container %s points to %s, but Radar could not verify Service/%s in namespace %s.", env.Name, c.Name, ref.display, ref.name, ref.namespace)
					out = append(out, check)
					continue
				}
				if serviceExposesNumericPort(svc, ref.port) {
					continue
				}
				check.Status = "port_mismatch"
				check.ServicePorts = servicePortLabels(svc)
				check.Message = fmt.Sprintf("Env %s in container %s points to %s, but Service/%s exposes %s.", env.Name, c.Name, ref.display, ref.name, formatServicePorts(svc))
				out = append(out, check)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].WorkloadKind != out[j].WorkloadKind {
			return out[i].WorkloadKind < out[j].WorkloadKind
		}
		if out[i].WorkloadName != out[j].WorkloadName {
			return out[i].WorkloadName < out[j].WorkloadName
		}
		return out[i].EnvName < out[j].EnvName
	})
	return out
}

func envServiceRefReason(status string) string {
	switch status {
	case "missing_service":
		return "Missing referenced Service"
	case "port_mismatch":
		return "Service port mismatch"
	default:
		return "Invalid Service reference"
	}
}

func envServiceRefSeverity(status string) string {
	if status == "missing_service" {
		return "warning"
	}
	return "high"
}

func envServiceRefHasCausalEvidence(cache *ResourceCache, check EnvServiceRefCheck) bool {
	switch check.Status {
	case "missing_service":
		return true
	case "port_mismatch":
	default:
		return false
	}
	// Env refs are noisy as standalone facts in healthy apps. Promote them to
	// live Issues only when the owning workload is already degraded. Missing
	// Service is handled above because an explicit same-namespace reference
	// with no target is stronger evidence than a port mismatch.
	wl, ok := envServiceWorkloadByName(cache, check.WorkloadKind, check.Namespace, check.WorkloadName)
	return ok && wl.degraded
}

func envServiceWorkloadByName(cache *ResourceCache, kind, namespace, name string) (envServiceWorkload, bool) {
	if cache == nil {
		return envServiceWorkload{}, false
	}
	switch kind {
	case "Deployment":
		if l := cache.Deployments(); l != nil {
			if d, err := l.Deployments(namespace).Get(name); err == nil {
				return envServiceWorkloadForDeployment(d), true
			}
		}
	case "StatefulSet":
		if l := cache.StatefulSets(); l != nil {
			if ss, err := l.StatefulSets(namespace).Get(name); err == nil {
				return envServiceWorkloadForStatefulSet(ss), true
			}
		}
	case "DaemonSet":
		if l := cache.DaemonSets(); l != nil {
			if ds, err := l.DaemonSets(namespace).Get(name); err == nil {
				return envServiceWorkloadForDaemonSet(ds), true
			}
		}
	case "Job":
		if l := cache.Jobs(); l != nil {
			if j, err := l.Jobs(namespace).Get(name); err == nil {
				return envServiceWorkloadForJob(j), true
			}
		}
	case "CronJob":
		if l := cache.CronJobs(); l != nil {
			if cj, err := l.CronJobs(namespace).Get(name); err == nil {
				wl := envServiceWorkloadForCronJob(cj)
				wl.degraded = cronJobEnvServiceRefsDegraded(cache, cj)
				return wl, true
			}
		}
	}
	return envServiceWorkload{}, false
}

func envServiceWorkloadForObject(obj runtime.Object) (envServiceWorkload, bool) {
	switch v := obj.(type) {
	case *corev1.Pod:
		return envServiceWorkload{"", "Pod", v.Namespace, v.Name, v.CreationTimestamp.Time, v.Spec, nil, podEnvServiceRefsDegraded(v)}, true
	case *appsv1.Deployment:
		return envServiceWorkloadForDeployment(v), true
	case *appsv1.StatefulSet:
		return envServiceWorkloadForStatefulSet(v), true
	case *appsv1.DaemonSet:
		return envServiceWorkloadForDaemonSet(v), true
	case *batchv1.Job:
		return envServiceWorkloadForJob(v), true
	case *batchv1.CronJob:
		return envServiceWorkloadForCronJob(v), true
	default:
		return envServiceWorkload{}, false
	}
}

func envServiceWorkloadForDeployment(d *appsv1.Deployment) envServiceWorkload {
	return envServiceWorkload{"apps", "Deployment", d.Namespace, d.Name, d.CreationTimestamp.Time, d.Spec.Template.Spec, d.Spec.Selector, deploymentEnvServiceRefsDegraded(d)}
}

func envServiceWorkloadForStatefulSet(ss *appsv1.StatefulSet) envServiceWorkload {
	return envServiceWorkload{"apps", "StatefulSet", ss.Namespace, ss.Name, ss.CreationTimestamp.Time, ss.Spec.Template.Spec, ss.Spec.Selector, statefulSetEnvServiceRefsDegraded(ss)}
}

func envServiceWorkloadForDaemonSet(ds *appsv1.DaemonSet) envServiceWorkload {
	return envServiceWorkload{"apps", "DaemonSet", ds.Namespace, ds.Name, ds.CreationTimestamp.Time, ds.Spec.Template.Spec, ds.Spec.Selector, daemonSetEnvServiceRefsDegraded(ds)}
}

func envServiceWorkloadForJob(j *batchv1.Job) envServiceWorkload {
	return envServiceWorkload{"batch", "Job", j.Namespace, j.Name, j.CreationTimestamp.Time, j.Spec.Template.Spec, nil, jobEnvServiceRefsDegraded(j)}
}

func envServiceWorkloadForCronJob(cj *batchv1.CronJob) envServiceWorkload {
	return envServiceWorkload{"batch", "CronJob", cj.Namespace, cj.Name, cj.CreationTimestamp.Time, cj.Spec.JobTemplate.Spec.Template.Spec, nil, false}
}

func deploymentEnvServiceRefsDegraded(d *appsv1.Deployment) bool {
	if d == nil {
		return false
	}
	if d.Status.UnavailableReplicas > 0 {
		return true
	}
	for _, cond := range d.Status.Conditions {
		switch cond.Type {
		case appsv1.DeploymentAvailable:
			if cond.Status == corev1.ConditionFalse {
				return true
			}
		case appsv1.DeploymentProgressing:
			if cond.Status == corev1.ConditionFalse || cond.Reason == "ProgressDeadlineExceeded" {
				return true
			}
		case appsv1.DeploymentReplicaFailure:
			if cond.Status == corev1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

func statefulSetEnvServiceRefsDegraded(ss *appsv1.StatefulSet) bool {
	return ss != nil && ss.Status.Replicas > 0 && ss.Status.ReadyReplicas < ss.Status.Replicas
}

func daemonSetEnvServiceRefsDegraded(ds *appsv1.DaemonSet) bool {
	if ds == nil {
		return false
	}
	return ds.Status.NumberUnavailable > 0 || (ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberAvailable < ds.Status.DesiredNumberScheduled)
}

func jobEnvServiceRefsDegraded(j *batchv1.Job) bool {
	if j == nil {
		return false
	}
	if j.Status.Failed > 0 {
		return true
	}
	for _, cond := range j.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func cronJobEnvServiceRefsDegraded(cache *ResourceCache, cj *batchv1.CronJob) bool {
	if cache == nil || cj == nil || cache.Jobs() == nil {
		return false
	}
	jobs, err := cache.Jobs().Jobs(cj.Namespace).List(labels.Everything())
	if err != nil {
		logConfigListError("Job", cj.Namespace, err)
		return false
	}
	for _, job := range jobs {
		controller := metav1.GetControllerOf(job)
		if controller == nil || controller.Kind != "CronJob" || controller.Name != cj.Name {
			continue
		}
		if jobEnvServiceRefsDegraded(job) {
			return true
		}
	}
	return false
}

func podEnvServiceRefsDegraded(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodUnknown {
		return true
	}
	statuses := append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, cs := range statuses {
		if !cs.Ready || cs.State.Waiting != nil || cs.State.Terminated != nil {
			return true
		}
	}
	return false
}

func envServiceWorkloads(cache *ResourceCache, namespace string) []envServiceWorkload {
	if cache == nil {
		return nil
	}
	var out []envServiceWorkload
	if l := cache.Deployments(); l != nil {
		var items []*appsv1.Deployment
		var err error
		if namespace == "" {
			items, err = l.List(labels.Everything())
		} else {
			items, err = l.Deployments(namespace).List(labels.Everything())
		}
		if err != nil {
			logConfigListError("Deployment", namespace, err)
		}
		for _, d := range items {
			out = append(out, envServiceWorkloadForDeployment(d))
		}
	}
	if l := cache.StatefulSets(); l != nil {
		var items []*appsv1.StatefulSet
		var err error
		if namespace == "" {
			items, err = l.List(labels.Everything())
		} else {
			items, err = l.StatefulSets(namespace).List(labels.Everything())
		}
		if err != nil {
			logConfigListError("StatefulSet", namespace, err)
		}
		for _, ss := range items {
			out = append(out, envServiceWorkloadForStatefulSet(ss))
		}
	}
	if l := cache.DaemonSets(); l != nil {
		var items []*appsv1.DaemonSet
		var err error
		if namespace == "" {
			items, err = l.List(labels.Everything())
		} else {
			items, err = l.DaemonSets(namespace).List(labels.Everything())
		}
		if err != nil {
			logConfigListError("DaemonSet", namespace, err)
		}
		for _, ds := range items {
			out = append(out, envServiceWorkloadForDaemonSet(ds))
		}
	}
	if l := cache.Jobs(); l != nil {
		var items []*batchv1.Job
		var err error
		if namespace == "" {
			items, err = l.List(labels.Everything())
		} else {
			items, err = l.Jobs(namespace).List(labels.Everything())
		}
		if err != nil {
			logConfigListError("Job", namespace, err)
		}
		for _, j := range items {
			out = append(out, envServiceWorkloadForJob(j))
		}
	}
	if l := cache.CronJobs(); l != nil {
		var items []*batchv1.CronJob
		var err error
		if namespace == "" {
			items, err = l.List(labels.Everything())
		} else {
			items, err = l.CronJobs(namespace).List(labels.Everything())
		}
		if err != nil {
			logConfigListError("CronJob", namespace, err)
		}
		for _, cj := range items {
			out = append(out, envServiceWorkloadForCronJob(cj))
		}
	}
	return out
}

func logConfigListError(kind, namespace string, err error) {
	if err == nil {
		return
	}
	scope := "all namespaces"
	if namespace != "" {
		scope = "namespace " + namespace
	}
	log.Printf("[config-detect] failed to list %s in %s: %s", logsafe.Sanitize(kind), logsafe.Sanitize(scope), logsafe.Sanitize(err.Error()))
}

func serviceRefEnvName(name string) bool {
	n := strings.ToUpper(name)
	return strings.HasSuffix(n, "_ADDR") || strings.HasSuffix(n, "_URL") || strings.HasSuffix(n, "_HOST") || strings.HasSuffix(n, "_ENDPOINT")
}

// hostEnvPrefix returns the prefix before the "_HOST" suffix, preserving original case.
// e.g. "FLAGD_HOST" → "FLAGD", "MyService_HOST" → "MyService"
func hostEnvPrefix(name string) string {
	return name[:len(name)-len("_HOST")]
}

// containerPortIndex builds a map from env-name-prefix → port-string for all
// _PORT vars in a container whose value is a valid port number. Used to pair
// FOO_HOST + FOO_PORT into a single host:port reference.
func containerPortIndex(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string)
	for _, e := range envs {
		if e.Value == "" {
			continue
		}
		upper := strings.ToUpper(e.Name)
		if !strings.HasSuffix(upper, "_PORT") {
			continue
		}
		port, err := strconv.ParseInt(strings.TrimSpace(e.Value), 10, 32)
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		prefix := e.Name[:len(e.Name)-len("_PORT")]
		m[prefix] = e.Value
	}
	return m
}

func parseEnvServiceRef(value, defaultNamespace string) (envServiceRef, bool) {
	hostPort := strings.Trim(strings.TrimSpace(value), `"'`)
	if hostPort == "" || strings.ContainsAny(hostPort, " \t\n,;") {
		return envServiceRef{}, false
	}
	if u, err := url.Parse(hostPort); err == nil && u.Scheme != "" && u.Host != "" {
		hostPort = u.Host
	} else if idx := strings.IndexAny(hostPort, "/?#"); idx >= 0 {
		hostPort = hostPort[:idx]
	}

	host, portText, ok := splitHostPort(hostPort)
	if !ok {
		return envServiceRef{}, false
	}
	port64, err := strconv.ParseInt(portText, 10, 32)
	if err != nil || port64 <= 0 || port64 > 65535 {
		return envServiceRef{}, false
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if net.ParseIP(host) != nil {
		return envServiceRef{}, false
	}
	// localhost is a loopback name, never a cluster Service — treating it as a
	// missing Service produces noise (e.g. sidecars/exporters that POST to
	// localhost) that drowns the real missing-Service findings.
	if strings.EqualFold(host, "localhost") {
		return envServiceRef{}, false
	}

	parts := strings.Split(host, ".")
	ref := envServiceRef{namespace: defaultNamespace, host: host, port: int32(port64), display: fmt.Sprintf("%s:%d", host, port64)}
	switch {
	case len(parts) == 1:
		ref.name = parts[0]
	case len(parts) == 3 && parts[2] == "svc":
		ref.name = parts[0]
		ref.namespace = parts[1]
	case len(parts) == 5 && parts[2] == "svc" && parts[3] == "cluster" && parts[4] == "local":
		ref.name = parts[0]
		ref.namespace = parts[1]
	default:
		return envServiceRef{}, false
	}
	if !isDNSLabel(ref.name) || !isDNSLabel(ref.namespace) {
		return envServiceRef{}, false
	}
	return ref, true
}

func envServiceNodeHosts(cache *ResourceCache) map[string]bool {
	out := map[string]bool{}
	if cache == nil || cache.Nodes() == nil {
		return out
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		logConfigListError("Node", "", err)
		return out
	}
	add := func(value string) {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if value != "" {
			out[value] = true
		}
	}
	for _, node := range nodes {
		add(node.Name)
		for _, addr := range node.Status.Addresses {
			add(addr.Address)
		}
	}
	return out
}

func splitHostPort(value string) (string, string, bool) {
	if host, port, err := net.SplitHostPort(value); err == nil {
		return host, port, true
	}
	if strings.Count(value, ":") != 1 {
		return "", "", false
	}
	i := strings.LastIndex(value, ":")
	if i <= 0 || i == len(value)-1 {
		return "", "", false
	}
	return value[:i], value[i+1:], true
}

func isDNSLabel(s string) bool {
	return len(s) > 0 && len(s) <= 63 && dnsLabelPattern.MatchString(s)
}

func serviceExposesNumericPort(svc *corev1.Service, port int32) bool {
	for _, p := range svc.Spec.Ports {
		if p.Port == port {
			return true
		}
	}
	return false
}

func servicePortLabels(svc *corev1.Service) []string {
	if svc == nil || len(svc.Spec.Ports) == 0 {
		return nil
	}
	ports := make([]string, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		label := strconv.Itoa(int(p.Port))
		if p.Name != "" {
			label = fmt.Sprintf("%s/%s", p.Name, label)
		}
		ports = append(ports, label)
	}
	sort.Strings(ports)
	return ports
}

func formatServicePorts(svc *corev1.Service) string {
	ports := servicePortLabels(svc)
	if len(ports) == 0 {
		return "no ports"
	}
	return "ports [" + strings.Join(ports, ", ") + "]"
}

func ageSeconds(now, created time.Time) int64 {
	if created.IsZero() {
		return 0
	}
	if d := now.Sub(created); d > 0 {
		return int64(d.Seconds())
	}
	return 0
}
