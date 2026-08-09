package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	envHistoryLookback                  = time.Hour
	envHistoryContextMaxLookback        = 24 * time.Hour
	staleSecretEnvRolloutEvidenceWindow = 10 * time.Minute
	maxSecretDataHistoryEvents          = 1000
	maxStaleSecretEnvChecksPerContainer = 5
	// A mass Secret rotation coinciding with a rollout can leave many pods
	// simultaneously not-Ready with in-window key changes; a sweep must not
	// turn that into an unbounded warning burst. The bound is per detector
	// invocation, which is per namespace: the cluster-wide sweep (namespace
	// "") is a single invocation and thus globally bounded, while an
	// N-namespace query is bounded at N times this cap.
	maxStaleSecretEnvDetectionsPerNamespace = 20
)

type RemovedServiceEnvCheck struct {
	WorkloadGroup    string
	WorkloadKind     string
	Namespace        string
	WorkloadName     string
	Container        string
	EnvName          string
	OldValue         string
	ServiceNamespace string
	ServiceName      string
	ReferencedPort   int32
	RemovedAt        time.Time
	Message          string
}

type StaleSecretEnvCheck struct {
	Namespace          string
	PodName            string
	Container          string
	EnvName            string
	ReferenceKind      string
	EvidenceSource     string
	Prefix             string
	SecretName         string
	Key                string
	AffectedPods       int
	ContainerStartedAt time.Time
	SecretChangedAt    time.Time
	Message            string
}

const (
	staleSecretEvidenceObservedChange = "observed_change"
	staleSecretEvidenceManagedFields  = "managed_fields_mtime"
)

// FindRemovedServiceEnvChecksForObject returns recent env-removal facts for a
// workload. It deliberately consumes only the already-redacted timeline diff;
// sensitive env names therefore under-report instead of reaching for raw history.
// These stay as per-object facts because an intentional removal has no cheap,
// reliable active-failure signal that would justify a triage issue.
func FindRemovedServiceEnvChecksForObject(ctx context.Context, cache *ResourceCache, obj runtime.Object) []RemovedServiceEnvCheck {
	wl, ok := envServiceWorkloadForObject(obj)
	if !ok || !supportsEnvRemovalHistory(wl.kind) {
		return nil
	}
	events := queryEnvHistory(ctx, timeline.QueryOptions{
		Namespaces:     []string{wl.namespace},
		Kinds:          []string{wl.kind},
		Names:          []string{wl.name},
		Since:          time.Now().Add(-envHistoryLookback),
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     []timeline.EventType{timeline.EventTypeUpdate},
		ClusterContext: ActiveClusterContext(),
		Limit:          1000,
	})
	var uid string
	if object, ok := obj.(metav1.Object); ok {
		uid = string(object.GetUID())
	}
	return findRemovedServiceEnvChecks(cache, wl, uid, events)
}

func supportsEnvRemovalHistory(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return true
	default:
		return false
	}
}

