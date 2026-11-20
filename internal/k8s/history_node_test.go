package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestComputeDiff_NodeMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, key  string
		annotation bool
	}{
		{name: "topology label", key: "topology.kubernetes.io/zone"},
		{name: "role label", key: "node-role.kubernetes.io/worker"},
		{name: "karpenter scheduling label", key: "karpenter.sh/nodepool"},
		{name: "GPU feature label", key: "nvidia.com/gpu.product"},
		{name: "KubeVirt scheduling label", key: "kubevirt.io/schedulable"},
		{name: "calico annotation", key: "projectcalico.org/IPv4Address", annotation: true},
		{name: "cilium annotation", key: "network.cilium.io/ipv4-cilium-host", annotation: true},
		{name: "karpenter user policy", key: "karpenter.sh/do-not-disrupt", annotation: true},
		{name: "karpenter eviction policy", key: "karpenter.sh/do-not-evict", annotation: true},
		{name: "karpenter repair policy", key: "karpenter.sh/do-not-repair", annotation: true},
		{name: "autoscaler removal policy", key: "cluster-autoscaler.kubernetes.io/scale-down-disabled", annotation: true},
	} {
		for _, op := range []string{"added", "changed", "removed"} {
			t.Run(tt.name+"/"+op, func(t *testing.T) {
				oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-uid"}}
				newNode := oldNode.DeepCopy()
				var oldMap, newMap map[string]string
				if op != "added" {
					oldMap = map[string]string{tt.key: "old"}
				}
				if op != "removed" {
					newMap = map[string]string{tt.key: "new"}
				}
				path := specPathKey("metadata.labels", tt.key)
				var oldShown, newShown any = valueOrNil("old", op != "added"), valueOrNil("new", op != "removed")
				if tt.annotation {
					oldNode.Annotations, newNode.Annotations = oldMap, newMap
					path = specPathKey("metadata.annotations", tt.key)
					oldShown, newShown = hiddenValues(op != "added", op != "removed")
				} else {
					oldNode.Labels, newNode.Labels = oldMap, newMap
				}
				diff := ComputeDiff("Node", oldNode, newNode)
				want := []FieldChange{{Path: path, OldValue: oldShown, NewValue: newShown}}
				if diff == nil || !reflect.DeepEqual(diff.Fields, want) || !strings.Contains(diff.Summary, tt.key+" "+op) {
					t.Fatalf("diff = %+v, want fields %+v and %s summary", diff, want, op)
				}
				oldU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(oldNode)
				if err != nil {
					t.Fatal(err)
				}
				newU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(newNode)
				if err != nil {
					t.Fatal(err)
				}
				fromUnstructured := ComputeDiffFromUnstructured("Node", &unstructured.Unstructured{Object: oldU}, &unstructured.Unstructured{Object: newU})
				if !reflect.DeepEqual(fromUnstructured, diff) {
					t.Fatalf("unstructured diff = %+v, want %+v", fromUnstructured, diff)
				}
			})
		}
	}
}

func TestComputeDiff_NodeMetadataValues(t *testing.T) {
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{"example.com/api-token": "secret-one"},
		Annotations: map[string]string{"example.com/config": "secret-one"},
	}}
	newNode := oldNode.DeepCopy()
	newNode.Labels["example.com/api-token"] = "secret-two"
	newNode.Annotations["example.com/config"] = strings.Repeat("arbitrary credential or config", 10000)
	diff := ComputeDiff("Node", oldNode, newNode)
	want := []FieldChange{
		{Path: "metadata.labels[example.com/api-token]", OldValue: "[REDACTED]", NewValue: "[REDACTED]"},
		{Path: "metadata.annotations[example.com/config]", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker},
	}
	if diff == nil || !reflect.DeepEqual(diff.Fields, want) {
		t.Fatalf("diff = %+v, want %+v", diff, want)
	}

	oldNode.Labels = nil
	oldNode.Annotations = nil
	newNode = oldNode.DeepCopy()
	newNode.Labels = map[string]string{"node-role.kubernetes.io/worker": ""}
	diff = ComputeDiff("Node", oldNode, newNode)
	if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].OldValue != nil || diff.Fields[0].NewValue != "" {
		t.Fatalf("empty-valued role label addition lost: %+v", diff)
	}
}

