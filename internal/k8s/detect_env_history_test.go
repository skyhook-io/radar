package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

	t.Run("current env suppresses removal hidden by container re-add", func(t *testing.T) {
		restoredDeployment := deployment.DeepCopy()
		restoredDeployment.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "CART_ADDR", Value: "cart:8080"}}
		restoredCache := envHistoryTestCache(t, restoredDeployment, service)
		if checks := findRemovedServiceEnvChecks(restoredCache, envServiceWorkloadForDeployment(restoredDeployment), "", []timeline.TimelineEvent{removal}); len(checks) != 0 {
			t.Fatalf("current env should suppress historical removal hidden by container-level diffs: %+v", checks)
		}
	})

	t.Run("init container Service env removal is recorded", func(t *testing.T) {
		initDeployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "wait-for-cart"}},
				Containers:     []corev1.Container{{Name: "frontend"}},
			}}},
		}
		initCache := envHistoryTestCache(t, initDeployment, service)
		initRemoval := envHistoryEvent(now.Add(-time.Minute), "Deployment", "shop", "frontend", "spec.template.spec.initContainers[wait-for-cart].env[CART_ADDR]", "cart:8080", nil)
		checks := findRemovedServiceEnvChecks(initCache, envServiceWorkloadForDeployment(initDeployment), "", []timeline.TimelineEvent{initRemoval})
		if len(checks) != 1 || checks[0].Container != "wait-for-cart" || checks[0].EnvName != "CART_ADDR" || checks[0].ServiceName != "cart" {
			t.Fatalf("init container Service env removal was not recorded: %+v", checks)
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

	t.Run("Ready pod with observed key change promotes", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-healthy", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		checks := FindStaleSecretEnvChecksForObject(context.Background(), cache, pod)
		if len(checks) != 1 {
			t.Fatalf("got %d stale Secret env facts, want 1: %+v", len(checks), checks)
		}
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" || detections[0].Severity != "warning" ||
			detections[0].Kind != "Pod" || detections[0].Name != pod.Name || detections[0].Fingerprint != "stale-secret-env" {
			t.Fatalf("unexpected Ready-pod detection: %+v", detections)
		}
		detection := detections[0]
		if !strings.Contains(detection.Message, "is Ready") || !strings.Contains(detection.Message, "may still hold the pre-change value") ||
			!strings.Contains(detection.Cause, "does not refresh") || !strings.Contains(detection.Action, "Confirm no replacement") {
			t.Fatalf("Ready-pod detection did not preserve evidence and uncertainty: %+v", detection)
		}
	})

	t.Run("Ready pod consumes an operator-owned Secret rotation", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-external-secret", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		managedChange := change
		managedChange.ID = "managed-secret-change"
		managedChange.Owner = &timeline.OwnerInfo{Kind: "ExternalSecret", Name: "db-conn"}
		setEnvHistoryEvents(t, managedChange)

		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" || detections[0].Name != pod.Name {
			t.Fatalf("owner-bearing Secret updates must remain exact observed evidence: %+v", detections)
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
		if detections[0].Message != "Container app is not Ready and may be running a stale env value for Secret/db-conn key password; restart the pod to refresh it." ||
			detections[0].Cause != "A running container loaded a Secret-backed environment value before Radar observed that Secret key change." ||
			detections[0].Action != "Restart the pod so its containers re-read Secret-backed environment variables." {
			t.Fatalf("not-Ready detection contract changed: %+v", detections[0])
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
		pod := staleSecretEnvPod("restarted", changedAt.Add(time.Second), true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("post-change start must suppress stale fact: %+v", checks)
		}
		setEnvHistoryEvents(t, change)
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("post-change Ready pod must not produce an issue: %+v", detections)
		}
	})

	t.Run("terminating Ready pod is ignored", func(t *testing.T) {
		pod := staleSecretEnvPod("terminating", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		deletedAt := metav1.NewTime(now.Add(-time.Second))
		pod.DeletionTimestamp = &deletedAt
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("terminating pod must not produce a stale-env issue: %+v", detections)
		}
	})

	t.Run("Ready issue describes only containers started before the change", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog-mixed-starts", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Env: []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")}})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: "sidecar", Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(changedAt.Add(time.Second))}},
		})
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || !strings.Contains(detections[0].Message, "a Secret-backed environment value") ||
			strings.Contains(detections[0].Message, "2 Secret-backed") {
			t.Fatalf("Ready issue must include only pre-change container instances: %+v", detections)
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

	t.Run("removed consumed keys are stale evidence", func(t *testing.T) {
		tests := []struct {
			name          string
			path          string
			pod           *corev1.Pod
			referenceKind string
			envName       string
		}{
			{
				name:          "secretKeyRef data",
				path:          "data (removed keys)",
				pod:           staleSecretEnvPod("key-ref-removed", startedAt, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password")),
				referenceKind: "secretKeyRef",
				envName:       "DB_PASSWORD",
			},
			{
				name: "envFrom stringData",
				path: "stringData (removed keys)",
				pod: func() *corev1.Pod {
					pod := staleSecretEnvPod("env-from-removed", startedAt, false)
					pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{
						Prefix:    "DB_",
						SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}},
					}}
					return pod
				}(),
				referenceKind: "envFrom",
				envName:       "DB_password",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				secretAfterRemoval := secret.DeepCopy()
				delete(secretAfterRemoval.Data, "password")
				cache := envHistoryTestCache(t, tt.pod, secretAfterRemoval)
				removed := envHistoryEvent(changedAt, "Secret", "shop", "db-conn", tt.path, []string{"password"}, nil)
				checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{tt.pod}, []timeline.TimelineEvent{removed})
				if len(checks) != 1 || checks[0].ReferenceKind != tt.referenceKind || checks[0].EnvName != tt.envName || checks[0].Key != "password" {
					t.Fatalf("removed consumed key was not reported as stale evidence: %+v", checks)
				}
			})
		}
	})

	t.Run("missing required key is left to the structural issue", func(t *testing.T) {
		pod := staleSecretEnvPod("key-ref-removed-issue", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		secretAfterRemoval := secret.DeepCopy()
		delete(secretAfterRemoval.Data, "password")
		cache := envHistoryTestCache(t, pod, secretAfterRemoval)
		removed := envHistoryEvent(changedAt, "Secret", "shop", "db-conn", "data (removed keys)", []string{"password"}, nil)
		checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{removed})
		if len(checks) != 1 {
			t.Fatalf("drawer evidence should retain the observed removal: %+v", checks)
		}
		changes := latestPodEnvSourceChanges([]timeline.TimelineEvent{removed}, nil)
		if issueChecks := omitMissingRequiredSecretKeyChecks(cache, pod, checks, changes); len(issueChecks) != 0 {
			t.Fatalf("stale-value issue would duplicate the missing-key issue: %+v", issueChecks)
		}

		optionalPod := pod.DeepCopy()
		optionalPod.Name = "key-ref-removed-optional"
		optional := true
		optionalPod.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Optional = &optional
		if issueChecks := omitMissingRequiredSecretKeyChecks(cache, optionalPod, checks, changes); len(issueChecks) != 1 {
			t.Fatalf("optional key removal remains behavioral drift, got %+v", issueChecks)
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

	t.Run("issue sweep reaches past the one-hour window to container start", func(t *testing.T) {
		oldStart := now.Add(-3 * time.Hour)
		oldChange := now.Add(-2 * time.Hour)
		pod := staleSecretEnvPod("long-running-broken", oldStart, false, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, secretHistoryEvent(oldChange, "shop", "db-conn", "data (modified keys)", []string{"password"}))
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" {
			t.Fatalf("a 2h-old key change on a container started 3h ago must still promote to an issue: %+v", detections)
		}
	})

	t.Run("restartable init sidecar promotes", func(t *testing.T) {
		pod := staleSecretEnvPod("sidecar-broken", startedAt, false)
		pod.Spec.Containers = []corev1.Container{{Name: "app"}}
		pod.Spec.InitContainers = []corev1.Container{{
			Name:          "vault-agent",
			RestartPolicy: alwaysRestartPolicy(),
			Env:           []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")},
		}}
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "app", Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "vault-agent", Ready: false,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		if !podHasSecretEnvReference(pod) {
			t.Fatal("restartable init sidecar Secret env must pass the timeline-query precheck")
		}
		checks := FindStaleSecretEnvChecksForObject(context.Background(), cache, pod)
		if len(checks) != 1 || checks[0].Container != "vault-agent" {
			t.Fatalf("restartable init sidecar lost its stale env fact: %+v", checks)
		}
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" || !strings.Contains(detections[0].Message, "Container vault-agent") {
			t.Fatalf("not-Ready restartable init sidecar must promote: %+v", detections)
		}
	})

	t.Run("Ready restartable init sidecar promotes", func(t *testing.T) {
		pod := staleSecretEnvPod("sidecar-ready", startedAt, true)
		pod.Spec.InitContainers = []corev1.Container{{
			Name:          "vault-agent",
			RestartPolicy: alwaysRestartPolicy(),
			Env:           []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")},
		}}
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "vault-agent", Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}
		cache := envHistoryTestCache(t, pod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Reason != "StaleSecretEnv" ||
			!strings.Contains(detections[0].Message, "is Ready") {
			t.Fatalf("Ready restartable init sidecar must promote from exact observed evidence: %+v", detections)
		}
	})

	t.Run("regular init container is not detected", func(t *testing.T) {
		pod := staleSecretEnvPod("init-only", startedAt, false)
		pod.Spec.Containers = []corev1.Container{{Name: "app"}}
		pod.Spec.InitContainers = []corev1.Container{{
			Name: "migrate", // no RestartPolicy => runs once, re-reads env on next pod start
			Env:  []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")},
		}}
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "app", Ready: false,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "migrate", Ready: false,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}}
		cache := envHistoryTestCache(t, pod, secret)
		if podHasSecretEnvReference(pod) {
			t.Fatal("a regular init container's Secret env must not trigger the timeline-query precheck")
		}
		if checks := findStaleSecretEnvChecks(cache, []*corev1.Pod{pod}, []timeline.TimelineEvent{change}); len(checks) != 0 {
			t.Fatalf("a regular init container must be excluded from the stale-env walk: %+v", checks)
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

	t.Run("metadata write preserves the prior data timestamp in diagnosis", func(t *testing.T) {
		pod := staleSecretEnvPod("catalog", startedAt, true, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		secret := newSecret()
		secret.UID = types.UID("secret-uid")
		secret.ResourceVersion = "1"
		cache := envHistoryTestCache(t, pod, secret)

		metadataOnly := secret.DeepCopy()
		metadataOnly.ResourceVersion = "2"
		metadataOnly.Annotations = map[string]string{"rotation": "checked"}
		metadataOnly.ManagedFields[0].Time = &metav1.Time{Time: now}
		cache.secretWriteTimes.capture(metadataOnly)
		cache.secretWriteTimes.reconcile(k8score.ResourceChange{
			Kind: "Secret", Namespace: "shop", Name: "db-conn", UID: "secret-uid",
			Operation: k8score.OpUpdate,
			Diff:      &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "metadata.annotations"}}},
		}, metadataOnly)

		checks := findStaleSecretEnvChecksWithManagedFields(cache, []*corev1.Pod{pod}, nil)
		if len(checks) != 1 || !checks[0].SecretChangedAt.Equal(changedAt) {
			t.Fatalf("metadata-only write replaced the real data timestamp in diagnosis: %+v", checks)
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
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 1 || detections[0].Name != pod.Name {
			t.Fatalf("live issue horizon must align with precise diagnostic history: %+v", detections)
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
		if issueChecks, _ := staleSecretEnvIssueChecks(pod, checks); len(issueChecks) != 0 {
			t.Fatalf("managedFields fallback must never enter issue promotion: %+v", issueChecks)
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
	dataField := func(manager string, at time.Time) metav1.ManagedFieldsEntry {
		return metav1.ManagedFieldsEntry{
			Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate,
			Time: &metav1.Time{Time: at}, FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
		}
	}
	metadataField := func(manager string, at time.Time) metav1.ManagedFieldsEntry {
		return metav1.ManagedFieldsEntry{
			Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate,
			Time: &metav1.Time{Time: at}, FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:annotations":{"f:data":{}}}}`)},
		}
	}
	secret := func(uid, resourceVersion string, fields ...metav1.ManagedFieldsEntry) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "credentials", Namespace: "shop", UID: types.UID(uid),
			ResourceVersion: resourceVersion, ManagedFields: fields,
		}}
	}
	change := func(uid, operation string, diff *k8score.DiffInfo) k8score.ResourceChange {
		return k8score.ResourceChange{
			Kind: "Secret", Namespace: "shop", Name: "credentials",
			UID: uid, Operation: operation, Diff: diff,
		}
	}
	dataDiff := &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "data (modified keys)"}}}
	metadataDiff := &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "metadata.annotations"}}}
	assertWrite := func(t *testing.T, index *secretDataManagerWriteIndex, uid string, at time.Time) {
		t.Helper()
		write, ok := index.load("shop", "credentials")
		if !ok || write.uid != uid || !write.at.Equal(at) {
			t.Fatalf("indexed write = (%+v, %v), want UID %s at %v", write, ok, uid, at)
		}
	}

	t.Run("metadata-only update with no prior state stays suppressed", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		metadataOnly := secret("old", "2", dataField("secret-controller", metadataWrite))
		index.capture(metadataOnly)
		index.reconcile(change("old", k8score.OpUpdate, metadataDiff), metadataOnly)
		if write, ok := index.load("shop", "credentials"); ok {
			t.Fatalf("metadata-only update created a write timestamp without prior state: %+v", write)
		}
	})

	t.Run("same data-owning manager metadata write preserves real data timestamp", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		initial := secret("old", "1", dataField("secret-controller", dataWrite))
		index.capture(initial)
		index.reconcile(change("old", k8score.OpAdd, nil), initial)

		metadataOnly := secret("old", "2", dataField("secret-controller", metadataWrite))
		index.capture(metadataOnly)
		index.reconcile(change("old", k8score.OpUpdate, metadataDiff), metadataOnly)
		assertWrite(t, index, "old", dataWrite)
	})

	t.Run("different manager metadata update preserves prior timestamp", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		initial := secret("old", "1", dataField("secret-controller", dataWrite))
		index.capture(initial)
		index.reconcile(change("old", k8score.OpAdd, nil), initial)

		metadataOnly := secret("old", "2",
			dataField("secret-controller", dataWrite),
			metadataField("metadata-controller", metadataWrite),
		)
		index.capture(metadataOnly)
		index.reconcile(change("old", k8score.OpUpdate, metadataDiff), metadataOnly)
		assertWrite(t, index, "old", dataWrite)
	})

	t.Run("later data write publishes only on exact object version", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		actualWrite := secret("old", "3", dataField("secret-controller", metadataWrite))
		index.capture(actualWrite)

		wrongVersion := actualWrite.DeepCopy()
		wrongVersion.ResourceVersion = "4"
		index.reconcile(change("old", k8score.OpUpdate, dataDiff), wrongVersion)
		if write, ok := index.load("shop", "credentials"); ok {
			t.Fatalf("mismatched resourceVersion published staged timestamp: %+v", write)
		}

		index.reconcile(change("old", k8score.OpUpdate, dataDiff), actualWrite)
		assertWrite(t, index, "old", metadataWrite)
	})

	t.Run("queued data then metadata callbacks preserve the real write", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		dataUpdate := secret("old", "2", dataField("secret-controller", dataWrite))
		metadataUpdate := secret("old", "3", dataField("secret-controller", metadataWrite))
		index.capture(dataUpdate)
		index.capture(metadataUpdate)

		index.reconcile(change("old", k8score.OpUpdate, dataDiff), dataUpdate)
		index.reconcile(change("old", k8score.OpUpdate, metadataDiff), metadataUpdate)
		assertWrite(t, index, "old", dataWrite)
	})

	t.Run("unmatched callbacks remain globally bounded", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		index.pendingLimit = 2
		for _, resourceVersion := range []string{"1", "2", "3"} {
			index.capture(secret("old", resourceVersion, dataField("secret-controller", dataWrite)))
		}
		index.mu.Lock()
		pending := len(index.pending)
		index.mu.Unlock()
		if pending != 1 {
			t.Fatalf("pending candidates = %d after bounded reset, want 1", pending)
		}

		stale := secret("old", "2", dataField("secret-controller", dataWrite))
		index.reconcile(change("old", k8score.OpUpdate, dataDiff), stale)
		if write, ok := index.load("shop", "credentials"); ok {
			t.Fatalf("evicted stale callback published a timestamp: %+v", write)
		}
		latest := secret("old", "3", dataField("secret-controller", metadataWrite))
		index.reconcile(change("old", k8score.OpUpdate, dataDiff), latest)
		assertWrite(t, index, "old", dataWrite)
		index.mu.Lock()
		pending = len(index.pending)
		index.mu.Unlock()
		if pending != 0 {
			t.Fatalf("pending candidates = %d after exact callback, want 0", pending)
		}
	})

	t.Run("recreate and late old callbacks are UID safe", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		oldSecret := secret("old", "1", dataField("secret-controller", dataWrite))
		index.capture(oldSecret)
		index.reconcile(change("old", k8score.OpAdd, nil), oldSecret)

		recreatedAt := dataWrite.Add(2 * time.Hour)
		recreated := secret("new", "1", dataField("secret-controller", recreatedAt))
		index.capture(recreated)
		index.reconcile(change("new", k8score.OpAdd, nil), recreated)
		index.reconcile(change("old", k8score.OpDelete, nil), oldSecret)
		assertWrite(t, index, "new", recreatedAt)

		lateOld := secret("old", "2", dataField("secret-controller", recreatedAt.Add(time.Hour)))
		index.capture(lateOld)
		index.reconcile(change("old", k8score.OpUpdate, dataDiff), lateOld)
		assertWrite(t, index, "new", recreatedAt)
	})

	t.Run("Helm release payload clears only its matching UID", func(t *testing.T) {
		index := newSecretDataManagerWriteIndex()
		recreated := secret("new", "1", dataField("secret-controller", dataWrite))
		index.capture(recreated)
		index.reconcile(change("new", k8score.OpAdd, nil), recreated)

		helmRelease := recreated.DeepCopy()
		helmRelease.ResourceVersion = "2"
		helmRelease.Type = corev1.SecretType("helm.sh/release.v1")
		index.capture(helmRelease)
		index.reconcile(change("new", k8score.OpUpdate, metadataDiff), helmRelease)
		if write, ok := index.load("shop", "credentials"); ok {
			t.Fatalf("Helm release payload Secret occupied the diagnostic timestamp index: %+v", write)
		}
	})
}