func findRemovedServiceEnvChecks(cache *ResourceCache, wl envServiceWorkload, uid string, events []timeline.TimelineEvent) []RemovedServiceEnvCheck {
	if cache == nil || cache.Services() == nil {
		return nil
	}
	sortTimelineEventsNewestFirst(events)
	seenPaths := make(map[string]bool)
	var out []RemovedServiceEnvCheck
	for _, event := range events {
		if event.Kind != wl.kind || event.Namespace != wl.namespace || event.Name != wl.name ||
			event.Source != timeline.SourceInformer || event.EventType != timeline.EventTypeUpdate || event.Diff == nil {
			continue
		}
		if uid != "" && event.UID != "" && event.UID != uid {
			continue
		}
		for _, change := range event.Diff.Fields {
			container, envName, ok := parseWorkloadEnvPath(change.Path)
			if !ok || seenPaths[change.Path] {
				continue
			}
			seenPaths[change.Path] = true
			if change.NewValue != nil || workloadSpecHasEnv(wl.spec, container, envName) {
				continue
			}
			oldValue, ok := change.OldValue.(string)
			if !ok || oldValue == "" || oldValue == "[REDACTED]" {
				continue
			}
			ref, ok := parseEnvServiceRef(oldValue, wl.namespace)
			if !ok {
				continue
			}
			svc, err := cache.Services().Services(ref.namespace).Get(ref.name)
			if err != nil || svc == nil {
				continue
			}
			out = append(out, RemovedServiceEnvCheck{
				WorkloadGroup:    wl.group,
				WorkloadKind:     wl.kind,
				Namespace:        wl.namespace,
				WorkloadName:     wl.name,
				Container:        container,
				EnvName:          envName,
				OldValue:         oldValue,
				ServiceNamespace: ref.namespace,
				ServiceName:      ref.name,
				ReferencedPort:   ref.port,
				RemovedAt:        event.Timestamp,
				Message:          fmt.Sprintf("Env %s in container %s, which referenced Service/%s at %s, was removed at %s.", envName, container, ref.name, ref.display, event.Timestamp.UTC().Format(time.RFC3339)),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		return out[i].EnvName < out[j].EnvName
	})
	return out
}

func workloadSpecHasEnv(spec corev1.PodSpec, containerName, envName string) bool {
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, container := range containers {
			if container.Name != containerName {
				continue
			}
			for _, env := range container.Env {
				if env.Name == envName {
					return true
				}
			}
		}
	}
	return false
}

func parseWorkloadEnvPath(path string) (container, envName string, ok bool) {
	const (
		containersPrefix     = "spec.template.spec.containers["
		initContainersPrefix = "spec.template.spec.initContainers["
	)
	var prefix string
	switch {
	case strings.HasPrefix(path, containersPrefix):
		prefix = containersPrefix
	case strings.HasPrefix(path, initContainersPrefix):
		prefix = initContainersPrefix
	default:
		return "", "", false
	}
	if !strings.HasSuffix(path, "]") {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "]")
	container, envName, ok = strings.Cut(rest, "].env[")
	if !ok || container == "" || envName == "" {
		return "", "", false
	}
	return container, envName, true
}

// FindStaleSecretEnvChecksForObject returns key-and-time metadata for a Pod's
// Secret-backed env values. Secret data and stringData are never accessed.
func FindStaleSecretEnvChecksForObject(ctx context.Context, cache *ResourceCache, obj runtime.Object) []StaleSecretEnvCheck {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil || !podHasSecretEnvReference(pod) {
		return nil
	}
	now := time.Now()
	pods := []*corev1.Pod{pod}
	return findStaleSecretEnvChecks(cache, pods, querySecretDataHistory(ctx, pod.Namespace, pods, staleSecretEnvHistorySince(pods, now)))
}

// FindStaleSecretEnvChecksForPods aggregates the Pod-level facts needed by a
// workload diagnosis, including the managedFields fallback that is deliberately
// confined to this diagnostic path.
func FindStaleSecretEnvChecksForPods(ctx context.Context, cache *ResourceCache, pods []*corev1.Pod) []StaleSecretEnvCheck {
	if cache == nil {
		return nil
	}
	podsByNamespace := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		if pod != nil && podHasSecretEnvReference(pod) {
			podsByNamespace[pod.Namespace] = append(podsByNamespace[pod.Namespace], pod)
		}
	}
	namespaces := make([]string, 0, len(podsByNamespace))
	for namespace := range podsByNamespace {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	var out []StaleSecretEnvCheck
	for _, namespace := range namespaces {
		now := time.Now()
		pods := podsByNamespace[namespace]
		events := querySecretDataHistory(ctx, namespace, pods, staleSecretEnvHistorySince(pods, now))
		checks := groupStaleSecretEnvChecks(findStaleSecretEnvChecksWithManagedFields(cache, pods, events))
		if len(checks) > maxStaleSecretEnvDetectionsPerNamespace {
			checks = checks[:maxStaleSecretEnvDetectionsPerNamespace]
		}
		out = append(out, checks...)
	}
	return out
}

func groupStaleSecretEnvChecks(checks []StaleSecretEnvCheck) []StaleSecretEnvCheck {
	type group struct {
		check StaleSecretEnvCheck
		pods  map[string]bool
	}
	byKey := make(map[string]*group)
	for _, check := range checks {
		key := check.Namespace + "\x00" + check.Container + "\x00" + check.EnvName + "\x00" +
			check.ReferenceKind + "\x00" + check.EvidenceSource + "\x00" + check.Prefix + "\x00" +
			check.SecretName + "\x00" + check.Key
		current := byKey[key]
		if current == nil {
			current = &group{check: check, pods: make(map[string]bool)}
			byKey[key] = current
		}
		current.pods[check.PodName] = true
		if check.PodName < current.check.PodName {
			current.check = check
		}
	}
	out := make([]StaleSecretEnvCheck, 0, len(byKey))
	for _, current := range byKey {
		current.check.AffectedPods = len(current.pods)
		out = append(out, current.check)
	}
	sortStaleSecretEnvChecks(out)
	return out
}

func detectStaleSecretEnv(cache *ResourceCache, namespace string, now time.Time) []Detection {
	if cache == nil || cache.Pods() == nil || cache.Secrets() == nil {
		return nil
	}
	var pods []*corev1.Pod
	var err error
	if namespace == "" {
		pods, err = cache.Pods().List(labels.Everything())
	} else {
		pods, err = cache.Pods().Pods(namespace).List(labels.Everything())
	}
	if err != nil {
		logConfigListError("Pod", namespace, err)
		return nil
	}
	var secretEnvPods []*corev1.Pod
	for _, pod := range pods {
		if podHasSecretEnvReference(pod) {
			secretEnvPods = append(secretEnvPods, pod)
		}
	}
	if len(secretEnvPods) == 0 {
		return nil
	}
	events := querySecretDataHistory(context.Background(), namespace, secretEnvPods, staleSecretEnvHistorySince(secretEnvPods, now))
	checks := findStaleSecretEnvChecks(cache, secretEnvPods, events)
	changes := latestPodEnvSourceChanges(events, nil)
	checksByPod := make(map[string][]StaleSecretEnvCheck)
	for _, check := range checks {
		key := check.Namespace + "\x00" + check.PodName
		checksByPod[key] = append(checksByPod[key], check)
	}

	subjectByPod := make(map[string]staleSecretEnvSubject, len(pods))
	podsBySubject := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		subject := staleSecretEnvSubjectForPod(cache, pod)
		podKey := pod.Namespace + "\x00" + pod.Name
		subjectByPod[podKey] = subject
		podsBySubject[subject.key()] = append(podsBySubject[subject.key()], pod)
	}

	var notReadyOut []Detection
	readyGroups := make(map[string]*staleSecretEnvReadyGroup)
	notReadySubjects := make(map[string]bool)
	for _, pod := range pods {
		podChecks := omitMissingRequiredSecretKeyChecks(cache, pod, checksByPod[pod.Namespace+"\x00"+pod.Name], changes)
		issueChecks, podReady := staleSecretEnvIssueChecks(pod, podChecks)
		if len(issueChecks) == 0 {
			continue
		}
		subject := subjectByPod[pod.Namespace+"\x00"+pod.Name]
		if podReady {
			group := readyGroups[subject.key()]
			if group == nil {
				group = &staleSecretEnvReadyGroup{subject: subject}
				readyGroups[subject.key()] = group
			}
			group.checks = append(group.checks, issueChecks...)
			continue
		}

		notReadySubjects[subject.key()] = true
		ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
		notReadyOut = append(notReadyOut, Detection{
			Kind:              "Pod",
			Namespace:         pod.Namespace,
			Name:              pod.Name,
			Severity:          "warning",
			Reason:            "StaleSecretEnv",
			Message:           staleSecretEnvDetectionMessage(issueChecks),
			OwnerGroup:        ownerGroup,
			OwnerKind:         ownerKind,
			OwnerName:         ownerName,
			Fingerprint:       "stale-secret-env",
			Age:               FormatAge(now.Sub(pod.CreationTimestamp.Time)),
			AgeSeconds:        int64(now.Sub(pod.CreationTimestamp.Time).Seconds()),
			ResourceCreatedAt: pod.CreationTimestamp.Time,
			Cause:             "A running container loaded a Secret-backed environment value before Radar observed that Secret key change.",
			Action:            "Restart the pod so its containers re-read Secret-backed environment variables.",
		})
		setStaleSecretEnvOnset(&notReadyOut[len(notReadyOut)-1], issueChecks, now)
	}

	readyKeys := make([]string, 0, len(readyGroups))
	for key := range readyGroups {
		readyKeys = append(readyKeys, key)
	}
	sort.Strings(readyKeys)
	var readyOut []Detection
	for _, key := range readyKeys {
		if notReadySubjects[key] {
			continue
		}
		group := readyGroups[key]
		rolloutEvidence := newStaleSecretEnvRolloutEvidence(group.subject, podsBySubject[key], now)
		var currentChecks []StaleSecretEnvCheck
		for _, check := range group.checks {
			if !rolloutEvidence.supersedes(check) {
				currentChecks = append(currentChecks, check)
			}
		}
		if len(currentChecks) == 0 {
			continue
		}
		affectedPods := make(map[string]bool)
		for _, check := range currentChecks {
			affectedPods[check.PodName] = true
		}
		detection := Detection{
			Group:       group.subject.group,
			Kind:        group.subject.kind,
			Namespace:   group.subject.namespace,
			Name:        group.subject.name,
			Severity:    "warning",
			Reason:      "StaleSecretEnv",
			Message:     staleSecretEnvReadyDetectionMessage(group.subject, len(affectedPods), len(currentChecks)),
			Fingerprint: "stale-secret-env",
			Cause:       "Radar observed consumed Secret keys change after these Ready container instances started; Kubernetes does not refresh Secret-backed environment variables in running containers.",
			Action:      staleSecretEnvReadyAction(group.subject),
		}
		if !group.subject.created.IsZero() && !group.subject.created.After(now) {
			age := now.Sub(group.subject.created)
			detection.Age = FormatAge(age)
			detection.AgeSeconds = int64(age.Seconds())
			detection.ResourceCreatedAt = group.subject.created
		}
		setStaleSecretEnvOnset(&detection, currentChecks, now)
		readyOut = append(readyOut, detection)
	}

	sort.Slice(notReadyOut, func(i, j int) bool {
		if notReadyOut[i].Namespace != notReadyOut[j].Namespace {
			return notReadyOut[i].Namespace < notReadyOut[j].Namespace
		}
		return notReadyOut[i].Name < notReadyOut[j].Name
	})
	sort.Slice(readyOut, func(i, j int) bool {
		if readyOut[i].Namespace != readyOut[j].Namespace {
			return readyOut[i].Namespace < readyOut[j].Namespace
		}
		if readyOut[i].Kind != readyOut[j].Kind {
			return readyOut[i].Kind < readyOut[j].Kind
		}
		return readyOut[i].Name < readyOut[j].Name
	})
	out := append(notReadyOut, readyOut...)
	if len(out) > maxStaleSecretEnvDetectionsPerNamespace {
		out = out[:maxStaleSecretEnvDetectionsPerNamespace]
	}
	return out
}