func TestRecordToTimelineStore_NodeExcludedMetadata(t *testing.T) {
	for _, key := range []string{
		"node.alpha.kubernetes.io/ttl",
		"nvidia.com/gfd.timestamp",
		"kubevirt.io/heartbeat",
		"volumes.kubernetes.io/controller-managed-attach-detach",
		"csi.volume.kubernetes.io/nodeid",
		"kubeadm.alpha.kubernetes.io/cri-socket",
		"kubelet.alpha.kubernetes.io/cri-socket",
		"alpha.kubernetes.io/provided-node-ip",
		"flannel.alpha.coreos.com/backend-data",
		"rke2.io/node-config-hash", "k3s.io/node-args", "k3s.io/node-env",
		"karpenter.sh/nodepool-hash", "karpenter.sh/registration-hash",
		"kubectl.kubernetes.io/last-applied-configuration",
		"management.cattle.io/pod-requests", "management.cattle.io/pod-limits",
	} {
		for _, op := range []string{"added", "changed", "removed"} {
			t.Run(key+"/"+op, func(t *testing.T) {
				oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-uid"}}
				newNode := oldNode.DeepCopy()
				if op != "added" {
					oldNode.Annotations = map[string]string{key: "old"}
				}
				if op != "removed" {
					newNode.Annotations = map[string]string{key: "new"}
				}
				oldNode.Labels = map[string]string{"node.alpha.kubernetes.io/ttl": "0", "rke2.io/internal": "old"}
				newNode.Labels = map[string]string{"node.alpha.kubernetes.io/ttl": "10", "rke2.io/internal": "new"}
				if key == "nvidia.com/gfd.timestamp" {
					oldNode.Labels, newNode.Labels = oldNode.Annotations, newNode.Annotations
					oldNode.Annotations, newNode.Annotations = nil, nil
				}
				if diff := ComputeDiff("Node", oldNode, newNode); diff != nil {
					t.Fatalf("excluded-only diff = %+v", diff)
				}
				timeline.ResetStore()
				t.Cleanup(timeline.ResetStore)
				if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
					t.Fatal(err)
				}
				for _, precomputed := range []bool{false, true} {
					recordToTimelineStore(ActiveClusterContext(), "Node", "", "worker", "node-uid", "update", oldNode, newNode, nil, precomputed)
				}
				events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Node"}})
				if err != nil || len(events) != 0 {
					t.Fatalf("excluded-only update recorded: %+v, err=%v", events, err)
				}
			})
		}
	}
}

func TestComputeDiff_NodeMixedMetadata(t *testing.T) {
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{"nvidia.com/gfd.timestamp": "1724632719", "nvidia.com/gpu.product": "A100"},
		Annotations: map[string]string{"karpenter.sh/nodepool-hash": "old", "projectcalico.org/IPv4Address": "10.0.0.1/24"},
	}}
	newNode := oldNode.DeepCopy()
	newNode.Labels["nvidia.com/gfd.timestamp"] = "1724632779"
	newNode.Labels["nvidia.com/gpu.product"] = "H100"
	newNode.Annotations["karpenter.sh/nodepool-hash"] = "new"
	newNode.Annotations["projectcalico.org/IPv4Address"] = "10.0.0.2/24"
	diff := ComputeDiff("Node", oldNode, newNode)
	want := []FieldChange{
		{Path: "metadata.labels[nvidia.com/gpu.product]", OldValue: "A100", NewValue: "H100"},
		{Path: "metadata.annotations[projectcalico.org/IPv4Address]", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker},
	}
	if diff == nil || !reflect.DeepEqual(diff.Fields, want) {
		t.Fatalf("mixed diff = %+v, want %+v", diff, want)
	}
}