func TestSecretDataManagerWriteIndexInformerLifecycle(t *testing.T) {
	dataWrite := time.Date(2026, time.July, 23, 8, 0, 0, 0, time.UTC)
	metadataWrite := dataWrite.Add(time.Hour)
	actualWrite := metadataWrite.Add(time.Hour)
	managedData := func(at time.Time) []metav1.ManagedFieldsEntry {
		return []metav1.ManagedFieldsEntry{{
			Manager: "secret-controller", Operation: metav1.ManagedFieldsOperationUpdate,
			Time: &metav1.Time{Time: at}, FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
		}}
	}
	newCache := func(t *testing.T, secret *corev1.Secret) (*k8score.ResourceCache, *secretDataManagerWriteIndex, *watch.RaceFreeFakeWatcher) {
		t.Helper()
		index := newSecretDataManagerWriteIndex()
		client := fake.NewClientset(secret)
		watcher := watch.NewRaceFreeFake()
		client.PrependWatchReactor("secrets", func(k8stesting.Action) (bool, watch.Interface, error) {
			return true, watcher, nil
		})
		core, err := k8score.NewResourceCache(k8score.CacheConfig{
			Client:        client,
			ResourceTypes: map[string]bool{k8score.Secrets: true},
			DeferredTypes: map[string]bool{},
			OnTransform: func(obj any) {
				index.capture(obj)
			},
			OnObservedChange: func(change k8score.ResourceChange, obj, _ any) {
				index.reconcile(change, obj)
			},
			IsNoisyResource: func(kind, _ string, _ string) bool {
				return kind == "Secret"
			},
			ComputeDiff: func(kind string, oldObj, newObj any) *k8score.DiffInfo {
				return ComputeDiff(kind, oldObj, newObj)
			},
		})
		if err != nil {
			t.Fatalf("NewResourceCache: %v", err)
		}
		t.Cleanup(core.Stop)
		return core, index, watcher
	}
	waitReconciled := func(t *testing.T, core *k8score.ResourceCache, index *secretDataManagerWriteIndex, secret *corev1.Secret) {
		t.Helper()
		version := secretDataManagerWriteVersion{
			namespace:       secret.Namespace,
			name:            secret.Name,
			uid:             string(secret.UID),
			resourceVersion: secret.ResourceVersion,
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			cached, err := core.Secrets().Secrets(secret.Namespace).Get(secret.Name)
			index.mu.Lock()
			_, pending := index.pending[version]
			index.mu.Unlock()
			if err == nil && cached.ResourceVersion == secret.ResourceVersion && !pending {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("Secret %s/%s rv=%s was not reconciled", secret.Namespace, secret.Name, secret.ResourceVersion)
	}

	t.Run("metadata-only update rolls back advanced owner time", func(t *testing.T) {
		initial := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "credentials", Namespace: "shop", UID: types.UID("secret-uid"),
				ResourceVersion: "1", ManagedFields: managedData(dataWrite),
			},
			Data: map[string][]byte{"password": []byte("old")},
		}
		core, index, watcher := newCache(t, initial)
		waitReconciled(t, core, index, initial)

		metadataOnly := initial.DeepCopy()
		metadataOnly.ResourceVersion = "2"
		metadataOnly.ManagedFields = managedData(metadataWrite)
		metadataOnly.Annotations = map[string]string{"rotation": "checked"}
		watcher.Modify(metadataOnly.DeepCopy())
		waitReconciled(t, core, index, metadataOnly)
		write, ok := index.load("shop", "credentials")
		if !ok || !write.at.Equal(dataWrite) {
			t.Fatalf("metadata-only informer update indexed %+v, want prior data timestamp %v", write, dataWrite)
		}
	})

	t.Run("no prior stays suppressed until later data write", func(t *testing.T) {
		initial := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "credentials", Namespace: "shop", UID: types.UID("secret-uid"),
				ResourceVersion: "1",
			},
			Data: map[string][]byte{"password": []byte("old")},
		}
		core, index, watcher := newCache(t, initial)
		waitReconciled(t, core, index, initial)

		metadataOnly := initial.DeepCopy()
		metadataOnly.ResourceVersion = "2"
		metadataOnly.ManagedFields = managedData(metadataWrite)
		metadataOnly.Annotations = map[string]string{"rotation": "checked"}
		watcher.Modify(metadataOnly.DeepCopy())
		waitReconciled(t, core, index, metadataOnly)
		if write, ok := index.load("shop", "credentials"); ok {
			t.Fatalf("metadata-only informer update created timestamp without prior state: %+v", write)
		}

		dataUpdate := metadataOnly.DeepCopy()
		dataUpdate.ResourceVersion = "3"
		dataUpdate.ManagedFields = managedData(actualWrite)
		dataUpdate.Data = map[string][]byte{"password": []byte("new")}
		watcher.Modify(dataUpdate.DeepCopy())
		waitReconciled(t, core, index, dataUpdate)
		write, ok := index.load("shop", "credentials")
		if !ok || !write.at.Equal(actualWrite) {
			t.Fatalf("later data write indexed %+v, want %v", write, actualWrite)
		}
	})
}

