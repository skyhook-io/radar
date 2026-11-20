package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if err := writeJava4ShellCheck(&output, "json", result); err != nil {
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
	if err := writeJava4ShellCheck(&output, "table", result); err != nil ||
		!strings.Contains(output.String(), "Java version could not be assessed") ||
		!strings.Contains(output.String(), "image identity unknown") {
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
}

func TestJava4ShellCLIPrintsJavaHomeAsUnassessed(t *testing.T) {
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers: 1,
		Unassessed: map[string]int{"java_home_declared": 1},
	}}
	var output bytes.Buffer
	if err := writeJava4ShellCheck(&output, "table", report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Java version could not be assessed", "Image sets JAVA_HOME. Java version unknown"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("expected %q in %s", want, output.String())
		}
	}
}
