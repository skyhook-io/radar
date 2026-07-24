package k8s

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// The cache layer computes the update diff once and hands it to
// recordToTimelineStore (diffPrecomputed=true) instead of recomputing it. This
// pins the invariant that the precomputed path produces a timeline entry
// identical to the recompute path — i.e. reusing the diff changed nothing.
func TestRecordToTimelineStore_PrecomputedDiffMatchesRecompute(t *testing.T) {
	now := time.Now()
	oldDep := testDeployment("uid-1", "nginx:1.0", now)
	newDep := testDeployment("uid-1", "nginx:2.0", now)

	pre := ComputeDiff("Deployment", oldDep, newDep)
	if pre == nil {
		t.Fatal("expected a non-nil diff for an image change")
	}

	record := func(precomputed *DiffInfo, diffPrecomputed bool) *timeline.DiffInfo {
		t.Helper()
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
		recordToTimelineStore(ActiveClusterContext(), "Deployment", "shop", "web", "uid-1", "update", oldDep, newDep, precomputed, diffPrecomputed)
		events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Deployment"}})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected exactly 1 recorded event, got %d", len(events))
		}
		return events[0].Diff
	}

	got := record(pre, true)   // production path: reuse the cache-computed diff
	want := record(nil, false) // fallback path: recompute in recordToTimelineStore
	t.Cleanup(timeline.ResetStore)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("precomputed-diff timeline output differs from recompute:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestRecordToTimelineStore_PreservesDNSFieldPath(t *testing.T) {
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)

	oldDep := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{DNSPolicy: corev1.DNSClusterFirst}}}}
	newDep := oldDep.DeepCopy()
	newDep.Spec.Template.Spec.DNSPolicy = corev1.DNSNone
	recordToTimelineStore(ActiveClusterContext(), "Deployment", "shop", "web", "uid-1", "update", oldDep, newDep, nil, false)

	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Deployment"}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].Diff == nil {
		t.Fatalf("expected one timeline event with a diff, got %+v", events)
	}
	var dnsPolicyChange *timeline.FieldChange
	for _, change := range events[0].Diff.Fields {
		if change.Path == "spec.template.spec.dnsPolicy" {
			changeCopy := change
			dnsPolicyChange = &changeCopy
			break
		}
	}
	if dnsPolicyChange == nil {
		t.Fatalf("timeline diff lost exact DNS field path: %+v", events[0].Diff.Fields)
	}
	if dnsPolicyChange.OldValue != "ClusterFirst" || dnsPolicyChange.NewValue != "None" {
		t.Fatalf("timeline diff lost DNS values: %+v", *dnsPolicyChange)
	}
}

func TestRecordToTimelineStore_DeploymentGenerationFallbackRequiresSpecChange(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*appsv1.Deployment)
		wantEvents int
	}{
		{
			name: "top-level annotation only",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Annotations = map[string]string{"example.com/revision": "2"}
			},
		},
		{
			name: "untracked spec field",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.MinReadySeconds = 5
			},
			wantEvents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			oldDeployment := testDeployment("uid-1", "nginx:1.0", now)
			oldDeployment.Generation = 1
			newDeployment := oldDeployment.DeepCopy()
			newDeployment.Generation = 2
			tt.mutate(newDeployment)

			timeline.ResetStore()
			if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
				t.Fatalf("InitStore: %v", err)
			}
			t.Cleanup(timeline.ResetStore)

			recordToTimelineStore(ActiveClusterContext(), "Deployment", "shop", "web", "uid-1", "update", oldDeployment, newDeployment, nil, false)
			events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Deployment"}})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(events) != tt.wantEvents {
				t.Fatalf("recorded %d events, want %d: %+v", len(events), tt.wantEvents, events)
			}
			if tt.wantEvents == 1 && (events[0].Diff == nil || len(events[0].Diff.Fields) != 1 || events[0].Diff.Fields[0].Path != "metadata.generation") {
				t.Fatalf("expected generation fallback for untracked spec change, got %+v", events[0].Diff)
			}
		})
	}
}
