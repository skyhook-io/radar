package k8s

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

const (
	rookMonHost    = "[v2:10.43.0.11:3300,v1:10.43.0.11:6789],[v2:10.43.0.12:3300,v1:10.43.0.12:6789],[v2:10.43.0.13:3300,v1:10.43.0.13:6789]"
	rookMonHostRe  = "[v2:10.43.0.13:3300,v1:10.43.0.13:6789],[v2:10.43.0.11:3300,v1:10.43.0.11:6789],[v2:10.43.0.12:3300,v1:10.43.0.12:6789]"
	rookMonHostNew = "[v2:10.43.0.11:3300,v1:10.43.0.11:6789],[v2:10.43.0.12:3300,v1:10.43.0.12:6789],[v2:10.43.0.14:3300,v1:10.43.0.14:6789]"
)

func TestSameEntriesReordered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		old, new string
		want     bool
	}{
		{"Rook mon_host reorder keeps bracketed entries whole", rookMonHost, rookMonHostRe, true},
		{"Rook mon_initial_members reorder", "a,b,c", "c,a,b", true},
		{"IPv6 mon_host entries nest brackets", "[v2:[fd00::1]:3300,v1:[fd00::1]:6789],[v2:[fd00::2]:3300]", "[v2:[fd00::2]:3300],[v2:[fd00::1]:3300,v1:[fd00::1]:6789]", true},
		{"msgr2-only entries", "[v2:10.0.0.1:3300],[v2:10.0.0.2:3300]", "[v2:10.0.0.2:3300],[v2:10.0.0.1:3300]", true},
		{"swapping protocols inside one bracketed entry is a change", "[v2:a:3300,v1:a:6789],[v2:b:3300]", "[v1:a:6789,v2:a:3300],[v2:b:3300]", false},
		{"a mon replaced", rookMonHost, rookMonHostNew, false},
		{"a member added", "a,b", "b,a,c", false},
		{"a member removed", "a,b,c", "b,a", false},
		{"duplicate count changed", "a,a,b", "b,a,b", false},
		{"identical value", "a,b", "a,b", false},
		{"single entry", "abc", "cba", false},
		{"whole value in one bracket", "[a,b]", "[b,a]", false},
		{"quoted entries", `"a","b"`, `"b","a"`, false},
		{"newline", "a,b\nc", "c\na,b", false},
		{"escape", `a\,b,c`, `c,a\,b`, false},
		{"unbalanced bracket", "[a,b", "b,[a", false},
		{"mismatched brackets", "(a],b", "b,(a]", false},
		{"empty entry", "a,,b", ",b,a", false},
		{"spacing differs per entry", "a, b", "b, a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameEntriesReordered(tc.old, tc.new); got != tc.want {
				t.Fatalf("sameEntriesReordered(%q, %q) = %v, want %v", tc.old, tc.new, got, tc.want)
			}
		})
	}
}

func TestSecretAndConfigMapDiffsMarkReorderedKeys(t *testing.T) {
	t.Run("Secret: reordered key is listed as modified and reordered, a real change only as modified", func(t *testing.T) {
		oldSecret := &corev1.Secret{Data: map[string][]byte{"hosts": []byte("a:1,b:1"), "password": []byte("x")}}
		newSecret := &corev1.Secret{Data: map[string][]byte{"hosts": []byte("b:1,a:1"), "password": []byte("y")}}
		changes, summary := diffSecret(oldSecret, newSecret)
		assertKeyListField(t, changes, "data (modified keys)", "hosts", "password")
		assertKeyListField(t, changes, "data (reordered keys)", "hosts")
		if !strings.Contains(strings.Join(summary, "; "), "data reordered keys: [hosts]") {
			t.Fatalf("summary = %q", summary)
		}
	})

	t.Run("ConfigMap: reordered key is named, structured diffs and binaryData are unchanged", func(t *testing.T) {
		oldCM := &corev1.ConfigMap{
			Data:       map[string]string{"BROKERS": "k0:9092,k1:9092", "app.json": `{"level":"info"}`},
			BinaryData: map[string][]byte{"blob": []byte("a,b")},
		}
		newCM := &corev1.ConfigMap{
			Data:       map[string]string{"BROKERS": "k1:9092,k0:9092", "app.json": `{"level":"debug"}`},
			BinaryData: map[string][]byte{"blob": []byte("b,a")},
		}
		changes, _ := diffConfigMap(oldCM, newCM)
		assertKeyListField(t, changes, "data (modified keys)", "BROKERS")
		assertKeyListField(t, changes, "data (reordered keys)", "BROKERS")
		assertKeyListField(t, changes, "binaryData (modified keys)", "blob")
		if hasField(changes, "binaryData (reordered keys)") {
			t.Fatalf("binaryData annotated as reordered: %+v", changes)
		}
		if !hasField(changes, "data.app.json.level") {
			t.Fatalf("structured diff of app.json lost: %+v", changes)
		}
	})
}

