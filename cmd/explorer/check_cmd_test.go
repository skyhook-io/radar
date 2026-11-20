package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestJava4ShellCommandFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCheckCommand([]string{"java4shell", "--help"}, &out, &errOut); code != 0 || errOut.Len() != 0 ||
		!strings.Contains(out.String(), "--fail-on-match") {
		t.Fatalf("help exit %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"java4shell", "-A", "-n", "team"}, {"java4shell", "-o", "yaml"}, {"unknown"}} {
		out.Reset()
		errOut.Reset()
		if code := runCheckCommand(args, &out, &errOut); code != 1 || out.Len() != 0 || errOut.Len() == 0 {
			t.Fatalf("%v exit %d, stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestJava4ShellCLIListsScopedPodsAndResolvesWorkloads(t *testing.T) {
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "replicaset", Controller: new(true)}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-pod", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Name: "api", Image: "registry.example/api:21"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "api", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	sibling := pod.DeepCopy()
	sibling.Namespace = "other"
	finished := pod.DeepCopy()
	finished.Name = "finished"
	finished.Status.Phase = corev1.PodSucceeded
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: pod.Namespace, Name: owner.Name, UID: owner.UID,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Controller: new(true)}},
	}}
	kube := fake.NewClientset(pod, sibling, finished, rs)
	var progress bytes.Buffer
	result, err := checkJava4Shell(context.Background(), kube, "team", &progress)
	if err != nil || result.Containers != 1 || result.Matches != 0 || len(result.UnassessedRows) != 1 {
		t.Fatalf("unexpected scoped check: %+v, %v", result, err)
	}
	wantProgress := "Listing Pods in namespace \"team\"…\nReading Node information…\nChecking registry image metadata…\n"
	if progress.String() != wantProgress {
		t.Errorf("progress = %q, want %q", progress.String(), wantProgress)
	}
	row := result.UnassessedRows[0]
	if row.Workload.Kind != "Deployment" || row.Workload.Name != "api" || row.Reason != "image_identity_unknown" {
		t.Fatalf("unexpected unassessed row: %+v", row)
	}
	if err := result.Coverage.Validate(); err != nil || result.Coverage.Scope.Namespaces.Names[0] != "team" {
		t.Fatalf("invalid coverage: %+v, %v", result.Coverage, err)
	}

	var output bytes.Buffer
	if err := writeJava4ShellCheck(&output, "json", result, false); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		CVE string
		images.Java4ShellReport
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || decoded.CVE != images.Java4ShellCVE ||
		decoded.Containers != result.Containers || decoded.Unassessed[row.Reason] != 1 {
		t.Fatalf("invalid report JSON: %s (%v)", output.String(), err)
	}
	output.Reset()
	if err := writeJava4ShellCheck(&output, "table", result, false); err != nil ||
		!strings.Contains(output.String(), "Assessment gaps: 1") ||
		!strings.Contains(output.String(), "Running image digest unavailable") {
		t.Fatalf("invalid report table: %s (%v)", output.String(), err)
	}
}

func TestJava4ShellCLIRefusesUnreadablePodScope(t *testing.T) {
	kube := fake.NewClientset()
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	kube.PrependReactor("list", "pods", func(kubetesting.Action) (bool, runtime.Object, error) {
		return true, nil, denied
	})
	report, err := checkJava4Shell(context.Background(), kube, "", io.Discard)
	if !apierrors.IsForbidden(err) || report.Coverage.Status == resourceapi.CoverageComplete || len(kube.Actions()) != 1 {
		t.Fatalf("denied scope reported as readable: %+v, %v, %v", report, err, kube.Actions())
	}
	if !strings.Contains(err.Error(), "Use -n NAMESPACE") {
		t.Errorf("missing narrower-scope instruction: %v", err)
	}
}

func TestJava4ShellCLIPrintsJavaHomeAsUnassessed(t *testing.T) {
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers: 1,
		Unassessed: map[string]int{"java_home_declared": 1},
	}}
	var output bytes.Buffer
	if err := writeJava4ShellCheck(&output, "table", report, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"No Java versions could be assessed.", "Assessment gaps: 1", "Verify the installed Java version for images that set JAVA_HOME."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("expected %q in %s", want, output.String())
		}
	}
}

