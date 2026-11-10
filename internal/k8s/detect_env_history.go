package k8s

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/timeutil"
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
	// OrderOnly is set when every change Radar observed to the key since the
	// container started only reordered the value's comma-separated entries,
	// and Radar's history covers that whole time.
	OrderOnly bool
	// NoRestartNeeded marks the Rook monitor reorder note, which explains a
	// write instead of reporting a possibly stale value. It sorts after every
	// other row, so caps drop it first.
	NoRestartNeeded bool
	// HistoryIncomplete is set on that note when Radar's history doesn't
	// cover the container's whole run. Rows that differ in it are never
	// merged, so the note's caveat can't be lost to another Pod's.
	HistoryIncomplete bool
	Message           string
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
// workload diagnosis, including the managedFields fallback, which only diagnose
// and get_resource's staleEnv (FindStaleEnvForPods) report.
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
			check.SecretName + "\x00" + check.Key + "\x00" + strconv.FormatBool(check.OrderOnly) + "\x00" + strconv.FormatBool(check.HistoryIncomplete)
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

// secretReads is the evidence a finding drawn from a namespace's Secrets rests on.
func secretReads(namespace string) []issuesapi.EvidenceRead {
	return []issuesapi.EvidenceRead{{Resource: "secrets", Namespace: namespace, Verb: "get"}}
}

// A caller who can't read the Secret still needs to know its containers may
// run stale values and how to fix that, which takes no Secret access. What it
// doesn't get is which Secret, which keys, how many, or when they changed.
const staleSecretEnvDegradedCause = "Radar observed a change to a Secret these containers reference for environment variables after they started, and Kubernetes doesn't refresh Secret-backed environment variables in running containers. Which keys changed, and when, is shown only to callers who can read the Secret."

func staleSecretEnvDegraded(message, action string) *issuesapi.DegradedFinding {
	return &issuesapi.DegradedFinding{Message: message, Cause: staleSecretEnvDegradedCause, Action: action}
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
	subjects := make(map[string]staleSecretEnvSubject)
	podsBySubject := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		subject := staleSecretEnvSubjectIdentityForPod(cache, pod)
		subjectKey := subject.key()
		if resolved, ok := subjects[subjectKey]; ok {
			subject = resolved
		} else {
			subject.created = staleSecretEnvSubjectCreatedAt(cache, pod, subject.group, subject.kind, subject.name)
			subjects[subjectKey] = subject
		}
		podKey := pod.Namespace + "\x00" + pod.Name
		subjectByPod[podKey] = subject
		podsBySubject[subjectKey] = append(podsBySubject[subjectKey], pod)
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
			Age:               timeutil.FormatAgeShort(now.Sub(pod.CreationTimestamp.Time)),
			AgeSeconds:        int64(now.Sub(pod.CreationTimestamp.Time).Seconds()),
			ResourceCreatedAt: pod.CreationTimestamp.Time,
			Cause:             staleSecretEnvNotReadyCause(issueChecks),
			Action:            staleSecretEnvNotReadyAction(issueChecks),
			RequiredReads:     secretReads(pod.Namespace),
			Degraded: staleSecretEnvDegraded(
				"A not-Ready container may be running stale Secret-backed environment values; restart the pod to refresh them.",
				"Restart the pod so its containers re-read Secret-backed environment variables."),
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
			Group:         group.subject.group,
			Kind:          group.subject.kind,
			Namespace:     group.subject.namespace,
			Name:          group.subject.name,
			Severity:      "warning",
			Reason:        "StaleSecretEnv",
			Message:       staleSecretEnvReadyDetectionMessage(group.subject, currentChecks),
			Fingerprint:   "stale-secret-env",
			Cause:         staleSecretEnvReadyCause(currentChecks),
			Action:        staleSecretEnvReadyAction(group.subject, currentChecks),
			RequiredReads: secretReads(group.subject.namespace),
			Degraded: staleSecretEnvDegraded(
				fmt.Sprintf("Running containers of %s/%s may hold stale Secret-backed environment values.", group.subject.kind, group.subject.name),
				staleSecretEnvReadyAction(group.subject, nil)),
		}
		if !group.subject.created.IsZero() && !group.subject.created.After(now) {
			age := now.Sub(group.subject.created)
			detection.Age = timeutil.FormatAgeShort(age)
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
	subject := staleSecretEnvSubjectIdentityForPod(cache, pod)
	subject.created = staleSecretEnvSubjectCreatedAt(cache, pod, subject.group, subject.kind, subject.name)
	return subject
}

