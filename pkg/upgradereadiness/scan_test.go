package upgradereadiness

import (
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	bp "github.com/skyhook-io/radar/pkg/audit"
)

func completeInput() *Input {
	return &Input{
		Pods:                           []*corev1.Pod{},
		Deployments:                    []*appsv1.Deployment{},
		ReplicaSets:                    []*appsv1.ReplicaSet{},
		StatefulSets:                   []*appsv1.StatefulSet{},
		DaemonSets:                     []*appsv1.DaemonSet{},
		Jobs:                           []*batchv1.Job{},
		CronJobs:                       []*batchv1.CronJob{},
		Services:                       []*corev1.Service{},
		WebhookServices:                []*corev1.Service{},
		PersistentVolumes:              []*corev1.PersistentVolume{},
		Nodes:                          []*corev1.Node{readyNode("node-a", "v1.35.7")},
		Events:                         []*corev1.Event{},
		PodDisruptionBudgets:           []*policyv1.PodDisruptionBudget{},
		EndpointSlices:                 []*discoveryv1.EndpointSlice{},
		AdmissionWebhookConfigurations: []*unstructured.Unstructured{},
		CustomResourceDefinitions:      []*unstructured.Unstructured{},
		APIServices:                    []*unstructured.Unstructured{},
		NodeRuntimeEvidence:            []NodeRuntimeEvidence{{NodeName: "node-a", MetricsAvailable: true, CgroupVersion: 2, CgroupVersionAvailable: true}},
		ManifestResources: []ManifestResource{{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "default", Name: "api", Source: "Helm",
			Object: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "api", "namespace": "default"},
			}},
		}},
		DeprecatedAPIRequests:             []DeprecatedAPIRequest{},
		PrometheusRules:                   []*unstructured.Unstructured{},
		PrometheusRulesInstalled:          false,
		PrometheusRulesDiscoveryAvailable: true,
		Platform:                          "generic",
	}
}

func TestReviewedThroughMatchesDeprecationCatalog(t *testing.T) {
	if bp.DeprecationCatalogReviewedThrough != ReviewedThrough {
		t.Fatalf("deprecation catalog reviewed through %s, upgrade catalog through %s", bp.DeprecationCatalogReviewedThrough, ReviewedThrough)
	}
}

func readyNode(name, version string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: version, ContainerRuntimeVersion: "containerd://2.1.0", OperatingSystem: "linux"},
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

func TestScanBaselineCanPassWithSampledMetricsEvidence(t *testing.T) {
	got, err := Scan(completeInput(), "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictNoKnownBlockers || got.Summary.Blocked != 0 || got.Summary.Passed != 12 || got.Summary.Unknown != 0 || got.Summary.NotApplicable != 4 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Checks) != 16 {
		t.Fatalf("checks = %d, want 16", len(got.Checks))
	}
	metrics := checkByID(t, got, "deprecated-api-requests")
	if metrics.Status != CheckPassed || metrics.EvidenceNote == "" || metrics.Caveat != "" {
		t.Fatalf("sampled metrics should pass with an evidence note: %+v", metrics)
	}
}

func TestUnknownCheckForcesPartialCoverage(t *testing.T) {
	input := completeInput()
	input.DeprecatedAPIRequests = nil
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.State != "partial" || got.Verdict != VerdictUnknown {
		t.Fatalf("coverage=%+v verdict=%q, want partial unknown", got.Coverage, got.Verdict)
	}
}