func TestJava4ShellTablePrioritizesActionableGaps(t *testing.T) {
	const gapCount = java4ShellTableGapLimit + 1
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers:        gapCount + 2,
		UnaffectedByMajor: map[int]int{11: 1},
		Unassessed:        map[string]int{images.StatusAuthRequired: gapCount, "no_java_declaration": 1},
		UnassessedGroups:  gapCount + 1,
	}}
	for i := range gapCount {
		report.UnassessedRows = append(report.UnassessedRows, images.Java4ShellUnassessed{
			Workload:  resourceid.NewRef("apps", "Deployment", "prod", fmt.Sprintf("private-%02d", i)),
			Image:     "example.com/team/image",
			Container: "server",
			Reason:    images.StatusAuthRequired,
			Pods:      1,
		})
	}
	report.UnassessedRows = append(report.UnassessedRows, images.Java4ShellUnassessed{
		Workload: resourceid.NewRef("apps", "Deployment", "prod", "busybox"),
		Image:    "busybox",
		Reason:   "no_java_declaration",
		Pods:     1,
	})
	var output bytes.Buffer
	if err := writeJava4ShellCheck(&output, "table", report, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"No affected Java declarations among assessed containers (1).", "No Java version declared: 1", "Assessment gaps: 11", "Registry authentication required: 11",
		"private-09", "Showing the first 10 workload groups with gaps", java4ShellRegistryDocs}
	for _, phrase := range want {
		if !strings.Contains(output.String(), phrase) {
			t.Errorf("missing %q in %s", phrase, output.String())
		}
	}
	for _, hidden := range []string{"private-10", "busybox", "example.com/team/image"} {
		if strings.Contains(output.String(), hidden) {
			t.Errorf("unexpected %q in %s", hidden, output.String())
		}
	}
	for _, format := range []string{"table", "json"} {
		output.Reset()
		if err := writeJava4ShellCheck(&output, format, report, format == "table"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "private-10") || !strings.Contains(output.String(), "busybox") {
			t.Errorf("%s lost detailed rows: %s", format, output.String())
		}
	}
}

func TestCheckImageProgressTerminalAndRedirect(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		var output bytes.Buffer
		update, finish := checkImageProgress(&output, interactive)
		update(0, 0)
		update(0, 121)
		update(1, 121)
		update(121, 121)
		finish()
		want := "Processed 0 of 121 image groups\nProcessed 121 of 121 image groups\n"
		if interactive {
			want = "\rProcessed 0 of 121 image groups\rProcessed 121 of 121 image groups\n"
		}
		if output.String() != want {
			t.Errorf("interactive %t: got %q, want %q", interactive, output.String(), want)
		}
	}
}

func TestJava4ShellCLIShowsCPUInferenceAlongsideMatch(t *testing.T) {
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers: 3,
		Matches:    3,
		Findings: []images.Java4ShellFinding{{
			Workload:             resourceid.NewRef("apps", "Deployment", "team", "api"),
			Version:              "jdk-21.0.8+9",
			Pods:                 3,
			UnknownCPUPods:       1,
			NoVBMIByInstanceType: map[string]int{"m6a.large": 2},
		}},
	}}
	for _, details := range []bool{false, true} {
		var output bytes.Buffer
		if err := writeJava4ShellCheck(&output, "table", report, details); err != nil {
			t.Fatal(err)
		}
		want := []string{"Affected Java declarations in 3 of 3 assessed containers"}
		if details {
			want = append(want, "VBMI ABSENT (INFERRED)", "2 (m6a.large)")
		} else {
			want = append(want, "2 VBMI absent (inferred from m6a.large)")
		}
		for _, want := range want {
			if !strings.Contains(output.String(), want) {
				t.Errorf("details %t: missing %q in %s", details, want, output.String())
			}
		}
	}
}