func TestSecretChangeWritesData(t *testing.T) {
	for _, path := range []string{
		"data (added keys)", "data (removed keys)", "data (modified keys)",
		"stringData (added keys)", "stringData (removed keys)", "stringData (modified keys)",
	} {
		t.Run(path, func(t *testing.T) {
			change := k8score.ResourceChange{Diff: &k8score.DiffInfo{
				Fields: []k8score.FieldChange{{Path: path}},
			}}
			if !secretChangeWritesData(change) {
				t.Fatalf("%q was not recognized as a Secret payload write", path)
			}
		})
	}
	for _, change := range []k8score.ResourceChange{
		{},
		{Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "metadata.annotations"}}}},
	} {
		if secretChangeWritesData(change) {
			t.Fatalf("non-data change was recognized as a Secret payload write: %+v", change)
		}
	}
}

func TestStaleSecretEnvDetectionResolvesWorkloadOwner(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	controller := true
	deploymentCreatedAt := now.Add(-time.Hour)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "shop", UID: types.UID("dep"), CreationTimestamp: metav1.NewTime(deploymentCreatedAt)}}
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

func TestStaleSecretEnvReadyWorkloadAggregationAndRolloutSuppression(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	changedAt := now.Add(-time.Minute)
	startedAt := changedAt.Add(-time.Minute)
	controller := true
	deploymentCreatedAt := now.Add(-time.Hour)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "shop", UID: types.UID("dep"), CreationTimestamp: metav1.NewTime(deploymentCreatedAt)}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "catalog-abc", Namespace: "shop", UID: types.UID("rs"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: &controller}},
	}}
	replacementReplicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "catalog-def", Namespace: "shop", UID: types.UID("replacement-rs"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: &controller}},
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "shop"}}
	change := secretHistoryEvent(changedAt, "shop", "db-conn", "data (modified keys)", []string{"password"})
	ownedPodForReplicaSet := func(replicaSet *appsv1.ReplicaSet, name string, start time.Time, ready bool) *corev1.Pod {
		pod := staleSecretEnvPod(name, start, ready, secretKeyEnv("DB_PASSWORD", "db-conn", "password"))
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet.Name, UID: replicaSet.UID, Controller: &controller}}
		return pod
	}
	ownedPod := func(name string, start time.Time, ready bool) *corev1.Pod {
		return ownedPodForReplicaSet(replicaSet, name, start, ready)
	}

	t.Run("Ready replicas aggregate to one workload issue", func(t *testing.T) {
		first := ownedPod("catalog-abc-1", startedAt, true)
		second := ownedPod("catalog-abc-2", startedAt, true)
		cache := envHistoryTestCache(t, deployment, replicaSet, first, second, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Group != "apps" || detections[0].Kind != "Deployment" ||
			detections[0].Name != "catalog" || detections[0].OwnerKind != "" {
			t.Fatalf("Ready replicas did not aggregate under the workload: %+v", detections)
		}
		if !strings.Contains(detections[0].Message, "2 Ready pods") || !strings.Contains(detections[0].Message, "2 Secret-backed environment values") ||
			!strings.Contains(detections[0].Action, "Confirm no rollout") {
			t.Fatalf("aggregated workload issue is not operationally clear: %+v", detections[0])
		}
		if detections[0].AgeSeconds != int64(time.Hour.Seconds()) || !detections[0].ResourceCreatedAt.Equal(deploymentCreatedAt) {
			t.Fatalf("aggregated workload resource age/context = %q/%v, want 1h/%v", detections[0].Age, detections[0].ResourceCreatedAt, deploymentCreatedAt)
		}
	})

	t.Run("post-change sibling suppresses an in-progress rollout", func(t *testing.T) {
		oldPod := ownedPod("catalog-abc-old", startedAt, true)
		newPod := ownedPodForReplicaSet(replacementReplicaSet, "catalog-def-new", changedAt.Add(time.Second), true)
		cache := envHistoryTestCache(t, deployment, replicaSet, replacementReplicaSet, oldPod, newPod, secret)
		setEnvHistoryEvents(t, change)
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("a Ready replacement revision consuming the changed key must suppress rollout noise: %+v", detections)
		}
	})

	t.Run("post-change init sidecar sibling suppresses an in-progress rollout", func(t *testing.T) {
		oldPod := ownedPod("catalog-abc-old", startedAt, true)
		newPod := ownedPodForReplicaSet(replacementReplicaSet, "catalog-def-new", changedAt.Add(time.Second), true)
		for _, pod := range []*corev1.Pod{oldPod, newPod} {
			start := pod.Status.ContainerStatuses[0].State.Running.StartedAt
			pod.Spec.Containers[0].Env = nil
			pod.Spec.InitContainers = []corev1.Container{{
				Name:          "vault-agent",
				RestartPolicy: alwaysRestartPolicy(),
				Env:           []corev1.EnvVar{secretKeyEnv("DB_PASSWORD", "db-conn", "password")},
			}}
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
				Name: "vault-agent", Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: start}},
			}}
		}
		cache := envHistoryTestCache(t, deployment, replicaSet, replacementReplicaSet, oldPod, newPod, secret)
		setEnvHistoryEvents(t, change)
		if detections := detectStaleSecretEnv(cache, "shop", now); len(detections) != 0 {
			t.Fatalf("a Ready replacement revision with the same restartable init sidecar must suppress rollout noise: %+v", detections)
		}
	})

	t.Run("same-revision replacement does not suppress indefinitely", func(t *testing.T) {
		oldPod := ownedPod("catalog-abc-old", startedAt, true)
		manuallyReplacedPod := ownedPod("catalog-abc-new", changedAt.Add(time.Second), true)
		cache := envHistoryTestCache(t, deployment, replicaSet, oldPod, manuallyReplacedPod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Kind != "Deployment" || detections[0].Name != deployment.Name {
			t.Fatalf("same-revision sibling must not hide a stale pod indefinitely: %+v", detections)
		}
	})

	t.Run("old replacement evidence does not suppress indefinitely", func(t *testing.T) {
		olderChangeAt := now.Add(-30 * time.Minute)
		oldPod := ownedPod("catalog-abc-old", olderChangeAt.Add(-time.Minute), true)
		oldReplacement := ownedPodForReplicaSet(replacementReplicaSet, "catalog-def-old", olderChangeAt.Add(time.Minute), true)
		cache := envHistoryTestCache(t, deployment, replicaSet, replacementReplicaSet, oldPod, oldReplacement, secret)
		setEnvHistoryEvents(t, secretHistoryEvent(olderChangeAt, "shop", "db-conn", "data (modified keys)", []string{"password"}))
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Kind != "Deployment" || detections[0].Name != deployment.Name {
			t.Fatalf("expired rollout evidence must not hide a stale pod indefinitely: %+v", detections)
		}
	})

	t.Run("replacement revision must consume the same Secret key", func(t *testing.T) {
		oldPod := ownedPod("catalog-abc-old", startedAt, true)
		newPod := ownedPodForReplicaSet(replacementReplicaSet, "catalog-def-new", changedAt.Add(time.Second), true)
		newPod.Spec.Containers[0].Env = []corev1.EnvVar{secretKeyEnv("OTHER", "other-secret", "token")}
		cache := envHistoryTestCache(t, deployment, replicaSet, replacementReplicaSet, oldPod, newPod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Kind != "Deployment" || detections[0].Name != deployment.Name {
			t.Fatalf("an unrelated replacement revision must not suppress the stale key: %+v", detections)
		}
	})

	t.Run("not-Ready evidence takes precedence over the Ready summary", func(t *testing.T) {
		readyPod := ownedPod("catalog-abc-ready", startedAt, true)
		brokenPod := ownedPod("catalog-abc-broken", startedAt, false)
		cache := envHistoryTestCache(t, deployment, replicaSet, readyPod, brokenPod, secret)
		setEnvHistoryEvents(t, change)
		detections := detectStaleSecretEnv(cache, "shop", now)
		if len(detections) != 1 || detections[0].Kind != "Pod" || detections[0].Name != brokenPod.Name ||
			detections[0].OwnerKind != "Deployment" || !strings.Contains(detections[0].Message, "is not Ready") {
			t.Fatalf("not-Ready pod must retain the established issue contract without a duplicate Ready summary: %+v", detections)
		}
	})
}

func TestStaleSecretEnvReadySubjectKeepsOwnerIdentityAndSafeAction(t *testing.T) {
	controller := true
	pod := staleSecretEnvPod("report-abc", time.Now().Add(-time.Minute), true)
	pod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: "Job", Name: "report", Controller: &controller,
	}}

	subject := staleSecretEnvSubjectForPod(nil, pod)
	if subject.group != "batch" || subject.kind != "Job" || subject.name != "report" || subject.restartable {
		t.Fatalf("non-restartable Job owner must retain stable owner identity without restart capability: %+v", subject)
	}
	if action := staleSecretEnvReadyAction(subject); !strings.Contains(action, "Job/report's normal controller workflow") ||
		strings.Contains(action, "restart Job/") {
		t.Fatalf("non-restartable controller received unsafe workload guidance: %q", action)
	}
}