func omitMissingRequiredSecretKeyChecks(cache *ResourceCache, pod *corev1.Pod, checks []StaleSecretEnvCheck, changes map[podEnvSourceKey]podEnvSourceChange) []StaleSecretEnvCheck {
	if cache == nil || cache.Secrets() == nil || pod == nil || len(checks) == 0 {
		return checks
	}
	missing := make(map[string]bool)
	for _, container := range staleSecretEnvContainers(pod) {
		for _, env := range container.Env {
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				continue
			}
			ref := env.ValueFrom.SecretKeyRef
			if ref.Optional != nil && *ref.Optional {
				continue
			}
			secret, err := cache.Secrets().Secrets(pod.Namespace).Get(ref.Name)
			if err != nil || secret == nil {
				continue
			}
			if _, exists := secret.Data[ref.Key]; !exists {
				missing[container.Name+"\x00"+env.Name+"\x00"+ref.Name+"\x00"+ref.Key] = true
			}
		}
	}
	if len(missing) == 0 {
		return checks
	}
	out := make([]StaleSecretEnvCheck, 0, len(checks))
	for _, check := range checks {
		key := check.Container + "\x00" + check.EnvName + "\x00" + check.SecretName + "\x00" + check.Key
		change := changes[podEnvSourceKey{kind: "Secret", namespace: pod.Namespace, name: check.SecretName, key: check.Key}]
		if check.ReferenceKind != "secretKeyRef" || !missing[key] || change.kind != "removed" {
			out = append(out, check)
		}
	}
	return out
}

