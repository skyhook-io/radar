package upgradereadiness

import (
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func completeInput() *Input {
	return &Input{
		Pods:                              []*corev1.Pod{},
		Deployments:                       []*appsv1.Deployment{},
		ReplicaSets:                       []*appsv1.ReplicaSet{},
		StatefulSets:                      []*appsv1.StatefulSet{},
		DaemonSets:                        []*appsv1.DaemonSet{},
		Jobs:                              []*batchv1.Job{},
		CronJobs:                          []*batchv1.CronJob{},
		Services:                          []*corev1.Service{},
		PersistentVolumes:                 []*corev1.PersistentVolume{},
		Nodes:                             []*corev1.Node{readyNode("node-a", "v1.35.7")},
		ManifestResources:                 []ManifestResource{{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "default", Name: "api", Source: "Helm"}},
		DeprecatedAPIRequests:             []DeprecatedAPIRequest{},
		PrometheusRules:                   []*unstructured.Unstructured{},
		PrometheusRulesInstalled:          false,
		PrometheusRulesDiscoveryAvailable: true,
	}
}

func readyNode(name, version string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: version},
		},
	}
}

func checkByID(t *testing.T, result *ScanResults, id string) Check {
	t.Helper()
	for _, check := range result.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %q not found", id)
	return Check{}
}

func gitRepoSpec() corev1.PodSpec {
	return corev1.PodSpec{Volumes: []corev1.Volume{{
		Name: "source",
		VolumeSource: corev1.VolumeSource{GitRepo: &corev1.GitRepoVolumeSource{
			Repository: "https://example.com/repo.git",
		}},
	}}}
}

func TestScanBaselineHasNamedChecksAndHonestMetricsCoverage(t *testing.T) {
	got, err := Scan(completeInput(), "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictUnknown || got.Summary.Blocked != 0 || got.Summary.Passed != 7 || got.Summary.Unknown != 1 || got.Summary.NotApplicable != 2 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Checks) != 10 {
		t.Fatalf("checks = %d, want 10", len(got.Checks))
	}
	metrics := checkByID(t, got, "deprecated-api-requests")
	if metrics.Status != CheckUnknown || metrics.Caveat == "" {
		t.Fatalf("single-replica metrics must not produce a pass: %+v", metrics)
	}
}

