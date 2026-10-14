package server

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Contract tests for the typed ?include=summary keep-lists. Two directions,
// both load-bearing:
//   - kept paths feed live table cells, sorts, filters and problem badges —
//     losing one breaks a column silently;
//   - dropped paths are why the summary exists — one leaking back quietly
//     regrows the payload toward the 25k-guard problem.

func fatPod() *corev1.Pod {
	restartAlways := corev1.ContainerRestartPolicyAlways
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-0",
			Namespace:         "prod",
			UID:               "uid-1",
			ResourceVersion:   "12345",
			Generation:        7,
			Finalizers:        []string{"example.com/guard"},
			CreationTimestamp: metav1.Unix(1700000000, 0),
			Labels:            map[string]string{"app": "web", "team": "core"},
			Annotations:       map[string]string{"example.com/build": "abc123"},
			OwnerReferences:   []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-rs"}},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-7",
			ServiceAccountName: "web-sa",
			NodeSelector:       map[string]string{"zone": "a"},
			Tolerations:        []corev1.Toleration{{Key: "dedicated"}},
			Volumes:            []corev1.Volume{{Name: "data"}},
			InitContainers: []corev1.Container{{
				Name:          "sidecar",
				Image:         "sidecar:1",
				RestartPolicy: &restartAlways,
				Env:           []corev1.EnvVar{{Name: "MODE", Value: "sidecar"}},
			}},
			Containers: []corev1.Container{{
				Name:  "web",
				Image: "web:1.2.3",
				Env:   []corev1.EnvVar{{Name: "SECRET", Value: "x"}},
				EnvFrom: []corev1.EnvFromSource{{
					ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}},
				}},
				VolumeMounts:   []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				LivenessProbe:  &corev1.Probe{},
				ReadinessProbe: &corev1.Probe{},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:                    resource.MustParse("100m"),
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Reason:     "",
			PodIP:      "10.0.0.9",
			PodIPs:     []corev1.PodIP{{IP: "10.0.0.9"}},
			HostIP:     "192.168.1.1",
			QOSClass:   corev1.PodQOSBurstable,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "web",
				Ready:        true,
				RestartCount: 3,
				Image:        "web:1.2.3",
				ImageID:      "sha256:deadbeef",
				ContainerID:  "containerd://abc",
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Unix(1700000100, 0)}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137,
				}},
			}},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "sidecar", Ready: true, RestartCount: 1,
				Image: "sidecar:1",
			}},
		},
	}
}

func asJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func dig(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for i, seg := range path {
		asMap, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: segment %d (%s) is not an object", path, i, seg)
		}
		cur = asMap[seg]
	}
	return cur
}

func firstElem(t *testing.T, v any) map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("expected non-empty array, got %T", v)
	}
	m, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("expected object element, got %T", arr[0])
	}
	return m
}

func TestSummarizePodRowKeepsTableFields(t *testing.T) {
	m := asJSONMap(t, summarizePodRow(fatPod()))

	meta := dig(t, m, "metadata").(map[string]any)
	for _, k := range []string{"name", "namespace", "uid", "generation", "creationTimestamp", "labels", "annotations", "ownerReferences"} {
		if meta[k] == nil {
			t.Errorf("metadata.%s dropped — table identity/filter field", k)
		}
	}
	if got := dig(t, m, "spec", "nodeName"); got != "node-7" {
		t.Errorf("spec.nodeName = %v", got)
	}
	c := firstElem(t, dig(t, m, "spec", "containers"))
	if c["name"] != "web" {
		t.Errorf("containers[0].name = %v", c["name"])
	}
	requests := dig(t, c, "resources", "requests").(map[string]any)
	if requests["nvidia.com/gpu"] == nil {
		t.Error("GPU request dropped — GPU column reads it")
	}
	ic := firstElem(t, dig(t, m, "spec", "initContainers"))
	if ic["restartPolicy"] != "Always" {
		t.Errorf("initContainers[0].restartPolicy = %v — native-sidecar marker for podCrashHistoryLevel", ic["restartPolicy"])
	}
	if dig(t, m, "status", "phase") != "Running" || dig(t, m, "status", "podIP") != "10.0.0.9" {
		t.Error("status.phase/podIP dropped")
	}
	if dig(t, m, "status", "podIPs") == nil || dig(t, m, "status", "conditions") == nil {
		t.Error("status.podIPs/conditions dropped — problem detection reads them")
	}
	cs := firstElem(t, dig(t, m, "status", "containerStatuses"))
	if cs["restartCount"] != float64(3) || cs["ready"] != true {
		t.Errorf("containerStatuses restartCount/ready dropped: %v", cs)
	}
	if dig(t, cs, "state", "running") == nil {
		t.Error("containerStatuses[0].state dropped — container squares read it")
	}
	if got := dig(t, cs, "lastState", "terminated", "reason"); got != "OOMKilled" {
		t.Errorf("lastState.terminated.reason = %v — crash tooltip reads it", got)
	}
	ics := firstElem(t, dig(t, m, "status", "initContainerStatuses"))
	if ics["name"] != "sidecar" {
		t.Errorf("initContainerStatuses dropped: %v", ics)
	}
}