func TestIsRookMonOrderInsensitiveKey(t *testing.T) {
	controller := true
	rookSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-config", Namespace: "rook-ceph", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "ceph.rook.io/v1", Kind: "CephCluster", Name: "rook-ceph", UID: "c", Controller: &controller,
			}}},
			Type: "kubernetes.io/rook",
		}
	}
	if !isRookMonOrderInsensitiveKey(rookSecret(), "mon_host") || !isRookMonOrderInsensitiveKey(rookSecret(), "mon_initial_members") {
		t.Fatal("Rook's own mon config Secret keys must match")
	}
	for name, mutate := range map[string]func(*corev1.Secret) (string, *corev1.Secret){
		"other key": func(s *corev1.Secret) (string, *corev1.Secret) { return "fsid", s },
		"no owner":  func(s *corev1.Secret) (string, *corev1.Secret) { s.OwnerReferences = nil; return "mon_host", s },
		"owner not controller": func(s *corev1.Secret) (string, *corev1.Secret) {
			s.OwnerReferences[0].Controller = nil
			return "mon_host", s
		},
		"other owner group": func(s *corev1.Secret) (string, *corev1.Secret) {
			s.OwnerReferences[0].APIVersion = "example.com/v1"
			return "mon_host", s
		},
		"other owner kind": func(s *corev1.Secret) (string, *corev1.Secret) {
			s.OwnerReferences[0].Kind = "CephFilesystem"
			return "mon_host", s
		},
		"other type": func(s *corev1.Secret) (string, *corev1.Secret) {
			s.Type = corev1.SecretTypeOpaque
			return "mon_host", s
		},
		"other name": func(s *corev1.Secret) (string, *corev1.Secret) { s.Name = "rook-ceph-mon"; return "mon_host", s },
		"nil Secret": func(*corev1.Secret) (string, *corev1.Secret) { return "mon_host", nil },
	} {
		key, secret := mutate(rookSecret())
		if isRookMonOrderInsensitiveKey(secret, key) {
			t.Errorf("%s: matched", name)
		}
	}
}

func TestLatestSecretDataChangesTracksRealChangesIndependentlyOfOrder(t *testing.T) {
	realAt := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	reorderAt := realAt.Add(time.Minute)
	real := secretDiffEvent(realAt, "shop", "app", map[string]string{"hosts": "a,b"}, map[string]string{"hosts": "a,c"})
	reorder := secretDiffEvent(reorderAt, "shop", "app", map[string]string{"hosts": "a,c"}, map[string]string{"hosts": "c,a"})
	// The reordered annotation listed before the modified field must still apply.
	reorderFieldsFirst := reorder
	reorderFieldsFirst.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{reorder.Diff.Fields[1], reorder.Diff.Fields[0]}}
	sameTimeReal := secretDiffEvent(reorderAt, "shop", "app", map[string]string{"hosts": "c,a"}, map[string]string{"hosts": "c,d"})

	key := secretDataKey{namespace: "shop", name: "app", key: "hosts"}
	for name, tc := range map[string]struct {
		events   []timeline.TimelineEvent
		realWant time.Time
	}{
		"oldest first":                        {[]timeline.TimelineEvent{real, reorder}, realAt},
		"newest first":                        {[]timeline.TimelineEvent{reorder, real}, realAt},
		"annotation before the modified list": {[]timeline.TimelineEvent{reorderFieldsFirst, real}, realAt},
		"reorder and real change at one time": {[]timeline.TimelineEvent{reorder, sameTimeReal, real}, reorderAt},
		"reorder only":                        {[]timeline.TimelineEvent{reorder}, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := latestSecretDataChanges(tc.events)[key]
			if !got.changedAt.Equal(reorderAt) || !got.realChangedAt.Equal(tc.realWant) {
				t.Fatalf("change = %+v, want latest %v and real %v", got, reorderAt, tc.realWant)
			}
		})
	}
}