// A new node collects labels and annotations from the cloud controller,
// Karpenter and the CNI in its first minutes. Those additions are not changes
// anyone made; edits, removals and disruption policy still are.
func TestComputeDiff_JoiningNodeMetadata(t *testing.T) {
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker", CreationTimestamp: metav1.NewTime(time.Now().Add(-20 * time.Second)),
		Labels:      map[string]string{"node.kubernetes.io/instance-type": "m5.large", "team": "a"},
		Annotations: map[string]string{"example.com/owner": "a"},
	}}
	newNode := oldNode.DeepCopy()
	newNode.Labels["topology.kubernetes.io/zone"] = "us-east-1a"
	newNode.Labels["karpenter.k8s.aws/instance-cpu"] = "2"
	newNode.Labels["team"] = "b"
	newNode.Annotations["projectcalico.org/IPv4Address"] = "10.0.0.1/24"
	newNode.Annotations["karpenter.sh/do-not-disrupt"] = "true"
	delete(newNode.Annotations, "example.com/owner")
	diff := ComputeDiff("Node", oldNode, newNode)
	want := []FieldChange{
		{Path: "metadata.labels.team", OldValue: "a", NewValue: "b"},
		{Path: "metadata.annotations[example.com/owner]", OldValue: hiddenSetMarker, NewValue: nil},
		{Path: "metadata.annotations[karpenter.sh/do-not-disrupt]", OldValue: nil, NewValue: hiddenSetMarker},
	}
	if diff == nil || !reflect.DeepEqual(diff.Fields, want) {
		t.Fatalf("joining diff = %+v, want %+v", diff, want)
	}

	oldNode.CreationTimestamp = metav1.NewTime(time.Now().Add(-nodeJoinWindow - time.Minute))
	newNode.CreationTimestamp = oldNode.CreationTimestamp
	if diff := ComputeDiff("Node", oldNode, newNode); diff == nil || len(diff.Fields) != 6 {
		t.Fatalf("established node additions dropped: %+v", diff)
	}
}

func TestComputeDiff_NodeTaints(t *testing.T) {
	taint := corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}
	for _, tt := range []struct {
		name     string
		old, new []corev1.Taint
		want     []FieldChange
		summary  string
	}{
		{name: "value", old: []corev1.Taint{taint}, new: []corev1.Taint{{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoSchedule}},
			want: []FieldChange{{Path: "spec.taints[dedicated]", OldValue: []string{"dedicated=gpu:NoSchedule"}, NewValue: []string{"dedicated=batch:NoSchedule"}}}, summary: "taint dedicated changed"},
		{name: "effect", old: []corev1.Taint{taint}, new: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoExecute}},
			want: []FieldChange{{Path: "spec.taints[dedicated]", OldValue: []string{"dedicated=gpu:NoSchedule"}, NewValue: []string{"dedicated=gpu:NoExecute"}}}, summary: "taint dedicated changed"},
		{name: "add key", new: []corev1.Taint{taint},
			want: []FieldChange{{Path: "spec.taints", OldValue: []string{}, NewValue: []string{"dedicated"}}}, summary: "taints added: [dedicated]"},
		{name: "remove key", old: []corev1.Taint{taint},
			want: []FieldChange{{Path: "spec.taints", OldValue: []string{"dedicated"}, NewValue: []string{}}}, summary: "taints removed: [dedicated]"},
		{name: "same key multiple effects", old: []corev1.Taint{taint}, new: []corev1.Taint{taint, {Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoExecute}},
			want: []FieldChange{{Path: "spec.taints[dedicated]", OldValue: []string{"dedicated=gpu:NoSchedule"}, NewValue: []string{"dedicated=gpu:NoExecute", "dedicated=gpu:NoSchedule"}}}, summary: "taint dedicated changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			diff := ComputeDiff("Node", &corev1.Node{Spec: corev1.NodeSpec{Taints: tt.old}}, &corev1.Node{Spec: corev1.NodeSpec{Taints: tt.new}})
			if diff == nil || !reflect.DeepEqual(diff.Fields, tt.want) || diff.Summary != tt.summary {
				t.Fatalf("diff = %+v, want %+v, summary %q", diff, tt.want, tt.summary)
			}
		})
	}
}