func TestSummarizePodRowDropsHeavyFields(t *testing.T) {
	m := asJSONMap(t, summarizePodRow(fatPod()))

	spec := dig(t, m, "spec").(map[string]any)
	for _, k := range []string{"volumes", "tolerations", "nodeSelector", "serviceAccountName", "serviceAccount"} {
		if spec[k] != nil {
			t.Errorf("spec.%s survived the strip", k)
		}
	}
	c := firstElem(t, dig(t, m, "spec", "containers"))
	for _, k := range []string{"env", "envFrom", "volumeMounts", "livenessProbe", "readinessProbe", "image"} {
		if c[k] != nil {
			t.Errorf("spec.containers[0].%s survived the strip", k)
		}
	}
	status := dig(t, m, "status").(map[string]any)
	for _, k := range []string{"hostIP", "qosClass"} {
		if status[k] != nil {
			t.Errorf("status.%s survived the strip", k)
		}
	}
	cs := firstElem(t, dig(t, m, "status", "containerStatuses"))
	for _, k := range []string{"imageID", "containerID"} {
		if v, ok := cs[k]; ok && v != "" {
			t.Errorf("containerStatuses[0].%s survived the strip: %v", k, v)
		}
	}
	meta := dig(t, m, "metadata").(map[string]any)
	for _, k := range []string{"resourceVersion", "finalizers", "managedFields"} {
		if meta[k] != nil {
			t.Errorf("metadata.%s survived the strip", k)
		}
	}
}

func TestSummarizePodRowDoesNotMutateInput(t *testing.T) {
	p := fatPod()
	summarizePodRow(p)
	if len(p.Spec.Containers[0].Env) != 1 || len(p.Spec.Volumes) != 1 || p.Status.HostIP == "" {
		t.Fatal("summarizePodRow mutated the cache object")
	}
}

func TestSummarizeReplicaSetRow(t *testing.T) {
	replicas := int32(4)
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-rs", Namespace: "prod", UID: "uid-2", Generation: 5,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
			Labels:          map[string]string{"app": "web"},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "web", Image: "web:1", Env: []corev1.EnvVar{{Name: "X", Value: "y"}}}},
			}},
		},
		Status: appsv1.ReplicaSetStatus{
			Replicas: 4, ReadyReplicas: 3, AvailableReplicas: 3, ObservedGeneration: 4,
			Conditions: []appsv1.ReplicaSetCondition{{Type: appsv1.ReplicaSetReplicaFailure}},
		},
	}

	m := asJSONMap(t, summarizeReplicaSetRow(rs))

	if got := dig(t, m, "spec", "replicas"); got != float64(4) {
		t.Errorf("spec.replicas = %v — Active/Old badge reads it", got)
	}
	status := dig(t, m, "status").(map[string]any)
	for _, k := range []string{"replicas", "readyReplicas", "availableReplicas"} {
		if status[k] == nil {
			t.Errorf("status.%s dropped — ready column/status filter reads it", k)
		}
	}
	if got := dig(t, m, "metadata", "generation"); got != float64(5) {
		t.Errorf("metadata.generation = %v — convergence-grace compares it to observedGeneration", got)
	}
	if got := dig(t, m, "status", "observedGeneration"); got != float64(4) {
		t.Errorf("status.observedGeneration = %v — convergence-grace reads it", got)
	}
	if dig(t, m, "metadata", "ownerReferences") == nil {
		t.Error("ownerReferences dropped — Owner column reads it")
	}

	spec := dig(t, m, "spec").(map[string]any)
	if spec["template"] != nil {
		t.Errorf("spec.template survived the strip: %v", spec["template"])
	}
	if spec["selector"] != nil {
		t.Errorf("spec.selector survived the strip: %v", spec["selector"])
	}
	if status["conditions"] != nil {
		t.Error("status.conditions survived the strip")
	}
}

func TestSummarizeTypedListShapes(t *testing.T) {
	pods := []*corev1.Pod{fatPod(), fatPod()}
	if out, ok := summarizeTypedList("pods", pods).([]any); !ok || len(out) != 2 {
		t.Fatalf("lister-shape input not summarized: %T", summarizeTypedList("pods", pods))
	}
	merged := []any{fatPod(), fatPod()}
	if out, ok := summarizeTypedList("pods", merged).([]any); !ok || len(out) != 2 {
		t.Fatalf("merged-shape input not summarized")
	} else if _, isRow := out[0].(*podRow); !isRow {
		t.Fatalf("merged-shape element not a summarized pod row: %T", out[0])
	}
	// Unprofiled kinds pass through untouched.
	svcs := []*corev1.Service{{}}
	if _, ok := summarizeTypedList("services", svcs).([]*corev1.Service); !ok {
		t.Fatalf("unprofiled kind was transformed")
	}
}

func TestSummarizePodRowKeepsPodLevelResources(t *testing.T) {
	p := fatPod()
	p.Spec.Resources = &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
	}
	m := asJSONMap(t, summarizePodRow(p))
	if got := dig(t, m, "spec", "resources", "limits", "cpu"); got != "2" {
		t.Errorf("spec.resources.limits.cpu = %v — the CPU/Mem column reads the pod-level ceiling", got)
	}
}
