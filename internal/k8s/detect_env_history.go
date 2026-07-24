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
	return findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, querySecretDataHistory(ctx, pod.Namespace, staleSecretEnvHistorySince([]*corev1.Pod{pod}, now)))
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
		events := querySecretDataHistory(ctx, namespace, staleSecretEnvHistorySince(podsByNamespace[namespace], now))
		checks := groupStaleSecretEnvChecks(findStaleSecretEnvChecksWithManagedFields(cache, podsByNamespace[namespace], events))
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
	usesSecretEnv := false
	for _, pod := range pods {
		if podHasSecretEnvReference(pod) {
			usesSecretEnv = true
			break
		}
	}
	if !usesSecretEnv {
		return nil
	}
	checks := findStaleSecretEnvChecks(cache, pods, querySecretDataHistory(context.Background(), namespace, staleSecretEnvHistorySince(pods, now)))
	checksByPod := make(map[string][]StaleSecretEnvCheck)
	for _, check := range checks {
		key := check.Namespace + "\x00" + check.PodName
		checksByPod[key] = append(checksByPod[key], check)
	}
	var out []Detection
	for _, pod := range pods {
		podChecks := checksByPod[pod.Namespace+"\x00"+pod.Name]
		bitingChecks := staleSecretEnvBitingChecks(pod, podChecks)
		if len(bitingChecks) == 0 {
			continue
		}
		changedAt := bitingChecks[0].SecretChangedAt
		for _, check := range bitingChecks[1:] {
			if check.SecretChangedAt.Before(changedAt) {
				changedAt = check.SecretChangedAt
			}
		}
		age := ageSeconds(now, changedAt)
		ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
		out = append(out, Detection{
			Kind:            "Pod",
			Namespace:       pod.Namespace,
			Name:            pod.Name,
			Severity:        "warning",
			Reason:          "StaleSecretEnv",
			Message:         staleSecretEnvDetectionMessage(bitingChecks),
			Age:             FormatAge(time.Duration(age) * time.Second),
			AgeSeconds:      age,
			Duration:        FormatAge(time.Duration(age) * time.Second),
			DurationSeconds: age,
			OwnerGroup:      ownerGroup,
			OwnerKind:       ownerKind,
			OwnerName:       ownerName,
			Fingerprint:     "stale-secret-env",
			Cause:           "A running container loaded a Secret-backed environment value before Radar observed that Secret key change.",
			Action:          "Restart the pod so its containers re-read Secret-backed environment variables.",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > maxStaleSecretEnvDetectionsPerNamespace {
		out = out[:maxStaleSecretEnvDetectionsPerNamespace]
	}
	return out
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

func querySecretDataHistory(ctx context.Context, namespace string, since time.Time) []timeline.TimelineEvent {
	opts := timeline.QueryOptions{
		Kinds:          []string{"Secret"},
		Since:          since,
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     []timeline.EventType{timeline.EventTypeUpdate},
		ClusterContext: ActiveClusterContext(),
		Limit:          1000,
	}
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	}
	return queryEnvHistory(ctx, opts)
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

func staleSecretEnvBitingChecks(pod *corev1.Pod, checks []StaleSecretEnvCheck) []StaleSecretEnvCheck {
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return nil
	}
	readyKnown, ready := false, false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			readyKnown = true
			ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	if !readyKnown || ready {
		return nil
	}
	// Only a Running-but-not-Ready container can be holding a stale value: a
	// Waiting or crashlooping container re-reads Secret-backed env on its next
	// (re)start, so it is deliberately outside this gate. Restartable init
	// containers (native sidecars) run for the pod's lifetime and their
	// readiness feeds the Pod Ready condition, so a not-Ready one bites exactly
	// like a regular container; pure init containers are already excluded.
	bitingContainers := make(map[string]bool)
	restartable := restartableInitContainerNames(pod)
	for _, status := range pod.Status.InitContainerStatuses {
		if restartable[status.Name] && status.State.Running != nil && !status.Ready {
			bitingContainers[status.Name] = true
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil && !status.Ready {
			bitingContainers[status.Name] = true
		}
	}
	var out []StaleSecretEnvCheck
	for _, check := range checks {
		if check.EvidenceSource == staleSecretEvidenceObservedChange && bitingContainers[check.Container] {
			out = append(out, check)
		}
	}
	return out
}

func staleSecretEnvDetectionMessage(checks []StaleSecretEnvCheck) string {
	if len(checks) == 1 {
		check := checks[0]
		return fmt.Sprintf("Container %s is not Ready and may be running a stale env value for Secret/%s key %s; restart the pod to refresh it.", check.Container, check.SecretName, check.Key)
	}
	return fmt.Sprintf("A not-Ready container may be running %d stale Secret-backed env values; restart the pod to refresh them.", len(checks))
}
