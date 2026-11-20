package images

import (
	"context"
	"encoding/json"
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
		{"8u511-b33-perf", true, "", true},
		{"8u511b33", false, "", false},
		{"jdk8u432-b06", false, "", true},
		{"1.8.0_452", false, "", true},
		{"1.8.0_511", false, "", false},
		{"11.0.33", false, "", true},
		{"21", false, "", false},
		{"", false, "", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			major, match, fix := java4ShellVersion(tc.version)
			known := major != 0
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
		p.Status.ContainerStatuses[0].Image = "sha256:" + strings.Repeat("e", 64)
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
	vbmi.Node.Spec.ProviderID = "aws:///us-east-1a/i-confirmed"
	vbmi.Node.Labels[corev1.LabelInstanceTypeStable] = "m6a.large"
	vbmi.Pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-XX:-UseBASE64Intrinsics"}}
	arm := newTarget("c-arm", digest, "arm64", nil)
	aws := newTarget("a-aws", digest, "amd64", map[string]string{corev1.LabelInstanceTypeStable: "m6a.large"})
	aws.Node.Spec.ProviderID = "aws:///us-east-1a/i-test"
	gcp := newTarget("a-gcp", digest, "amd64", map[string]string{corev1.LabelInstanceTypeStable: "c2d-standard-4"})
	gcp.Node.Spec.ProviderID = "gce://project/region-a/instance"
	azure := newTarget("a-azure", digest, "amd64", map[string]string{corev1.LabelInstanceTypeStable: "Standard_D4as_v4"})
	azure.Node.Spec.ProviderID = "azure:///subscriptions/sub/resourceGroups/group/providers/Microsoft.Compute/virtualMachineScaleSets/pool/virtualMachines/1"
	pods := []Java4ShellPod{
		unknownLabel, vbmi, arm, aws, gcp, azure,
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
	got, err := fakeInventory(f).CheckJava4Shell(context.Background(), pods, func(_ context.Context, pod *corev1.Pod) resourceid.Ref {
		workload := "web"
		if pod == anotherWorkload.Pod {
			workload = "another-workload"
		}
		return resourceid.NewRef("apps", "Deployment", pod.Namespace, workload)
	}, nil)
	if err != nil || got.Containers != 120 || got.Matches != 117 || got.FindingGroups != 2 || len(got.Findings) != 2 {
		t.Fatalf("check: %+v, %v", got, err)
	}
	if row := got.Findings[0]; row.Workload.Name != "web" || row.Image != "registry.example/team/app" ||
		row.MitigationDeclaredPods != 1 || row.FixedIn != "21.0.13.1" ||
		row.Pods != 116 || row.VBMIPods != 1 || row.UnknownCPUPods != 111 || got.Findings[1].Workload.Name != "another-workload" {
		t.Fatalf("findings: %+v", got.Findings)
	}
	if inferred := got.Findings[0].NoVBMIByInstanceType; len(inferred) != 3 || inferred["m6a.large"] != 1 || inferred["c2d-standard-4"] != 1 || inferred["Standard_D4as_v4"] != 1 {
		t.Fatalf("cloud inference: %v", inferred)
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

func TestJava4ShellInstanceTypeWithoutVBMI(t *testing.T) {
	for _, tt := range []struct {
		providerID   string
		instanceType string
		arch         string
		inferred     bool
	}{
		{"aws:///zone/id", "c6a.metal", "amd64", true},
		{"aws:///zone/id", "m6a.large", "amd64", true},
		{"aws:///zone/id", "r6a.xlarge", "amd64", true},
		{"gce://project/zone/node", "c2-standard-4", "amd64", true},
		{"gce://project/zone/node", "c2d-highcpu-16", "amd64", true},
		{"azure:///subscriptions/id", "Standard_D4a_v4", "amd64", true},
		{"azure:///subscriptions/id", "standard_d16as_v4", "amd64", true},
		{"aws:///zone/id", "m6i.large", "amd64", false},
		{"aws:///zone/id", "m6a", "amd64", false},
		{"gce://project/zone/node", "n2-standard-4", "amd64", false},
		{"azure:///subscriptions/id", "Standard_D4as_v5", "amd64", false},
		{"azure:///subscriptions/id", "Standard_D4s_v4", "amd64", false},
		{"gce://project/zone/node", "m6a.large", "amd64", false},
		{"", "m6a.large", "amd64", false},
		{"aws:///zone/id", "m6a.large", "", false},
		{"aws:///zone/id", "m6a.large", "arm64", false},
	} {
		t.Run(tt.providerID+"/"+tt.instanceType+"/"+tt.arch, func(t *testing.T) {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{corev1.LabelInstanceTypeStable: tt.instanceType}},
				Spec:       corev1.NodeSpec{ProviderID: tt.providerID},
				Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: tt.arch}},
			}
			want := ""
			if tt.inferred {
				want = tt.instanceType
			}
			if got := java4ShellInstanceTypeWithoutVBMI(node); got != want {
				t.Fatalf("inferred instance type = %q, want %q", got, want)
			}
		})
	}
}

