package envresolve

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolvePodPrecedenceAndSecretTaint(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop", Labels: map[string]string{"tier": "api"}},
		Spec: corev1.PodSpec{NodeName: "worker-1", ServiceAccountName: "api", Containers: []corev1.Container{{
			Name: "app",
			EnvFrom: []corev1.EnvFromSource{
				{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "base"}}},
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}}},
			},
			Env: []corev1.EnvVar{
				{Name: "HOST", Value: "override"},
				{Name: "DSN", Value: "postgres://$(PASSWORD)@$(HOST)"},
				{Name: "TIER", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['tier']"}}},
				{Name: "HOST_IPS", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIPs"}}},
				{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}},
				{Name: "POD_IPS", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIPs"}}},
				{Name: "NODE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
			},
		}}},
		Status: corev1.PodStatus{HostIPs: []corev1.HostIP{{IP: "192.0.2.1"}, {IP: "2001:db8::1"}}, PodIP: "10.0.0.2", PodIPs: []corev1.PodIP{{IP: "10.0.0.2"}, {IP: "fd00::2"}}},
	}
	sources := map[SourceID]SourceData{
		{Kind: "ConfigMap", Name: "base"}: {Kind: "ConfigMap", Name: "base", State: SourceAvailable, Keys: []string{"HOST"}, Values: map[string]string{"HOST": "db"}},
		{Kind: "Secret", Name: "auth"}:    {Kind: "Secret", Name: "auth", State: SourceAvailable, Keys: []string{"PASSWORD"}, Sensitive: true},
	}
	got := ResolvePod(pod, sources, NodeData{})
	rows := rowsByName(got.Containers[0].Rows)
	if rows["HOST"].Value != "override" || len(rows["HOST"].ShadowedSources) != 1 {
		t.Fatalf("HOST = %+v", rows["HOST"])
	}
	if !rows["DSN"].Sensitive || rows["DSN"].State != ValueMasked || rows["DSN"].Value != "" {
		t.Fatalf("DSN = %+v", rows["DSN"])
	}
	foundSecretDependency := false
	for _, dependency := range rows["DSN"].Dependencies {
		foundSecretDependency = foundSecretDependency || dependency.Kind == "Secret" && dependency.Name == "auth" && dependency.Key == "PASSWORD"
	}
	if !foundSecretDependency {
		t.Fatalf("DSN lost Secret provenance: %+v", rows["DSN"])
	}
	if rows["TIER"].Value != "api" || !rows["TIER"].CurrentPodValue {
		t.Fatalf("TIER = %+v", rows["TIER"])
	}
	if rows["HOST_IPS"].Value != "192.0.2.1,2001:db8::1" || rows["POD_IP"].Value != "10.0.0.2" || rows["POD_IPS"].Value != "10.0.0.2,fd00::2" || rows["NODE"].Value != "worker-1" {
		t.Fatalf("field refs: HOST_IPS=%+v POD_IP=%+v POD_IPS=%+v NODE=%+v", rows["HOST_IPS"], rows["POD_IP"], rows["POD_IPS"], rows["NODE"])
	}
}

func TestResolvePodSinglePassAndRuntimeDependency(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{
		{Name: "A", Value: "$(IMAGE_VAR)"},
		{Name: "B", Value: "$(A)"},
		{Name: "C", Value: "$$(A)"},
	}}}}}
	rows := rowsByName(ResolvePod(pod, nil, NodeData{}).Containers[0].Rows)
	if !rows["A"].RuntimeDependent || rows["A"].State != ValueUnavailable {
		t.Fatalf("A = %+v", rows["A"])
	}
	if !rows["B"].RuntimeDependent || rows["B"].Value != "$(IMAGE_VAR)" || rows["B"].State != ValueUnavailable {
		t.Fatalf("B = %+v", rows["B"])
	}
	if rows["C"].Value != "$(A)" {
		t.Fatalf("C = %+v", rows["C"])
	}
}

func TestResolvePodDeniedEnvFromDoesNotDiscloseKeys(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		EnvFrom: []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}},
		}},
	}}}}
	got := ResolvePod(pod, map[SourceID]SourceData{
		{Kind: "Secret", Name: "auth"}: {Kind: "Secret", Name: "auth", State: SourceDenied, Sensitive: true},
	}, NodeData{})
	if len(got.Containers[0].Rows) != 1 || !got.Containers[0].Rows[0].Placeholder || got.Containers[0].Rows[0].State != ValueDenied {
		t.Fatalf("rows = %+v", got.Containers[0].Rows)
	}
}

func TestResolvePodMarksOptionalMissingKey(t *testing.T) {
	optional := true
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{Name: "FEATURE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "feature", Optional: &optional,
		}}}},
	}}}}
	rows := ResolvePod(pod, map[SourceID]SourceData{
		{Kind: "ConfigMap", Name: "settings"}: {Kind: "ConfigMap", Name: "settings", State: SourceAvailable},
	}, NodeData{}).Containers[0].Rows
	if len(rows) != 1 || rows[0].State != ValueMissing || !rows[0].Optional {
		t.Fatalf("optional missing key = %+v", rows)
	}
	if rows[0].MissingImpact != "" {
		t.Fatalf("optional missing key must not carry failure impact: %+v", rows[0])
	}
}

