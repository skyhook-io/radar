package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func writeJava4ShellCheck(out io.Writer, output string, report images.Java4ShellReport, details bool) error {
	return (java4ShellCLIReport{CVE: images.Java4ShellCVE, Java4ShellReport: report}).write(out, output, details, "")
}

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
	result, err := checkJava4Shell(context.Background(), kube, images.NewCLIJava4ShellInventory(), "team", &progress)
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
		!strings.Contains(output.String(), "1 could not be checked") ||
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
	report, err := checkJava4Shell(context.Background(), kube, images.NewCLIJava4ShellInventory(), "", io.Discard)
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
	for _, want := range []string{"No Java versions could be assessed.", "1 could not be checked", "Verify the installed Java version for images that set JAVA_HOME."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("expected %q in %s", want, output.String())
		}
	}
}

func TestJava4ShellTablePrioritizesActionableGaps(t *testing.T) {
	const gapCount = java4ShellTableGapLimit + 1
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers:             gapCount + 2,
		UnaffectedByMajor:      map[int]int{11: 1},
		Unassessed:             map[string]int{images.StatusAuthRequired: gapCount, "no_java_declaration": 1},
		UnassessedGroups:       gapCount + 1,
		AuthRequiredRegistries: []string{"example.com"},
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
	want := []string{"No affected Java version declared in 1 assessed container.", "1 without a Java version in image metadata", "11 could not be checked", "Registry authentication required · 11 containers",
		"private-09", "Showing 10 of 11 rows", "docker login REGISTRY", "  example.com\n", java4ShellRegistryDocs}
	for _, phrase := range want {
		if !strings.Contains(output.String(), phrase) {
			t.Errorf("missing %q in %s", phrase, output.String())
		}
	}
	for _, hidden := range []string{"private-10", "busybox", "example.com/team/image", "AWS_PROFILE"} {
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

func TestJava4ShellTableShowsECRRegistryHosts(t *testing.T) {
	const first = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	const second = "987654321012.dkr.ecr.eu-west-1.amazonaws.com"
	t.Setenv(images.ECRRegistriesEnv, "")
	var output bytes.Buffer
	(java4ShellCLIReport{}).writeAuthHint(&output, []string{first, second, "ghcr.io"})
	for _, want := range []string{
		"export AWS_PROFILE=your-profile",
		"export RADAR_JAVA4SHELL_ECR_REGISTRIES=" + first + "," + second + "\n",
		"aws sso login --profile your-profile", "  ghcr.io\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %s", want, output.String())
		}
	}
	t.Setenv(images.ECRRegistriesEnv, first)
	output.Reset()
	(java4ShellCLIReport{}).writeAuthHint(&output, []string{first, second})
	for _, want := range []string{"For ECR hosts already configured, verify", `export RADAR_JAVA4SHELL_ECR_REGISTRIES="$RADAR_JAVA4SHELL_ECR_REGISTRIES,` + second + `"`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %s", want, output.String())
		}
	}
	output.Reset()
	(java4ShellCLIReport{}).writeAuthHint(&output, []string{first})
	if strings.Contains(output.String(), "export RADAR_JAVA4SHELL_ECR_REGISTRIES") {
		t.Errorf("suggested reconfiguring a host already set: %s", output.String())
	}
}