func staleSecretEnvSubjectIdentityForPod(cache *ResourceCache, pod *corev1.Pod) staleSecretEnvSubject {
	group, kind, name := podOwnerKindName(cache, pod)
	if kind == "" {
		kind = "Pod"
		name = pod.Name
	}
	return staleSecretEnvSubject{
		group: group, kind: kind, namespace: pod.Namespace, name: name,
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

func staleSecretEnvReadyDetectionMessage(subject staleSecretEnvSubject, checks []StaleSecretEnvCheck) string {
	pods := make(map[string]bool)
	var podName string
	for _, check := range checks {
		pods[check.PodName] = true
		podName = check.PodName
	}
	facts := staleSecretEnvFacts(checks)
	// The Issues list truncates the message, so order-only leads.
	if staleSecretEnvOrderOnly(checks) {
		const tail = "Running containers keep the old order until they restart."
		switch {
		case subject.kind == "Pod":
			return fmt.Sprintf("Only the order of entries changed in Secret data that Ready %s/%s reads as env: %s. %s", subject.kind, subject.name, facts, tail)
		case len(pods) == 1:
			return fmt.Sprintf("Only the order of entries changed in Secret data that a Ready pod (%s) of %s/%s reads as env: %s. %s", podName, subject.kind, subject.name, facts, tail)
		default:
			return fmt.Sprintf("Only the order of entries changed in Secret data that %d Ready pods of %s/%s read as env: %s. %s", len(pods), subject.kind, subject.name, facts, tail)
		}
	}
	switch {
	case subject.kind == "Pod":
		return fmt.Sprintf("%s/%s is Ready, and a running container in it started before Radar observed a Secret key it references change: %s. Kubernetes reads Secret-backed env values only when a container starts.", subject.kind, subject.name, facts)
	case len(pods) == 1:
		return fmt.Sprintf("%s/%s has a Ready pod (%s) with a running container that started before Radar observed a Secret key it references change: %s. Kubernetes reads Secret-backed env values only when a container starts.", subject.kind, subject.name, podName, facts)
	default:
		return fmt.Sprintf("%s/%s has %d Ready pods with running containers that started before Radar observed Secret keys they reference change: %s. Kubernetes reads Secret-backed env values only when a container starts.", subject.kind, subject.name, len(pods), facts)
	}
}

// staleSecretEnvOrderOnly reports whether every check is order-only. The
// per-container cap drops order-only checks first, so this holds for every
// key Radar found, not just the ones listed.
func staleSecretEnvOrderOnly(checks []StaleSecretEnvCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if !check.OrderOnly {
			return false
		}
	}
	return true
}

const maxStaleSecretEnvMessageFacts = 3

// staleSecretEnvFacts states what Radar observed for each referenced key: when
// the container started, which Secret key it references as which env var, and
// when Radar observed that key change. The change time is when Radar saw the
// update, not when it was written, so the facts don't claim which value the
// container read.
func staleSecretEnvFacts(checks []StaleSecretEnvCheck) string {
	type fact struct {
		check     StaleSecretEnvCheck
		pods      map[string]bool
		startedAt time.Time
		changedAt time.Time
	}
	byKey := make(map[string]*fact)
	var keys []string
	for _, check := range checks {
		key := check.SecretName + "\x00" + check.Key + "\x00" + check.Container + "\x00" + check.EnvName + "\x00" + strconv.FormatBool(check.OrderOnly)
		current := byKey[key]
		if current == nil {
			current = &fact{check: check, pods: make(map[string]bool)}
			byKey[key] = current
			keys = append(keys, key)
		}
		current.pods[check.PodName] = true
		if check.ContainerStartedAt.After(current.startedAt) {
			current.startedAt = check.ContainerStartedAt
		}
		if check.SecretChangedAt.After(current.changedAt) {
			current.changedAt = check.SecretChangedAt
		}
	}
	// Real changes lead, so the "+N more" tail never hides one behind reorders.
	sort.Slice(keys, func(i, j int) bool {
		if a, b := byKey[keys[i]].check.OrderOnly, byKey[keys[j]].check.OrderOnly; a != b {
			return b
		}
		return keys[i] < keys[j]
	})
	clauses := make([]string, 0, maxStaleSecretEnvMessageFacts+1)
	for i, key := range keys {
		if i == maxStaleSecretEnvMessageFacts {
			clauses = append(clauses, fmt.Sprintf("+%d more", len(keys)-i))
			break
		}
		f := byKey[key]
		started := fmt.Sprintf("container %s started at %s and references", f.check.Container, f.startedAt.UTC().Format(time.RFC3339))
		if len(f.pods) > 1 {
			started = fmt.Sprintf("%d %s containers started at or before %s and reference", len(f.pods), f.check.Container, f.startedAt.UTC().Format(time.RFC3339))
		}
		if f.check.OrderOnly {
			clauses = append(clauses, fmt.Sprintf("%s Secret/%s key %s as env %s; Radar observed that key's comma-separated entries reordered at %s, with none added or removed",
				started, f.check.SecretName, f.check.Key, f.check.EnvName, f.changedAt.UTC().Format(time.RFC3339)))
			continue
		}
		clauses = append(clauses, fmt.Sprintf("%s Secret/%s key %s as env %s; Radar observed that key change at %s",
			started, f.check.SecretName, f.check.Key, f.check.EnvName, f.changedAt.UTC().Format(time.RFC3339)))
	}
	return strings.Join(clauses, "; ")
}

const staleSecretEnvOrderCheck = "Only the order of entries changed. Check whether %s treats the new order differently, for example as a failover or priority order. If it doesn't, no restart is needed. If it does, "

func staleSecretEnvReadyAction(subject staleSecretEnvSubject, checks []StaleSecretEnvCheck) string {
	if staleSecretEnvOrderOnly(checks) {
		app := fmt.Sprintf("%s/%s", subject.kind, subject.name)
		if subject.kind == "Pod" {
			app = "the app in " + app
		}
		prefix := fmt.Sprintf(staleSecretEnvOrderCheck, app)
		if subject.restartable {
			return prefix + fmt.Sprintf("confirm no rollout is already replacing the affected pods, then restart %s/%s.", subject.kind, subject.name)
		}
		if subject.kind == "Pod" {
			return prefix + fmt.Sprintf("confirm no replacement is already in progress, then replace Pod/%s through its owning controller (or recreate it if standalone).", subject.name)
		}
		return prefix + fmt.Sprintf("confirm no replacement is already in progress, then use %s/%s's normal controller workflow to replace the affected pods.", subject.kind, subject.name)
	}
	if subject.restartable {
		return fmt.Sprintf("Confirm no rollout is already replacing the affected pods, then restart %s/%s so its containers re-read Secret-backed environment variables.", subject.kind, subject.name)
	}
	if subject.kind == "Pod" {
		return fmt.Sprintf("Confirm no replacement is already in progress, then replace Pod/%s through its owning controller (or recreate it if standalone) so its containers re-read Secret-backed environment variables.", subject.name)
	}
	return fmt.Sprintf("Confirm no replacement is already in progress, then use %s/%s's normal controller workflow to replace the affected pods so they re-read Secret-backed environment variables.", subject.kind, subject.name)
}

func staleSecretEnvReadyCause(checks []StaleSecretEnvCheck) string {
	if staleSecretEnvOrderOnly(checks) {
		return "Radar observed only the order of comma-separated entries change in Secret keys these Ready containers read as environment variables, after the containers started. Kubernetes does not refresh Secret-backed environment variables in running containers, so they keep the old order."
	}
	return "Radar observed Secret keys referenced by these Ready containers' environment change after the containers started; Kubernetes does not refresh Secret-backed environment variables in running containers."
}

func staleSecretEnvNotReadyCause(checks []StaleSecretEnvCheck) string {
	if staleSecretEnvOrderOnly(checks) {
		return "A running container that references a Secret key in its environment started before Radar observed that key's comma-separated entries reordered, with none added or removed."
	}
	return "A running container that references a Secret key in its environment started before Radar observed that key change."
}

func staleSecretEnvNotReadyAction(checks []StaleSecretEnvCheck) string {
	if staleSecretEnvOrderOnly(checks) {
		return fmt.Sprintf(staleSecretEnvOrderCheck, "the app") + "restart the pod so its containers re-read Secret-backed environment variables."
	}
	return "Restart the pod so its containers re-read Secret-backed environment variables."
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
	return findStaleSecretEnvChecksUpTo(cache, pods, events, maxStaleSecretEnvChecksPerContainer)
}

// findStaleSecretEnvChecksUpTo is findStaleSecretEnvChecks with the
// per-container cap as a parameter; perContainer <= 0 means no cap, for callers
// that filter to one Secret first and cap only what they present.
func findStaleSecretEnvChecksUpTo(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent, perContainer int) []StaleSecretEnvCheck {
	checks, _ := findStaleSecretEnvChecksAndRookReorders(cache, pods, events, perContainer)
	return checks
}

// rookMonReorderRead is a container whose only observed change to Rook's
// monitor list keys since it started was a reorder, which the Rook rule keeps
// out of the checks.
type rookMonReorderRead struct {
	pod           *corev1.Pod
	container     string
	referenceKind string
	prefix        string
	secretName    string
	startedAt     time.Time
	complete      bool
}

func findStaleSecretEnvChecksAndRookReorders(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent, perContainer int) ([]StaleSecretEnvCheck, []rookMonReorderRead) {
	if perContainer <= 0 {
		perContainer = math.MaxInt
	}
	if cache == nil || cache.Secrets() == nil {
		return nil, nil
	}
	changes := latestSecretDataChanges(events)
	if len(changes) == 0 {
		return nil, nil
	}
	changesBySecret := secretDataChangesBySecret(changes)
	completeSince := envHistoryCompleteSince(time.Now())
	exists := make(map[string]bool)
	checked := make(map[string]bool)
	var out []StaleSecretEnvCheck
	var rookReorders []rookMonReorderRead
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
			var containerChecks []StaleSecretEnvCheck
			var containerRookReorders []rookMonReorderRead
			add := func(envName, referenceKind, prefix, secretName, key string, change secretKeyChange) {
				changedAt, orderOnly, stale := change.since(startedAt, completeSince)
				if !stale {
					return
				}
				// Unlike order-only copy, this needs no complete history: a
				// reorder of Rook's monitor sets is no evidence of a stale
				// value, so the Issue reads as if the reorder never happened.
				if change.onlyReorderedSince(startedAt) && isRookMonOrderInsensitiveKey(cachedSecret(cache, pod.Namespace, secretName), key) {
					if !slices.ContainsFunc(containerRookReorders, func(read rookMonReorderRead) bool { return read.secretName == secretName }) {
						containerRookReorders = append(containerRookReorders, rookMonReorderRead{pod: pod, container: container.Name, referenceKind: referenceKind,
							prefix: prefix, secretName: secretName, startedAt: startedAt, complete: !startedAt.Before(completeSince)})
					}
					return
				}
				containerChecks = append(containerChecks, newStaleSecretEnvCheck(pod, container.Name, envName, referenceKind, prefix, secretName, key, startedAt, changedAt, orderOnly))
			}
			for _, env := range container.Env {
				if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					continue
				}
				ref := env.ValueFrom.SecretKeyRef
				change, ok := changes[secretDataKey{namespace: pod.Namespace, name: ref.Name, key: ref.Key}]
				if !ok || !secretExists(cache, pod.Namespace, ref.Name, exists, checked) {
					continue
				}
				add(env.Name, "secretKeyRef", "", ref.Name, ref.Key, change)
			}
			for _, envFrom := range container.EnvFrom {
				if envFrom.SecretRef == nil || !secretExists(cache, pod.Namespace, envFrom.SecretRef.Name, exists, checked) {
					continue
				}
				identity := pod.Namespace + "\x00" + envFrom.SecretRef.Name
				for _, change := range changesBySecret[identity] {
					if envFromKeyOverridden(container, envFrom.Prefix+change.key) {
						continue
					}
					add(envFrom.Prefix+change.key, "envFrom", envFrom.Prefix, envFrom.SecretRef.Name, change.key, change.secretKeyChange)
				}
			}
			// The note says the reorder was the only change Radar saw, so a
			// container with any other change to that Secret doesn't get it.
			for _, read := range containerRookReorders {
				if !slices.ContainsFunc(containerChecks, func(check StaleSecretEnvCheck) bool { return check.SecretName == read.secretName }) {
					rookReorders = append(rookReorders, read)
				}
			}
			// The cap may only drop order-only checks, so "every check is
			// order-only" stays a statement about every changed key.
			sort.SliceStable(containerChecks, func(i, j int) bool {
				return !containerChecks[i].OrderOnly && containerChecks[j].OrderOnly
			})
			if len(containerChecks) > perContainer {
				containerChecks = containerChecks[:perContainer]
			}
			out = append(out, containerChecks...)
		}
	}
	sortStaleSecretEnvChecks(out)
	return out, rookReorders
}

