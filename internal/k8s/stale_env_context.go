package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

// StaleEnvFact is an env var that running containers read from a Secret or
// ConfigMap key before that key's last data write. Kubernetes resolves env
// values only when a container starts, so those containers keep the earlier
// value until they restart. Volume mounts are not covered: the kubelet
// refreshes them in place. Rows are grouped across Pods: Pod names one of
// AffectedPods Pods, Pods lists their names, and the times are that Pod's.
//
// Secret rows reuse the stale-Secret-env check diagnose runs, including its
// managedFields fallback for writes made before Radar started (Key and Env are
// empty on those rows). ConfigMap rows come only from changes Radar observed.
type StaleEnvFact struct {
	Pod                string
	Pods               []string
	AffectedPods       int
	Container          string
	Env                string
	SourceKind         string
	SourceName         string
	Key                string
	ReferenceKind      string
	EvidenceSource     string
	ContainerStartedAt time.Time
	ChangedAt          time.Time
	Message            string
}

// FindStaleEnvForPods returns the stale env facts of these Pods' Secret- and
// ConfigMap-backed env vars. The result is not capped; callers cap what they
// show.
func FindStaleEnvForPods(ctx context.Context, cache *ResourceCache, pods []*corev1.Pod) []StaleEnvFact {
	return findStaleEnv(ctx, cache, pods, true, true, time.Now())
}

// FindStaleEnvConsumersOf returns the stale env facts of the Pods in the
// Secret's or ConfigMap's namespace whose env reads it, limited to that
// source.
func FindStaleEnvConsumersOf(ctx context.Context, cache *ResourceCache, kind, namespace, name string) []StaleEnvFact {
	if cache == nil || cache.Pods() == nil || (kind != "Secret" && kind != "ConfigMap") {
		return nil
	}
	pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var consumers []*corev1.Pod
	for _, pod := range pods {
		if podEnvReadsSource(pod, kind, name) {
			consumers = append(consumers, pod)
		}
	}
	facts := findStaleEnv(ctx, cache, consumers, kind == "Secret", kind == "ConfigMap", time.Now())
	out := facts[:0]
	for _, fact := range facts {
		if fact.SourceKind == kind && fact.SourceName == name {
			out = append(out, fact)
		}
	}
	return out
}

func findStaleEnv(ctx context.Context, cache *ResourceCache, pods []*corev1.Pod, secrets, configMaps bool, now time.Time) []StaleEnvFact {
	if cache == nil {
		return nil
	}
	podsByNamespace := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		if pod != nil {
			podsByNamespace[pod.Namespace] = append(podsByNamespace[pod.Namespace], pod)
		}
	}
	var rows []StaleEnvFact
	for namespace, nsPods := range podsByNamespace {
		if secrets && cache.Secrets() != nil {
			var secretPods []*corev1.Pod
			for _, pod := range nsPods {
				if podHasSecretEnvReference(pod) {
					secretPods = append(secretPods, pod)
				}
			}
			if len(secretPods) > 0 {
				events := querySecretDataHistory(ctx, namespace, secretPods, staleSecretEnvHistorySince(secretPods, now))
				for _, check := range findStaleSecretEnvChecksWithManagedFieldsUpTo(cache, secretPods, events, 0) {
					rows = append(rows, StaleEnvFact{
						Pod:                check.PodName,
						Container:          check.Container,
						Env:                check.EnvName,
						SourceKind:         "Secret",
						SourceName:         check.SecretName,
						Key:                check.Key,
						ReferenceKind:      check.ReferenceKind,
						EvidenceSource:     check.EvidenceSource,
						ContainerStartedAt: check.ContainerStartedAt,
						ChangedAt:          check.SecretChangedAt,
						Message:            check.Message,
					})
				}
			}
		}
		if configMaps {
			rows = append(rows, findStaleConfigMapEnv(ctx, cache, namespace, nsPods, now)...)
		}
	}
	return groupStaleEnvFacts(rows)
}