type staleSecretEnvSubject struct {
	group       string
	kind        string
	namespace   string
	name        string
	created     time.Time
	restartable bool
}

func staleSecretEnvSubjectForPod(cache *ResourceCache, pod *corev1.Pod) staleSecretEnvSubject {
	group, kind, name := podOwnerKindName(cache, pod)
	if kind == "" {
		kind = "Pod"
		name = pod.Name
	}
	created := staleSecretEnvSubjectCreatedAt(cache, pod, group, kind, name)
	return staleSecretEnvSubject{
		group: group, kind: kind, namespace: pod.Namespace, name: name,
		created:     created,
		restartable: staleSecretEnvRestartableOwner(group, kind),
	}
}

func staleSecretEnvSubjectCreatedAt(cache *ResourceCache, pod *corev1.Pod, group, kind, name string) time.Time {
	if kind == "Pod" || cache == nil {
		return pod.CreationTimestamp.Time
	}
	switch {
	case group == "apps" && kind == "Deployment" && cache.Deployments() != nil:
		if obj, err := cache.Deployments().Deployments(pod.Namespace).Get(name); err == nil {
			return obj.CreationTimestamp.Time
		}
	case group == "apps" && kind == "StatefulSet" && cache.StatefulSets() != nil:
		if obj, err := cache.StatefulSets().StatefulSets(pod.Namespace).Get(name); err == nil {
			return obj.CreationTimestamp.Time
		}
	case group == "apps" && kind == "DaemonSet" && cache.DaemonSets() != nil:
		if obj, err := cache.DaemonSets().DaemonSets(pod.Namespace).Get(name); err == nil {
			return obj.CreationTimestamp.Time
		}
	case group == "batch" && kind == "Job" && cache.Jobs() != nil:
		if obj, err := cache.Jobs().Jobs(pod.Namespace).Get(name); err == nil {
			return obj.CreationTimestamp.Time
		}
	case group == "batch" && kind == "CronJob" && cache.CronJobs() != nil:
		if obj, err := cache.CronJobs().CronJobs(pod.Namespace).Get(name); err == nil {
			return obj.CreationTimestamp.Time
		}
	case group == "argoproj.io" && kind == "Rollout":
		dynamicCache := GetDynamicResourceCache()
		discovery := GetResourceDiscovery()
		if dynamicCache == nil || discovery == nil {
			break
		}
		gvr, ok := discovery.GetGVRWithGroup("Rollout", "argoproj.io")
		if !ok {
			break
		}
		// This request path must not probe RBAC or start an informer on a miss.
		obj, err := dynamicCache.GetWatched(gvr, pod.Namespace, name)
		if err != nil {
			break
		}
		return obj.GetCreationTimestamp().Time
	}
	return time.Time{}
}

func staleSecretEnvRestartableOwner(group, kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return group == "apps"
	case "Rollout":
		return group == "argoproj.io"
	default:
		return false
	}
}

func (s staleSecretEnvSubject) key() string {
	return s.group + "\x00" + s.kind + "\x00" + s.namespace + "\x00" + s.name
}

type staleSecretEnvReadyGroup struct {
	subject staleSecretEnvSubject
	checks  []StaleSecretEnvCheck
}

type staleSecretEnvConsumption struct {
	container string
	secret    string
	key       string
}

type staleSecretEnvReplacement struct {
	revision  string
	startedAt time.Time
}

type staleSecretEnvRolloutEvidence struct {
	revisionByPod map[string]string
	replacements  map[staleSecretEnvConsumption][]staleSecretEnvReplacement
}

func newStaleSecretEnvRolloutEvidence(subject staleSecretEnvSubject, pods []*corev1.Pod, now time.Time) staleSecretEnvRolloutEvidence {
	evidence := staleSecretEnvRolloutEvidence{}
	if (subject.group != "apps" || subject.kind != "Deployment") &&
		(subject.group != "argoproj.io" || subject.kind != "Rollout") {
		return evidence
	}
	evidence.revisionByPod = make(map[string]string, len(pods))
	evidence.replacements = make(map[staleSecretEnvConsumption][]staleSecretEnvReplacement)
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		revisionRef := controllerOwnerRef(pod.OwnerReferences)
		if revisionRef == nil || revisionRef.Kind != "ReplicaSet" {
			continue
		}
		revision := ownerReferenceIdentity(revisionRef)
		evidence.revisionByPod[pod.Name] = revision
		if pod.DeletionTimestamp != nil || !isPodReadyForProblem(pod) {
			continue
		}
		statusByContainer := staleSecretEnvContainerStatuses(pod)
		for _, container := range staleSecretEnvContainers(pod) {
			status, ok := statusByContainer[container.Name]
			if !ok || !status.Ready || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
				continue
			}
			startedAt := status.State.Running.StartedAt.Time
			if startedAt.After(now) || now.Sub(startedAt) > staleSecretEnvRolloutEvidenceWindow {
				continue
			}
			replacement := staleSecretEnvReplacement{revision: revision, startedAt: startedAt}
			for _, env := range container.Env {
				if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					continue
				}
				ref := env.ValueFrom.SecretKeyRef
				evidence.add(staleSecretEnvConsumption{container: container.Name, secret: ref.Name, key: ref.Key}, replacement)
			}
			for _, envFrom := range container.EnvFrom {
				if envFrom.SecretRef != nil {
					evidence.add(staleSecretEnvConsumption{container: container.Name, secret: envFrom.SecretRef.Name}, replacement)
				}
			}
		}
	}
	return evidence
}