func cachedSecret(cache *ResourceCache, namespace, name string) *corev1.Secret {
	secret, err := cache.Secrets().Secrets(namespace).Get(name)
	if err != nil {
		return nil
	}
	return secret
}

// envHistoryCompleteSince is the earliest time from which Radar holds every
// change it observed: after this process started watching, after anything
// the store evicted or failed to record for Secrets or ConfigMaps, and inside
// the env-history lookback. A container that started earlier may have missed
// a change Radar never saw.
func envHistoryCompleteSince(now time.Time) time.Time {
	started := timeline.ObservationStart()
	if started.IsZero() {
		return now
	}
	since := now.Add(-envHistoryContextMaxLookback)
	if started.After(since) {
		since = started
	}
	for _, boundary := range []time.Time{timeline.HistoryEvictedThrough(), timeline.LastStoreFailure("Secret"), timeline.LastStoreFailure("ConfigMap")} {
		if boundary.After(since) {
			since = boundary
		}
	}
	return since
}

func findStaleSecretEnvChecksWithManagedFields(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent) []StaleSecretEnvCheck {
	return findStaleSecretEnvChecksWithManagedFieldsUpTo(cache, pods, events, maxStaleSecretEnvChecksPerContainer)
}

func findStaleSecretEnvChecksWithManagedFieldsUpTo(cache *ResourceCache, pods []*corev1.Pod, events []timeline.TimelineEvent, perContainer int) []StaleSecretEnvCheck {
	out, rookReorders := findStaleSecretEnvChecksAndRookReorders(cache, pods, events, perContainer)
	if perContainer <= 0 {
		perContainer = math.MaxInt
	}
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
				if checksPerContainer[containerIdentity] >= perContainer || secretName == "" {
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
				write, ok := secretDataManagerLastWrite(cache, pod.Namespace, secretName)
				if !ok || !startedAt.Before(write.at) {
					return
				}
				out = append(out, newManagedFieldsStaleSecretEnvCheck(pod, container.Name, referenceKind, prefix, secretName, startedAt, write))
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
	// A Rook monitor list reorder raises no Issue, but the write still
	// happened after these containers started; it gets the same soft,
	// keyless row as any other write Radar can't call a data change. It needs
	// no action, so it only takes cap space the rows above left free.
	for _, read := range rookReorders {
		containerIdentity := read.pod.Namespace + "\x00" + read.pod.Name + "\x00" + read.container
		if checksPerContainer[containerIdentity] >= perContainer {
			continue
		}
		// History matches Secrets by name, so a Secret recreated after the
		// container started may not be the one whose reorder Radar saw.
		if secret := cachedSecret(cache, read.pod.Namespace, read.secretName); secret == nil || !secret.CreationTimestamp.Time.Before(read.startedAt) {
			continue
		}
		write, ok := secretDataManagerLastWrite(cache, read.pod.Namespace, read.secretName)
		if !ok || !read.startedAt.Before(write.at) {
			continue
		}
		out = append(out, newRookMonReorderStaleSecretEnvCheck(read, write))
		checksPerContainer[containerIdentity]++
	}
	sortStaleSecretEnvChecks(out)
	return out
}

// envFromKeyOverridden reports whether an explicit env entry sets the env var
// that an envFrom source supplies; explicit env wins over envFrom unless it
// expands $(variable) and so still depends on that value. A later envFrom
// source with the same key also wins, but only its current keys are known, not
// the ones the container started with, so that case is not checked.
func envFromKeyOverridden(container corev1.Container, variable string) bool {
	for _, env := range container.Env {
		if env.Name == variable {
			return !strings.Contains(env.Value, "$("+variable+")")
		}
	}
	return false
}

func sortStaleSecretEnvChecks(out []StaleSecretEnvCheck) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].EvidenceSource != out[j].EvidenceSource {
			return out[i].EvidenceSource == staleSecretEvidenceObservedChange
		}
		if out[i].NoRestartNeeded != out[j].NoRestartNeeded {
			return out[j].NoRestartNeeded
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

// secretKeyChange is what Radar observed for one Secret key: the latest
// change of any kind, and the latest one that did more than reorder the
// value's comma-separated entries. Each is a maximum of its own, so the
// event order doesn't matter.
type secretKeyChange struct {
	changedAt     time.Time
	realChangedAt time.Time
}

func (c *secretKeyChange) observe(at time.Time, orderOnly bool) {
	if at.After(c.changedAt) {
		c.changedAt = at
	}
	if !orderOnly && at.After(c.realChangedAt) {
		c.realChangedAt = at
	}
}

func (c *secretKeyChange) observeAdd(at time.Time) {
	if at.After(c.realChangedAt) {
		c.realChangedAt = at
	}
}

// since classifies the key for a container that started at startedAt. A key
// whose only changes after the start were reorders is order-only, but only
// when Radar's history is complete since the container started; otherwise an
// earlier change Radar never saw may also apply, and it reads as a change.
func (c secretKeyChange) since(startedAt, completeSince time.Time) (changedAt time.Time, orderOnly, stale bool) {
	switch {
	case !startedAt.Before(c.changedAt):
		return time.Time{}, false, false
	case startedAt.Before(c.realChangedAt):
		return c.realChangedAt, false, true
	case startedAt.Before(completeSince):
		return c.changedAt, false, true
	default:
		return c.changedAt, true, true
	}
}

// onlyReorderedSince reports whether every change Radar observed after
// startedAt only reordered the value's entries.
func (c secretKeyChange) onlyReorderedSince(startedAt time.Time) bool {
	return startedAt.Before(c.changedAt) && !startedAt.Before(c.realChangedAt)
}

type secretDataChange struct {
	key string
	secretKeyChange
}

func secretDataChangesBySecret(changes map[secretDataKey]secretKeyChange) map[string][]secretDataChange {
	out := make(map[string][]secretDataChange)
	for identity, change := range changes {
		secret := identity.namespace + "\x00" + identity.name
		out[secret] = append(out[secret], secretDataChange{key: identity.key, secretKeyChange: change})
	}
	for secret := range out {
		sort.Slice(out[secret], func(i, j int) bool { return out[secret][i].key < out[secret][j].key })
	}
	return out
}

func newStaleSecretEnvCheck(pod *corev1.Pod, container, envName, referenceKind, prefix, secretName, key string, startedAt, changedAt time.Time, orderOnly bool) StaleSecretEnvCheck {
	message := fmt.Sprintf("Container %s started at %s, before Radar observed Secret/%s key %s change at %s; Kubernetes does not refresh env %s in a running container.", container, startedAt.UTC().Format(time.RFC3339), secretName, key, changedAt.UTC().Format(time.RFC3339), envName)
	if orderOnly {
		message = fmt.Sprintf("Container %s started at %s, before Radar observed the comma-separated entries of Secret/%s key %s reordered at %s, with none added or removed. Env %s in the running container keeps the old order, which matters only if the app treats the new order differently.", container, startedAt.UTC().Format(time.RFC3339), secretName, key, changedAt.UTC().Format(time.RFC3339), envName)
	}
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
		OrderOnly:          orderOnly,
		Message:            message,
	}
}

