package k8s

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

const maxPodEnvSourceHistoryEvents = 1000

type PodEnvChange struct {
	Container string
	Variable  string
	Source    envresolve.SourceRef
	Evidence  envresolve.ChangeEvidence
}

type podEnvSourceChange struct {
	kind      string
	changedAt time.Time
}

type podEnvSourceKey struct {
	kind      string
	namespace string
	name      string
	key       string
}

func FindPodEnvChanges(ctx context.Context, cache *ResourceCache, pod *corev1.Pod, sources map[envresolve.SourceID]envresolve.SourceData) ([]PodEnvChange, envresolve.Coverage) {
	var coverage envresolve.Coverage
	store := timeline.GetStore()
	if store == nil || pod == nil {
		return nil, coverage
	}
	stats := store.Stats()
	if !stats.OldestEvent.IsZero() {
		observedSince := stats.OldestEvent
		coverage.ObservedSince = &observedSince
	}
	coverage.Degraded = stats.Degraded
	if stats.Degraded {
		coverage.DegradedReason = "Change history is limited to this Radar session."
	}

	names := podEnvSourceNames(pod)
	if len(names) == 0 {
		return nil, coverage
	}
	events, err := store.Query(ctx, timeline.QueryOptions{
		Namespaces:     []string{pod.Namespace},
		Kinds:          []string{"ConfigMap", "Secret"},
		Names:          names,
		Since:          podEnvEvidenceHistorySince(pod, time.Now()),
		Sources:        []timeline.EventSource{timeline.SourceInformer},
		EventTypes:     []timeline.EventType{timeline.EventTypeUpdate},
		ClusterContext: ActiveClusterContext(),
		IncludeManaged: true,
		Limit:          maxPodEnvSourceHistoryEvents,
	})
	if err != nil {
		log.Printf("[environment] Failed to query change history: %q", err.Error())
		coverage.Degraded = true
		coverage.DegradedReason = "Recent changes could not be checked."
		events = nil
	}
	coverage.Saturated = len(events) >= maxPodEnvSourceHistoryEvents
	changes := latestPodEnvSourceChanges(events, sources)

	var out []PodEnvChange
	for _, check := range FindStaleSecretEnvChecksForObject(ctx, cache, pod) {
		identity := podEnvSourceKey{kind: "Secret", namespace: pod.Namespace, name: check.SecretName, key: check.Key}
		changeKind := "modified"
		if change, ok := changes[identity]; ok && change.changedAt.Equal(check.SecretChangedAt) {
			changeKind = change.kind
		}
		out = append(out, newPodEnvChange(
			check.Container, check.EnvName, "Secret", check.SecretName, check.Key, changeKind, check.SecretChangedAt,
		))
	}

	statuses := podEnvEvidenceContainerStatuses(pod)
	for _, container := range podEnvEvidenceContainers(pod) {
		status, ok := statuses[container.Name]
		if !ok || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
			continue
		}
		startedAt := status.State.Running.StartedAt.Time
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
				appendCurrentSourceChange(&out, changes, pod.Namespace, container.Name, env.Name, "ConfigMap", ref.Name, ref.Key, startedAt, false)
			}
			if ref := env.ValueFrom.SecretKeyRef; ref != nil {
				appendCurrentSourceChange(&out, changes, pod.Namespace, container.Name, env.Name, "Secret", ref.Name, ref.Key, startedAt, true)
			}
		}
	}
	appendEnvFromChanges(&out, pod, changes)
	sortPodEnvChanges(out)
	return deduplicatePodEnvChanges(out), coverage
}

func appendEnvFromChanges(out *[]PodEnvChange, pod *corev1.Pod, changes map[podEnvSourceKey]podEnvSourceChange) {
	statuses := podEnvEvidenceContainerStatuses(pod)
	for _, container := range podEnvEvidenceContainers(pod) {
		status, ok := statuses[container.Name]
		if !ok || status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
			continue
		}
		startedAt := status.State.Running.StartedAt.Time
		for _, from := range container.EnvFrom {
			kind, name, _ := envFromIdentityForEvidence(from)
			if name == "" {
				continue
			}
			for identity, change := range changes {
				if identity.kind != kind || identity.namespace != pod.Namespace || identity.name != name || (kind == "Secret" && change.kind != "added") || !startedAt.Before(change.changedAt) {
					continue
				}
				variable := from.Prefix + identity.key
				if len(validation.IsEnvVarName(variable)) != 0 {
					continue
				}
				*out = append(*out, newPodEnvChange(container.Name, variable, kind, name, identity.key, change.kind, change.changedAt))
			}
		}
	}
}

func appendCurrentSourceChange(out *[]PodEnvChange, changes map[podEnvSourceKey]podEnvSourceChange, namespace, container, variable, kind, name, key string, startedAt time.Time, additionsOnly bool) {
	change, ok := changes[podEnvSourceKey{kind: kind, namespace: namespace, name: name, key: key}]
	if !ok || !startedAt.Before(change.changedAt) || (additionsOnly && change.kind != "added") {
		return
	}
	*out = append(*out, newPodEnvChange(container, variable, kind, name, key, change.kind, change.changedAt))
}