func TestScanGitRepoAcrossWorkloadKinds(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: gitRepoSpec()}}
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "ns"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "ns"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.StatefulSets = []*appsv1.StatefulSet{{ObjectMeta: metav1.ObjectMeta{Name: "stateful", Namespace: "ns"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.DaemonSets = []*appsv1.DaemonSet{{ObjectMeta: metav1.ObjectMeta{Name: "daemon", Namespace: "ns"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.Jobs = []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.CronJobs = []*batchv1.CronJob{{ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "ns"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}}}

	got, err := Scan(input, "v1.35.7", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "gitrepo-volume-removed")
	if got.Verdict != VerdictBlocked || check.Status != CheckBlocked || len(check.Findings) != 7 || check.Inspected != 7 {
		t.Fatalf("unexpected gitRepo result: %+v", check)
	}
	wantPaths := map[string]string{
		"Pod": "spec.volumes[0].gitRepo", "Deployment": "spec.template.spec.volumes[0].gitRepo",
		"ReplicaSet": "spec.template.spec.volumes[0].gitRepo", "StatefulSet": "spec.template.spec.volumes[0].gitRepo",
		"DaemonSet": "spec.template.spec.volumes[0].gitRepo", "Job": "spec.template.spec.volumes[0].gitRepo",
		"CronJob": "spec.jobTemplate.spec.template.spec.volumes[0].gitRepo",
	}
	for _, finding := range check.Findings {
		if finding.Resource == nil || finding.Evidence.Path != wantPaths[finding.Resource.Kind] {
			t.Fatalf("unexpected finding: %+v", finding)
		}
	}
}

func TestScanDeprecatedAPIRequests(t *testing.T) {
	input := completeInput()
	input.DeprecatedAPIRequests = []DeprecatedAPIRequest{{Group: "extensions", Version: "v1beta1", Resource: "ingresses", RemovedRelease: "1.22", Requests: 4}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "deprecated-api-requests")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource != nil {
		t.Fatalf("unexpected API request result: %+v", check)
	}

	input.DeprecatedAPIRequests = nil
	got, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, got, "deprecated-api-requests").Status != CheckUnknown {
		t.Fatalf("unavailable metrics should be unknown")
	}
}

func TestScanScopedNamespaceDoesNotClaimClusterWideCoverage(t *testing.T) {
	input := completeInput()
	input.Namespaces = []string{"default"}
	input.PrometheusRulesInstalled = true
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.State != "partial" || len(got.Coverage.ScopedNamespaces) != 1 || got.Coverage.ScopedNamespaces[0] != "default" {
		t.Fatalf("scope was not preserved: %+v", got.Coverage)
	}
	proxy := checkByID(t, got, "kube-proxy-version-skew")
	if proxy.Status != CheckUnknown {
		t.Fatalf("kube-proxy outside namespace scope must be unknown: %+v", proxy)
	}
	for _, id := range []string{"manifest-api-compatibility", "gitrepo-volume-removed", "flexvolume-kubeadm-support", "service-externalips-deprecated", "renamed-control-plane-metrics"} {
		check := checkByID(t, got, id)
		if check.Caveat == "" || !strings.Contains(check.Summary, "selected namespace scope") {
			t.Fatalf("%s must qualify namespace-scoped evidence: %+v", id, check)
		}
	}
}

func TestScanMultiMinorControlPlaneJumpIsBlocked(t *testing.T) {
	got, err := Scan(completeInput(), "1.32", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "control-plane-upgrade-path")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Evidence.Detail != "1.32 → 1.33 → 1.34 → 1.35 → 1.36" {
		t.Fatalf("unexpected upgrade path result: %+v", check)
	}
}

func TestScanManifestCompatibilityFromHelmAndLastApplied(t *testing.T) {
	input := completeInput()
	input.ManifestResources = []ManifestResource{{APIVersion: "extensions/v1beta1", Kind: "Ingress", Namespace: "web", Name: "api", Source: "Helm", SourceNamespace: "web", SourceName: "api"}}
	lastApplied := `{"apiVersion":"batch/v1beta1","kind":"CronJob","metadata":{"name":"cleanup","namespace":"ops"}}`
	input.SourceObjects = []metav1.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "holder", Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": lastApplied}}}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "manifest-api-compatibility")
	if check.Status != CheckBlocked || len(check.Findings) != 2 || check.Inspected != 2 {
		t.Fatalf("unexpected manifest result: %+v", check)
	}
	if check.Findings[0].ManagedBy == nil && check.Findings[1].ManagedBy == nil {
		t.Fatalf("Helm ownership was not preserved: %+v", check.Findings)
	}
}

func TestScanManifestFindingRetainsPartialHelmCoverage(t *testing.T) {
	input := completeInput()
	input.ManifestResources = []ManifestResource{{APIVersion: "extensions/v1beta1", Kind: "Ingress", Namespace: "web", Name: "api", Source: "Helm"}}
	input.HelmUnavailableNamespaces = []string{"payments"}
	got, _ := Scan(input, "1.35", "1.36")
	check := checkByID(t, got, "manifest-api-compatibility")
	if check.Status != CheckBlocked || check.Caveat == "" || got.Coverage.State != "partial" {
		t.Fatalf("partial Helm coverage was hidden by finding: %+v", check)
	}
}

func TestScanHealthAndComponentSkew(t *testing.T) {
	input := completeInput()
	input.Nodes = []*corev1.Node{readyNode("old", "v1.32.9"), readyNode("new", "v1.35.2")}
	input.Nodes[1].Status.Conditions[0].Status = corev1.ConditionFalse
	input.DaemonSets = []*appsv1.DaemonSet{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system"},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-proxy", Image: "registry.k8s.io/kube-proxy:v1.32.9"}}}}},
	}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if checkByID(t, got, "node-health").Status != CheckWarning || checkByID(t, got, "kubelet-version-skew").Status != CheckBlocked || checkByID(t, got, "kube-proxy-version-skew").Status != CheckBlocked {
		t.Fatalf("unexpected health/skew checks: %+v", got.Checks)
	}
}

func TestScanSkewFindingRetainsUnparsedVersionCaveat(t *testing.T) {
	input := completeInput()
	input.Nodes = []*corev1.Node{readyNode("old", "v1.31.9"), readyNode("unknown", "vendor-build")}
	got, _ := Scan(input, "1.35", "1.36")
	check := checkByID(t, got, "kubelet-version-skew")
	if check.Status != CheckBlocked || check.Caveat == "" {
		t.Fatalf("unparsed kubelet caveat was hidden by blocker: %+v", check)
	}
}