func TestComputeDiff_NodeTaintRedaction(t *testing.T) {
	for _, tt := range []struct {
		name, key, oldValue, newValue string
	}{
		{name: "sensitive key", key: "example.com/api-token", oldValue: "secret-one", newValue: "secret-two"},
		{name: "secret pattern", key: "example.com/config", oldValue: "ghp_" + strings.Repeat("a", 36), newValue: "ghp_" + strings.Repeat("b", 36)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldNode := &corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: tt.key, Value: tt.oldValue, Effect: corev1.TaintEffectNoSchedule}}}}
			newNode := oldNode.DeepCopy()
			newNode.Spec.Taints[0].Value = tt.newValue
			want := []FieldChange{{
				Path:     specPathKey("spec.taints", tt.key),
				OldValue: []string{tt.key + "=[REDACTED]:NoSchedule"},
				NewValue: []string{tt.key + "=[REDACTED]:NoSchedule"},
			}}
			diff := ComputeDiff("Node", oldNode, newNode)
			if diff == nil || !reflect.DeepEqual(diff.Fields, want) || diff.Summary != "taint "+tt.key+" changed" {
				t.Fatalf("rotation diff = %+v, want fields %+v", diff, want)
			}
			oldU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(oldNode)
			if err != nil {
				t.Fatal(err)
			}
			newU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(newNode)
			if err != nil {
				t.Fatal(err)
			}
			fromUnstructured := ComputeDiffFromUnstructured("Node", &unstructured.Unstructured{Object: oldU}, &unstructured.Unstructured{Object: newU})
			if !reflect.DeepEqual(fromUnstructured, diff) {
				t.Fatalf("unstructured diff = %+v, want %+v", fromUnstructured, diff)
			}
			if unchanged := ComputeDiff("Node", oldNode, oldNode.DeepCopy()); unchanged != nil {
				t.Fatalf("unchanged sensitive taint produced a diff: %+v", unchanged)
			}
			if oldNode.Spec.Taints[0].Value != tt.oldValue || newNode.Spec.Taints[0].Value != tt.newValue {
				t.Fatal("diff mutated input taint values")
			}
		})
	}
}

func TestComputeDiff_NodeSystemTaints(t *testing.T) {
	for _, key := range []string{"node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable", "ToBeDeletedByClusterAutoscaler", "DeletionCandidateOfClusterAutoscaler", "karpenter.sh/disrupted"} {
		t.Run(key, func(t *testing.T) {
			oldNode := &corev1.Node{}
			newNode := oldNode.DeepCopy()
			newNode.Spec.Taints = []corev1.Taint{{Key: key, Effect: corev1.TaintEffectNoSchedule, TimeAdded: &metav1.Time{Time: time.Now()}}}
			for _, pair := range [][2]*corev1.Node{{oldNode, newNode}, {newNode, oldNode}} {
				diff := ComputeDiff("Node", pair[0], pair[1])
				if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "spec.taints" {
					t.Fatalf("system taint add/remove lost: %+v", diff)
				}
			}
			oldNode = newNode.DeepCopy()
			newNode.Spec.Taints[0].TimeAdded = &metav1.Time{Time: time.Now().Add(time.Minute)}
			if diff := ComputeDiff("Node", oldNode, newNode); diff != nil {
				t.Fatalf("system taint timestamp produced noise: %+v", diff)
			}
			if key == "ToBeDeletedByClusterAutoscaler" || key == "DeletionCandidateOfClusterAutoscaler" {
				oldNode.Spec.Taints[0].Value = "1724632719"
				newNode.Spec.Taints[0].Value = "1724632779"
				if diff := ComputeDiff("Node", oldNode, newNode); diff != nil {
					t.Fatalf("autoscaler timestamp value produced noise: %+v", diff)
				}
			}
		})
	}
	oldNode := &corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoExecute},
		{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}}}
	newNode := oldNode.DeepCopy()
	newNode.Spec.Taints[0], newNode.Spec.Taints[2] = newNode.Spec.Taints[2], newNode.Spec.Taints[0]
	if diff := ComputeDiff("Node", oldNode, newNode); diff != nil {
		t.Fatalf("taint reorder produced noise: %+v", diff)
	}
}

