package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
)

const runningPreviousValueNote = "These Pods started before Radar observed this write and read the changed keys as env vars. Env values are set when a container starts, so they likely still hold the earlier value until they restart."

func annotateRunningPreviousValue(ctx context.Context, changes []issuesapi.RecentChange, podsByNamespace map[string][]*corev1.Pod) {
	cache := k8s.GetResourceCache()
	for namespace, pods := range podsByNamespace {
		candidates := map[string]bool{}
		for _, change := range changes {
			if change.Namespace == namespace && (change.Kind == "Secret" || change.Kind == "ConfigMap") && change.ChangeType == "update" {
				candidates[resourceKey(change.Kind, namespace, change.Name)] = true
			}
		}
		var readers []*corev1.Pod
		byName := map[string]*corev1.Pod{}
		for _, pod := range pods {
			// A terminating Pod is on its way out; counting it would keep a
			// finished restart reading as stale.
			if pod.DeletionTimestamp != nil {
				continue
			}
			for _, ref := range DirectConfigReferences(pod) {
				if candidates[resourceKey(ref.Kind, namespace, ref.Name)] {
					readers = append(readers, pod)
					byName[pod.Name] = pod
					break
				}
			}
		}
		if len(readers) == 0 {
			continue
		}
		facts := k8s.FindStaleEnvForPods(ctx, cache, readers)
		for i := range changes {
			change := &changes[i]
			if change.Namespace != namespace || change.ChangeType != "update" {
				continue
			}
			changedAt, err := time.Parse(time.RFC3339, change.Timestamp)
			if err != nil {
				continue
			}
			// Secret rows list their changed keys, so a fact must also name one
			// of them: two writes in the same second can't swap evidence.
			var rowKeys map[string]bool
			if change.Kind == "Secret" {
				rowKeys = secretChangedKeys(change.Fields)
			}
			podNames, keys, workloads := map[string]bool{}, map[string]bool{}, map[string]bool{}
			for _, fact := range facts {
				if fact.EvidenceSource != "observed_change" || fact.SourceKind != change.Kind || fact.SourceName != change.Name || fact.ChangedAt.Unix() != changedAt.Unix() {
					continue
				}
				if rowKeys != nil && !rowKeys[fact.Key] {
					continue
				}
				keys[fact.Key] = true
				for _, name := range fact.Pods {
					podNames[name] = true
					if owner, ok := k8s.PodWorkload(cache, byName[name]); ok {
						workloads[owner.Kind+"/"+owner.Name] = true
					} else {
						workloads["Pod/"+name] = true
					}
				}
			}
			if len(podNames) == 0 {
				continue
			}
			annotation := &issuesapi.RunningPreviousValue{Pods: len(podNames), Note: runningPreviousValueNote}
			for key := range keys {
				annotation.Keys = append(annotation.Keys, key)
			}
			for workload := range workloads {
				annotation.Workloads = append(annotation.Workloads, workload)
			}
			sort.Strings(annotation.Keys)
			sort.Strings(annotation.Workloads)
			if len(annotation.Workloads) > maxConsumersPerEntry {
				annotation.Workloads = annotation.Workloads[:maxConsumersPerEntry]
			}
			change.RunningPreviousValue = annotation
		}
	}
}

func collapseSecretUpdates(changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	sort.SliceStable(changes, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339, changes[i].Timestamp)
		b, _ := time.Parse(time.RFC3339, changes[j].Timestamp)
		return a.After(b)
	})
	out := make([]issuesapi.RecentChange, 0, len(changes))
	groups := map[string]int{}
	newestBySecret := map[string]int{}
	for _, change := range changes {
		if change.Kind != "Secret" || change.ChangeType != "update" {
			out = append(out, change)
			continue
		}
		secret := resourceKey(change.Kind, change.Namespace, change.Name)
		key := secret
		if change.RunningPreviousValue != nil {
			key += "\x00" + strings.Join(change.RunningPreviousValue.Keys, "\x00")
		}
		index, found := groups[key]
		if change.RunningPreviousValue == nil {
			index, found = newestBySecret[secret]
		}
		if !found {
			groups[key] = len(out)
			if _, seen := newestBySecret[secret]; !seen {
				newestBySecret[secret] = len(out)
			}
			out = append(out, change)
			continue
		}
		newest := &out[index]
		if newest.UpdatesInWindow == 0 {
			newest.UpdatesInWindow = 1
		}
		newest.UpdatesInWindow++
		newest.FirstUpdateAt = change.Timestamp
		for _, field := range change.Fields {
			if !strings.HasPrefix(field.Path, "data (") && !strings.HasPrefix(field.Path, "stringData (") {
				continue
			}
			merged := false
			for i := range newest.Fields {
				if newest.Fields[i].Path == field.Path {
					newest.Fields[i].OldValue = mergeChangedKeys(newest.Fields[i].OldValue, field.OldValue)
					newest.Fields[i].NewValue = mergeChangedKeys(newest.Fields[i].NewValue, field.NewValue)
					merged = true
					break
				}
			}
			if !merged {
				newest.Fields = append(newest.Fields, field)
			}
		}
		var summary []string
		for _, field := range newest.Fields {
			keys := mergeChangedKeys(field.OldValue, field.NewValue)
			if keys != nil {
				// "data (modified keys)" reads "data modified keys: [...]", as diffSecret writes it.
				label := strings.TrimSuffix(strings.Replace(field.Path, " (", " ", 1), ")")
				summary = append(summary, fmt.Sprintf("%s: %v", label, keys))
			}
		}
		newest.Summary = strings.Join(summary, "; ")
	}
	return out
}

// secretChangedKeys returns the key names a Secret row's data or stringData
// fields list as added, removed or modified.
func secretChangedKeys(fields []issuesapi.ChangeField) map[string]bool {
	keys := map[string]bool{}
	for _, field := range fields {
		if !strings.HasPrefix(field.Path, "data (") && !strings.HasPrefix(field.Path, "stringData (") {
			continue
		}
		if names, ok := mergeChangedKeys(field.OldValue, field.NewValue).([]string); ok {
			for _, name := range names {
				keys[name] = true
			}
		}
	}
	return keys
}

func mergeChangedKeys(a, b any) any {
	var keys []string
	for _, value := range []any{a, b} {
		switch list := value.(type) {
		case []string:
			keys = append(keys, list...)
		case []any:
			for _, key := range list {
				if name, ok := key.(string); ok {
					keys = append(keys, name)
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}