func TestScanKubernetes136ConfigurationChanges(t *testing.T) {
	input := completeInput()
	input.Services = []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"203.0.113.10"}}}}
	input.PersistentVolumes = []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "flex"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: "example.com/driver"}}}}}
	input.PrometheusRulesInstalled = true
	input.PrometheusRules = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1", "kind": "PrometheusRule",
		"metadata": map[string]any{"name": "kubernetes", "namespace": "monitoring"},
		"spec":     map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"alert": "VolumeErrors", "expr": "rate(volume_operation_total_errors[5m]) > 0"}}}}},
	}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"flexvolume-kubeadm-support", "service-externalips-deprecated", "renamed-control-plane-metrics"} {
		check := checkByID(t, got, id)
		if check.Status != CheckWarning || len(check.Findings) != 1 {
			t.Fatalf("%s = %+v", id, check)
		}
	}
}

func TestScanPrometheusFindingRetainsPartialNamespaceCoverage(t *testing.T) {
	input := completeInput()
	input.PrometheusRulesInstalled = true
	input.PrometheusRuleUnavailableNamespaces = []string{"payments"}
	input.PrometheusRules = []*unstructured.Unstructured{{Object: map[string]any{
		"metadata": map[string]any{"name": "kubernetes", "namespace": "monitoring"},
		"spec":     map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"expr": "volume_operation_total_errors"}}}}},
	}}}
	got, _ := Scan(input, "1.35", "1.36")
	check := checkByID(t, got, "renamed-control-plane-metrics")
	if check.Status != CheckWarning || check.Caveat == "" || got.Coverage.State != "partial" {
		t.Fatalf("partial PrometheusRule coverage was hidden by finding: %+v", check)
	}
}

func TestScanKubeadmFlexVolumeChangeIsNotApplicableToManagedClusters(t *testing.T) {
	input := completeInput()
	input.Platform = "eks"
	input.Nodes[0].Labels = map[string]string{"eks.amazonaws.com/nodegroup": "workers"}
	input.PersistentVolumes = []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "flex"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: "example.com/driver"}}}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "flexvolume-kubeadm-support")
	if check.Status != CheckNotApplicable || len(check.Findings) != 0 {
		t.Fatalf("managed-cluster FlexVolume check = %+v", check)
	}
}

func TestScanCloudProviderIDDoesNotImplyManagedControlPlane(t *testing.T) {
	input := completeInput()
	input.Platform = "eks"
	input.Nodes[0].Spec.ProviderID = "aws://us-east-1a/i-0123456789"
	input.PersistentVolumes = []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "flex"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: "example.com/driver"}}}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "flexvolume-kubeadm-support")
	if check.Status != CheckWarning || len(check.Findings) != 1 {
		t.Fatalf("cloud provider ID must not suppress kubeadm review: %+v", check)
	}
}

func TestScanCoverageAndVerdictPrecedence(t *testing.T) {
	input := completeInput()
	input.CronJobs = nil
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictUnknown || got.Coverage.State != "partial" || got.Summary.Unknown == 0 {
		t.Fatalf("unexpected partial result: %+v", got)
	}

	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: gitRepoSpec()}}
	got, _ = Scan(input, "1.35", "1.36")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("definite blocker must win over unknown coverage: %+v", got)
	}

	got, _ = Scan(completeInput(), "1.36", "1.37")
	if got.Verdict != VerdictUnknown {
		t.Fatalf("target beyond reviewed catalog must be unknown: %+v", got)
	}
}

func TestScanDeduplicatesControllerOwnedPods(t *testing.T) {
	controller := true
	input := completeInput()
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "ns", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-abc", Controller: &controller}}}, Spec: gitRepoSpec()}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "ns", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", Annotations: map[string]string{"meta.helm.sh/release-name": "api", "meta.helm.sh/release-namespace": "ns"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "gitrepo-volume-removed")
	if len(check.Findings) != 1 || check.Inspected != 1 || check.Findings[0].ManagedBy == nil || check.Findings[0].ManagedBy.Kind != "HelmRelease" {
		t.Fatalf("controller chain was not deduplicated: %+v", check)
	}
}

func TestScanValidatesVersions(t *testing.T) {
	for _, tc := range []struct {
		current string
		target  string
		want    error
	}{{"nope", "1.36", ErrInvalidCurrentVersion}, {"1.35", "1.36.1", ErrInvalidTargetVersion}, {"1.35", "1.35", ErrNonForwardTarget}} {
		_, err := Scan(completeInput(), tc.current, tc.target)
		if !errors.Is(err, tc.want) {
			t.Fatalf("Scan(%q, %q) error = %v, want %v", tc.current, tc.target, err, tc.want)
		}
	}
}
