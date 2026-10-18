package k8s

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFindStaleEnvConsumersOfSecret(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-10 * time.Minute)
	changedAt := now.Add(-5 * time.Minute)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "shop"},
		Data:       map[string][]byte{"password": []byte("sentinel-secret-value"), "host": []byte("db")},
	}
	rotated := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"password"})

	t.Run("rotated key read as env is flagged", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret.DeepCopy())
		setEnvHistoryEvents(t, rotated)
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn")
		if len(facts) != 1 {
			t.Fatalf("got %d facts, want 1: %+v", len(facts), facts)
		}
		fact := facts[0]
		if fact.Pod != "catalog" || fact.Container != "app" || fact.Env != "DB_PASSWORD" || fact.Key != "password" ||
			fact.SourceKind != "Secret" || fact.EvidenceSource != staleSecretEvidenceObservedChange || !fact.ChangedAt.Equal(changedAt) {
			t.Fatalf("unexpected fact: %+v", fact)
		}
		want := fmt.Sprintf("Pod catalog container app reads Secret/db-conn key password as env DB_PASSWORD and started at %s, before Radar observed a write to that key at %s. Env values don't refresh, so it likely still holds the earlier value until it restarts.",
			startedAt.Format(time.RFC3339), changedAt.Format(time.RFC3339))
		if fact.Message != want {
			t.Fatalf("message = %q\nwant      %q", fact.Message, want)
		}
		if strings.Contains(fmt.Sprintf("%+v", facts), "sentinel-secret-value") {
			t.Fatalf("Secret value leaked: %+v", facts)
		}
	})

	t.Run("volume consumer is not flagged", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true)
		pod.Spec.Volumes = []corev1.Volume{{Name: "db", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-conn"}}}}
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "db", MountPath: "/etc/db"}}
		cache := envHistoryTestCache(t, pod, secret.DeepCopy())
		setEnvHistoryEvents(t, rotated)
		if facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn"); len(facts) != 0 {
			t.Fatalf("volume consumer flagged: %+v", facts)
		}
	})

	t.Run("Pod started after the write is not flagged", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", changedAt.Add(time.Minute), true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret.DeepCopy())
		setEnvHistoryEvents(t, rotated)
		if facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn"); len(facts) != 0 {
			t.Fatalf("Pod started after the write flagged: %+v", facts)
		}
	})

	t.Run("other key and other Secret are not flagged", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_HOST", "db-conn", "host"), secretKeyEnv("API", "other", "password"))
		other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "shop"}}
		cache := envHistoryTestCache(t, pod, secret.DeepCopy(), other)
		setEnvHistoryEvents(t, rotated, secretHistoryEvent(changedAt, "shop", "other", "data (modified keys)", []string{"password"}))
		if facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn"); len(facts) != 0 {
			t.Fatalf("unrelated key or Secret flagged: %+v", facts)
		}
	})

	t.Run("replicas group into one row", func(t *testing.T) {
		a := staleSecretEnvPod("catalog-b", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		b := staleSecretEnvPod("catalog-a", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, a, b, secret.DeepCopy())
		setEnvHistoryEvents(t, rotated)
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn")
		if len(facts) != 1 || facts[0].Pod != "catalog-a" || facts[0].AffectedPods != 2 ||
			!strings.HasPrefix(facts[0].Message, "Pod catalog-a (and 1 other Pods) container app reads") {
			t.Fatalf("replicas not grouped: %+v", facts)
		}
	})

	t.Run("managedFields write before Radar started keeps the hedge", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		withWrite := secret.DeepCopy()
		withWrite.ManagedFields = []metav1.ManagedFieldsEntry{{
			Manager: "secret-controller", Operation: metav1.ManagedFieldsOperationUpdate,
			Time: &metav1.Time{Time: changedAt}, FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
		}}
		cache := envHistoryTestCache(t, pod, withWrite)
		setEnvHistoryEvents(t)
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn")
		if len(facts) != 1 {
			t.Fatalf("got %d facts, want 1: %+v", len(facts), facts)
		}
		fact := facts[0]
		if fact.EvidenceSource != staleSecretEvidenceManagedFields || fact.Key != "" || fact.Env != "" {
			t.Fatalf("fallback claimed a key or env: %+v", fact)
		}
		if !strings.HasPrefix(fact.Message, "Pod catalog: Secret/db-conn had a write at "+changedAt.Format(time.RFC3339)+" by secret-controller") ||
			!strings.Contains(fact.Message, "the write may not have touched data at all") {
			t.Fatalf("fallback message lost its hedge: %q", fact.Message)
		}
	})

	t.Run("managedFields write by a release tool names its metadata writes", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		withWrite := secret.DeepCopy()
		withWrite.ManagedFields = []metav1.ManagedFieldsEntry{{
			Manager: "helm", Operation: metav1.ManagedFieldsOperationUpdate,
			Time: &metav1.Time{Time: changedAt}, FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}},"f:metadata":{"f:labels":{"f:helm.sh/chart":{}}}}`)},
		}}
		cache := envHistoryTestCache(t, pod, withWrite)
		setEnvHistoryEvents(t)
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn")
		if len(facts) != 1 || facts[0].EvidenceSource != staleSecretEvidenceManagedFields || facts[0].Key != "" {
			t.Fatalf("want one keyless managedFields row: %+v", facts)
		}
		if !strings.Contains(facts[0].Message, "helm also writes Secret labels or annotations, so managedFields cannot tell whether this write changed data") {
			t.Fatalf("release-tool wording missing: %q", facts[0].Message)
		}
	})

	t.Run("every rotated envFrom key is reported, past the diagnose per-container cap", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, corev1.EnvVar{Name: "K1", Value: "pinned"}, corev1.EnvVar{Name: "K2", Value: "$(K2)?sslmode=require"})
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}}}}
		keys := []string{"K1", "K2", "K3", "K4", "K5", "K6", "K7"}
		cache := envHistoryTestCache(t, pod, secret.DeepCopy())
		setEnvHistoryEvents(t, secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", keys))
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "Secret", "shop", "db-conn")
		// K1 is pinned by an explicit env entry, which overrides envFrom; K2 expands
		// $(K2), so it still depends on the Secret value.
		if len(facts) != len(keys)-1 {
			t.Fatalf("got %d facts, want %d: %+v", len(facts), len(keys)-1, facts)
		}
		for _, fact := range facts {
			if fact.Env == "K1" {
				t.Fatalf("envFrom key overridden by explicit env flagged: %+v", fact)
			}
			if fact.ReferenceKind != "envFrom" || !strings.Contains(fact.Message, "through envFrom as env "+fact.Env) {
				t.Fatalf("unexpected envFrom fact: %+v", fact)
			}
		}
	})
}

func TestFindStaleEnvConfigMap(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-10 * time.Minute)
	changedAt := now.Add(-5 * time.Minute)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "shop"},
		Data:       map[string]string{"log.level": "debug", "region": "eu"},
	}
	configMapKeyEnv := func(envName, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: envName, ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: key,
		}}}
	}
	// Radar diffs ConfigMap values structurally, as data.<key>.
	modified := envHistoryEvent(changedAt, "ConfigMap", "shop", "settings", "data.log.level", "info", "debug")

	t.Run("modified dotted key read as env is flagged on the ConfigMap and the Pod", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, configMapKeyEnv("LOG_LEVEL", "log.level"), configMapKeyEnv("REGION", "region"))
		cache := envHistoryTestCache(t, pod, configMap.DeepCopy())
		setEnvHistoryEvents(t, modified)
		for name, facts := range map[string][]StaleEnvFact{
			"ConfigMap": FindStaleEnvConsumersOf(context.Background(), cache, "ConfigMap", "shop", "settings"),
			"Pod":       FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}),
		} {
			if len(facts) != 1 || facts[0].Env != "LOG_LEVEL" || facts[0].Key != "log.level" || facts[0].SourceKind != "ConfigMap" ||
				facts[0].ReferenceKind != "configMapKeyRef" || !strings.Contains(facts[0].Message, "reads ConfigMap/settings key log.level as env LOG_LEVEL") {
				t.Fatalf("%s: unexpected facts: %+v", name, facts)
			}
		}
	})

	t.Run("envFrom key held in binaryData is not flagged", func(t *testing.T) {
		withBinary := configMap.DeepCopy()
		withBinary.BinaryData = map[string][]byte{"TRUSTSTORE": []byte{1}}
		withBinary.Data["MODE"] = "a"
		pod := staleSecretEnvPod("catalog", startedAt, true)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}},
		}
		cache := envHistoryTestCache(t, pod, withBinary)
		setEnvHistoryEvents(t,
			envHistoryEvent(changedAt, "ConfigMap", "shop", "settings", "data.MODE", "b", "a"),
			envHistoryEvent(changedAt.Add(time.Second), "ConfigMap", "shop", "settings", "binaryData (modified keys)", []string{"TRUSTSTORE"}, []string{"TRUSTSTORE"}),
		)
		facts := FindStaleEnvConsumersOf(context.Background(), cache, "ConfigMap", "shop", "settings")
		if len(facts) != 1 || facts[0].Env != "MODE" || facts[0].ReferenceKind != "envFrom" {
			t.Fatalf("want only MODE flagged: %+v", facts)
		}
	})

	t.Run("change inside a JSON array value is flagged", func(t *testing.T) {
		withArray := configMap.DeepCopy()
		withArray.Data["origins"] = `["a","c"]`
		pod := staleSecretEnvPod("catalog", startedAt, true, configMapKeyEnv("ORIGINS", "origins"))
		cache := envHistoryTestCache(t, pod, withArray)
		setEnvHistoryEvents(t, envHistoryEvent(changedAt, "ConfigMap", "shop", "settings", "data.origins[1]", "b", "c"))
		if facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}); len(facts) != 1 || facts[0].Key != "origins" {
			t.Fatalf("array element change not flagged: %+v", facts)
		}
	})

	t.Run("added key is not flagged", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, configMapKeyEnv("REGION", "region"))
		cache := envHistoryTestCache(t, pod, configMap.DeepCopy())
		setEnvHistoryEvents(t, envHistoryEvent(changedAt, "ConfigMap", "shop", "settings", "data (added keys)", nil, []string{"region"}))
		if facts := FindStaleEnvForPods(context.Background(), cache, []*corev1.Pod{pod}); len(facts) != 0 {
			t.Fatalf("added key flagged: %+v", facts)
		}
	})
}