func TestStaleSecretEnvNonRestartableOwnerDoesNotDuplicateReadyAndNotReady(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	changedAt := now.Add(-time.Minute)
	controller := true
	ownedPod := func(name string, ready bool) *corev1.Pod {
		pod := staleSecretEnvPod(name, changedAt.Add(-time.Minute), ready, secretKeyEnv("TOKEN", "job-secret", "token"))
		pod.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: "Job", Name: "report", UID: types.UID("job"), Controller: &controller,
		}}
		return pod
	}
	readyPod := ownedPod("report-ready", true)
	notReadyPod := ownedPod("report-not-ready", false)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "job-secret", Namespace: "shop"}}
	readyCache := envHistoryTestCache(t, readyPod.DeepCopy(), secret.DeepCopy())
	setEnvHistoryEvents(t, secretHistoryEvent(changedAt, "shop", "job-secret", "data (modified keys)", []string{"token"}))
	readyDetections := detectStaleSecretEnv(readyCache, "shop", now)
	if len(readyDetections) != 1 || readyDetections[0].Group != "batch" || readyDetections[0].Kind != "Job" ||
		readyDetections[0].Name != "report" || !strings.Contains(readyDetections[0].Message, "has a Ready pod") ||
		strings.Contains(readyDetections[0].Message, "Job/report is Ready") || strings.Contains(readyDetections[0].Action, "restart Job/") {
		t.Fatalf("Ready Job-owned pod lost stable identity or received unsafe wording: %+v", readyDetections)
	}

	cache := envHistoryTestCache(t, readyPod, notReadyPod, secret)
	setEnvHistoryEvents(t, secretHistoryEvent(changedAt, "shop", "job-secret", "data (modified keys)", []string{"token"}))

	detections := detectStaleSecretEnv(cache, "shop", now)
	if len(detections) != 1 || detections[0].Kind != "Pod" || detections[0].Name != notReadyPod.Name ||
		detections[0].OwnerGroup != "batch" || detections[0].OwnerKind != "Job" || detections[0].OwnerName != "report" {
		t.Fatalf("not-Ready evidence must dominate without a duplicate Ready Job issue: %+v", detections)
	}
}