// newManagedFieldsStaleSecretEnvCheck names the writer as recorded in the
// Secret's managedFields, unmapped. A manager's timestamp moves on a write to
// any field it owns, and the index keeps an earlier data timestamp across a
// later metadata-only update, so the note claims only that this manager
// wrote at that time, not that it was the latest write or a data change. When
// every named writer is a release tool or secret operator, the note says that
// writer also writes Secret labels or annotations, which can move its
// timestamp without a data change. The names are Secret
// metadata: they reach only callers the resource context lets read the
// Secret, which drops the whole row for anyone else.
func newManagedFieldsStaleSecretEnvCheck(pod *corev1.Pod, container, referenceKind, prefix, secretName string, startedAt time.Time, write secretDataManagerWrite) StaleSecretEnvCheck {
	changedAt := write.at
	writer, names := managedFieldsWriter(write)
	hedge := "managedFields cannot identify which field changed, so the write may not have touched data at all"
	if len(names) > 0 && !slices.ContainsFunc(names, func(name string) bool { return !writesSecretMetadata(name) }) {
		verb := "writes"
		if len(names) > 1 {
			verb = "write"
		}
		hedge = fmt.Sprintf("%s also %s Secret labels or annotations, so managedFields cannot tell whether this write changed data", strings.Join(names, " and "), verb)
	}
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
		Message:            fmt.Sprintf("Secret/%s had a write at %s by %s, after container %s started at %s; %s, and if the write touched a consumed key the running process may hold a stale env value until restart.", secretName, changedAt.UTC().Format(time.RFC3339), writer, container, startedAt.UTC().Format(time.RFC3339), hedge),
	}
}