func TestJava4ShellSpendsLookupBudgetOnUncachedImages(t *testing.T) {
	for _, tc := range []struct {
		registry string
		limit    int
		reason   string
	}{
		{registry: "docker.io", limit: java4ShellDockerHubLookups, reason: java4ShellDockerHubLimit},
		{registry: "registry.example", limit: maxLookupsPerCall, reason: StatusNotChecked},
	} {
		t.Run(tc.registry, func(t *testing.T) {
			configs := make(map[string]*v1.ConfigFile, tc.limit+2)
			var targets []Java4ShellPod
			node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}}}
			for i := range tc.limit + 2 {
				name := fmt.Sprintf("fixture-%03d", i)
				image := tc.registry + "/example/" + name
				ref := image + "@sha256:" + strings.Repeat("a", 64)
				cfg := &v1.ConfigFile{}
				if i == tc.limit+1 {
					cfg.Config.Env = []string{"JAVA_VERSION=21.0.13"}
				}
				configs[refKey(t, ref)] = cfg
				pod := runningPod("team", name, "worker", name, map[string][2]string{"app": {image + ":1", ref}})
				pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
				targets = append(targets, Java4ShellPod{Pod: pod, Node: node})
			}
			f := &fakeLookup{configs: configs}
			inventory := fakeInventory(f)
			first, err := inventory.CheckJava4Shell(context.Background(), targets[tc.limit+1:], nil, nil)
			if err != nil || first.Matches != 1 || f.calls.Load() != 1 {
				t.Fatalf("narrow check: matches=%d lookups=%d err=%v", first.Matches, f.calls.Load(), err)
			}
			wide, err := inventory.CheckJava4Shell(context.Background(), targets, nil, nil)
			if err != nil || wide.Matches != 1 || wide.Unassessed[tc.reason] != 1 || int(f.calls.Load()) != tc.limit+1 {
				t.Fatalf("wide check: matches=%d unassessed=%v lookups=%d err=%v", wide.Matches, wide.Unassessed, f.calls.Load(), err)
			}
			retry, err := inventory.CheckJava4Shell(context.Background(), targets, nil, nil)
			if err != nil || retry.Matches != 1 || retry.Unassessed[tc.reason] != 0 ||
				retry.Unassessed[noJavaDeclarationReason] != tc.limit+1 || int(f.calls.Load()) != tc.limit+2 {
				t.Fatalf("repeat check: matches=%d unassessed=%v lookups=%d err=%v", retry.Matches, retry.Unassessed, f.calls.Load(), err)
			}
		})
	}
}

func TestJava4ShellPacedScanChecksMoreThanTheOneShotLimit(t *testing.T) {
	const images = maxLookupsPerCall + 1
	var targets []Java4ShellPod
	lookup := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}}}
	for i := range 1200 {
		image := fmt.Sprintf("registry.example/team/app-%03d", i%images)
		ref := image + "@sha256:" + strings.Repeat("a", 64)
		lookup.configs[refKey(t, ref)] = &v1.ConfigFile{Config: v1.Config{Env: []string{"JAVA_VERSION=jdk8u432-b06"}}}
		pod := runningPod("team", fmt.Sprintf("fixture-%04d", i), "worker", "app", map[string][2]string{"app": {image + ":1", ref}})
		pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
		targets = append(targets, Java4ShellPod{Pod: pod, Node: node})
	}
	next := 0
	result, err := fakeInventory(lookup).ScanJava4Shell(context.Background(), targets, nil, nil, func(done, total int) {
		if done != next || total != images {
			t.Errorf("unexpected progress %d/%d, wanted %d/%d", done, total, next, images)
		}
		next++
	})
	if err != nil || next != images+1 || lookup.calls.Load() != images ||
		len(result.Unassessed) != 0 || result.UnaffectedByMajor[8] != 1200 || result.Matches != 0 {
		t.Fatalf("paced scan: completed=%d lookups=%d result=%+v error=%v", next-1, lookup.calls.Load(), result, err)
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
	got, err := newRuntimeInventory(registryConfigLookup(newGuardedTransport(false)), clusterKeychain, registryFetchSlots).
		CheckJava4Shell(context.Background(), []Java4ShellPod{{Pod: pod}}, nil, nil)
	if err != nil || requests.Load() != 0 || got.Unassessed[StatusBlocked] != 1 {
		t.Fatalf("got %+v, %v, and %d loopback requests", got, err, requests.Load())
	}
}