func TestStaleSecretEnvOrderOnlyChanges(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-10 * time.Minute)
	realAt := now.Add(-5 * time.Minute)
	reorderAt := now.Add(-2 * time.Minute)
	controller := true
	watched := startedAt.Add(-time.Minute)

	cephCluster := metav1.OwnerReference{APIVersion: "ceph.rook.io/v1", Kind: "CephCluster", Name: "rook-ceph", UID: "cluster-uid", Controller: &controller}
	rookConfig := func(name string, owners ...metav1.OwnerReference) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", OwnerReferences: owners},
			Type:       "kubernetes.io/rook",
			Data:       map[string][]byte{"mon_host": []byte(rookMonHostRe), "mon_initial_members": []byte("c,a,b"), "fsid": []byte("2,1")},
		}
	}
	monPod := func(secret string) *corev1.Pod {
		return staleSecretEnvPod("rook-ceph-mon-a", startedAt, true,
			secretKeyEnv("ROOK_CEPH_MON_HOST", secret, "mon_host"),
			secretKeyEnv("ROOK_CEPH_MON_INITIAL_MEMBERS", secret, "mon_initial_members"))
	}
	monReorder := secretDiffEvent(reorderAt, "shop", "rook-ceph-config",
		map[string]string{"mon_host": rookMonHost, "mon_initial_members": "a,b,c"},
		map[string]string{"mon_host": rookMonHostRe, "mon_initial_members": "c,a,b"})

	t.Run("Rook's own mon list reorder raises nothing on any surface", func(t *testing.T) {
		pod := monPod("rook-ceph-config")
		cache := envHistoryTestCache(t, pod, rookConfig("rook-ceph-config", cephCluster))
		recordObserved(t, watched, monReorder)
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("Rook reorder raised an Issue: %+v", detections)
		}
		if facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}); len(facts) != 0 {
			t.Fatalf("Rook reorder produced stale env facts: %+v", facts)
		}
		if changes, _ := FindPodEnvChanges(context.Background(), cache, pod, nil); len(changes) != 0 {
			t.Fatalf("Rook reorder produced env evidence: %+v", changes)
		}
	})

	t.Run("a real Rook mon change still raises today's advisory", func(t *testing.T) {
		pod := monPod("rook-ceph-config")
		cache := envHistoryTestCache(t, pod, rookConfig("rook-ceph-config", cephCluster))
		recordObserved(t, watched, secretDiffEvent(reorderAt, "shop", "rook-ceph-config",
			map[string]string{"mon_host": rookMonHost, "mon_initial_members": "a,b,c"},
			map[string]string{"mon_host": rookMonHostNew, "mon_initial_members": "a,b,d"}))
		detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
		assertRealChangeCopy(t, detection, "key mon_host as env ROOK_CEPH_MON_HOST; Radar observed that key change at "+reorderAt.Format(time.RFC3339))
	})

	t.Run("a Rook mon change followed by a reorder keeps the real change and its time", func(t *testing.T) {
		pod := monPod("rook-ceph-config")
		cache := envHistoryTestCache(t, pod, rookConfig("rook-ceph-config", cephCluster))
		recordObserved(t, watched,
			secretDiffEvent(realAt, "shop", "rook-ceph-config", map[string]string{"mon_host": rookMonHostNew}, map[string]string{"mon_host": rookMonHost}),
			secretDiffEvent(reorderAt, "shop", "rook-ceph-config", map[string]string{"mon_host": rookMonHost}, map[string]string{"mon_host": rookMonHostRe}))
		detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
		assertRealChangeCopy(t, detection, "Radar observed that key change at "+realAt.Format(time.RFC3339))
	})

	t.Run("a lookalike Secret without the CephCluster controller gets order-only copy", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt, monPod("rook-ceph-config").Spec.Containers[0].Env...)
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, rookConfig("rook-ceph-config"))
		recordObserved(t, watched, monReorder)
		assertOrderOnlyCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Deployment/catalog")
	})

	t.Run("another key of Rook's Secret gets order-only copy", func(t *testing.T) {
		pod := staleSecretEnvPod("rook-ceph-mon-a", startedAt, true, secretKeyEnv("FSID", "rook-ceph-config", "fsid"))
		cache := envHistoryTestCache(t, pod, rookConfig("rook-ceph-config", cephCluster))
		recordObserved(t, watched, secretDiffEvent(reorderAt, "shop", "rook-ceph-config", map[string]string{"fsid": "1,2"}, map[string]string{"fsid": "2,1"}))
		detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
		if !strings.Contains(detection.Message, "key fsid as env FSID; Radar observed that key's comma-separated entries reordered") {
			t.Fatalf("message = %q", detection.Message)
		}
	})

	appSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "shop"}, Data: map[string][]byte{"BROKERS": []byte("k1,k0")}}
	brokersReorder := secretDiffEvent(reorderAt, "shop", "app-config", map[string]string{"BROKERS": "k0,k1"}, map[string]string{"BROKERS": "k1,k0"})

	t.Run("an app Secret reorder keeps the advisory with order-only copy on every surface", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, watched, brokersReorder)
		assertOrderOnlyCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Deployment/catalog")
		facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(facts) != 1 || !facts[0].OrderOnly || !strings.Contains(facts[0].Message, "comma-separated entries reordered") {
			t.Fatalf("facts = %+v", facts)
		}
		changes, _ := FindPodEnvChanges(context.Background(), cache, pod, nil)
		if len(changes) != 1 || !strings.Contains(changes[0].Evidence.Message, "reordered after this container started, with none added or removed") {
			t.Fatalf("env evidence = %+v", changes)
		}
	})

	t.Run("an app Secret reorder through envFrom gets order-only copy", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}}
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, watched, brokersReorder)
		assertOrderOnlyCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Deployment/catalog")
	})

	t.Run("a not-Ready pod gets order-only copy without a bare restart instruction", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, false, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
		cache := envHistoryTestCache(t, pod, appSecret.DeepCopy())
		recordObserved(t, watched, brokersReorder)
		detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
		if !strings.HasPrefix(detection.Message, "Only the order of entries changed in Secret data that this not-Ready pod's running containers read as env: ") ||
			!strings.HasSuffix(detection.Message, "Check whether the order matters to the app before restarting the pod.") ||
			strings.Contains(detection.Message, "Restart the pod to refresh them") ||
			!strings.HasPrefix(detection.Action, "Only the order of entries changed. Check whether the app treats the new order differently") {
			t.Fatalf("not-Ready order-only copy: %+v", detection)
		}
	})

	t.Run("a real app Secret change keeps today's copy", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, watched, secretDiffEvent(reorderAt, "shop", "app-config", map[string]string{"BROKERS": "k0,k1"}, map[string]string{"BROKERS": "k1,k2"}))
		assertRealChangeCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Radar observed that key change at "+reorderAt.Format(time.RFC3339))
	})

	t.Run("a container that started before Radar was watching gets today's copy", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, startedAt.Add(time.Minute), brokersReorder)
		assertRealChangeCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Radar observed that key change at "+reorderAt.Format(time.RFC3339))
	})

	t.Run("a key added after the container started, then reordered, is not order-only", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}}
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, watched,
			secretDiffEvent(realAt, "shop", "app-config", map[string]string{}, map[string]string{"BROKERS": "k0,k1"}),
			brokersReorder)
		assertRealChangeCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Radar observed that key change at "+realAt.Format(time.RFC3339))
	})

	t.Run("a failed history write since the container started rules out order-only", func(t *testing.T) {
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, appSecret.DeepCopy())
		recordObserved(t, watched, brokersReorder)
		timeline.RecordDrop("Secret", "shop", "app-config", timeline.DropReasonStoreFailed, "update", ActiveClusterContext())
		t.Cleanup(timeline.ResetMetricsForContextSwitch)
		assertRealChangeCopy(t, onlyDetection(t, detectStaleSecretEnv(cache, "shop", now)), "Radar observed that key change at "+reorderAt.Format(time.RFC3339))
		timeline.ResetMetricsForContextSwitch()
	})

	t.Run("the per-container cap never hides a real change behind reorders", func(t *testing.T) {
		data := map[string][]byte{}
		oldValues, newValues := map[string]string{}, map[string]string{}
		for i := range maxStaleSecretEnvChecksPerContainer {
			key := fmt.Sprintf("A%d", i)
			oldValues[key], newValues[key] = "x,y", "y,x"
			data[key] = []byte("y,x")
		}
		oldValues["Z_PASSWORD"], newValues["Z_PASSWORD"] = "old", "new"
		data["Z_PASSWORD"] = []byte("new")
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "shop"}, Data: data}
		deployment, replicaSet, pod := ownedStaleSecretEnvPod("catalog", startedAt)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}}
		cache := envHistoryTestCache(t, deployment, replicaSet, pod, secret)
		recordObserved(t, watched, secretDiffEvent(reorderAt, "shop", "app-config", oldValues, newValues))
		detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
		if !strings.Contains(detection.Message, "key Z_PASSWORD as env Z_PASSWORD; Radar observed that key change") {
			t.Fatalf("real change dropped by the cap: %q", detection.Message)
		}
		if strings.Contains(detection.Action, "Only the order") {
			t.Fatalf("mixed Issue reads as order-only: %q", detection.Action)
		}
	})

	t.Run("pods on both sides of a real change keep separate facts in either name order", func(t *testing.T) {
		for _, names := range [][2]string{{"catalog-a", "catalog-b"}, {"catalog-b", "catalog-a"}} {
			deployment, replicaSet, early := ownedStaleSecretEnvPod(names[0], startedAt, secretKeyEnv("BROKERS", "app-config", "BROKERS"))
			_, _, late := ownedStaleSecretEnvPod(names[1], realAt.Add(time.Minute), secretKeyEnv("BROKERS", "app-config", "BROKERS"))
			cache := envHistoryTestCache(t, deployment, replicaSet, early, late, appSecret.DeepCopy())
			recordObserved(t, watched,
				secretDiffEvent(realAt, "shop", "app-config", map[string]string{"BROKERS": "k0,k2"}, map[string]string{"BROKERS": "k0,k1"}),
				brokersReorder)
			detection := onlyDetection(t, detectStaleSecretEnv(cache, "shop", now))
			if !strings.Contains(detection.Message, "Radar observed that key change at "+realAt.Format(time.RFC3339)) ||
				!strings.Contains(detection.Message, "Radar observed that key's comma-separated entries reordered at "+reorderAt.Format(time.RFC3339)) ||
				strings.Contains(detection.Action, "Only the order") {
				t.Fatalf("%v: mixed pods merged into one reading: %+v", names, detection)
			}
		}
	})
}

func TestStaleConfigMapEnvOrderOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-10 * time.Minute)
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "shop"}, Data: map[string]string{"BROKERS": "k1,k0"}}
	pod := staleSecretEnvPod("catalog", startedAt, true, corev1.EnvVar{Name: "BROKERS", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "BROKERS",
	}}})
	changes, _ := diffConfigMap(&corev1.ConfigMap{Data: map[string]string{"BROKERS": "k0,k1"}}, configMap)
	event := timeline.TimelineEvent{
		ID: "cm-reorder", Timestamp: now.Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: ActiveClusterContext(),
		Kind: "ConfigMap", Namespace: "shop", Name: "settings", EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: changes},
	}
	cache := envHistoryTestCache(t, pod, configMap)
	recordObserved(t, startedAt.Add(-time.Minute), event)

	facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod})
	if len(facts) != 1 || !facts[0].OrderOnly || !strings.Contains(facts[0].Message, "reads ConfigMap/settings key BROKERS as env BROKERS") ||
		!strings.Contains(facts[0].Message, "comma-separated entries reordered") {
		t.Fatalf("ConfigMap facts = %+v", facts)
	}
	evidence, _ := FindPodEnvChanges(context.Background(), cache, pod, map[envresolve.SourceID]envresolve.SourceData{
		{Kind: "ConfigMap", Name: "settings"}: {Kind: "ConfigMap", Name: "settings", Keys: []string{"BROKERS"}},
	})
	if len(evidence) != 1 || evidence[0].Evidence.Kind != "modified" || !strings.Contains(evidence[0].Evidence.Message, "ConfigMap/settings key BROKERS reordered") {
		t.Fatalf("ConfigMap env evidence = %+v", evidence)
	}
}

