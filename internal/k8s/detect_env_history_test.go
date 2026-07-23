package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRemovedServiceEnvExposureAndPrecision(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "frontend"}},
		}}},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"}}
	cache := envHistoryTestCache(t, deployment, service)
	removal := envHistoryEvent(now.Add(-time.Minute), "Deployment", "shop", "frontend", "spec.template.spec.containers[frontend].env[CART_ADDR]", "cart:8080", nil)
	setEnvHistoryEvents(t, removal)

	got := FindRemovedServiceEnvChecksForObject(context.Background(), cache, deployment)
	if len(got) != 1 {
		t.Fatalf("got %d removed Service env facts, want 1: %+v", len(got), got)
	}
	if got[0].Container != "frontend" || got[0].EnvName != "CART_ADDR" || got[0].ServiceName != "cart" || got[0].ReferencedPort != 8080 || !got[0].RemovedAt.Equal(removal.Timestamp) {
		t.Fatalf("unexpected removed Service env fact: %+v", got[0])
	}
	for _, detection := range detectConfigProblems(cache, "shop", now) {
		if detection.Reason == "RemovedServiceEnv" {
			t.Fatalf("removed env must remain FYI-only, got issue detection: %+v", detection)
		}
	}

	t.Run("missing Service", func(t *testing.T) {
		missingCache := envHistoryTestCache(t, deployment)
		if checks := findRemovedServiceEnvChecks(missingCache, envServiceWorkloadForDeployment(deployment), "", []timeline.TimelineEvent{removal}); len(checks) != 0 {
			t.Fatalf("missing Service should suppress fact: %+v", checks)
		}
	})

	t.Run("non-Service value", func(t *testing.T) {
		event := envHistoryEvent(now, "Deployment", "shop", "frontend", "spec.template.spec.containers[frontend].env[LOG_LEVEL]", "debug", nil)
		if checks := findRemovedServiceEnvChecks(cache, envServiceWorkloadForDeployment(deployment), "", []timeline.TimelineEvent{event}); len(checks) != 0 {
			t.Fatalf("ordinary env removal should not produce fact: %+v", checks)
		}
	})

	t.Run("no removal", func(t *testing.T) {
		event := envHistoryEvent(now, "Deployment", "shop", "frontend", "spec.template.spec.containers[frontend].env[CART_ADDR]", "cart:8080", "cart:9090")
		if checks := findRemovedServiceEnvChecks(cache, envServiceWorkloadForDeployment(deployment), "", []timeline.TimelineEvent{event}); len(checks) != 0 {
			t.Fatalf("env update should not produce removal fact: %+v", checks)
		}
	})

	t.Run("later restore suppresses old removal", func(t *testing.T) {
		restored := envHistoryEvent(now, "Deployment", "shop", "frontend", "spec.template.spec.containers[frontend].env[CART_ADDR]", nil, "cart:8080")
		if checks := findRemovedServiceEnvChecks(cache, envServiceWorkloadForDeployment(deployment), "", []timeline.TimelineEvent{removal, restored}); len(checks) != 0 {
			t.Fatalf("restored env should suppress older removal: %+v", checks)
		}
	})
}