func TestResolvePodDoesNotTreatOptionalMissingDependencyAsBlocking(t *testing.T) {
	optional := true
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "FEATURE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "feature", Optional: &optional,
				}}},
				{Name: "MESSAGE", Value: "feature=$(FEATURE)"},
			},
		}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	rows := rowsByName(ResolvePod(pod, map[SourceID]SourceData{
		{Kind: "Secret", Name: "settings"}: {Kind: "Secret", Name: "settings", State: SourceAvailable, Sensitive: true},
	}, NodeData{}).Containers[0].Rows)
	if rows["MESSAGE"].State != ValueResolved || rows["MESSAGE"].Value != "feature=$(FEATURE)" || rows["MESSAGE"].Sensitive || len(rows["MESSAGE"].Dependencies) != 0 || rows["MESSAGE"].MissingImpact != "" {
		t.Fatalf("dependent value did not preserve optional missing reference: %+v", rows["MESSAGE"])
	}
}

func TestResolvePodClassifiesRequiredMissingImpact(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{Name: "SETTING", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "required",
		}}}},
	}}}}
	sources := map[SourceID]SourceData{
		{Kind: "ConfigMap", Name: "settings"}: {Kind: "ConfigMap", Name: "settings", State: SourceAvailable},
	}

	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", ContainerID: "containerd://running", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	running := ResolvePod(pod, sources, NodeData{}).Containers[0].Rows[0]
	if running.MissingImpact != MissingImpactRestartBlocked || !strings.Contains(running.Message, "cannot start again") {
		t.Fatalf("running container impact = %+v", running)
	}

	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"}},
	}
	blocked := ResolvePod(pod, sources, NodeData{}).Containers[0].Rows[0]
	if blocked.MissingImpact != MissingImpactStartupBlocked || !strings.Contains(blocked.Message, "prevents the container from starting") {
		t.Fatalf("blocked container impact = %+v", blocked)
	}

	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name:                 "app",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	down := ResolvePod(pod, sources, NodeData{}).Containers[0].Rows[0]
	if down.MissingImpact != MissingImpactStartupBlocked || !strings.Contains(down.Message, "prevents the container from starting") {
		t.Fatalf("down container impact = %+v", down)
	}
}

func TestResolvePodKeepsUnavailableEnvFromSourcesSeparate(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		EnvFrom: []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "first"}}},
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "second"}}},
		},
	}}}}
	got := ResolvePod(pod, map[SourceID]SourceData{
		{Kind: "ConfigMap", Name: "first"}:  {Kind: "ConfigMap", Name: "first", State: SourceMissing},
		{Kind: "ConfigMap", Name: "second"}: {Kind: "ConfigMap", Name: "second", State: SourceDenied},
	}, NodeData{})
	if len(got.Containers[0].Rows) != 2 || !got.Containers[0].Rows[0].Placeholder || !got.Containers[0].Rows[1].Placeholder {
		t.Fatalf("unavailable sources collapsed: %+v", got.Containers[0].Rows)
	}
}

func TestResolvePodPropagatesDeniedDependency(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{
		{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}},
		{Name: "DSN", Value: "postgres://$(PASSWORD)"},
	}}}}}
	rows := rowsByName(ResolvePod(pod, map[SourceID]SourceData{
		{Kind: "Secret", Name: "db"}: {Kind: "Secret", Name: "db", State: SourceDenied, Sensitive: true},
	}, NodeData{}).Containers[0].Rows)
	if rows["DSN"].State != ValueDenied || !rows["DSN"].Sensitive {
		t.Fatalf("DSN did not inherit denied Secret state: %+v", rows["DSN"])
	}
}

func TestResolvePodUsesNodeAllocatableForMissingLimit(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{Name: "CPU_LIMIT", ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{
			Resource: "limits.cpu", Divisor: resource.MustParse("1m"),
		}}}},
	}}}}
	node := NodeData{State: SourceAvailable, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}

	rows := rowsByName(ResolvePod(pod, nil, node).Containers[0].Rows)
	if rows["CPU_LIMIT"].Value != "2000" || !rows["CPU_LIMIT"].CurrentPodValue {
		t.Fatalf("CPU_LIMIT = %+v", rows["CPU_LIMIT"])
	}

	denied := rowsByName(ResolvePod(pod, nil, NodeData{State: SourceDenied}).Containers[0].Rows)
	if denied["CPU_LIMIT"].State != ValueDenied {
		t.Fatalf("denied CPU_LIMIT = %+v", denied["CPU_LIMIT"])
	}
}

func TestResolvePodUsesResourceFieldRefContainerName(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{
		{Name: "producer", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")}}},
		{Name: "consumer", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}, Env: []corev1.EnvVar{
			{Name: "PRODUCER_CPU", ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{
				ContainerName: "producer", Resource: "limits.cpu", Divisor: resource.MustParse("1m"),
			}}},
		}},
	}}}
	if PodEnvironmentNeedsNode(pod) {
		t.Fatal("declared target-container limit should not require Node data")
	}
	rows := rowsByName(ResolvePod(pod, nil, NodeData{}).Containers[1].Rows)
	if rows["PRODUCER_CPU"].Value != "250" {
		t.Fatalf("PRODUCER_CPU = %+v", rows["PRODUCER_CPU"])
	}

	delete(pod.Spec.Containers[0].Resources.Limits, corev1.ResourceCPU)
	if !PodEnvironmentNeedsNode(pod) {
		t.Fatal("missing target-container limit should require Node data")
	}
	rows = rowsByName(ResolvePod(pod, nil, NodeData{
		State: SourceAvailable, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
	}).Containers[1].Rows)
	if rows["PRODUCER_CPU"].Value != "2000" {
		t.Fatalf("PRODUCER_CPU with Node fallback = %+v", rows["PRODUCER_CPU"])
	}
}

func rowsByName(rows []Row) map[string]Row {
	out := make(map[string]Row, len(rows))
	for _, row := range rows {
		out[row.Name] = row
	}
	return out
}