func (e *staleSecretEnvRolloutEvidence) add(consumption staleSecretEnvConsumption, replacement staleSecretEnvReplacement) {
	candidates := e.replacements[consumption]
	for i := range candidates {
		if candidates[i].revision == replacement.revision {
			if replacement.startedAt.After(candidates[i].startedAt) {
				candidates[i] = replacement
			}
			e.replacements[consumption] = candidates
			return
		}
	}
	candidates = append(candidates, replacement)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].startedAt.After(candidates[j].startedAt)
	})
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}
	e.replacements[consumption] = candidates
}

func (e staleSecretEnvRolloutEvidence) supersedes(check StaleSecretEnvCheck) bool {
	staleRevision := e.revisionByPod[check.PodName]
	if staleRevision == "" {
		return false
	}
	consumptions := [...]staleSecretEnvConsumption{
		{container: check.Container, secret: check.SecretName, key: check.Key},
		{container: check.Container, secret: check.SecretName},
	}
	for _, consumption := range consumptions {
		for _, replacement := range e.replacements[consumption] {
			if replacement.revision != staleRevision && !replacement.startedAt.Before(check.SecretChangedAt) {
				return true
			}
		}
	}
	return false
}

func ownerReferenceIdentity(ref *metav1.OwnerReference) string {
	if ref.UID != "" {
		return string(ref.UID)
	}
	return ref.APIVersion + "\x00" + ref.Kind + "\x00" + ref.Name
}