func TestJava4ShellTableDistinguishesRegistryAndScanLimits(t *testing.T) {
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers:            3,
		Unassessed:            map[string]int{images.StatusRateLimited: 2, "docker_hub_limit": 1},
		RateLimitedRegistries: []string{"public.ecr.aws"},
		UnassessedRows: []images.Java4ShellUnassessed{
			{Reason: images.StatusRateLimited, Workload: resourceid.NewRef("apps", "DaemonSet", "kube-system", "csi"), Pods: 2},
			{Reason: "docker_hub_limit", Workload: resourceid.NewRef("apps", "DaemonSet", "velero", "node-agent"), Pods: 1},
		},
	}}
	var output bytes.Buffer
	if err := writeJava4ShellCheck(&output, "table", report, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Could not check · next steps",
		"HTTP 429 from public.ecr.aws.", "one anonymous pull per second", "Wait a few seconds",
		"use -n kube-system instead of -A", "Radar's 24-lookup cap",
		"docker login, then rerun the scan with --docker-hub-auth", "use -n velero instead of -A",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %s", want, output.String())
		}
	}
	report.RateLimitedRegistries = []string{"registry.example.com"}
	output.Reset()
	if err := writeJava4ShellCheck(&output, "table", report, false); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "HTTP 429 from registry.example.com.") ||
		!strings.Contains(text, "The reset time is unknown") || strings.Contains(text, "Wait a few seconds") {
		t.Errorf("a registry with an unknown quota should not get a specific wait time: %s", text)
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
		want := []string{"Check node CPU · 1 container", "1 CPU unknown", "2 VBMI absent (inferred from m6a.large)"}
		for _, want := range want {
			if !strings.Contains(output.String(), want) {
				t.Errorf("details %t: missing %q in %s", details, want, output.String())
			}
		}
	}
}

func TestJava4ShellReportSeparatesPatchFromOtherActions(t *testing.T) {
	report := images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
		Containers: 6, Matches: 6,
		Findings: []images.Java4ShellFinding{
			{Workload: resourceid.NewRef("apps", "Deployment", "team", "later"), Pods: 2},
			{Workload: resourceid.NewRef("apps", "Deployment", "team", "investigate"), Pods: 1, UnknownCPUPods: 1},
			{Workload: resourceid.NewRef("apps", "Deployment", "team", "now"), Container: "jvm", Pods: 3, VBMIPods: 1, UnknownCPUPods: 1, Version: "21.0.8", FixedIn: "21.0.13.1", MitigationDeclaredPods: 1},
		},
	}}
	for _, details := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeJava4ShellCheck(&out, "table", report, details); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		last := -1
		for _, part := range []string{"PATCH FIRST · 1 container", "team/Deployment/now · container jvm",
			"Declared 21.0.8 → advisory fix 21.0.13.1", "1 AVX-512 VBMI reported · 1 CPU unknown · 1 non-x86", "Pods declaring a mitigation flag: 1 of 3",
			"Check node CPU · 1 container", "Deployment/investigate", "Lower priority on current nodes · 2 containers", "Coverage"} {
			i := strings.Index(got, part)
			if i <= last {
				t.Fatalf("missing or misplaced %q (details=%t): %s", part, details, got)
			}
			last = i
		}
		if strings.Count(got, "Deployment/now") != 1 || strings.Contains(got, "Deployment/later") != details || strings.Contains(got, "\x1b") {
			t.Fatalf("wrong rows or style for details=%t: %s", details, got)
		}
	}
}

func TestJava4ShellReportKeepsBrowserExportScope(t *testing.T) {
	report := images.Java4ShellReport{Coverage: resourceapi.Coverage{
		Status: resourceapi.CoveragePartial,
		Scope:  resourceapi.Scope{Type: resourceapi.ScopeAllNamespaces},
		Read:   &resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: &resourceapi.NameSet{Count: 72}},
	}}
	var out bytes.Buffer
	if err := writeJava4ShellCheck(&out, "table", report, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "72 namespaces") || !strings.Contains(out.String(), "Partial Pod coverage") || strings.Contains(out.String(), "all namespaces") {
		t.Fatal(out.String())
	}
	ref := resourceid.NewRef("apps", "Deployment", "team", "api")
	report.Workload = &ref
	out.Reset()
	if err := writeJava4ShellCheck(&out, "table", report, false); err != nil || !strings.Contains(out.String(), "workload team/Deployment/api") {
		t.Fatal(err, out.String())
	}
}