func TestStaleSecretEnvExposureAndFalsePositiveMatrix(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	changedAt := now.Add(-time.Minute)
	startedAt := changedAt.Add(-time.Minute)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "shop"},
		Data:       map[string][]byte{"password": []byte("sentinel-secret-value"), "host": []byte("db")},
	}
	change := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"password"})

	t.Run("healthy is FYI only", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-healthy", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		checks := FindStaleSecretEnvChecksForObject(context.Background(), cache, pod)
		if len(checks) != 1 {
			t.Fatalf("got %d stale Secret env facts, want 1: %+v", len(checks), checks)
		}
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("healthy stale pod must remain FYI-only: %+v", detections)
		}
	})

	t.Run("running not-Ready promotes", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-broken", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		checks := FindStaleSecretEnvChecksForObject(context.Background(), cache, pod)
		if len(checks) != 1 {
			t.Fatalf("not-Ready pod lost FYI fact: %+v", checks)
		}
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" || detections[0].Severity != "warning" || detections[0].Kind != "Pod" || detections[0].Name != pod.Name || detections[0].Fingerprint != "stale-secret-env" {
			t.Fatalf("unexpected promoted detection: %+v", detections)
		}
		encoded, err := json.Marshal(struct {
			Checks     []StaleSecretEnvCheck `json:"checks"`
			Detections []Detection           `json:"detections"`
		}{checks, detections})
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if strings.Contains(string(encoded), "sentinel-secret-value") {
			t.Fatalf("Secret value leaked into diagnostic output: %s", encoded)
		}
	})

	t.Run("promotion describes only not-Ready containers", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-mixed", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Env: []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")}})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: "sidecar", Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		})
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || !strings.Contains(detections[0].Message, "Container app") || strings.Contains(detections[0].Message, "2 stale") {
			t.Fatalf("promotion should describe only the biting container checks: %+v", detections)
		}
	})

	t.Run("mounted Secret volume is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("volume-only", startedAt, false)
		pod.Spec.Volumes = []corev1.Volume{{Name: "creds", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-conn"}}}}
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("mounted Secret must not produce env staleness: %+v", checks)
		}
		if podHasSecretEnvReference(pod) {
			t.Fatal("mounted Secret must not trigger the timeline-query precheck")
		}
	})

	t.Run("restart after change suppresses", func(t *testing.T) {
		pod := staleSecretEnvPod("restarted", changedAt.Add(time.Second), false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("post-change start must suppress stale fact: %+v", checks)
		}
	})

	t.Run("backoff latest start after change suppresses", func(t *testing.T) {
		pod := staleSecretEnvPod("backoff", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.NewTime(changedAt.Add(time.Second))}}
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("post-change crashloop attempt must suppress stale fact: %+v", checks)
		}
	})

	t.Run("metadata-only update is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("metadata", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		metadata := secretHistoryEvent(changedAt, "shop", "db-conn", "immutable", true)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{metadata}); len(checks) != 0 {
			t.Fatalf("metadata-only Secret update must not produce fact: %+v", checks)
		}
	})

	t.Run("newly added envFrom key is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("env-from-added", startedAt, false)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}}}}
		cache := envHistoryTestCache(t, pod, secret)
		added := secretHistoryEvent(changedAt, "shop", "db-conn", "data (added keys)", []string{"password"})
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{added}); len(checks) != 0 {
			t.Fatalf("key added after container start is absent, not a stale env value: %+v", checks)
		}
	})

	t.Run("different key is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("other-key", startedAt, false, secretKeyEnv("DB_HOST", "db-conn", "host"))
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("change to unconsumed key must not produce fact: %+v", checks)
		}
	})

	t.Run("envFrom imports changed key but not values", func(t *testing.T) {
		pod := staleSecretEnvPod("env-from", startedAt, true)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{Prefix: "DB_", SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}}}}
		cache := envHistoryTestCache(t, pod, secret)
		checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change})
		if len(checks) != 1 || checks[0].ReferenceKind != "envFrom" || checks[0].EvidenceSource != staleSecretEvidenceObservedChange || checks[0].EnvName != "DB_password" || checks[0].Key != "password" {
			t.Fatalf("unexpected envFrom fact: %+v", checks)
		}
		if !podHasSecretEnvReference(pod) {
			t.Fatal("envFrom Secret must pass the timeline-query precheck")
		}
	})

	t.Run("newly added secretKeyRef key is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("key-ref-added", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		added := secretHistoryEvent(changedAt, "shop", "db-conn", "data (added keys)", []string{"password"})
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{added}); len(checks) != 0 {
			t.Fatalf("added-key event must not mark a secretKeyRef env stale: %+v", checks)
		}
	})

	// Known accepted limitation: the promotion gate corroborates on the same
	// container being Running-and-not-Ready, but cannot distinguish "not Ready
	// because of the stale value" from "not Ready for an unrelated reason while
	// a consumed key happened to change in-window." The hedged message wording
	// ("may be running a stale env value") is the mitigation.
	t.Run("unrelated not-Ready with coincidental change still promotes (documented limitation)", func(t *testing.T) {
		pod := staleSecretEnvPod("coincidental", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || !strings.Contains(detections[0].Message, "may be running a stale env value") {
			t.Fatalf("expected one hedged warning for the coincidental case: %+v", detections)
		}
	})

	t.Run("promotion sweep is capped", func(t *testing.T) {
		objects := []runtime.Object{secret}
		var pods []*corev1.Pod
		for i := 0; i < maxStaleSecretEnvDetectionsPerNamespace+5; i++ {
			pod := staleSecretEnvPod(fmt.Sprintf("burst-%02d", i), startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
			pods = append(pods, pod)
			objects = append(objects, pod)
		}
		cache := envHistoryTestCache(t, objects...)
		setEnvHistoryEvents(t, change)
		if checks := findStaleSecretEnvChecks(cache, pods, []timeline.TimelineEvent{change}); len(checks) != maxStaleSecretEnvDetectionsPerNamespace+5 {
			t.Fatalf("precondition: every burst pod should produce a fact, got %d", len(checks))
		}
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != maxStaleSecretEnvDetectionsPerNamespace {
			t.Fatalf("mass rotation + rollout must not burst past the sweep cap: got %d, want %d", len(detections), maxStaleSecretEnvDetectionsPerNamespace)
		}
	})
}

func TestStaleSecretEnvManagedFieldsFallback(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-2 * time.Minute)
	changedAt := now.Add(-time.Minute)
	newSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "db-conn",
				Namespace: "shop",
				ManagedFields: []metav1.ManagedFieldsEntry{{
					Manager:    "secret-controller",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					Time:       &metav1.Time{Time: changedAt},
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}},"f:metadata":{"f:annotations":{"f:sync":{}}}}`)},
				}},
			},
			Data: map[string][]byte{"password": []byte("sentinel-secret-value")},
		}
	}

	t.Run("empty timeline emits hedged FYI without a key", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, newSecret())
		if checks := FindStaleSecretEnvChecksForObject(context.Background(), cache, pod); len(checks) != 0 {
			t.Fatalf("basic object context must not emit coarse managedFields evidence: %+v", checks)
		}
		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, nil)
		if len(checks) != 1 {
			t.Fatalf("got %d managedFields checks, want 1: %+v", len(checks), checks)
		}
		check := checks[0]
		if check.EvidenceSource != staleSecretEvidenceManagedFields || check.ReferenceKind != "secretKeyRef" || check.Key != "" || check.EnvName != "" || !check.SecretChangedAt.Equal(changedAt) {
			t.Fatalf("unexpected managedFields check: %+v", check)
		}
		if !strings.Contains(check.Message, "managedFields cannot identify which field changed") || !strings.Contains(check.Message, "if the write touched a consumed key") || strings.Contains(check.Message, "password") {
			t.Fatalf("fallback wording is not properly hedged: %q", check.Message)
		}
		encoded, err := json.Marshal(checks)
		if err != nil {
			t.Fatalf("marshal checks: %v", err)
		}
		if strings.Contains(string(encoded), "sentinel-secret-value") {
			t.Fatalf("Secret value leaked into fallback output: %s", encoded)
		}
	})

	t.Run("precise timeline evidence wins for the whole Secret", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, newSecret())
		precise := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"password"})
		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{precise})
		if len(checks) != 1 || checks[0].EvidenceSource != staleSecretEvidenceObservedChange || checks[0].Key != "password" {
			t.Fatalf("precise evidence must replace the fallback: %+v", checks)
		}

		otherKey := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"host"})
		if checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{otherKey}); len(checks) != 0 {
			t.Fatalf("any precise in-window evidence for the Secret must suppress coarse fallback: %+v", checks)
		}

		addedKey := secretHistoryEvent(changedAt, "shop", "db-conn", "data (added keys)", []string{"new-flag"})
		if checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{addedKey}); len(checks) != 0 {
			t.Fatalf("an observed added-key-only write must suppress the coarse fallback without claiming staleness: %+v", checks)
		}
	})

	t.Run("diagnostic history reaches back to the running container start", func(t *testing.T) {
		oldStart := now.Add(-2 * time.Hour)
		oldChange := now.Add(-90 * time.Minute)
		pod := staleSecretEnvPod("long-running", oldStart, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		secret := newSecret()
		secret.ManagedFields[0].Time = &metav1.Time{Time: oldChange}
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, secretHistoryEvent(oldChange, "shop", "db-conn", "data (modified keys)", []string{"password"}))
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, []*corev1.Pod{pod})
		if len(checks) != 1 || checks[0].EvidenceSource != staleSecretEvidenceObservedChange || checks[0].Key != "password" {
			t.Fatalf("older precise evidence should win over managedFields fallback: %+v", checks)
		}
	})

	t.Run("pre-start write and restart suppress", func(t *testing.T) {
		secret := newSecret()
		secret.ManagedFields[0].Time = &metav1.Time{Time: startedAt.Add(-time.Second)}
		pod := staleSecretEnvPod("pre-start", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, nil); len(checks) != 0 {
			t.Fatalf("pre-start Secret write must not produce a check: %+v", checks)
		}

		restarted := staleSecretEnvPod("restarted", changedAt.Add(time.Second), true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		restartedCache := envHistoryTestCache(t, restarted, newSecret())
		if checks := findStaleSecretEnvChecksWithManagedFields(restartedCache, []*corev1.Pod{restarted}, nil); len(checks) != 0 {
			t.Fatalf("container restart after the Secret write must suppress: %+v", checks)
		}
	})

	t.Run("waiting container and volume-only use are ignored", func(t *testing.T) {
		waiting := staleSecretEnvPod("waiting", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		waiting.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		waitingCache := envHistoryTestCache(t, waiting, newSecret())
		if checks := findStaleSecretEnvChecksWithManagedFields(waitingCache, []*corev1.Pod{waiting}, nil); len(checks) != 0 {
			t.Fatalf("waiting container must not produce a managedFields fallback: %+v", checks)
		}
		precise := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"password"})
		if checks := findStaleSecretEnvChecks(waitingCache, []*corev1.Pod{waiting}, []timeline.TimelineEvent{precise}); len(checks) != 0 {
			t.Fatalf("waiting container must not produce a precise stale-env FYI: %+v", checks)
		}

		volume := staleSecretEnvPod("volume", startedAt, true)
		volume.Spec.Volumes = []corev1.Volume{{Name: "creds", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-conn"}}}}
		volumeCache := envHistoryTestCache(t, volume, newSecret())
		if checks := findStaleSecretEnvChecksWithManagedFields(volumeCache, []*corev1.Pod{volume}, nil); len(checks) != 0 {
			t.Fatalf("Secret volume must not produce a managedFields fallback: %+v", checks)
		}
	})

	t.Run("metadata-only caveat remains FYI-only", func(t *testing.T) {
		pod := staleSecretEnvPod("not-ready", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, newSecret())
		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, nil)
		if len(checks) != 1 {
			t.Fatalf("data-owning manager write should produce one hedged FYI: %+v", checks)
		}
		if biting := staleSecretEnvBitingChecks(pod, checks); len(biting) != 0 {
			t.Fatalf("managedFields fallback must never enter issue promotion: %+v", biting)
		}
	})

	t.Run("envFrom is eligible but unavailable metadata fails closed", func(t *testing.T) {
		pod := staleSecretEnvPod("env-from", startedAt, true)
		pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{
			Prefix:    "DB_",
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}},
		}}
		cache := envHistoryTestCache(t, pod, newSecret())
		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, nil)
		if len(checks) != 1 || checks[0].ReferenceKind != "envFrom" || checks[0].Prefix != "DB_" || checks[0].Key != "" {
			t.Fatalf("unexpected envFrom managedFields check: %+v", checks)
		}

		withoutManagedFields := newSecret()
		withoutManagedFields.ManagedFields = nil
		noSignalCache := envHistoryTestCache(t, pod, withoutManagedFields)
		if checks := findStaleSecretEnvChecksWithManagedFields(noSignalCache, []*corev1.Pod{pod}, nil); len(checks) != 0 {
			t.Fatalf("missing managedFields must fail closed: %+v", checks)
		}
	})

	t.Run("precise evidence wins the per-container cap", func(t *testing.T) {
		pod := staleSecretEnvPod("many-secrets", startedAt, true, secretKeyEnv("PRECISE", "precise", "password"))
		objects := []runtime.Object{pod}
		preciseSecret := newSecret()
		preciseSecret.Name = "precise"
		objects = append(objects, preciseSecret)
		for i := 0; i < maxStaleSecretEnvChecksPerContainer; i++ {
			name := fmt.Sprintf("coarse-%d", i)
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, secretKeyEnv(fmt.Sprintf("COARSE_%d", i), name, "password"))
			secret := newSecret()
			secret.Name = name
			objects = append(objects, secret)
		}
		cache := envHistoryTestCache(t, objects...)
		precise := secretHistoryEvent(changedAt, "shop", "precise", "data (modified keys)", []string{"password"})
		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{precise})
		if len(checks) != maxStaleSecretEnvChecksPerContainer {
			t.Fatalf("checks = %d, want per-container cap %d: %+v", len(checks), maxStaleSecretEnvChecksPerContainer, checks)
		}
		if checks[0].EvidenceSource != staleSecretEvidenceObservedChange || checks[0].SecretName != "precise" {
			t.Fatalf("precise evidence was displaced by coarse fallback: %+v", checks)
		}
	})

	t.Run("workload replicas are grouped before the cap", func(t *testing.T) {
		objects := []runtime.Object{newSecret()}
		var pods []*corev1.Pod
		for i := 0; i < maxStaleSecretEnvDetectionsPerNamespace+5; i++ {
			pod := staleSecretEnvPod(fmt.Sprintf("catalog-%02d", i), startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
			pods = append(pods, pod)
			objects = append(objects, pod)
		}
		cache := envHistoryTestCache(t, objects...)
		setEnvHistoryEvents(t)
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, pods)
		if len(checks) != 1 || checks[0].AffectedPods != maxStaleSecretEnvDetectionsPerNamespace+5 {
			t.Fatalf("replica checks were not grouped before bounding: %+v", checks)
		}
	})

	t.Run("workload-sized distinct fallback is capped", func(t *testing.T) {
		var objects []runtime.Object
		var pods []*corev1.Pod
		for i := 0; i < maxStaleSecretEnvDetectionsPerNamespace+5; i++ {
			name := fmt.Sprintf("db-conn-%02d", i)
			pod := staleSecretEnvPod(fmt.Sprintf("catalog-%02d", i), startedAt, true, secretKeyEnv("DB_PASSWORD", name, "password"))
			secret := newSecret()
			secret.Name = name
			pods = append(pods, pod)
			objects = append(objects, pod, secret)
		}
		cache := envHistoryTestCache(t, objects...)
		setEnvHistoryEvents(t)
		checks := FindStaleSecretEnvChecksForPods(context.Background(), cache, pods)
		if len(checks) != maxStaleSecretEnvDetectionsPerNamespace {
			t.Fatalf("managedFields workload checks = %d, want cap %d", len(checks), maxStaleSecretEnvDetectionsPerNamespace)
		}
	})
}

func TestSecretDataManagerWriteIndex(t *testing.T) {
	dataWrite := time.Date(2026, time.July, 23, 8, 0, 0, 0, time.UTC)
	metadataWrite := dataWrite.Add(time.Hour)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "credentials", Namespace: "shop", UID: types.UID("old"),
		ManagedFields: []metav1.ManagedFieldsEntry{
			{
				Manager: "secret-controller", Operation: metav1.ManagedFieldsOperationUpdate,
				Time: &metav1.Time{Time: dataWrite}, FieldsType: "FieldsV1",
				FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
			},
			{
				Manager: "metadata-controller", Operation: metav1.ManagedFieldsOperationUpdate,
				Time: &metav1.Time{Time: metadataWrite}, FieldsType: "FieldsV1",
				FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:annotations":{"f:data":{}}}}`)},
			},
		},
	}}
	index := &sync.Map{}
	captureSecretDataManagerWriteTime(index, secret)
	value, ok := index.Load(secretWriteKey("shop", "credentials"))
	write, typed := value.(secretDataManagerWrite)
	if !ok || !typed || write.uid != "old" || !write.at.Equal(dataWrite) {
		t.Fatalf("index captured (%+v, %v), want old UID at %v", value, ok, dataWrite)
	}

	recreated := secret.DeepCopy()
	recreated.UID = types.UID("new")
	recreated.ManagedFields[0].Time = &metav1.Time{Time: dataWrite.Add(2 * time.Hour)}
	captureSecretDataManagerWriteTime(index, recreated)
	deleteSecretDataManagerWriteTime(index, k8score.ResourceChange{
		Kind: "Secret", Namespace: "shop", Name: "credentials", UID: "old", Operation: k8score.OpDelete,
	})
	value, ok = index.Load(secretWriteKey("shop", "credentials"))
	write, typed = value.(secretDataManagerWrite)
	if !ok || !typed || write.uid != "new" {
		t.Fatalf("old delete removed the recreated Secret's timestamp: %+v", value)
	}

	helmRelease := recreated.DeepCopy()
	helmRelease.Type = corev1.SecretType("helm.sh/release.v1")
	captureSecretDataManagerWriteTime(index, helmRelease)
	if _, ok := index.Load(secretWriteKey("shop", "credentials")); ok {
		t.Fatal("Helm release payload Secret should not occupy the diagnostic timestamp index")
	}
}