func TestJava4ShellUsesJavaHomeOnlyWhenVersionIsMissing(t *testing.T) {
	const javaHome = "/opt/fixture-jvm"
	for _, tc := range []struct {
		name                string
		env                 []string
		noNode              bool
		reason              string
		wantMatch           int
		wantUnaffectedMajor int
	}{
		{name: "home only", env: []string{"JAVA_HOME=" + javaHome}, reason: javaHomeDeclaredReason},
		{name: "no Java declaration", env: []string{"OTHER_JAVA_HOME=" + javaHome}, reason: noJavaDeclarationReason},
		{name: "last home cleared", env: []string{"JAVA_HOME=" + javaHome, "JAVA_HOME= "}, reason: noJavaDeclarationReason},
		{name: "last home set", env: []string{"JAVA_HOME=", "JAVA_HOME=" + javaHome}, reason: javaHomeDeclaredReason},
		{name: "affected version", env: []string{"JAVA_HOME=" + javaHome, "JAVA_VERSION=jdk-21.0.13+9"}, wantMatch: 1},
		{name: "fixed version", env: []string{"JAVA_HOME=" + javaHome, "JAVA_VERSION=jdk-21.0.13.1+2"}, wantUnaffectedMajor: 21},
		{name: "ordinary Java 8", env: []string{"JAVA_VERSION=jdk8u432-b06"}, wantUnaffectedMajor: 8},
		{name: "affected Java 8 performance pack", env: []string{"JAVA_VERSION=8u511b33-perf"}, wantMatch: 1},
		{name: "ambiguous Java 8 build", env: []string{"JAVA_VERSION=8u511b33"}, reason: "version_unknown"},
		{name: "unrecognized version", env: []string{"JAVA_HOME=" + javaHome, "JAVA_VERSION=21"}, reason: "version_unknown"},
		{name: "invalid version value", env: []string{"JAVA_HOME=" + javaHome, "JAVA_VERSION=invalid version"}, reason: javaHomeDeclaredReason},
		{name: "unresolved platform", env: []string{"JAVA_HOME=" + javaHome}, noNode: true, reason: "platform_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := "registry.example/team/java"
			ref := image + "@sha256:" + strings.Repeat("a", 64)
			f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, ref): {Config: v1.Config{Env: tc.env}}}}
			pod := runningPod("team", "app", "node", "app", map[string][2]string{"app": {image + ":1", ref}})
			pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
			target := Java4ShellPod{Pod: pod}
			if !tc.noNode {
				target.Node = &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}}}
			}
			result, err := fakeInventory(f).CheckJava4Shell(context.Background(), []Java4ShellPod{target}, nil, nil)
			if err != nil || result.Matches != tc.wantMatch || result.Containers != 1 || f.calls.Load() != 1 {
				t.Fatalf("unexpected check result %+v, %v, lookups=%d", result, err, f.calls.Load())
			}
			if tc.reason == "" {
				if len(result.Unassessed) != 0 {
					t.Fatalf("assessed version reported as unassessed: %+v", result.Unassessed)
				}
			} else if result.Unassessed[tc.reason] != 1 || len(result.UnassessedRows) != 1 || result.UnassessedRows[0].Reason != tc.reason {
				t.Fatalf("want reason %q, got %+v", tc.reason, result)
			}
			if tc.wantUnaffectedMajor == 0 && len(result.UnaffectedByMajor) != 0 ||
				tc.wantUnaffectedMajor != 0 && result.UnaffectedByMajor[tc.wantUnaffectedMajor] != 1 {
				t.Fatalf("unaffected by Java major: %+v", result.UnaffectedByMajor)
			}
			report, err := json.Marshal(result)
			if err != nil || strings.Contains(string(report), javaHome) {
				t.Fatalf("unexpected report: %s, %v", report, err)
			}
		})
	}
}