func TestStaleSecretEnvRookReorderNote(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-10 * time.Minute)
	reorderAt := now.Add(-time.Minute)
	controller := true
	secretWrittenAt := func(name string, writeAt time.Time, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "shop", UID: types.UID(name + "-uid"),
				ManagedFields: []metav1.ManagedFieldsEntry{{
					Manager: "rotator", Operation: metav1.ManagedFieldsOperationUpdate, Time: &metav1.Time{Time: writeAt},
					FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:PASSWORD":{}}}`)},
				}},
			},
			Data: data,
		}
	}
	rookSecret := func(writeAt time.Time) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "rook-ceph-config", Namespace: "shop", UID: types.UID("rook-config-uid"),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "ceph.rook.io/v1", Kind: "CephCluster", Name: "rook-ceph", UID: "c", Controller: &controller}},
				ManagedFields: []metav1.ManagedFieldsEntry{{
					Manager: "rook", Operation: metav1.ManagedFieldsOperationUpdate, Time: &metav1.Time{Time: writeAt},
					FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:mon_host":{},"f:mon_initial_members":{}}}`)},
				}},
			},
			Type: "kubernetes.io/rook",
			Data: map[string][]byte{"mon_host": []byte(rookMonHostRe), "mon_initial_members": []byte("c,a,b")},
		}
	}
	monPod := func(envs ...corev1.EnvVar) *corev1.Pod {
		return staleSecretEnvPod("rook-ceph-mon-a", startedAt, true, envs...)
	}
	monHostEnv := secretKeyEnv("ROOK_CEPH_MON_HOST", "rook-ceph-config", "mon_host")
	membersEnv := secretKeyEnv("ROOK_CEPH_MON_INITIAL_MEMBERS", "rook-ceph-config", "mon_initial_members")
	monReorder := secretDiffEvent(reorderAt, "shop", "rook-ceph-config",
		map[string]string{"mon_host": rookMonHost, "mon_initial_members": "a,b,c"},
		map[string]string{"mon_host": rookMonHostRe, "mon_initial_members": "c,a,b"})
	wantNote := "Secret/rook-ceph-config had a write at " + reorderAt.Format(time.RFC3339) + " by rook, a field manager that owns its data, after container app started at " +
		startedAt.Format(time.RFC3339) + ". Since then, the only change Radar saw to the monitor list this container reads from it was a reorder, and Ceph reads that list as a set, so no restart is needed for it."
	const notWatched = " Radar's change history doesn't cover this container's whole run, so it can't rule out an earlier monitor change it missed."

	onlyFact := func(t *testing.T, cache *ResourceCache, pod *corev1.Pod) StaleEnvFact {
		t.Helper()
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("Rook reorder raised an Issue: %+v", detections)
		}
		facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(facts) != 1 {
			t.Fatalf("facts = %+v", facts)
		}
		if facts[0].EvidenceSource != staleSecretEvidenceManagedFields || facts[0].Key != "" || facts[0].Env != "" || !facts[0].ChangedAt.Equal(reorderAt) {
			t.Fatalf("note is not a keyless managedFields row: %+v", facts[0])
		}
		return facts[0]
	}

	t.Run("a reorder Radar watched end to end gets the note and no Issue", func(t *testing.T) {
		pod := monPod(monHostEnv, membersEnv)
		cache := envHistoryTestCache(t, pod, rookSecret(reorderAt))
		recordObserved(t, startedAt.Add(-time.Minute), monReorder)
		if fact := onlyFact(t, cache, pod); fact.Message != "Pod rook-ceph-mon-a: "+wantNote {
			t.Fatalf("message = %q", fact.Message)
		}
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(checks) != 1 || checks[0].Message != wantNote {
			t.Fatalf("diagnose checks = %+v", checks)
		}
	})

	t.Run("a container older than Radar's history says an earlier change can't be ruled out", func(t *testing.T) {
		pod := monPod(monHostEnv, membersEnv)
		cache := envHistoryTestCache(t, pod, rookSecret(reorderAt))
		recordObserved(t, startedAt.Add(time.Minute), monReorder)
		if fact := onlyFact(t, cache, pod); fact.Message != "Pod rook-ceph-mon-a: "+wantNote+notWatched {
			t.Fatalf("message = %q", fact.Message)
		}
	})

	t.Run("Pods with and without full history keep separate notes", func(t *testing.T) {
		watched := startedAt.Add(time.Minute)
		early := staleSecretEnvPod("rook-ceph-mon-z", startedAt, true, monHostEnv, membersEnv)
		late := staleSecretEnvPod("rook-ceph-mon-a", watched.Add(time.Minute), true, monHostEnv, membersEnv)
		cache := envHistoryTestCache(t, early, late, rookSecret(reorderAt))
		recordObserved(t, watched, monReorder)
		facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{early, late})
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, []*corev1.Pod{early, late})
		for _, messages := range [][]string{factMessages(facts), checkMessages(checks)} {
			if len(messages) != 2 || strings.Contains(messages[0], notWatched) == strings.Contains(messages[1], notWatched) {
				t.Fatalf("notes merged across history coverage: %q", messages)
			}
		}
	})

	t.Run("the note only takes cap space other rows left free", func(t *testing.T) {
		pod := monPod(monHostEnv, secretKeyEnv("PASSWORD", "db-password", "PASSWORD"))
		cache := envHistoryTestCache(t, pod, rookSecret(reorderAt), secretWrittenAt("db-password", reorderAt, map[string][]byte{"PASSWORD": []byte("x")}))
		recordObserved(t, startedAt.Add(-time.Minute), monReorder)
		checks := findStaleSecretEnvChecksWithManagedFieldsUpTo(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{monReorder}, 1)
		if len(checks) != 1 || checks[0].SecretName != "db-password" {
			t.Fatalf("checks = %+v", checks)
		}
	})

	t.Run("the note sorts after rows that may need a restart", func(t *testing.T) {
		pod := monPod(monHostEnv, secretKeyEnv("PASSWORD", "s1", "PASSWORD"))
		cache := envHistoryTestCache(t, pod, rookSecret(reorderAt), secretWrittenAt("s1", reorderAt, map[string][]byte{"PASSWORD": []byte("x")}))
		recordObserved(t, startedAt.Add(-time.Minute), monReorder)
		facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod})
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(facts) != 2 || facts[0].SourceName != "s1" || !facts[1].NoRestartNeeded {
			t.Fatalf("facts = %+v", facts)
		}
		if len(checks) != 2 || checks[0].SecretName != "s1" || !checks[1].NoRestartNeeded {
			t.Fatalf("checks = %+v", checks)
		}
	})

	t.Run("no note when another key of the Secret really changed", func(t *testing.T) {
		pod := monPod(monHostEnv, membersEnv)
		cache := envHistoryTestCache(t, pod, rookSecret(reorderAt))
		recordObserved(t, startedAt.Add(-time.Minute), secretDiffEvent(reorderAt, "shop", "rook-ceph-config",
			map[string]string{"mon_host": rookMonHost, "mon_initial_members": "a,b,d"},
			map[string]string{"mon_host": rookMonHostRe, "mon_initial_members": "c,a,b"}))
		facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(facts) != 1 || facts[0].EvidenceSource != staleSecretEvidenceObservedChange || facts[0].Key != "mon_initial_members" {
			t.Fatalf("facts = %+v", facts)
		}
	})

	t.Run("no note when the Secret was recreated after the container started", func(t *testing.T) {
		pod := monPod(monHostEnv, membersEnv)
		secret := rookSecret(reorderAt)
		secret.CreationTimestamp = metav1.Time{Time: startedAt.Add(time.Minute)}
		cache := envHistoryTestCache(t, pod, secret)
		recordObserved(t, startedAt.Add(-time.Minute), monReorder)
		if facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}); len(facts) != 0 {
			t.Fatalf("facts = %+v", facts)
		}
	})

	t.Run("no note when the last write came before the container started", func(t *testing.T) {
		pod := monPod(monHostEnv, membersEnv)
		cache := envHistoryTestCache(t, pod, rookSecret(startedAt.Add(-time.Minute)))
		recordObserved(t, startedAt.Add(-time.Minute), monReorder)
		if facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}); len(facts) != 0 {
			t.Fatalf("facts = %+v", facts)
		}
	})
}