// findStaleConfigMapEnv is the ConfigMap counterpart of
// findStaleSecretEnvChecks: a running container that reads a ConfigMap key as
// env and started before Radar observed that key modified or removed. Added
// keys are skipped, as for Secrets: an env var can't hold a value that didn't
// exist when the container started.
func findStaleConfigMapEnv(ctx context.Context, cache *ResourceCache, namespace string, pods []*corev1.Pod, now time.Time) []StaleEnvFact {
	if cache.ConfigMaps() == nil {
		return nil
	}
	sources := make(map[envresolve.SourceID]envresolve.SourceData)
	// binaryData keys never become env vars; their diffs share the
	// "(modified keys)" path suffix with data, so they are skipped by name.
	binaryKeys := make(map[string]map[string][]byte)
	var consumers []*corev1.Pod
	var names []string
	for _, pod := range pods {
		reads := false
		for _, container := range staleSecretEnvContainers(pod) {
			for _, name := range containerConfigMapEnvNames(container) {
				reads = true
				id := envresolve.SourceID{Kind: "ConfigMap", Name: name}
				if _, seen := sources[id]; seen {
					continue
				}
				// The current key inventory lets latestPodEnvSourceChanges match
				// structured data.<key> diffs, including dotted keys.
				cm, err := cache.ConfigMaps().ConfigMaps(namespace).Get(name)
				if err != nil || cm == nil {
					sources[id] = envresolve.SourceData{}
					continue
				}
				keys := make([]string, 0, len(cm.Data))
				for key := range cm.Data {
					keys = append(keys, key)
				}
				sources[id] = envresolve.SourceData{Kind: "ConfigMap", Name: name, Keys: keys}
				binaryKeys[name] = cm.BinaryData
				names = append(names, name)
			}
		}
		if reads {
			consumers = append(consumers, pod)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	events := queryEnvHistory(ctx, timeline.QueryOptions{
		Namespaces:     []string{namespace},
		Kinds:          []string{"ConfigMap"},
		Names:          names,
		Since:          staleSecretEnvHistorySince(consumers, now),
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     []timeline.EventType{timeline.EventTypeUpdate},
		ClusterContext: ActiveClusterContext(),
		IncludeManaged: true,
		Limit:          maxSecretDataHistoryEvents,
	})
	// A saturated history can't say which write was last; report nothing.
	if len(events) >= maxSecretDataHistoryEvents {
		return nil
	}
	changes := latestPodEnvSourceChanges(events, sources)
	if len(changes) == 0 {
		return nil
	}
	stale := func(change podEnvSourceChange, startedAt time.Time) bool {
		return (change.kind == "modified" || change.kind == "removed") && startedAt.Before(change.changedAt)
	}
	var out []StaleEnvFact
	for _, pod := range consumers {
		statuses := staleSecretEnvContainerStatuses(pod)
		for _, container := range staleSecretEnvContainers(pod) {
			status, ok := statuses[container.Name]
			if !ok || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
				continue
			}
			startedAt := status.State.Running.StartedAt.Time
			for _, env := range container.Env {
				if env.ValueFrom == nil || env.ValueFrom.ConfigMapKeyRef == nil {
					continue
				}
				ref := env.ValueFrom.ConfigMapKeyRef
				change, ok := changes[podEnvSourceKey{kind: "ConfigMap", namespace: namespace, name: ref.Name, key: ref.Key}]
				if ok && stale(change, startedAt) {
					out = append(out, newStaleConfigMapEnvFact(pod, container.Name, env.Name, "configMapKeyRef", ref.Name, ref.Key, startedAt, change.changedAt))
				}
			}
			for _, from := range container.EnvFrom {
				if from.ConfigMapRef == nil {
					continue
				}
				for identity, change := range changes {
					if identity.kind != "ConfigMap" || identity.name != from.ConfigMapRef.Name || !stale(change, startedAt) {
						continue
					}
					if _, binary := binaryKeys[identity.name][identity.key]; binary {
						continue
					}
					variable := from.Prefix + identity.key
					if len(validation.IsEnvVarName(variable)) != 0 || envFromKeyOverridden(container, variable) {
						continue
					}
					out = append(out, newStaleConfigMapEnvFact(pod, container.Name, variable, "envFrom", from.ConfigMapRef.Name, identity.key, startedAt, change.changedAt))
				}
			}
		}
	}
	return out
}

func newStaleConfigMapEnvFact(pod *corev1.Pod, container, env, referenceKind, name, key string, startedAt, changedAt time.Time) StaleEnvFact {
	return StaleEnvFact{
		Pod:                pod.Name,
		Container:          container,
		Env:                env,
		SourceKind:         "ConfigMap",
		SourceName:         name,
		Key:                key,
		ReferenceKind:      referenceKind,
		EvidenceSource:     staleSecretEvidenceObservedChange,
		ContainerStartedAt: startedAt,
		ChangedAt:          changedAt,
	}
}

func containerConfigMapEnvNames(container corev1.Container) []string {
	var names []string
	for _, env := range container.Env {
		if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil && env.ValueFrom.ConfigMapKeyRef.Name != "" {
			names = append(names, env.ValueFrom.ConfigMapKeyRef.Name)
		}
	}
	for _, from := range container.EnvFrom {
		if from.ConfigMapRef != nil && from.ConfigMapRef.Name != "" {
			names = append(names, from.ConfigMapRef.Name)
		}
	}
	return names
}

// podEnvReadsSource reports whether a container that can hold a stale value
// reads the Secret or ConfigMap as env. Volume references don't count.
func podEnvReadsSource(pod *corev1.Pod, kind, name string) bool {
	if pod == nil {
		return false
	}
	for _, container := range staleSecretEnvContainers(pod) {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if kind == "Secret" && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
			if kind == "ConfigMap" && env.ValueFrom.ConfigMapKeyRef != nil && env.ValueFrom.ConfigMapKeyRef.Name == name {
				return true
			}
		}
		for _, from := range container.EnvFrom {
			if kind == "Secret" && from.SecretRef != nil && from.SecretRef.Name == name {
				return true
			}
			if kind == "ConfigMap" && from.ConfigMapRef != nil && from.ConfigMapRef.Name == name {
				return true
			}
		}
	}
	return false
}