func managedFieldsWriter(write secretDataManagerWrite) (string, []string) {
	names := slices.DeleteFunc(slices.Clone(write.managers), isUnnamedManagedFieldsManager)
	switch len(names) {
	case 0:
		return "a field manager that owns its data", names
	case 1:
		return names[0] + ", a field manager that owns its data", names
	default:
		return strings.Join(names, " and ") + ", field managers that own its data", names
	}
}

// newRookMonReorderStaleSecretEnvCheck is the soft row for a container whose
// only observed monitor list change was a reorder. It keeps the managedFields
// row's shape (no key, the writer's last write time) so every surface treats
// it as a note, never an Issue, and says what Radar saw instead of the
// generic "may not have touched data" hedge.
func newRookMonReorderStaleSecretEnvCheck(read rookMonReorderRead, write secretDataManagerWrite) StaleSecretEnvCheck {
	writer, _ := managedFieldsWriter(write)
	message := fmt.Sprintf("Secret/%s had a write at %s by %s, after container %s started at %s. Since then, the only change Radar saw to the monitor list this container reads from it was a reorder, and Ceph reads that list as a set, so no restart is needed for it.",
		read.secretName, write.at.UTC().Format(time.RFC3339), writer, read.container, read.startedAt.UTC().Format(time.RFC3339))
	if !read.complete {
		message += " Radar's change history doesn't cover this container's whole run, so it can't rule out an earlier monitor change it missed."
	}
	return StaleSecretEnvCheck{
		Namespace:          read.pod.Namespace,
		PodName:            read.pod.Name,
		Container:          read.container,
		ReferenceKind:      read.referenceKind,
		EvidenceSource:     staleSecretEvidenceManagedFields,
		Prefix:             read.prefix,
		SecretName:         read.secretName,
		ContainerStartedAt: read.startedAt,
		SecretChangedAt:    write.at,
		NoRestartNeeded:    true,
		HistoryIncomplete:  !read.complete,
		Message:            message,
	}
}