func TestStaleSecretEnvRolloutEvidenceIndexBoundsReplacementCandidates(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	controller := true
	var pods []*corev1.Pod
	for i := 0; i < 100; i++ {
		pod := staleSecretEnvPod(fmt.Sprintf("catalog-%03d", i), now.Add(-time.Minute), true, secretKeyEnv("PASSWORD", "db-conn", "password"))
		pod.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "ReplicaSet", Name: fmt.Sprintf("catalog-%03d", i),
			UID: types.UID(fmt.Sprintf("rs-%03d", i)), Controller: &controller,
		}}
		pods = append(pods, pod)
	}
	subject := staleSecretEnvSubject{group: "apps", kind: "Deployment", namespace: "shop", name: "catalog", restartable: true}
	evidence := newStaleSecretEnvRolloutEvidence(subject, pods, now)
	key := staleSecretEnvConsumption{container: "app", secret: "db-conn", key: "password"}
	if got := len(evidence.replacements[key]); got != 2 {
		t.Fatalf("replacement candidates = %d, want a lookup bounded to the two newest revisions", got)
	}
	check := StaleSecretEnvCheck{
		PodName: pods[0].Name, Container: "app", SecretName: "db-conn", Key: "password",
		SecretChangedAt: now.Add(-2 * time.Minute),
	}
	if !evidence.supersedes(check) {
		t.Fatal("bounded replacement index lost valid evidence from another revision")
	}
}