// groupStaleEnvFacts folds rows that differ only by Pod into one row naming
// the first Pod by name, and writes each row's message.
func groupStaleEnvFacts(rows []StaleEnvFact) []StaleEnvFact {
	type group struct {
		fact StaleEnvFact
		pods map[string]bool
	}
	byKey := make(map[string]*group)
	for _, row := range rows {
		key := row.Container + "\x00" + row.Env + "\x00" + row.SourceKind + "\x00" + row.SourceName + "\x00" +
			row.Key + "\x00" + row.ReferenceKind + "\x00" + row.EvidenceSource
		current := byKey[key]
		if current == nil {
			current = &group{fact: row, pods: make(map[string]bool)}
			byKey[key] = current
		}
		current.pods[row.Pod] = true
		if row.Pod < current.fact.Pod {
			current.fact = row
		}
	}
	out := make([]StaleEnvFact, 0, len(byKey))
	for _, current := range byKey {
		fact := current.fact
		fact.AffectedPods = len(current.pods)
		for pod := range current.pods {
			fact.Pods = append(fact.Pods, pod)
		}
		sort.Strings(fact.Pods)
		fact.Message = staleEnvFactMessage(fact)
		out = append(out, fact)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.EvidenceSource != b.EvidenceSource {
			return a.EvidenceSource == staleSecretEvidenceObservedChange
		}
		if a.SourceKind != b.SourceKind {
			return a.SourceKind > b.SourceKind // Secret before ConfigMap
		}
		if a.SourceName != b.SourceName {
			return a.SourceName < b.SourceName
		}
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		if a.Env != b.Env {
			return a.Env < b.Env
		}
		return a.Pod < b.Pod
	})
	return out
}

func staleEnvFactMessage(fact StaleEnvFact) string {
	pod := "Pod " + fact.Pod
	if fact.AffectedPods > 1 {
		pod = fmt.Sprintf("Pod %s (and %d other Pods)", fact.Pod, fact.AffectedPods-1)
	}
	if fact.EvidenceSource != staleSecretEvidenceObservedChange {
		// The managedFields fallback can't name a key; keep the check's hedged note.
		return pod + ": " + fact.Message
	}
	via := ""
	if fact.ReferenceKind == "envFrom" {
		via = " through envFrom"
	}
	return fmt.Sprintf("%s container %s reads %s/%s key %s%s as env %s and started at %s, before Radar observed a write to that key at %s. Env values don't refresh, so it likely still holds the earlier value until it restarts.",
		pod, fact.Container, fact.SourceKind, fact.SourceName, fact.Key, via, fact.Env,
		fact.ContainerStartedAt.UTC().Format(time.RFC3339), fact.ChangedAt.UTC().Format(time.RFC3339))
}