func factMessages(facts []StaleEnvFact) []string {
	var out []string
	for _, fact := range facts {
		out = append(out, fact.Message)
	}
	return out
}

func checkMessages(checks []StaleSecretEnvCheck) []string {
	var out []string
	for _, check := range checks {
		out = append(out, check.Message)
	}
	return out
}

// recordObserved records events in a fresh timeline whose observation began
// at watchedSince.
func recordObserved(t *testing.T, watchedSince time.Time, events ...timeline.TimelineEvent) {
	t.Helper()
	setEnvHistoryEvents(t, events...)
	timeline.SetObservationStartForTest(watchedSince)
}

func secretDiffEvent(at time.Time, namespace, name string, oldValues, newValues map[string]string) timeline.TimelineEvent {
	toData := func(values map[string]string) map[string][]byte {
		out := make(map[string][]byte, len(values))
		for key, value := range values {
			out[key] = []byte(value)
		}
		return out
	}
	fields, _ := diffSecret(&corev1.Secret{Data: toData(oldValues)}, &corev1.Secret{Data: toData(newValues)})
	return timeline.TimelineEvent{
		ID: fmt.Sprintf("secret-%s-%d", name, at.UnixNano()), Timestamp: at, Source: timeline.SourceInformer,
		ClusterContext: ActiveClusterContext(), Kind: "Secret", Namespace: namespace, Name: name,
		EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: fields},
	}
}

