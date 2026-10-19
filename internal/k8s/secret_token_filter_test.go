package k8s

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/internal/timeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestIsNoisyResource_Secrets(t *testing.T) {
	for _, name := range []string{"api-token", "github-token", "slack-token", "default-token-abcde", "controller-leader-election-lock"} {
		for _, op := range []string{"add", "update", "delete"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				want := name == "controller-leader-election-lock" && op == "update"
				if got := isNoisyResource("Secret", name, op); got != want {
					t.Fatalf("isNoisyResource = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestRecordToTimelineStore_SecretTokenUpdate(t *testing.T) {
	for _, precomputed := range []bool{false, true} {
		name := "recompute"
		if precomputed {
			name = "precomputed"
		}
		t.Run(name, func(t *testing.T) {
			timeline.ResetStore()
			if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
				t.Fatalf("InitStore: %v", err)
			}
			t.Cleanup(timeline.ResetStore)

			oldSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "api-token", Namespace: "shop", UID: "secret-uid", ResourceVersion: "1"},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"credential": []byte("dummy-old-credential")},
			}
			newSecret := oldSecret.DeepCopy()
			newSecret.ResourceVersion = "2"
			newSecret.Data["credential"] = []byte("dummy-new-credential")
			if isNoisyResource("Secret", newSecret.Name, "update") {
				t.Fatal("Opaque api-token update was filtered")
			}
			var diff *DiffInfo
			if precomputed {
				diff = ComputeDiff("Secret", oldSecret, newSecret)
			}
			recordToTimelineStore("ctx", "Secret", "shop", newSecret.Name, string(newSecret.UID), "update", oldSecret, newSecret, diff, precomputed)

			events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Secret"}, ClusterContext: "ctx"})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("recorded %d events, want 1", len(events))
			}
			want := &timeline.DiffInfo{
				Fields:  []timeline.FieldChange{{Path: "data (modified keys)", OldValue: []string{"credential"}, NewValue: []string{"credential"}}},
				Summary: "data modified keys: [credential]",
			}
			if !reflect.DeepEqual(events[0].Diff, want) {
				t.Fatal("Secret diff must contain only the modified key name")
			}
			encoded, err := json.Marshal(events[0])
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, value := range [][]byte{oldSecret.Data["credential"], newSecret.Data["credential"]} {
				if bytes.Contains(encoded, value) || bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString(value))) {
					t.Fatal("recorded event contains a Secret value")
				}
			}
		})
	}
}

func TestRecordToTimelineStore_ServiceAccountTokenUpdateDrop(t *testing.T) {
	for _, shape := range []string{"typed", "unstructured"} {
		for _, storeState := range []string{"available", "unavailable"} {
			for _, name := range []string{"default-token-abcde", "manual-credential"} {
				t.Run(shape+"/"+storeState+"/"+name, func(t *testing.T) {
					timeline.ResetStore()
					t.Cleanup(timeline.ResetStore)
					timeline.ResetMetricsForContextSwitch()
					t.Cleanup(timeline.ResetMetricsForContextSwitch)
					if storeState == "available" {
						if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
							t.Fatalf("InitStore: %v", err)
						}
					}
					oldSecret := &corev1.Secret{
						TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: "secret-uid"},
						Type:       corev1.SecretTypeServiceAccountToken,
						Data:       map[string][]byte{"token": []byte("dummy-old-sa-credential")},
					}
					newSecret := oldSecret.DeepCopy()
					newSecret.Data["token"] = []byte("dummy-new-sa-credential")
					newSecret.Labels = map[string]string{"app": "rotated"}
					var oldObj, newObj any = oldSecret, newSecret
					if shape == "unstructured" {
						oldMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(oldSecret)
						if err != nil {
							t.Fatalf("ToUnstructured: %v", err)
						}
						newMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(newSecret)
						if err != nil {
							t.Fatalf("ToUnstructured: %v", err)
						}
						oldObj = &unstructured.Unstructured{Object: oldMap}
						newObj = &unstructured.Unstructured{Object: newMap}
					}
					recordToTimelineStore("ctx", "Secret", "shop", name, string(newSecret.UID), "update", oldObj, newObj, ComputeDiff("Secret", oldObj, newObj), true)

					if store := timeline.GetStore(); store != nil {
						events, err := store.Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Secret"}})
						if err != nil {
							t.Fatalf("Query: %v", err)
						}
						if len(events) != 0 {
							t.Fatalf("recorded %d ServiceAccount token updates, want 0", len(events))
						}
						// Later Kubernetes Events about the Secret are enriched from its tombstone.
						if entry, ok := tombstones.Get(string(newSecret.UID), "v1", "Secret", "shop", name); !ok || entry.Labels["app"] != "rotated" {
							t.Fatal("tombstone not refreshed from the dropped update")
						}
					}
					snapshot := timeline.GetMetrics().GetSnapshot()
					if snapshot.Counters.Dropped[timeline.DropReasonNoisyFilter] != 1 || len(snapshot.Counters.Dropped) != 1 {
						t.Fatal("expected exactly one noisy_filter drop")
					}
					if len(snapshot.RecentDrops) != 1 {
						t.Fatalf("got %d recent drops, want 1", len(snapshot.RecentDrops))
					}
					drop := snapshot.RecentDrops[0]
					if drop.Kind != "Secret" || drop.Namespace != "shop" || drop.Name != name || drop.Operation != "update" || drop.Reason != timeline.DropReasonNoisyFilter || drop.ClusterContext != "ctx" {
						t.Fatal("drop accounting lost the resource identity, reason or cluster context")
					}
				})
			}
		}
	}
}