func TestQuerySecretDataHistoryFailsClosedWhenReferencedHistorySaturates(t *testing.T) {
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: maxSecretDataHistoryEvents + 1}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)

	now := time.Now().UTC().Truncate(time.Second)
	pod := staleSecretEnvPod("catalog", now.Add(-time.Hour), true, secretKeyEnv("PASSWORD", "db-conn", "password"))
	for i := 0; i < maxSecretDataHistoryEvents; i++ {
		event := secretHistoryEvent(now.Add(-time.Duration(i)*time.Second), "shop", "db-conn", "data (modified keys)", []string{"password"})
		event.ID = fmt.Sprintf("secret-saturation-%04d", i)
		if err := timeline.RecordEvent(context.Background(), event); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}
	if events := querySecretDataHistory(context.Background(), "", []*corev1.Pod{pod}, now.Add(-24*time.Hour)); len(events) != 0 {
		t.Fatalf("saturated history must fail closed instead of returning a misleading partial set: %d events", len(events))
	}
}

func envHistoryTestCache(t *testing.T, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	secretWriteTimes := newSecretDataManagerWriteIndex()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(objects...),
		ResourceTypes: map[string]bool{
			k8score.Pods: true, k8score.Secrets: true, k8score.Services: true,
			k8score.Deployments: true, k8score.ReplicaSets: true,
		},
		DeferredTypes: map[string]bool{},
		OnTransform: func(obj any) {
			secretWriteTimes.capture(obj)
		},
		OnObservedChange: func(change k8score.ResourceChange, obj, _ any) {
			secretWriteTimes.reconcile(change, obj)
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

func alwaysRestartPolicy() *corev1.ContainerRestartPolicy {
	policy := corev1.ContainerRestartPolicyAlways
	return &policy
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