func TestCacheScopedKindsRemainExplicitPerKind(t *testing.T) {
	input := completeInput()
	input.CacheScopedKinds = map[string][]string{
		"pods":     {"team-b", "team-a"},
		"services": {"team-a"},
	}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.State != "partial" || !slices.Equal(got.Coverage.ScopedKinds["pods"], []string{"team-a", "team-b"}) {
		t.Fatalf("coverage = %+v, want sorted per-kind namespace ceilings", got.Coverage)
	}
	for _, id := range []string{"gitrepo-volume-removed", "service-externalips-deprecated", "node-drain-feasibility"} {
		check := checkByID(t, got, id)
		if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "Cached evidence is namespace-limited") {
			t.Fatalf("%s = %+v, want incomplete per-kind cache evidence", id, check)
		}
	}
	input.CacheScopedKinds["pods"][0] = "mutated"
	if got.Coverage.ScopedKinds["pods"][1] != "team-b" {
		t.Fatal("coverage retained the mutable input slice")
	}
}

func TestKubeProxyAbsenceRequiresKubeSystemInformerCoverage(t *testing.T) {
	input := completeInput()
	input.CacheScopedKinds = map[string][]string{"daemonsets": {"default"}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "kube-proxy-version-skew")
	if check.Status != CheckUnknown || !strings.Contains(check.Summary, "kube-system is outside") {
		t.Fatalf("kube-proxy check = %+v, want incomplete outside kube-system", check)
	}

	input.CacheScopedKinds["daemonsets"] = []string{"kube-system"}
	got, err = Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if check = checkByID(t, got, "kube-proxy-version-skew"); check.Status != CheckNotApplicable {
		t.Fatalf("kube-proxy check = %+v, kube-system coverage can support absence", check)
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
	if check.Summary != "7 workloads use the disabled gitRepo volume driver." {
		t.Fatalf("gitRepo summary = %q", check.Summary)
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
	input.DeprecatedAPIMetricsWindow = "6h"
	input.DeprecatedAPIRequests = []DeprecatedAPIRequest{{Group: "extensions", Version: "v1beta1", Resource: "ingresses", RemovedRelease: "1.22", Requests: 4}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "deprecated-api-requests")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource != nil {
		t.Fatalf("unexpected API request result: %+v", check)
	}
	if !strings.Contains(check.Summary, "6h process lifetime") || !strings.Contains(check.EvidenceNote, "not cluster-wide history") {
		t.Fatalf("API metrics scope is not explicit: %+v", check)
	}

	input.DeprecatedAPIRequests = nil
	got, _ = Scan(input, "1.35", "1.36")
	check = checkByID(t, got, "deprecated-api-requests")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "/metrics") || len(check.References) < 2 {
		t.Fatalf("unavailable metrics should be actionable and unknown: %+v", check)
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
	for _, id := range []string{"manifest-api-compatibility", "gitrepo-volume-removed", "flexvolume-kubeadm-support", "service-externalips-deprecated", "renamed-control-plane-metrics", "strict-ip-cidr-validation", "node-drain-feasibility"} {
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

func TestScanMajorControlPlaneJumpIsBlocked(t *testing.T) {
	got, err := Scan(completeInput(), "1.35", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "control-plane-upgrade-path")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Evidence.Detail != "1.35 → 2.0" || !strings.Contains(check.Findings[0].Remediation, "1.36") {
		t.Fatalf("unexpected major-version path result: %+v", check)
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
	if check.Summary != "2 source manifests use an API affected by the target version." {
		t.Fatalf("manifest summary = %q", check.Summary)
	}
	if check.Findings[0].ManagedBy == nil && check.Findings[1].ManagedBy == nil {
		t.Fatalf("Helm ownership was not preserved: %+v", check.Findings)
	}
}

func TestMalformedLastAppliedAnnotationKeepsManifestCoveragePartial(t *testing.T) {
	input := completeInput()
	input.SourceObjects = []metav1.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "broken", Namespace: "default",
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{"},
	}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"manifest-api-compatibility", "strict-ip-cidr-validation"} {
		check := checkByID(t, got, id)
		if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "1 kubectl last-applied annotation could not be parsed") {
			t.Fatalf("%s = %+v, want incomplete malformed-annotation coverage", id, check)
		}
	}
}

func TestUpgradeSourceObjectCandidatesFollowChecksAndDeprecationCatalog(t *testing.T) {
	for _, candidate := range []struct{ kind, group string }{
		{kind: "Node"},
		{kind: "Ingress", group: "networking.k8s.io"},
		{kind: "Role", group: "rbac.authorization.k8s.io"},
	} {
		if !IsUpgradeSourceObjectCandidate(candidate.kind, candidate.group) {
			t.Fatalf("%s.%s should be discovered as source evidence", candidate.kind, candidate.group)
		}
	}
	if IsUpgradeSourceObjectCandidate("Widget", "example.io") {
		t.Fatal("unrelated CRDs must not be listed for source evidence")
	}
	if IsUpgradeSourceObjectCandidate("Ingress", "example.io") || IsUpgradeSourceObjectCandidate("Event", "") || IsUpgradeSourceObjectCandidate("Service", "anything.io") {
		t.Fatal("same-kind resources from unrelated API groups must not become source evidence")
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

func TestSourceChecksExposeUnavailableKindsWithoutReadableManifest(t *testing.T) {
	input := completeInput()
	input.ManifestResources = []ManifestResource{}
	input.SourceObjects = []metav1.Object{}
	input.HelmUnavailableNamespaces = []string{"payments"}
	input.SourceObjectUnavailableKinds = []string{"ingresses"}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"manifest-api-compatibility", "strict-ip-cidr-validation"} {
		check := checkByID(t, got, id)
		if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "payments") || !strings.Contains(check.Caveat, "ingresses") {
			t.Fatalf("%s hid partial source coverage without readable manifests: %+v", id, check)
		}
	}
	if got.Coverage.State != "partial" {
		t.Fatalf("coverage state = %q, want partial", got.Coverage.State)
	}

	input.ManifestResources = nil
	input.SourceObjects = nil
	input.HelmUnavailableNamespaces = nil
	got, err = Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"manifest-api-compatibility", "strict-ip-cidr-validation"} {
		check := checkByID(t, got, id)
		if !strings.Contains(check.Caveat, "Helm release manifests and kubectl last-applied configuration were unavailable") || strings.Contains(check.Caveat, "only kubectl") {
			t.Fatalf("%s misrepresented unreadable source collectors: %+v", id, check)
		}
	}
}

func TestScanHealthAndComponentSkew(t *testing.T) {
	input := completeInput()
	input.Nodes = []*corev1.Node{readyNode("old", "v1.32.9"), readyNode("new", "v1.35.2")}
	input.Nodes[1].Status.Conditions[0].Status = corev1.ConditionFalse
	input.DaemonSets = []*appsv1.DaemonSet{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system"},
			Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-proxy", Image: "registry.k8s.io/kube-proxy:v1.32.9"}}}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy-canary", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-proxy"}},
			Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-proxy", Image: "registry.k8s.io/kube-proxy:v1.32.9"}}}}},
		},
	}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if checkByID(t, got, "node-health").Status != CheckWarning || checkByID(t, got, "kubelet-version-skew").Status != CheckBlocked || checkByID(t, got, "kube-proxy-version-skew").Status != CheckBlocked {
		t.Fatalf("unexpected health/skew checks: %+v", got.Checks)
	}
	if got := checkByID(t, got, "kube-proxy-version-skew").Summary; got != "2 kube-proxy installations are outside the supported skew." {
		t.Fatalf("kube-proxy summary = %q", got)
	}
}

func TestKubeletNewerThanTargetHasCorrectRemediation(t *testing.T) {
	input := completeInput()
	input.Nodes = []*corev1.Node{readyNode("newer", "v1.37.2")}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "kubelet-version-skew")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Remediation, "newer than the Kubernetes 1.36 target") {
		t.Fatalf("too-new kubelet remediation = %+v", check)
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
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "legacy", VolumeSource: corev1.VolumeSource{FlexVolume: &corev1.FlexVolumeSource{Driver: "example.com/driver"}}}}}}}}}
	input.Services = []*corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"203.0.113.10"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "legacy-secondary", Namespace: "default"}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"203.0.113.11"}}},
	}
	input.PersistentVolumes = []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "flex"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: "example.com/driver"}}}}}
	input.PrometheusRulesInstalled = true
	input.PrometheusRules = []*unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "monitoring.coreos.com/v1", "kind": "PrometheusRule",
			"metadata": map[string]any{"name": "kubernetes", "namespace": "monitoring"},
			"spec":     map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"alert": "VolumeErrors", "expr": "rate(volume_operation_total_errors[5m]) > 0"}}}}},
		}},
		{Object: map[string]any{
			"apiVersion": "monitoring.coreos.com/v1", "kind": "PrometheusRule",
			"metadata": map[string]any{"name": "kubernetes-secondary", "namespace": "monitoring"},
			"spec":     map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"alert": "VolumeErrorsSecondary", "expr": "rate(volume_operation_total_errors[5m]) > 0"}}}}},
		}},
	}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"flexvolume-kubeadm-support", "service-externalips-deprecated", "renamed-control-plane-metrics"} {
		check := checkByID(t, got, id)
		wantFindings := 1
		if id == "flexvolume-kubeadm-support" || id == "renamed-control-plane-metrics" || id == "service-externalips-deprecated" {
			wantFindings = 2
		}
		wantStatus := CheckWarning
		if id == "service-externalips-deprecated" {
			wantStatus = CheckReview
		}
		if check.Status != wantStatus || len(check.Findings) != wantFindings {
			t.Fatalf("%s = %+v", id, check)
		}
	}
	flex := checkByID(t, got, "flexvolume-kubeadm-support")
	if flex.Findings[0].Title == flex.Findings[1].Title {
		t.Fatalf("FlexVolume issue types must distinguish workload and PersistentVolume exposure: %+v", flex.Findings)
	}
	if got := checkByID(t, got, "service-externalips-deprecated").Summary; got != "2 Services use deprecated spec.externalIPs." {
		t.Fatalf("Service summary = %q", got)
	}
	if got := checkByID(t, got, "renamed-control-plane-metrics").Summary; got != "2 PrometheusRules reference a metric renamed in Kubernetes 1.36." {
		t.Fatalf("PrometheusRule summary = %q", got)
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

func TestScanFlexVolumeApplicabilityRequiresNodeEvidence(t *testing.T) {
	input := completeInput()
	input.Platform = "eks"
	input.Nodes = []*corev1.Node{nil}
	input.PersistentVolumes = []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "flex"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: "example.com/driver"}}}}}
	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, got, "flexvolume-kubeadm-support")
	if check.Status != CheckUnknown || len(check.Findings) != 0 {
		t.Fatalf("FlexVolume check without node evidence = %+v, want unknown applicability", check)
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

func TestScanWarningAndReviewVerdictsRemainDistinct(t *testing.T) {
	reviewInput := completeInput()
	reviewInput.Services = []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"203.0.113.10"}}}}
	review, err := Scan(reviewInput, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != VerdictReview || review.Summary.Reviews != 1 || review.Summary.Warnings != 0 {
		t.Fatalf("review-only result = %+v", review)
	}

	warningInput := completeInput()
	warningInput.Nodes[0].Status.Conditions[0].Status = corev1.ConditionFalse
	warning, err := Scan(warningInput, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if warning.Verdict != VerdictWarning || warning.Summary.Warnings != 1 || warning.Summary.Reviews != 0 {
		t.Fatalf("warning result = %+v", warning)
	}
}

func TestFinalizeCheckUsesHighestActionLevel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		findings  []Finding
		want      CheckStatus
		wantLevel Level
	}{
		{name: "review", findings: []Finding{{Level: LevelReview}}, want: CheckReview, wantLevel: LevelReview},
		{name: "warning over review", findings: []Finding{{Level: LevelReview}, {Level: LevelWarning}}, want: CheckWarning, wantLevel: LevelWarning},
		{name: "blocker over warning", findings: []Finding{{Level: LevelWarning}, {Level: LevelBlocker}}, want: CheckBlocked, wantLevel: LevelBlocker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := Check{Status: CheckPassed, Findings: tc.findings}
			finalizeCheck(&check)
			if check.Status != tc.want || check.Findings[0].Level != tc.wantLevel {
				t.Fatalf("finalized check = %+v, want status %s and highest action first", check, tc.want)
			}
		})
	}
}