func TestJava4ShellBoundsUnassessedRowsAndKeepsTotals(t *testing.T) {
	baseImage := "registry.example/team/base"
	digest := baseImage + "@sha256:" + strings.Repeat("a", 64)
	javaDigest := "registry.example/team/java@sha256:" + strings.Repeat("b", 64)
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{
		refKey(t, digest):     {},
		refKey(t, javaDigest): {Config: v1.Config{Env: []string{"JAVA_HOME=/opt/java"}}},
	}}
	var targets []Java4ShellPod
	node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}}}
	add := func(name, workload, imageID string) {
		pod := runningPod("team", name, "node", workload, map[string][2]string{"app": {baseImage + ":1", imageID}})
		pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
		targets = append(targets, Java4ShellPod{Pod: pod, Node: node})
	}
	for i := range java4ShellRowLimit + 2 {
		name := fmt.Sprintf("base-%03d", i)
		add(name, name, "")
	}
	add("generic", "generic", digest)
	add("java-1", "java", javaDigest)
	add("java-2", "java", javaDigest)
	var firstWorkload string
	result, err := fakeInventory(f).CheckJava4Shell(context.Background(), targets, func(_ context.Context, p *corev1.Pod) resourceid.Ref {
		workload := p.Labels["workload"]
		if firstWorkload == "" {
			firstWorkload = workload
		}
		return resourceid.NewRef("apps", "Deployment", p.Namespace, workload)
	}, nil)
	if err != nil || result.UnassessedGroups != java4ShellRowLimit+4 || len(result.UnassessedRows) != java4ShellRowLimit ||
		result.Unassessed[noJavaDeclarationReason] != 1 || result.Unassessed["image_identity_unknown"] != java4ShellRowLimit+2 ||
		result.Unassessed[javaHomeDeclaredReason] != 2 {
		t.Fatalf("wrong unassessed totals: groups=%d rows=%d reasons=%+v, err=%v", result.UnassessedGroups, len(result.UnassessedRows), result.Unassessed, err)
	}
	row := result.UnassessedRows[0]
	if row.Workload.Name != "java" || row.Container != "app" || row.Digest != "sha256:"+strings.Repeat("b", 64) ||
		row.Reason != javaHomeDeclaredReason || row.Pods != 2 || firstWorkload != "java" || f.calls.Load() != 2 {
		t.Fatalf("wrong priority, grouping or lookup count: %+v, first owner=%q, calls=%d", row, firstWorkload, f.calls.Load())
	}
}

func TestJava4ShellScopesAndRevalidatesPrivateRegistryAccess(t *testing.T) {
	digest := "registry.example/team/java@sha256:" + strings.Repeat("a", 64)
	ref := refKey(t, digest)
	allowed := runningPod("allowed", "app", "node", "app", map[string][2]string{"java": {"registry.example/team/java:v1", digest}})
	denied := runningPod("denied", "app", "node", "app", map[string][2]string{"java": {"registry.example/team/java:v1", digest}})
	for _, pod := range []*corev1.Pod{allowed, denied} {
		pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
	}
	node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}}}
	pods := []Java4ShellPod{{Pod: allowed, Node: node}, {Pod: denied, Node: node}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "allowed", Name: "pull", UID: "secret-uid", ResourceVersion: "1"}}
	f := &fakeLookup{
		configs: map[string]*v1.ConfigFile{ref: {Config: v1.Config{Env: []string{"JAVA_VERSION=jdk-21.0.13+9"}}}},
		errs:    map[string]error{ref + "|anonymous": unauthorized()},
	}
	inventory := fakeInventory(f)
	workloadFor := func(_ context.Context, p *corev1.Pod) resourceid.Ref {
		return resourceid.NewRef("", "Pod", p.Namespace, p.Name)
	}
	accessFor := func(p *corev1.Pod) Java4ShellRegistryAccess {
		if p == allowed {
			return Java4ShellRegistryAccess{PullSecrets: []*corev1.Secret{secret}}
		}
		return Java4ShellRegistryAccess{}
	}
	got, err := inventory.CheckJava4Shell(context.Background(), pods, workloadFor, accessFor)
	if err != nil || got.Matches != 1 || got.Unassessed[StatusAuthRequired] != 1 ||
		len(got.Findings) != 1 || got.Findings[0].Workload.Namespace != allowed.Namespace ||
		len(got.UnassessedRows) != 1 || got.UnassessedRows[0].Workload.Namespace != denied.Namespace ||
		got.UnassessedRows[0].Reason != StatusAuthRequired || got.UnassessedRows[0].Pods != 1 {
		t.Fatalf("private read crossed Pod scope or did not succeed: %+v, %v", got, err)
	}

	got, err = inventory.CheckJava4Shell(context.Background(), pods, workloadFor, nil)
	if err != nil || got.Matches != 0 || got.Unassessed[StatusAuthRequired] != 2 {
		t.Fatalf("private evidence reached a later unprivileged check: %+v, %v", got, err)
	}

	f.errs[ref+"|allowed/pull@rv1"] = unauthorized()
	got, err = inventory.CheckJava4Shell(context.Background(), pods, workloadFor, accessFor)
	if err != nil || got.Matches != 0 || got.Unassessed[StatusAuthRequired] != 2 {
		t.Fatalf("revoked registry access did not suppress private evidence: %+v, %v", got, err)
	}
}