// writesSecretMetadata reports the field managers of release tools and secret
// operators: Helm, Flux, Argo CD, External Secrets Operator (manager
// external-secrets, or externalsecrets.external-secrets.io/<name> in newer
// releases) and cert-manager. Each writes its own labels or annotations onto
// the Secrets it manages, so a release or a re-sync that leaves data unchanged
// still moves its managedFields time.
func writesSecretMetadata(manager string) bool {
	switch manager {
	case "helm", "helm-controller", "kustomize-controller":
		return true
	}
	return strings.HasPrefix(manager, "argocd") ||
		strings.Contains(manager, "external-secrets") ||
		strings.HasPrefix(manager, "cert-manager")
}

// isUnnamedManagedFieldsManager reports managedFields entries that name no
// actual writer: an empty manager, and the buckets the API server creates
// itself, ancient-changes (older update managers compacted together) and
// before-first-apply (fields owned before the first server-side apply).
func isUnnamedManagedFieldsManager(manager string) bool {
	switch manager {
	case "", "ancient-changes", "before-first-apply":
		return true
	}
	return false
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

func secretDataManagerLastWrite(cache *ResourceCache, namespace, name string) (secretDataManagerWrite, bool) {
	if cache == nil || cache.secretWriteTimes == nil || cache.Secrets() == nil {
		return secretDataManagerWrite{}, false
	}
	write, ok := cache.secretWriteTimes.load(namespace, name)
	if !ok || write.at.IsZero() {
		return secretDataManagerWrite{}, false
	}
	secret, err := cache.Secrets().Secrets(namespace).Get(name)
	if err != nil || secret == nil || secret.Type == corev1.SecretType("helm.sh/release.v1") || string(secret.UID) != write.uid {
		return secretDataManagerWrite{}, false
	}
	return write, true
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

func latestSecretDataChanges(events []timeline.TimelineEvent) map[secretDataKey]secretKeyChange {
	out := make(map[secretDataKey]secretKeyChange)
	for _, event := range events {
		if event.Kind != "Secret" || event.Source != timeline.SourceInformer || event.EventType != timeline.EventTypeUpdate || event.Diff == nil || event.Timestamp.IsZero() {
			continue
		}
		reordered := reorderedKeysByField(event.Diff.Fields)
		for _, field := range event.Diff.Fields {
			added := field.Path == "data (added keys)" || field.Path == "stringData (added keys)"
			if !added && !isSecretDataModificationPath(field.Path) {
				continue
			}
			keys := stringValues(field.NewValue)
			if len(keys) == 0 {
				keys = stringValues(field.OldValue)
			}
			modified := strings.HasSuffix(field.Path, " (modified keys)")
			for _, key := range keys {
				if key == "" {
					continue
				}
				identity := secretDataKey{namespace: event.Namespace, name: event.Name, key: key}
				change := out[identity]
				if added {
					// An added key can't make a running value stale, but a
					// container that started before the add doesn't hold the
					// key at all, so a later reorder isn't order-only for it.
					change.observeAdd(event.Timestamp)
				} else {
					change.observe(event.Timestamp, modified && reordered[keyListFieldBase(field.Path)][key])
				}
				out[identity] = change
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
	if staleSecretEnvOrderOnly(checks) {
		return fmt.Sprintf("Only the order of entries changed in Secret data that this not-Ready pod's running containers read as env: %s. Check whether the order matters to the app before restarting the pod.", staleSecretEnvFacts(checks))
	}
	return fmt.Sprintf("The pod is not Ready, and running containers in it started before Radar observed Secret keys they reference change: %s. Restart the pod to refresh them.", staleSecretEnvFacts(checks))
}