func TestFinalizeCheckSortsEquivalentResourcesByEvidence(t *testing.T) {
	resource := &ResourceRef{Kind: "Deployment", Namespace: "default", Name: "api"}
	check := Check{Status: CheckPassed, Findings: []Finding{
		{Level: LevelReview, Resource: resource, Evidence: Evidence{Source: "live", Path: "spec.z"}},
		{Level: LevelReview, Resource: resource, Evidence: Evidence{Source: "live", Path: "spec.a"}},
	}}
	finalizeCheck(&check)
	if check.Findings[0].Evidence.Path != "spec.a" || check.Findings[1].Evidence.Path != "spec.z" {
		t.Fatalf("finding order = %+v, want deterministic evidence-path order", check.Findings)
	}
}

func TestFormatBoundedList(t *testing.T) {
	got := formatBoundedList([]string{"z", "e", "d", "c", "b", "a", "a"}, ", ")
	if got != "a, b, c, d, e, and 1 more" {
		t.Fatalf("formatted list = %q", got)
	}
	if got := scopedCoverageNote([]string{}, "source manifests"); got != "" {
		t.Fatalf("empty scope note = %q, want empty", got)
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
	}{{"nope", "1.36", ErrInvalidCurrentVersion}, {"1.35", "1.36.1", ErrInvalidTargetVersion}, {"1.35", "1.99999", ErrInvalidTargetVersion}, {"1.35", "1.35", ErrNonForwardTarget}} {
		_, err := Scan(completeInput(), tc.current, tc.target)
		if !errors.Is(err, tc.want) {
			t.Fatalf("Scan(%q, %q) error = %v, want %v", tc.current, tc.target, err, tc.want)
		}
	}
}