func TestStaleSecretEnvDetectionResolvesWorkloadOwner(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	controller := true
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "shop", UID: types.UID("dep")}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "catalog-abc", Namespace: "shop", UID: types.UID("rs"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: &controller}},
	}}
	pod := staleSecretEnvPod("catalog-abc-1", now.Add(-2*time.Minute), false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet.Name, UID: replicaSet.UID, Controller: &controller}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "shop"}}
	cache := envHistoryTestCache(t, deployment, replicaSet, pod, secret)
	setEnvHistoryEvents(t, secretHistoryEvent(now.Add(-time.Minute), "shop", "db-conn", "data (modified keys)", []string{"password"}))
	detections := detectStaleSecretEnv(cache, "shop", now)
	if len(detections) != 1 || detections[0].OwnerGroup != "apps" || detections[0].OwnerKind != "Deployment" || detections[0].OwnerName != "catalog" {
		t.Fatalf("stale pod detection did not resolve stable workload owner: %+v", detections)
	}
}

func envHistoryTestCache(t *testing.T, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	secretWriteTimes := &sync.Map{}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(objects...),
		ResourceTypes: map[string]bool{
			k8score.Pods: true, k8score.Secrets: true, k8score.Services: true,
			k8score.Deployments: true, k8score.ReplicaSets: true,
		},
		DeferredTypes: map[string]bool{},
		OnTransform: func(obj any) {
			captureSecretDataManagerWriteTime(secretWriteTimes, obj)
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core, secretsEnabled: true, secretWriteTimes: secretWriteTimes}
}

func setEnvHistoryEvents(t *testing.T, events ...timeline.TimelineEvent) {
	t.Helper()
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)
	for _, event := range events {
		if err := timeline.RecordEvent(context.Background(), event); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
}

func envHistoryEvent(timestamp time.Time, kind, namespace, name, path string, oldValue, newValue any) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: "env-" + path, Timestamp: timestamp, Source: timeline.SourceInformer,
		ClusterContext: ActiveClusterContext(), Kind: kind, Namespace: namespace, Name: name,
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: oldValue, NewValue: newValue}}},
	}
}

func secretHistoryEvent(timestamp time.Time, namespace, name, path string, value any) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: "secret-" + path, Timestamp: timestamp, Source: timeline.SourceInformer,
		ClusterContext: ActiveClusterContext(), Kind: "Secret", Namespace: namespace, Name: name,
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, NewValue: value}}},
	}
}

func secretKeyEnv(envName, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: envName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key,
	}}}
}

func staleSecretEnvPod(name string, startedAt time.Time, ready bool, env ...corev1.EnvVar) *corev1.Pod {
	condition := corev1.ConditionFalse
	if ready {
		condition = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: env}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: condition}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: ready,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
			}},
		},
	}
}