func ownedStaleSecretEnvPod(name string, startedAt time.Time, env ...corev1.EnvVar) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	controller := true
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "shop", UID: "dep"}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "catalog-abc", Namespace: "shop", UID: "rs",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "catalog", UID: "dep", Controller: &controller}},
	}}
	pod := staleSecretEnvPod(name, startedAt, true, env...)
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "catalog-abc", UID: "rs", Controller: &controller}}
	return deployment, replicaSet, pod
}

func onlyDetection(t *testing.T, detections []Detection) Detection {
	t.Helper()
	if len(detections) != 1 {
		t.Fatalf("got %d detections, want 1: %+v", len(detections), detections)
	}
	return detections[0]
}

func assertOrderOnlyCopy(t *testing.T, detection Detection, subject string) {
	t.Helper()
	if detection.Severity != "warning" || detection.Reason != "StaleSecretEnv" {
		t.Fatalf("order-only advisory changed severity or reason: %+v", detection)
	}
	if !strings.HasPrefix(detection.Message, "Only the order of entries changed in Secret data that a Ready pod (") ||
		!strings.Contains(detection.Message, ") of "+subject+" reads as env: ") {
		t.Errorf("message does not lead with the order-only fact: %q", detection.Message)
	}
	for _, want := range []string{"with none added or removed", "Running containers keep the old order until they restart."} {
		if !strings.Contains(detection.Message, want) {
			t.Errorf("message %q does not contain %q", detection.Message, want)
		}
	}
	wantAction := "Only the order of entries changed. Check whether " + subject + " treats the new order differently, for example as a failover or priority order. If it doesn't, no restart is needed. If it does, confirm no rollout is already replacing the affected pods, then restart " + subject + "."
	if detection.Action != wantAction {
		t.Errorf("action:\n got %q\nwant %q", detection.Action, wantAction)
	}
	if !strings.HasPrefix(detection.Cause, "Radar observed only the order of comma-separated entries change") {
		t.Errorf("cause = %q", detection.Cause)
	}
}

func assertRealChangeCopy(t *testing.T, detection Detection, fact string) {
	t.Helper()
	if !strings.Contains(detection.Message, fact) || strings.Contains(detection.Message, "reordered") ||
		!strings.HasSuffix(detection.Message, "Kubernetes reads Secret-backed env values only when a container starts.") ||
		strings.Contains(detection.Action, "Only the order") ||
		detection.Cause != "Radar observed Secret keys referenced by these Ready containers' environment change after the containers started; Kubernetes does not refresh Secret-backed environment variables in running containers." {
		t.Fatalf("real change lost today's copy (want fact %q): %+v", fact, detection)
	}
}

func assertKeyListField(t *testing.T, changes []FieldChange, path string, keys ...string) {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			if got := strings.Join(stringValues(change.NewValue), ","); got != strings.Join(keys, ",") {
				t.Fatalf("%s = %s, want %v", path, got, keys)
			}
			return
		}
	}
	t.Fatalf("no %s field in %+v", path, changes)
}

func hasField(changes []FieldChange, path string) bool {
	for _, change := range changes {
		if change.Path == path {
			return true
		}
	}
	return false
}