func TestScanUpgradePathCapsRenderedSequence(t *testing.T) {
	check := scanUpgradePath(utilversion.MustParseGeneric("1.34"), utilversion.MustParseGeneric("1.9999"))
	if len(check.Findings) != 1 {
		t.Fatalf("findings = %+v", check.Findings)
	}
	sequence := check.Findings[0].Evidence.Detail
	if !strings.Contains(sequence, "…") || !strings.HasSuffix(sequence, "1.9999") || strings.Count(sequence, "→") != 9 || len(sequence) > 100 {
		t.Fatalf("uncapped upgrade sequence = %q", sequence)
	}
}

func TestEffectiveTargetNormalizesEquivalentSpellings(t *testing.T) {
	cases := []struct {
		current, target, want string
	}{
		{"v1.33.4-gke.1", "", "1.34"},
		{"v1.33.4-gke.1", "1.34", "1.34"},
		{"v1.33.4-gke.1", "v1.34", "1.34"},
	}
	for _, c := range cases {
		got, err := EffectiveTarget(c.current, c.target)
		if err != nil || got != c.want {
			t.Fatalf("EffectiveTarget(%q, %q) = (%q, %v), want %q — equivalent spellings must share one memo key", c.current, c.target, got, err, c.want)
		}
	}
	if _, err := EffectiveTarget("v1.33.4", "1.33"); err == nil {
		t.Fatal("EffectiveTarget accepted a non-forward target")
	}
	if _, err := EffectiveTarget("bogus", "1.34"); err == nil {
		t.Fatal("EffectiveTarget accepted an unparseable current version")
	}
}