func secretEnvReferenceNames(pods []*corev1.Pod) []string {
	names := make(map[string]bool)
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		for _, container := range staleSecretEnvContainers(pod) {
			for _, env := range container.Env {
				if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name != "" {
					names[env.ValueFrom.SecretKeyRef.Name] = true
				}
			}
			for _, envFrom := range container.EnvFrom {
				if envFrom.SecretRef != nil && envFrom.SecretRef.Name != "" {
					names[envFrom.SecretRef.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func secretEnvReferenceNamespaces(pods []*corev1.Pod) []string {
	namespaces := make(map[string]bool)
	for _, pod := range pods {
		if pod != nil && pod.Namespace != "" {
			namespaces[pod.Namespace] = true
		}
	}
	out := make([]string, 0, len(namespaces))
	for namespace := range namespaces {
		out = append(out, namespace)
	}
	sort.Strings(out)
	return out
}

func setStaleSecretEnvOnset(detection *Detection, checks []StaleSecretEnvCheck, now time.Time) {
	changedAt := checks[0].SecretChangedAt
	for _, check := range checks[1:] {
		if check.SecretChangedAt.Before(changedAt) {
			changedAt = check.SecretChangedAt
		}
	}
	setDetectionOnset(detection, now, changedAt)
}

func staleSecretEnvReadyDetectionMessage(subject staleSecretEnvSubject, podCount, checkCount int) string {
	if podCount == 1 {
		if checkCount == 1 {
			if subject.kind != "Pod" {
				return fmt.Sprintf("%s/%s has a Ready pod whose running container loaded a Secret-backed environment value before Radar observed its consumed Secret key change; it may still hold the pre-change value.", subject.kind, subject.name)
			}
			return fmt.Sprintf("%s/%s is Ready, but a running container loaded a Secret-backed environment value before Radar observed its consumed Secret key change; it may still hold the pre-change value.", subject.kind, subject.name)
		}
		if subject.kind != "Pod" {
			return fmt.Sprintf("%s/%s has a Ready pod whose running containers loaded %d Secret-backed environment values before Radar observed the consumed Secret keys change; they may still hold pre-change values.", subject.kind, subject.name, checkCount)
		}
		return fmt.Sprintf("%s/%s is Ready, but its running containers loaded %d Secret-backed environment values before Radar observed the consumed Secret keys change; they may still hold pre-change values.", subject.kind, subject.name, checkCount)
	}
	return fmt.Sprintf("%s/%s has %d Ready pods whose running containers loaded %d Secret-backed environment values before Radar observed the consumed Secret keys change; they may still hold pre-change values.", subject.kind, subject.name, podCount, checkCount)
}

func staleSecretEnvReadyAction(subject staleSecretEnvSubject) string {
	if subject.restartable {
		return fmt.Sprintf("Confirm no rollout is already replacing the affected pods, then restart %s/%s so its containers re-read Secret-backed environment variables.", subject.kind, subject.name)
	}
	if subject.kind == "Pod" {
		return fmt.Sprintf("Confirm no replacement is already in progress, then replace Pod/%s through its owning controller (or recreate it if standalone) so its containers re-read Secret-backed environment variables.", subject.name)
	}
	return fmt.Sprintf("Confirm no replacement is already in progress, then use %s/%s's normal controller workflow to replace the affected pods so they re-read Secret-backed environment variables.", subject.kind, subject.name)
}

func podHasSecretEnvReference(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, container := range staleSecretEnvContainers(pod) {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				return true
			}
		}
		for _, envFrom := range container.EnvFrom {
			if envFrom.SecretRef != nil {
				return true
			}
		}
	}
	return false
}

// staleSecretEnvContainers returns the containers whose Secret-backed env can go
// stale until restart: all regular containers plus restartable init containers
// (native sidecars, restartPolicy=Always), which run for the pod's lifetime.
// Regular init containers run once to completion and re-read env on the next pod
// start, so they can never hold a stale-until-restart value and are excluded.
func staleSecretEnvContainers(pod *corev1.Pod) []corev1.Container {
	out := make([]corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, container := range pod.Spec.InitContainers {
		if isRestartableInitContainer(container) {
			out = append(out, container)
		}
	}
	return append(out, pod.Spec.Containers...)
}

// staleSecretEnvContainerStatuses indexes the statuses of the containers that
// staleSecretEnvContainers walks: regular containers plus restartable init
// containers. Container names are unique across a Pod's regular and init
// containers, so merging the two status lists cannot collide, and only the
// runtime containers above are ever looked up.
func staleSecretEnvContainerStatuses(pod *corev1.Pod) map[string]corev1.ContainerStatus {
	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses)+len(pod.Status.InitContainerStatuses))
	restartable := restartableInitContainerNames(pod)
	for _, status := range pod.Status.InitContainerStatuses {
		if restartable[status.Name] {
			statuses[status.Name] = status
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		statuses[status.Name] = status
	}
	return statuses
}

func restartableInitContainerNames(pod *corev1.Pod) map[string]bool {
	var names map[string]bool
	for _, container := range pod.Spec.InitContainers {
		if isRestartableInitContainer(container) {
			if names == nil {
				names = make(map[string]bool)
			}
			names[container.Name] = true
		}
	}
	return names
}

func isRestartableInitContainer(container corev1.Container) bool {
	return container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

func querySecretDataHistory(ctx context.Context, namespace string, pods []*corev1.Pod, since time.Time) []timeline.TimelineEvent {
	names := secretEnvReferenceNames(pods)
	if len(names) == 0 {
		return nil
	}
	opts := timeline.QueryOptions{
		Kinds:          []string{"Secret"},
		Names:          names,
		Since:          since,
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     []timeline.EventType{timeline.EventTypeUpdate},
		ClusterContext: ActiveClusterContext(),
		IncludeManaged: true,
		Limit:          maxSecretDataHistoryEvents,
	}
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	} else {
		opts.Namespaces = secretEnvReferenceNamespaces(pods)
	}
	events := queryEnvHistory(ctx, opts)
	if len(events) >= maxSecretDataHistoryEvents {
		return nil
	}
	return events
}

func staleSecretEnvHistorySince(pods []*corev1.Pod, now time.Time) time.Time {
	floor := now.Add(-envHistoryContextMaxLookback)
	var earliest time.Time
	consider := func(status corev1.ContainerStatus) {
		if status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
			return
		}
		startedAt := status.State.Running.StartedAt.Time
		if earliest.IsZero() || startedAt.Before(earliest) {
			earliest = startedAt
		}
	}
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		restartable := restartableInitContainerNames(pod)
		for _, status := range pod.Status.InitContainerStatuses {
			if restartable[status.Name] {
				consider(status)
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			consider(status)
		}
	}
	if earliest.IsZero() || earliest.Before(floor) {
		return floor
	}
	since := earliest.Add(-time.Second)
	if since.Before(floor) {
		return floor
	}
	return since
}

func queryEnvHistory(ctx context.Context, opts timeline.QueryOptions) []timeline.TimelineEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	events, err := timeline.QueryEvents(ctx, opts)
	if err != nil {
		return nil
	}
	return events
}

type secretDataKey struct {
	namespace string
	name      string
	key       string
}

func findStaleSecretEnvChecks(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent) []StaleSecretEnvCheck {
	if cache == nil || cache.Secrets() == nil {
		return nil
	}
	changes := latestSecretDataChanges(events)
	if len(changes) == 0 {
		return nil
	}
	changesBySecret := secretDataChangesBySecret(changes)
	exists := make(map[string]bool)
	checked := make(map[string]bool)
	var out []StaleSecretEnvCheck
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		statuses := staleSecretEnvContainerStatuses(pod)
		for _, container := range staleSecretEnvContainers(pod) {
			status, ok := statuses[container.Name]
			if !ok || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
				continue
			}
			startedAt := status.State.Running.StartedAt.Time
			containerChecks := 0
			for _, env := range container.Env {
				if containerChecks >= maxStaleSecretEnvChecksPerContainer {
					break
				}
				if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					continue
				}
				ref := env.ValueFrom.SecretKeyRef
				changedAt, ok := changes[secretDataKey{namespace: pod.Namespace, name: ref.Name, key: ref.Key}]
				if !ok || !startedAt.Before(changedAt) || !secretExists(cache, pod.Namespace, ref.Name, exists, checked) {
					continue
				}
				out = append(out, newStaleSecretEnvCheck(pod, container.Name, env.Name, "secretKeyRef", "", ref.Name, ref.Key, startedAt, changedAt))
				containerChecks++
			}
			for _, envFrom := range container.EnvFrom {
				if containerChecks >= maxStaleSecretEnvChecksPerContainer {
					break
				}
				if envFrom.SecretRef == nil || !secretExists(cache, pod.Namespace, envFrom.SecretRef.Name, exists, checked) {
					continue
				}
				identity := pod.Namespace + "\x00" + envFrom.SecretRef.Name
				for _, change := range changesBySecret[identity] {
					if containerChecks >= maxStaleSecretEnvChecksPerContainer {
						break
					}
					if !startedAt.Before(change.changedAt) {
						continue
					}
					out = append(out, newStaleSecretEnvCheck(pod, container.Name, envFrom.Prefix+change.key, "envFrom", envFrom.Prefix, envFrom.SecretRef.Name, change.key, startedAt, change.changedAt))
					containerChecks++
				}
			}
		}
	}
	sortStaleSecretEnvChecks(out)
	return out
}