func TestJava4ShellSavedReportOpensOffline(t *testing.T) {
	snapshot := java4ShellCLIReport{
		CVE: images.Java4ShellCVE, CheckedAt: time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC),
		Java4ShellReport: images.Java4ShellReport{Java4ShellResult: images.Java4ShellResult{
			Containers: 102, Matches: 1,
			Findings: []images.Java4ShellFinding{{
				Workload:  resourceid.NewRef("apps", "Deployment", "boutique", "adservice"),
				Container: "server", Version: "jdk-25.0.4+7", Pods: 1,
				NoVBMIByInstanceType: map[string]int{"m6a.large": 1},
			}},
			Unassessed: map[string]int{images.StatusRateLimited: 97, "image_identity_unknown": 4},
		}},
	}
	for i := range 101 {
		reason := images.StatusRateLimited
		if i >= 97 {
			reason = "image_identity_unknown"
		}
		snapshot.UnassessedRows = append(snapshot.UnassessedRows, images.Java4ShellUnassessed{
			Workload:  resourceid.NewRef("", "Pod", "team", fmt.Sprintf("pod-%03d", i)),
			Container: "app", Image: "example.com/team/app:1", Reason: reason, Pods: 1,
		})
	}
	path := filepath.Join(t.TempDir(), "full report.json")
	file, err := openJava4ShellReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(snapshot.write(file, "json", false, ""), file.Close()); err != nil {
		t.Fatal(err)
	}
	var rejected bytes.Buffer
	if code := runCheckCommand([]string{"java4shell", "--save-report", path}, io.Discard, &rejected); code != 1 ||
		!strings.Contains(rejected.String(), "already exists") || strings.Contains(rejected.String(), "Loading Kubernetes") {
		t.Fatalf("expected an existing report to block a scan, got exit %d, stderr %s", code, rejected.String())
	}

	var out, progress bytes.Buffer
	code := runCheckCommand([]string{"java4shell", "--from-report", path}, &out, &progress)
	if code != 0 || progress.Len() != 0 {
		t.Fatalf("offline report: exit %d, stderr %s", code, progress.String())
	}
	brief := out.String()
	finding, coverage := strings.Index(brief, "Deployment/adservice"), strings.Index(brief, "Coverage")
	if finding < 0 || coverage < finding || !strings.Contains(brief, "pod-100") ||
		!strings.Contains(brief, "Showing 10 of 101 rows") || !strings.Contains(brief, "without a rescan") {
		t.Fatalf("brief report lost finding or clear path to the full result: %s", brief)
	}
	out.Reset()
	if code := runCheckCommand([]string{"java4shell", "--from-report", path, "--details"}, &out, &progress); code != 0 ||
		!strings.Contains(out.String(), "pod-096") || !strings.Contains(out.String(), "pod-100") ||
		!strings.Contains(out.String(), "2026-11-20T18:00:00Z") {
		t.Fatalf("detailed report: exit %d, stderr %s", code, progress.String())
	}
}

func TestJava4ShellCombinesDockerHubRecovery(t *testing.T) {
	scope := resourceapi.Scope{
		Type:       resourceapi.ScopeNamespaces,
		Namespaces: resourceapi.Names([]string{"payments"}, resourceapi.RESTSetLimit),
	}
	snapshot := java4ShellCLIReport{
		CVE: images.Java4ShellCVE, Context: "prod-eu", CheckedAt: time.Now(),
		Java4ShellReport: images.Java4ShellReport{
			Coverage: resourceapi.Coverage{Scope: scope},
			Java4ShellResult: images.Java4ShellResult{
				Containers: 5, Unassessed: map[string]int{images.StatusRateLimited: 3, "docker_hub_limit": 2},
				RateLimitedRegistries: []string{"index.docker.io"},
			},
		},
	}
	snapshot.dockerHubRetry = java4ShellDockerHubRetry(snapshot, "/tmp/test kubeconfig", "check.json", false, true)
	var out bytes.Buffer
	if err := snapshot.write(&out, "table", false, "check.json"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Docker Hub · 5 containers", "3 hit HTTP 429", "2 skipped by Radar", "Context prod-eu",
		"--context 'prod-eu' --kubeconfig '/tmp/test kubeconfig' -n 'payments'",
		"--fail-on-match --docker-hub-auth --save-report 'check.json.retry.json'",
		radarCLIInvocation() + " check java4shell --from-report 'check.json' --details",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if strings.Count(out.String(), "docker login\n") != 1 || strings.Contains(out.String(), " -A ") {
		t.Errorf("unexpected recovery command in %s", out.String())
	}
}
