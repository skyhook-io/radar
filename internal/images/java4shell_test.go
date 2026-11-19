package images

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestJava4ShellVersions(t *testing.T) {
	for _, tc := range []struct {
		version string
		match   bool
		fix     string
		known   bool
	}{
		{"17.0.5", false, "", true},
		{"17.0.6", true, "17.0.21.1", true},
		{"17.0.21.0.3", true, "17.0.21.1", true},
		{"17.0.21.1+2", false, "17.0.21.1", true},
		{"jdk-21.0.13+9", true, "21.0.13.1", true},
		{"jdk-21+35", true, "21.0.13.1", true},
		{"jdk-17+35", false, "", true},
		{"jdk-22+36", true, "", true},
		{"jdk-11+28", false, "", true},
		{"21.0.13.1", false, "21.0.13.1", true},
		{"21.0.13.9", false, "", false},
		{"21.0.13.7.1", false, "", false},
		{"21.0.13.1-ea+1", false, "", false},
		{"21.0.13+9-1", false, "", false},
		{"21.0.13+8-1ubuntu1~26.04", false, "", false},
		{"25.0.5", true, "25.0.5.1", true},
		{"26.0.2", true, "", true},
		{"27.0.1.1", false, "27.0.1.1", true},
		{"8u511b33-perf", true, "", true},
		{"8u511b33", false, "", false},
		{"jdk8u432-b06", false, "", true},
		{"11.0.33", false, "", true},
		{"21", false, "", false},
		{"", false, "", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			match, fix, known := java4ShellVersion(tc.version)
			if match != tc.match || fix != tc.fix || known != tc.known {
				t.Fatalf("got (%v, %q, %v), want (%v, %q, %v)", match, fix, known, tc.match, tc.fix, tc.known)
			}
		})
	}
}

func TestJava4ShellReadsRunningEphemeralContainerOptions(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{
				Name: "debug", Image: "eclipse-temurin:21",
				Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-XX:-UseBASE64Intrinsics"}},
			},
		}}},
		Status: corev1.PodStatus{EphemeralContainerStatuses: []corev1.ContainerStatus{{
			Name: "debug", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	containers := runningContainers(pod)
	if len(containers) != 1 || !java4ShellMitigationDeclared(containers[0]) {
		t.Fatalf("running debug container mitigation was lost: %+v", containers)
	}
}

func TestJava4ShellKeepsPlacementSeparateFromImageEvidence(t *testing.T) {
	digest := "registry.example/team/app@sha256:" + strings.Repeat("a", 64)
	private := "registry.example/team/private@sha256:" + strings.Repeat("b", 64)
	f := &fakeLookup{
		configs: map[string]*v1.ConfigFile{
			refKey(t, digest): {Config: v1.Config{Env: []string{"JAVA_VERSION=jdk-21.0.13+9"}}},
		},
		errs: map[string]error{refKey(t, private) + "|anonymous": unauthorized()},
	}
	newTarget := func(name, imageID, arch string, labels map[string]string) Java4ShellPod {
		p := runningPod("team", name, name+"-node", "web", map[string][2]string{"app": {"registry.example/team/app:latest", imageID}})
		p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "private-credentials"}}
		p.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
		var node *corev1.Node
		if arch != "" {
			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux", Architecture: arch}},
			}
		}
		return Java4ShellPod{Pod: p, Node: node}
	}
	unknownLabel := newTarget("a-unknown-label", digest, "amd64", map[string]string{"feature.node.kubernetes.io/cpu-cpuid.BMI2": "true"})
	vbmi := newTarget("b-vbmi", digest, "amd64", map[string]string{java4ShellVBMILabel: "true"})
	vbmi.Pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-XX:-UseBASE64Intrinsics"}}
	arm := newTarget("c-arm", digest, "arm64", nil)
	pods := []Java4ShellPod{
		unknownLabel, vbmi, arm,
		newTarget("d-unreadable-node", digest, "", nil),
		newTarget("e-tag-only", "sha256:"+strings.Repeat("c", 64), "amd64", nil),
		newTarget("f-private", private, "amd64", nil),
	}
	for i := range 110 {
		replica := unknownLabel.Pod.DeepCopy()
		replica.Name = fmt.Sprintf("replica-%d", i)
		pods = append(pods, Java4ShellPod{Pod: replica, Node: unknownLabel.Node})
	}
	anotherWorkload := newTarget("other-app", digest, "amd64", nil)
	pods = append(pods, anotherWorkload)
	got, err := fakeInventory(f).CheckJava4Shell(context.Background(), pods, func(pod *corev1.Pod) resourceid.Ref {
		workload := "web"
		if pod == anotherWorkload.Pod {
			workload = "another-workload"
		}
		return resourceid.NewRef("apps", "Deployment", pod.Namespace, workload)
	})
	if err != nil || got.Containers != 117 || got.Matches != 114 || got.FindingGroups != 2 || len(got.Findings) != 2 {
		t.Fatalf("check: %+v, %v", got, err)
	}
	if row := got.Findings[0]; row.Workload.Name != "web" || row.MitigationDeclaredPods != 1 || row.FixedIn != "21.0.13.1" ||
		row.Pods != 113 || row.VBMIPods != 1 || row.UnknownCPUPods != 111 || got.Findings[1].Workload.Name != "another-workload" {
		t.Fatalf("findings: %+v", got.Findings)
	}
	for _, reason := range []string{"platform_unknown", "image_identity_unknown", StatusAuthRequired} {
		if got.Unassessed[reason] != 1 {
			t.Fatalf("missing %s: %+v", reason, got.Unassessed)
		}
	}
	if f.calls.Load() != 3 {
		t.Fatalf("expected one request per digest and observed platform group: %v", f.seenCopy())
	}
	for _, call := range f.seenCopy() {
		if !strings.HasSuffix(call, " anonymous") {
			t.Fatalf("lookup used a credential: %s", call)
		}
	}
}

func TestJava4ShellNeverDialsRadarLoopback(t *testing.T) {
	var requests atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registry.Close()
	image := strings.TrimPrefix(registry.URL, "http://") + "/test/java"
	pod := runningPod("team", "app", "", "", map[string][2]string{
		"app": {image + ":1", image + "@sha256:" + strings.Repeat("a", 64)},
	})
	pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
	got, err := newRuntimeInventory(registryConfigLookup(newGuardedTransport(false)), clusterKeychain).
		CheckJava4Shell(context.Background(), []Java4ShellPod{{Pod: pod}}, nil)
	if err != nil || requests.Load() != 0 || got.Unassessed[StatusBlocked] != 1 {
		t.Fatalf("got %+v, %v, and %d loopback requests", got, err, requests.Load())
	}
}
