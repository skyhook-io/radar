package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/envresolve"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLatestPodEnvSourceChangesUsesLatestObservedKeyChange(t *testing.T) {
	older := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	events := []timeline.TimelineEvent{
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: older,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"HOST"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"HOST"}}, {Path: "data (added keys)", NewValue: []string{"PORT"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer.Add(time.Minute),
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "metadata.labels", NewValue: map[string]string{"team": "platform"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer.Add(2 * time.Minute),
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.app.json.feature.enabled", NewValue: true}}},
		},
	}

	got := latestPodEnvSourceChanges(events, map[envresolve.SourceID]envresolve.SourceData{
		{Kind: "ConfigMap", Name: "app"}: {Kind: "ConfigMap", Name: "app", Keys: []string{"app.json"}},
	})
	host := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "HOST"}]
	if !host.changedAt.Equal(newer) || host.kind != "modified" {
		t.Fatalf("HOST change = %+v, want modified at %v", host, newer)
	}
	port := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "PORT"}]
	if !port.changedAt.Equal(newer) || port.kind != "added" {
		t.Fatalf("PORT change = %+v, want added at %v", port, newer)
	}
	structured := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "app.json"}]
	if structured.kind != "modified" || !structured.changedAt.Equal(newer.Add(2*time.Minute)) {
		t.Fatalf("structured change = %+v", structured)
	}
	if len(got) != 3 {
		t.Fatalf("unexpected changes: %+v", got)
	}
}

func TestFindPodEnvChangesSkipsInvalidEnvFromKeys(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	changes := map[podEnvSourceKey]podEnvSourceChange{
		{kind: "ConfigMap", namespace: "shop", name: "app", key: "VALID"}:    {kind: "removed", changedAt: startedAt.Add(time.Minute)},
		{kind: "ConfigMap", namespace: "shop", name: "app", key: "bad=name"}: {kind: "removed", changedAt: startedAt.Add(time.Minute)},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			EnvFrom: []corev1.EnvFromSource{{
				ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app"}},
			}},
		}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}},
	}

	var got []PodEnvChange
	appendEnvFromChanges(&got, pod, changes)
	if len(got) != 1 || got[0].Variable != "VALID" {
		t.Fatalf("envFrom changes = %+v, want only VALID", got)
	}
}

func TestFindPodEnvChangesIncludesRunningInitContainers(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	changes := map[podEnvSourceKey]podEnvSourceChange{
		{kind: "ConfigMap", namespace: "shop", name: "setup", key: "MODE"}: {kind: "modified", changedAt: startedAt.Add(time.Minute)},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name: "setup",
				EnvFrom: []corev1.EnvFromSource{{
					ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "setup"}},
				}},
			}},
			Containers: []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
			Name: "setup", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt.Add(5 * time.Minute))}},
		}}},
	}

	var got []PodEnvChange
	appendEnvFromChanges(&got, pod, changes)
	if len(got) != 1 || got[0].Container != "setup" || got[0].Variable != "MODE" {
		t.Fatalf("running init changes = %+v", got)
	}
	names := podEnvSourceNames(pod)
	if len(names) != 1 || names[0] != "setup" {
		t.Fatalf("running init source names = %+v", names)
	}
	if since := podEnvEvidenceHistorySince(pod, startedAt.Add(10*time.Minute)); !since.Equal(startedAt.Add(-time.Second)) {
		t.Fatalf("running init history starts at %v, want %v", since, startedAt.Add(-time.Second))
	}
}