func TestRecordToTimelineStore_NodeConfiguration(t *testing.T) {
	for _, field := range []string{"label", "annotation", "autoscaler policy", "taint value", "sensitive taint rotation", "taint effect"} {
		for _, precomputed := range []bool{false, true} {
			t.Run(field+"/precomputed="+map[bool]string{true: "true", false: "false"}[precomputed], func(t *testing.T) {
				timeline.ResetStore()
				t.Cleanup(timeline.ResetStore)
				if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
					t.Fatal(err)
				}
				oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-uid"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}}}
				newNode := oldNode.DeepCopy()
				switch field {
				case "label":
					newNode.Labels = map[string]string{"topology.kubernetes.io/zone": "east"}
				case "annotation":
					newNode.Annotations = map[string]string{"projectcalico.org/IPv4Address": "10.0.0.1/24"}
				case "autoscaler policy":
					oldNode.Annotations = map[string]string{"cluster-autoscaler.kubernetes.io/scale-down-disabled": "true"}
					newNode.Annotations = map[string]string{"cluster-autoscaler.kubernetes.io/scale-down-disabled": "false"}
				case "taint value":
					newNode.Spec.Taints[0].Value = "batch"
				case "sensitive taint rotation":
					oldNode.Spec.Taints[0].Key = "example.com/api-token"
					oldNode.Spec.Taints[0].Value = "secret-one"
					newNode.Spec.Taints[0].Key = "example.com/api-token"
					newNode.Spec.Taints[0].Value = "secret-two"
				case "taint effect":
					newNode.Spec.Taints[0].Effect = corev1.TaintEffectNoExecute
				}
				want := ComputeDiff("Node", oldNode, newNode)
				if want == nil {
					t.Fatal("expected configuration diff with no generation")
				}
				recordToTimelineStore(ActiveClusterContext(), "Node", "", "worker", "node-uid", "update", oldNode, newNode, want, precomputed)
				events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Node"}})
				if err != nil || len(events) != 1 || !reflect.DeepEqual(events[0].Diff, want) {
					t.Fatalf("recorded events = %+v, err=%v, want diff %+v", events, err, want)
				}
			})
		}
	}
}

func TestRecordToTimelineStore_NodeCreationHasNoMetadataDiff(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatal(err)
	}
	wasSynced := initialSyncComplete.Swap(true)
	t.Cleanup(func() { initialSyncComplete.Store(wasSynced) })
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "fresh-node", UID: "fresh-uid", CreationTimestamp: metav1.Now(),
		Labels: map[string]string{"topology.kubernetes.io/zone": "east"}, Annotations: map[string]string{"projectcalico.org/IPv4Address": "10.0.0.1/24"}}}
	recordToTimelineStore(ActiveClusterContext(), "Node", "", node.Name, string(node.UID), "add", nil, node, nil, false)
	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Node"}})
	if err != nil || len(events) != 1 || events[0].EventType != timeline.EventTypeAdd || events[0].Diff != nil {
		t.Fatalf("creation produced metadata changes: %+v, err=%v", events, err)
	}
}