func findStaleSecretEnvChecksWithManagedFields(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent) []StaleSecretEnvCheck {
	out := findStaleSecretEnvChecks(cache, pods, events)
	if cache == nil || cache.Secrets() == nil {
		return out
	}

	observedSecrets := secretsWithObservedDataWrites(events)
	checksPerContainer := make(map[string]int)
	for _, check := range out {
		checksPerContainer[check.Namespace+"\x00"+check.PodName+"\x00"+check.Container]++
	}
	exists := make(map[string]bool)
	checked := make(map[string]bool)
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		statuses := staleSecretEnvContainerStatuses(pod)
		for _, container := range staleSecretEnvContainers(pod) {
			status, ok := statuses[container.Name]
			if !ok || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
				continue
			}
			startedAt := status.State.Running.StartedAt.Time
			containerIdentity := pod.Namespace + "\x00" + pod.Name + "\x00" + container.Name
			seenSecrets := make(map[string]bool)
			addFallback := func(referenceKind, prefix, secretName string) {
				if checksPerContainer[containerIdentity] >= maxStaleSecretEnvChecksPerContainer || secretName == "" {
					return
				}
				secretIdentity := pod.Namespace + "\x00" + secretName
				if seenSecrets[secretIdentity] {
					return
				}
				seenSecrets[secretIdentity] = true
				if observedSecrets[secretIdentity] || !secretExists(cache, pod.Namespace, secretName, exists, checked) {
					return
				}
				changedAt, ok := secretDataManagerWriteTime(cache, pod.Namespace, secretName)
				if !ok || !startedAt.Before(changedAt) {
					return
				}
				out = append(out, newManagedFieldsStaleSecretEnvCheck(pod, container.Name, referenceKind, prefix, secretName, startedAt, changedAt))
				checksPerContainer[containerIdentity]++
			}
			for _, env := range container.Env {
				if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
					addFallback("secretKeyRef", "", env.ValueFrom.SecretKeyRef.Name)
				}
			}
			for _, envFrom := range container.EnvFrom {
				if envFrom.SecretRef != nil {
					addFallback("envFrom", envFrom.Prefix, envFrom.SecretRef.Name)
				}
			}
		}
	}
	sortStaleSecretEnvChecks(out)
	return out
}

func sortStaleSecretEnvChecks(out []StaleSecretEnvCheck) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].EvidenceSource != out[j].EvidenceSource {
			return out[i].EvidenceSource == staleSecretEvidenceObservedChange
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].PodName != out[j].PodName {
			return out[i].PodName < out[j].PodName
		}
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		if out[i].SecretName != out[j].SecretName {
			return out[i].SecretName < out[j].SecretName
		}
		return out[i].Key < out[j].Key
	})
}

type secretDataChange struct {
	key       string
	changedAt time.Time
}

func secretDataChangesBySecret(changes map[secretDataKey]time.Time) map[string][]secretDataChange {
	out := make(map[string][]secretDataChange)
	for identity, changedAt := range changes {
		secret := identity.namespace + "\x00" + identity.name
		out[secret] = append(out[secret], secretDataChange{key: identity.key, changedAt: changedAt})
	}
	for secret := range out {
		sort.Slice(out[secret], func(i, j int) bool { return out[secret][i].key < out[secret][j].key })
	}
	return out
}

func newStaleSecretEnvCheck(pod *corev1.Pod, container, envName, referenceKind, prefix, secretName, key string, startedAt, changedAt time.Time) StaleSecretEnvCheck {
	return StaleSecretEnvCheck{
		Namespace:          pod.Namespace,
		PodName:            pod.Name,
		Container:          container,
		EnvName:            envName,
		ReferenceKind:      referenceKind,
		EvidenceSource:     staleSecretEvidenceObservedChange,
		Prefix:             prefix,
		SecretName:         secretName,
		Key:                key,
		ContainerStartedAt: startedAt,
		SecretChangedAt:    changedAt,
		Message:            fmt.Sprintf("Container %s started at %s, before Radar observed Secret/%s key %s change at %s; env %s may hold a stale value until the container restarts.", container, startedAt.UTC().Format(time.RFC3339), secretName, key, changedAt.UTC().Format(time.RFC3339), envName),
	}
}