func podEnvSourceNames(pod *corev1.Pod) []string {
	names := make(map[string]bool)
	for _, container := range podEnvEvidenceContainers(pod) {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef != nil {
				names[env.ValueFrom.ConfigMapKeyRef.Name] = true
			}
			if env.ValueFrom.SecretKeyRef != nil {
				names[env.ValueFrom.SecretKeyRef.Name] = true
			}
		}
		for _, from := range container.EnvFrom {
			_, name, _ := envFromIdentityForEvidence(from)
			if name != "" {
				names[name] = true
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

func podEnvEvidenceContainers(pod *corev1.Pod) []corev1.Container {
	out := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	out = append(out, pod.Spec.InitContainers...)
	return append(out, pod.Spec.Containers...)
}

func podEnvEvidenceContainerStatuses(pod *corev1.Pod) map[string]corev1.ContainerStatus {
	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.InitContainerStatuses {
		statuses[status.Name] = status
	}
	for _, status := range pod.Status.ContainerStatuses {
		statuses[status.Name] = status
	}
	return statuses
}

func podEnvEvidenceHistorySince(pod *corev1.Pod, now time.Time) time.Time {
	since := staleSecretEnvHistorySince([]*corev1.Pod{pod}, now)
	floor := now.Add(-envHistoryContextMaxLookback)
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
			continue
		}
		candidate := status.State.Running.StartedAt.Time.Add(-time.Second)
		if candidate.Before(floor) {
			candidate = floor
		}
		if candidate.Before(since) {
			since = candidate
		}
	}
	return since
}

func latestPodEnvSourceChanges(events []timeline.TimelineEvent, sources map[envresolve.SourceID]envresolve.SourceData) map[podEnvSourceKey]podEnvSourceChange {
	out := make(map[podEnvSourceKey]podEnvSourceChange)
	for _, event := range events {
		if (event.Kind != "ConfigMap" && event.Kind != "Secret") || event.Diff == nil || event.Timestamp.IsZero() {
			continue
		}
		for _, field := range event.Diff.Fields {
			changeKind := podEnvChangeKind(field.Path)
			if changeKind == "" && event.Kind == "ConfigMap" {
				matchedKey := ""
				for _, key := range sources[envresolve.SourceID{Kind: "ConfigMap", Name: event.Name}].Keys {
					if (field.Path == "data."+key || strings.HasPrefix(field.Path, "data."+key+".")) && len(key) > len(matchedKey) {
						matchedKey = key
					}
				}
				if matchedKey != "" {
					identity := podEnvSourceKey{kind: event.Kind, namespace: event.Namespace, name: event.Name, key: matchedKey}
					if previous, ok := out[identity]; !ok || event.Timestamp.After(previous.changedAt) {
						out[identity] = podEnvSourceChange{kind: "modified", changedAt: event.Timestamp}
					}
				}
			}
			if changeKind == "" {
				continue
			}
			keys := stringValues(field.NewValue)
			if len(keys) == 0 {
				keys = stringValues(field.OldValue)
			}
			for _, key := range keys {
				identity := podEnvSourceKey{kind: event.Kind, namespace: event.Namespace, name: event.Name, key: key}
				if previous, ok := out[identity]; key != "" && (!ok || event.Timestamp.After(previous.changedAt)) {
					out[identity] = podEnvSourceChange{kind: changeKind, changedAt: event.Timestamp}
				}
			}
		}
	}
	return out
}

func podEnvChangeKind(path string) string {
	switch {
	case strings.HasSuffix(path, "(added keys)"):
		return "added"
	case strings.HasSuffix(path, "(removed keys)"):
		return "removed"
	case strings.HasSuffix(path, "(modified keys)"):
		return "modified"
	default:
		return ""
	}
}

func envFromIdentityForEvidence(from corev1.EnvFromSource) (string, string, bool) {
	if from.ConfigMapRef != nil {
		return "ConfigMap", from.ConfigMapRef.Name, from.ConfigMapRef.Optional != nil && *from.ConfigMapRef.Optional
	}
	if from.SecretRef != nil {
		return "Secret", from.SecretRef.Name, from.SecretRef.Optional != nil && *from.SecretRef.Optional
	}
	return "", "", false
}

func newPodEnvChange(container, variable, kind, name, key, changeKind string, changedAt time.Time) PodEnvChange {
	verb := map[string]string{"added": "was added", "removed": "was removed", "modified": "changed"}[changeKind]
	return PodEnvChange{
		Container: container,
		Variable:  variable,
		Source:    envresolve.SourceRef{Kind: kind, Name: name, Key: key},
		Evidence: envresolve.ChangeEvidence{
			Kind:      changeKind,
			ChangedAt: changedAt,
			Message:   fmt.Sprintf("Radar observed that %s/%s key %s %s after this container started.", kind, name, key, verb),
		},
	}
}

func deduplicatePodEnvChanges(changes []PodEnvChange) []PodEnvChange {
	seen := make(map[string]bool)
	out := make([]PodEnvChange, 0, len(changes))
	for _, change := range changes {
		key := change.Container + "\x00" + change.Variable + "\x00" + change.Source.Kind + "\x00" + change.Source.Name + "\x00" + change.Source.Key + "\x00" + change.Evidence.ChangedAt.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, change)
	}
	return out
}

func sortPodEnvChanges(changes []PodEnvChange) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Container != changes[j].Container {
			return changes[i].Container < changes[j].Container
		}
		if changes[i].Variable != changes[j].Variable {
			return changes[i].Variable < changes[j].Variable
		}
		return changes[i].Evidence.ChangedAt.After(changes[j].Evidence.ChangedAt)
	})
}