func newManagedFieldsStaleSecretEnvCheck(pod *corev1.Pod, container, referenceKind, prefix, secretName string, startedAt, changedAt time.Time) StaleSecretEnvCheck {
	return StaleSecretEnvCheck{
		Namespace:          pod.Namespace,
		PodName:            pod.Name,
		Container:          container,
		ReferenceKind:      referenceKind,
		EvidenceSource:     staleSecretEvidenceManagedFields,
		Prefix:             prefix,
		SecretName:         secretName,
		ContainerStartedAt: startedAt,
		SecretChangedAt:    changedAt,
		Message:            fmt.Sprintf("Secret/%s was modified at %s by a field manager that owns Secret data, after container %s started at %s; managedFields cannot identify which field changed, so if the write touched a consumed key the running process may hold a stale env value until restart.", secretName, changedAt.UTC().Format(time.RFC3339), container, startedAt.UTC().Format(time.RFC3339)),
	}
}

func secretExists(cache *ResourceCache, namespace, name string, exists, checked map[string]bool) bool {
	identity := namespace + "\x00" + name
	if checked[identity] {
		return exists[identity]
	}
	checked[identity] = true
	secret, err := cache.Secrets().Secrets(namespace).Get(name)
	exists[identity] = err == nil && secret != nil
	return exists[identity]
}

func secretDataManagerWriteTime(cache *ResourceCache, namespace, name string) (time.Time, bool) {
	if cache == nil || cache.secretWriteTimes == nil || cache.Secrets() == nil {
		return time.Time{}, false
	}
	write, ok := cache.secretWriteTimes.load(namespace, name)
	if !ok || write.at.IsZero() {
		return time.Time{}, false
	}
	secret, err := cache.Secrets().Secrets(namespace).Get(name)
	if err != nil || secret == nil || secret.Type == corev1.SecretType("helm.sh/release.v1") || string(secret.UID) != write.uid {
		return time.Time{}, false
	}
	return write.at, true
}

func secretsWithObservedDataWrites(events []timeline.TimelineEvent) map[string]bool {
	out := make(map[string]bool)
	for _, event := range events {
		if event.Kind != "Secret" || event.Source != timeline.SourceInformer || event.EventType != timeline.EventTypeUpdate || event.Diff == nil {
			continue
		}
		for _, field := range event.Diff.Fields {
			if isSecretDataWritePath(field.Path) {
				out[event.Namespace+"\x00"+event.Name] = true
			}
		}
	}
	return out
}

func latestSecretDataChanges(events []timeline.TimelineEvent) map[secretDataKey]time.Time {
	out := make(map[secretDataKey]time.Time)
	for _, event := range events {
		if event.Kind != "Secret" || event.Source != timeline.SourceInformer || event.EventType != timeline.EventTypeUpdate || event.Diff == nil || event.Timestamp.IsZero() {
			continue
		}
		for _, field := range event.Diff.Fields {
			if !isSecretDataModificationPath(field.Path) {
				continue
			}
			keys := stringValues(field.NewValue)
			if len(keys) == 0 {
				keys = stringValues(field.OldValue)
			}
			for _, key := range keys {
				if key == "" {
					continue
				}
				identity := secretDataKey{namespace: event.Namespace, name: event.Name, key: key}
				if event.Timestamp.After(out[identity]) {
					out[identity] = event.Timestamp
				}
			}
		}
	}
	return out
}

func isSecretDataModificationPath(path string) bool {
	// Added keys were absent from existing environments, so they cannot make an injected value stale.
	switch path {
	case "data (modified keys)", "data (removed keys)",
		"stringData (modified keys)", "stringData (removed keys)":
		return true
	default:
		return false
	}
}

func stringValues(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func sortTimelineEventsNewestFirst(events []timeline.TimelineEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].Timestamp.After(events[j].Timestamp)
		}
		return events[i].Seq > events[j].Seq
	})
}

func staleSecretEnvIssueChecks(pod *corev1.Pod, checks []StaleSecretEnvCheck) ([]StaleSecretEnvCheck, bool) {
	if pod == nil || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return nil, false
	}
	readyKnown, ready := false, false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			readyKnown = true
			ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	if !readyKnown {
		return nil, false
	}
	// Waiting containers re-read Secret-backed env on their next start. Running
	// containers can retain it; for a not-Ready pod, only a not-Ready container
	// is evidence that the stale value is biting.
	eligibleContainers := make(map[string]bool)
	restartable := restartableInitContainerNames(pod)
	for _, status := range pod.Status.InitContainerStatuses {
		if restartable[status.Name] && status.State.Running != nil && (ready || !status.Ready) {
			eligibleContainers[status.Name] = true
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil && (ready || !status.Ready) {
			eligibleContainers[status.Name] = true
		}
	}
	var out []StaleSecretEnvCheck
	for _, check := range checks {
		if check.EvidenceSource == staleSecretEvidenceObservedChange && eligibleContainers[check.Container] {
			out = append(out, check)
		}
	}
	return out, ready
}

func staleSecretEnvDetectionMessage(checks []StaleSecretEnvCheck) string {
	if len(checks) == 1 {
		check := checks[0]
		return fmt.Sprintf("Container %s is not Ready and may be running a stale env value for Secret/%s key %s; restart the pod to refresh it.", check.Container, check.SecretName, check.Key)
	}
	return fmt.Sprintf("A not-Ready container may be running %d stale Secret-backed env values; restart the pod to refresh them.", len(checks))
}
