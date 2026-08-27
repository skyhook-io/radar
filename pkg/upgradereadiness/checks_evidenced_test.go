package upgradereadiness

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCrossedReleaseCatalog(t *testing.T) {
	result, err := Scan(completeInput(), "1.34", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 18 {
		t.Fatalf("checks = %d, want 18", len(result.Checks))
	}
	for _, id := range []string{"node-cgroup-v1", "gke-exec-probe-timeout", "container-runtime-support", "strict-ip-cidr-validation"} {
		_ = checkByID(t, result, id)
	}

	result, err = Scan(completeInput(), "1.36", "1.37")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 19 {
		t.Fatalf("checks = %d, want 19", len(result.Checks))
	}
	for _, id := range []string{"removed-feature-gates", "removed-component-flags", "removed-kubelet-cadvisor-options", "kubelet-event-qps-change", "removed-scheduling-apis", "kubeadm-config-v1beta3", "removed-control-plane-metrics", "selinux-mount-transition", "kube-proxy-mode-transition"} {
		_ = checkByID(t, result, id)
	}
	for _, check := range result.Checks {
		if check.AppliesFrom == "1.35" || check.AppliesFrom == "1.36" {
			t.Fatalf("already-crossed release check remained: %+v", check)
		}
	}
}

func TestNodeCompatibilityEvidence(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].CgroupVersion = 1
	result, _ := Scan(input, "1.34", "1.36")
	if checkByID(t, result, "node-cgroup-v1").Status != CheckBlocked {
		t.Fatal("cgroup v1 must block")
	}

	input.Nodes = nil
	result, _ = Scan(input, "1.34", "1.36")
	cgroup := checkByID(t, result, "node-cgroup-v1")
	if cgroup.Status != CheckUnknown || len(cgroup.Findings) != 0 {
		t.Fatalf("cgroup evidence without node-list access = %+v, want incomplete with no findings", cgroup)
	}

	input.Nodes = []*corev1.Node{}
	result, _ = Scan(input, "1.34", "1.36")
	for _, id := range []string{"node-cgroup-v1", "container-runtime-support"} {
		check := checkByID(t, result, id)
		if check.Status != CheckUnknown || len(check.Findings) != 0 {
			t.Fatalf("%s with an empty node list = %+v, want incomplete with no findings", id, check)
		}
	}

	input = completeInput()
	input.Nodes[0].Status.NodeInfo.ContainerRuntimeVersion = "containerd://1.7.24"
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "container-runtime-support").Status != CheckPassed {
		t.Fatal("runtime major version alone must not claim a Kubernetes 1.36 support cutoff")
	}

	input = completeInput()
	input.NodeRuntimeEvidence[0].CRILosingSupportAvailable = true
	input.NodeRuntimeEvidence[0].CRILosingSupportVersion = "1.36"
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "container-runtime-support").Status != CheckBlocked {
		t.Fatal("authoritative kubelet CRI support-loss metric must block at the target version")
	}

	input = completeInput()
	input.Nodes[0].Status.NodeInfo.KubeletVersion = "v1.34.9"
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "container-runtime-support").Status != CheckUnknown {
		t.Fatal("pre-1.35 kubelet without the CRI support metric must remain unknown")
	}

	input = completeInput()
	input.Nodes[0].Status.NodeInfo.ContainerRuntimeVersion = "cri-o://1.35.0"
	input.NodeRuntimeEvidence[0].MetricsAvailable = false
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "container-runtime-support").Status != CheckUnknown {
		t.Fatal("unrecognized CRI without metric must be incomplete")
	}

	input = completeInput()
	input.Nodes = append(input.Nodes, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}})
	result, _ = Scan(input, "1.34", "1.35")
	cgroup = checkByID(t, result, "node-cgroup-v1")
	if cgroup.Status != CheckUnknown || cgroup.Inspected != 2 || !strings.Contains(cgroup.Caveat, "1 node") {
		t.Fatalf("cgroup check with a node missing runtime evidence = %+v, want incomplete coverage", cgroup)
	}

	input = completeInput()
	input.Nodes[0].Status.NodeInfo.OperatingSystem = "windows"
	input.NodeRuntimeEvidence = nil
	result, _ = Scan(input, "1.34", "1.35")
	cgroup = checkByID(t, result, "node-cgroup-v1")
	if cgroup.Status != CheckNotApplicable || cgroup.Inspected != 0 {
		t.Fatalf("Windows-only cgroup check = %+v, want not applicable", cgroup)
	}
	result, _ = Scan(input, "1.35", "1.36")
	runtime := checkByID(t, result, "container-runtime-support")
	if runtime.Status != CheckNotApplicable || runtime.Caveat != "" || runtime.Inspected != 0 {
		t.Fatalf("Windows-only runtime check = %+v, want not applicable", runtime)
	}
}

func TestDrainPDBAuthorityAndAlwaysAllow(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Labels: map[string]string{"app": "api"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}}
	input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Generation: 2}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}, Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 2, ExpectedPods: 1, DesiredHealthy: 1, CurrentHealthy: 1, DisruptionsAllowed: 0}}}
	result, _ := Scan(input, "1.35", "1.36")
	if checkByID(t, result, "node-drain-feasibility").Status != CheckBlocked {
		t.Fatal("authoritative zero-disruption PDB must block")
	}

	policy := policyv1.AlwaysAllow
	input.Pods[0].Status.Conditions[0].Status = corev1.ConditionFalse
	input.PodDisruptionBudgets[0].Spec.UnhealthyPodEvictionPolicy = &policy
	input.PodDisruptionBudgets[0].Status.CurrentHealthy = 0
	result, _ = Scan(input, "1.35", "1.36")
	check := checkByID(t, result, "node-drain-feasibility")
	for _, finding := range check.Findings {
		if finding.Resource != nil && finding.Resource.Kind == "PodDisruptionBudget" {
			t.Fatalf("AlwaysAllow unhealthy-only PDB must not block: %+v", check)
		}
	}
}

func TestDrainDetectsOverlappingPDBSelectors(t *testing.T) {
	input := completeInput()
	input.Namespaces = []string{"default"}
	controller := true
	for _, name := range []string{"api-2", "api-1", "api-3"} {
		input.Pods = append(input.Pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default", Labels: map[string]string{"app": "api", "tier": "backend"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api", Controller: &controller}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Generation: 2},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
			Status:     policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1}, // stale status must not hide selector overlap
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "default", Generation: 1},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "backend"}}},
			Status:     policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, DisruptionsAllowed: 1},
		},
	}

	result, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, result, "node-drain-feasibility")
	if check.Status != CheckBlocked || !strings.Contains(check.Caveat, "Only namespace default") || !strings.Contains(check.Caveat, "stale or unreadable status") {
		t.Fatalf("overlap with stale status = %+v, want blocker plus coverage caveat", check)
	}
	var overlaps []Finding
	for _, finding := range check.Findings {
		if finding.Title == "Pod is selected by multiple PodDisruptionBudgets" {
			overlaps = append(overlaps, finding)
		}
	}
	if len(overlaps) != 1 {
		t.Fatalf("overlap findings = %+v, want one pair-level finding", overlaps)
	}
	if overlaps[0].Resource == nil || overlaps[0].Resource.Name != "api" || overlaps[0].Evidence.Detail != "also overlaps default/backend across 3 active pod(s), including Pod default/api-1" {
		t.Fatalf("overlap finding = %+v, want canonical pair and stable evidence", overlaps[0])
	}
}

func TestDrainIgnoresNonOverlappingDeletingAndInvalidPDBs(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Labels: map[string]string{"app": "api"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
	deletedAt := metav1.Now()
	input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{
		{ObjectMeta: metav1.ObjectMeta{Name: "deleting", Namespace: "default", DeletionTimestamp: &deletedAt}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "default"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOperator("Invalid")}}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default", Generation: 1}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}}}, Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, DisruptionsAllowed: 1}},
	}
	check := scanNodeDrainFeasibility(input)
	for _, finding := range check.Findings {
		if finding.Title == "Pod is selected by multiple PodDisruptionBudgets" {
			t.Fatalf("unexpected overlap finding: %+v", finding)
		}
	}
}

func TestDrainEmptyDirsCollapseToOneReviewPerPod(t *testing.T) {
	input := completeInput()
	controller := true
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs", Controller: &controller}}},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{
			{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		}},
	}}
	result, _ := Scan(input, "1.35", "1.36")
	check := checkByID(t, result, "node-drain-feasibility")
	if check.Status != CheckReview || len(check.Findings) != 1 || check.Findings[0].Level != LevelReview || check.Findings[0].Evidence.Detail != "cache, scratch" {
		t.Fatalf("emptyDir review = %+v", check)
	}
}

func TestAPIServiceReadinessSemantics(t *testing.T) {
	apiService := func(name string, delegated bool, status string) *unstructured.Unstructured {
		object := map[string]any{
			"apiVersion": "apiregistration.k8s.io/v1",
			"kind":       "APIService",
			"metadata":   map[string]any{"name": name},
		}
		if delegated {
			object["spec"] = map[string]any{"service": map[string]any{"namespace": "default", "name": "extension"}}
		}
		if status != "" {
			object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Available", "status": status, "reason": "FailedDiscoveryCheck", "message": "backend unavailable"}}}
		}
		return &unstructured.Unstructured{Object: object}
	}
	tests := []struct {
		name        string
		items       []*unstructured.Unstructured
		wantStatus  CheckStatus
		findings    int
		caveat      bool
		wantSummary string
	}{
		{name: "unreadable", items: nil, wantStatus: CheckUnknown},
		{name: "empty", items: []*unstructured.Unstructured{}, wantStatus: CheckNotApplicable},
		{name: "local only", items: []*unstructured.Unstructured{apiService("v1.local", false, "True")}, wantStatus: CheckNotApplicable},
		{name: "available", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "True")}, wantStatus: CheckPassed},
		{name: "false", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "False")}, wantStatus: CheckWarning, findings: 1},
		{name: "unknown", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "Unknown")}, wantStatus: CheckWarning, findings: 1},
		{name: "missing condition", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "")}, wantStatus: CheckUnknown, caveat: true},
		{name: "malformed condition", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "Unexpected")}, wantStatus: CheckUnknown, caveat: true},
		{name: "finding with incomplete peer", items: []*unstructured.Unstructured{apiService("v1.example.io", true, "False"), apiService("v1.other.io", true, "")}, wantStatus: CheckWarning, findings: 1, caveat: true, wantSummary: "1 delegated APIService is unavailable."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := scanAPIServiceReadiness(&Input{APIServices: tt.items})
			finalizeCheck(&check)
			if check.Status != tt.wantStatus || len(check.Findings) != tt.findings || (check.Caveat != "") != tt.caveat {
				t.Fatalf("check = %+v, want status=%s findings=%d caveat=%v", check, tt.wantStatus, tt.findings, tt.caveat)
			}
			if tt.wantSummary != "" && check.Summary != tt.wantSummary {
				t.Fatalf("summary = %q, want %q", check.Summary, tt.wantSummary)
			}
			if tt.findings > 0 && check.Findings[0].Evidence.Detail != tt.items[0].Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"].(string)+" (FailedDiscoveryCheck): backend unavailable" {
				t.Fatalf("finding evidence = %q", check.Findings[0].Evidence.Detail)
			}
		})
	}
}

func TestCRDConversionFindingSummarySurvivesIncompletePeer(t *testing.T) {
	input := completeInput()
	input.CustomResourceDefinitions = []*unstructured.Unstructured{
		{Object: map[string]any{
			"metadata": map[string]any{"name": "broken.example.io"},
			"spec": map[string]any{"conversion": map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"clientConfig": map[string]any{"service": map[string]any{"namespace": "default", "name": "missing"}},
			}}},
		}},
		{Object: map[string]any{
			"metadata": map[string]any{"name": "unreadable.example.io"},
			"spec":     map[string]any{"conversion": map[string]any{"strategy": "Webhook", "webhook": map[string]any{}}},
		}},
	}
	check := scanCRDConversionWebhookReadiness(input)
	if check.Caveat == "" || len(check.Findings) != 1 || check.Summary != "1 CRD conversion webhook condition requires action or review." {
		t.Fatalf("mixed CRD evidence = %+v", check)
	}
}

func TestAdmissionAndCRDBackendSemantics(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "policy"},
		"webhooks": []any{map[string]any{"name": "policy.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "default", "name": "missing"}}}},
	}}}
	result, _ := Scan(input, "1.35", "1.36")
	admission := checkByID(t, result, "admission-webhook-readiness")
	if admission.Status != CheckBlocked || admission.Findings[0].Title != "Fail-closed webhook backend unavailable" || admission.Findings[0].Evidence.Detail != "default/missing" || !strings.Contains(admission.Findings[0].Impact, "rejected") {
		t.Fatal("fail-closed webhook with no backend must block")
	}
	if got := admission.Findings[0].References; len(got) != 2 || got[0].URL != "https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#failure-policy" || got[1].URL != "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/#ha-deployment" {
		t.Fatalf("backend finding references = %+v, want failure-policy and availability guidance", got)
	}
	webhooks := input.AdmissionWebhookConfigurations[0].Object["webhooks"].([]any)
	webhooks[0].(map[string]any)["failurePolicy"] = "Ignore"
	result, _ = Scan(input, "1.35", "1.36")
	admission = checkByID(t, result, "admission-webhook-readiness")
	if admission.Status != CheckWarning || admission.Findings[0].Title != "Fail-open webhook backend unavailable" || !strings.Contains(admission.Findings[0].Impact, "fail open") {
		t.Fatalf("fail-open webhook with no backend must warn with a concrete impact: %+v", admission)
	}

	input.AdmissionWebhookConfigurations[0].Object["webhooks"] = []any{map[string]any{
		"name": "external.example", "clientConfig": map[string]any{"url": "https://policy.example/admit"},
	}}
	result, _ = Scan(input, "1.35", "1.36")
	admission = checkByID(t, result, "admission-webhook-readiness")
	if admission.Status != CheckReview || len(admission.Findings) != 1 || admission.Findings[0].Title != "URL webhook backend requires external verification" || admission.Findings[0].Evidence.Path != "webhooks[0].clientConfig.url" {
		t.Fatalf("URL admission backend = %+v, want an explicit external-readiness review", admission)
	}

	input.CustomResourceDefinitions = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "widgets.example.io"},
		"spec":   map[string]any{"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}}, "conversion": map[string]any{"strategy": "Webhook", "webhook": map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": "default", "name": "missing"}}}}},
		"status": map[string]any{"storedVersions": []any{"v1"}},
	}}}
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "crd-conversion-webhook-readiness").Status != CheckWarning {
		t.Fatal("dead backend without a current conversion path is review, not blocker")
	}
}

func TestAdmissionWebhookConfigurationWithoutWebhooksIsInspectedEmpty(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "empty-policy"},
	}}}

	check := scanAdmissionWebhookReadiness(input)
	finalizeCheck(&check)
	if check.Status != CheckPassed || len(check.Findings) != 0 || check.Caveat != "" || check.Inspected != 1 {
		t.Fatalf("empty admission webhook configuration = %+v, want inspected pass without a coverage caveat", check)
	}

	input.AdmissionWebhookConfigurations[0].Object["webhooks"] = "not-a-list"
	check = scanAdmissionWebhookReadiness(input)
	finalizeCheck(&check)
	if check.Status != CheckUnknown || len(check.Findings) != 0 || check.Caveat == "" {
		t.Fatalf("malformed admission webhook configuration = %+v, want incomplete coverage", check)
	}
}

func TestAdmissionWebhookConfigurationPartialRBACPreservesReadableEvidence(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookUnavailableKinds = []string{"mutatingwebhookconfigurations"}
	input.AdmissionWebhookDeniedKinds = []string{"mutatingwebhookconfigurations"}
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "policy"},
		"webhooks": []any{map[string]any{"name": "policy.example", "matchPolicy": "Exact"}},
	}}}

	check := scanAdmissionWebhookReadiness(input)
	finalizeCheck(&check)
	if check.Status != CheckReview || len(check.Findings) != 1 || strings.Count(check.Caveat, "mutatingwebhookconfigurations") != 1 || !strings.Contains(check.Caveat, "current identity cannot list them") || !strings.Contains(check.Caveat, "grant list access") {
		t.Fatalf("partially readable admission configurations = %+v, want readable findings plus a coverage caveat", check)
	}
}

func TestAdmissionWebhookTransientFailureDoesNotSuggestRBAC(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookUnavailableKinds = []string{"mutatingwebhookconfigurations"}

	check := scanAdmissionWebhookReadiness(input)
	if !strings.Contains(check.Caveat, "mutatingwebhookconfigurations") || strings.Contains(check.Caveat, "current identity") || strings.Contains(check.Caveat, "grant list access") {
		t.Fatalf("transient admission failure caveat = %q, want unattributed unavailable evidence", check.Caveat)
	}
}

func TestAdmissionWebhookUnavailableWithoutPermissionEvidenceStaysGeneric(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookConfigurations = nil

	check := scanAdmissionWebhookReadiness(input)
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "could not be inspected") || strings.Contains(check.Caveat, "rbac.viewWebhooks") {
		t.Fatalf("unattributed admission configuration failure = %+v, want a generic coverage caveat", check)
	}
}

func TestNodeRuntimeCoverageExplainsMissingNodeProxyPermission(t *testing.T) {
	input := completeInput()
	input.NodeProxyForbidden = true
	input.NodeRuntimeEvidence = nil

	result, err := Scan(input, "1.34", "1.37")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"node-cgroup-v1", "container-runtime-support", "removed-feature-gates", "kubelet-event-qps-change", "selinux-mount-transition"} {
		check := checkByID(t, result, id)
		if !strings.Contains(check.Caveat, "nodes/proxy request was forbidden") || !strings.Contains(check.Caveat, "grant the current identity get access to nodes/proxy") {
			t.Fatalf("%s caveat = %q, want actionable nodes/proxy RBAC attribution", id, check.Caveat)
		}
		if id == "container-runtime-support" && check.Inspected != 0 {
			t.Fatalf("%s inspected = %d, want zero when all runtime evidence is unavailable", id, check.Inspected)
		}
	}
}

func TestNodeRuntimeCoverageDoesNotBlameNodeProxyWhenNodesAreUnavailable(t *testing.T) {
	input := completeInput()
	input.Nodes = nil
	input.NodeProxyForbidden = true
	input.NodeRuntimeEvidence = nil

	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if strings.Contains(check.Caveat, "nodes/proxy") {
		t.Fatalf("SELinux caveat = %q, must not attribute missing Node list evidence to nodes/proxy", check.Caveat)
	}
}

func TestWebhookConfigurationFindingsSurviveMissingBackendEvidence(t *testing.T) {
	input := completeInput()
	input.Services = nil
	input.EndpointSlices = nil
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "policy"},
		"webhooks": []any{map[string]any{
			"name": "policy.example", "matchPolicy": "Exact",
			"clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "policy"}},
			"rules":        []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{"authentication.k8s.io"}, "resources": []any{"tokenreviews"}}},
		}},
	}}}

	admission := scanAdmissionWebhookReadiness(input)
	finalizeCheck(&admission)
	if admission.Status != CheckReview || len(admission.Findings) != 2 || admission.Caveat == "" {
		t.Fatalf("config-only admission evidence = %+v, want two review findings plus incomplete backend coverage", admission)
	}
	findingsByTitle := map[string]Finding{}
	for _, finding := range admission.Findings {
		findingsByTitle[finding.Title] = finding
		for _, reference := range finding.References {
			for _, generic := range admissionReferences {
				if reference.URL == generic.URL {
					t.Fatalf("finding %q repeated check-wide reference %q", finding.Title, reference.URL)
				}
			}
		}
	}
	if got := findingsByTitle["Exact API version matching"].References; len(got) != 1 || !strings.HasSuffix(got[0].URL, "#match-all-versions") {
		t.Fatalf("exact matching references = %+v, want specific matchPolicy guidance", got)
	}
	if got := findingsByTitle["Authentication review interception"].References; len(got) != 1 || !strings.HasSuffix(got[0].URL, "#webhook-limit-scope") {
		t.Fatalf("authentication review references = %+v, want specific scope guidance", got)
	}

	input.AdmissionWebhookConfigurations[0].Object["webhooks"] = []any{
		map[string]any{"name": "policy.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "policy"}}},
		map[string]any{"name": "audit.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "audit"}}},
	}
	admission = scanAdmissionWebhookReadiness(input)
	finalizeCheck(&admission)
	if admission.Status != CheckUnknown || len(admission.Findings) != 0 || strings.Count(admission.Caveat, "webhook backend readiness could not be verified") != 1 {
		t.Fatalf("unverified admission backends = %+v, want one incomplete-coverage caveat without false blockers", admission)
	}

	input.CustomResourceDefinitions = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "widgets.example.io"},
		"spec": map[string]any{
			"versions":   []any{map[string]any{"name": "v1", "served": true, "storage": true}},
			"conversion": map[string]any{"strategy": "Webhook", "webhook": map[string]any{"clientConfig": map[string]any{"url": "https://converter.example"}}},
		},
		"status": map[string]any{"storedVersions": []any{"v1"}},
	}}}
	conversion := scanCRDConversionWebhookReadiness(input)
	finalizeCheck(&conversion)
	if conversion.Status != CheckReview || len(conversion.Findings) != 1 || conversion.Caveat != "" {
		t.Fatalf("URL conversion evidence = %+v, want review without irrelevant Service coverage caveat", conversion)
	}
}

func TestWebhookBackendsOutsideBrowseScopeUseCollectedEvidence(t *testing.T) {
	input := completeInput()
	input.Namespaces = []string{"default"}
	input.WebhookServices = append(input.WebhookServices, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "policy-system"}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "converter", Namespace: "policy-system"}})
	input.EndpointSlices = append(input.EndpointSlices,
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "policy-system", Labels: map[string]string{discoveryv1.LabelServiceName: "policy"}}, Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "converter", Namespace: "policy-system", Labels: map[string]string{discoveryv1.LabelServiceName: "converter"}}, Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}}}},
	)
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "policy"},
		"webhooks": []any{map[string]any{"name": "policy.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "policy"}}}},
	}}}
	input.CustomResourceDefinitions = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "widgets.example.io"},
		"spec": map[string]any{
			"versions":   []any{map[string]any{"name": "v1", "served": true, "storage": true}, map[string]any{"name": "v2", "served": true, "storage": false}},
			"conversion": map[string]any{"strategy": "Webhook", "webhook": map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "converter"}}}},
		},
		"status": map[string]any{"storedVersions": []any{"v1"}},
	}}}
	result, _ := Scan(input, "1.35", "1.36")
	for _, id := range []string{"admission-webhook-readiness", "crd-conversion-webhook-readiness"} {
		check := checkByID(t, result, id)
		if check.Status != CheckPassed || len(check.Findings) != 0 {
			t.Fatalf("%s must trust explicitly collected backend evidence outside the browse scope: %+v", id, check)
		}
	}

	input.EndpointSlices = input.EndpointSlices[:len(input.EndpointSlices)-2]
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "admission-webhook-readiness").Status != CheckBlocked || checkByID(t, result, "crd-conversion-webhook-readiness").Status != CheckBlocked {
		t.Fatal("unready collected backends outside the browse scope must remain actionable")
	}
}

func TestStrictSourceValidationAndGKEProbeEvidence(t *testing.T) {
	input := completeInput()
	input.ManifestResources[0] = ManifestResource{APIVersion: "v1", Kind: "Service", Namespace: "default", Name: "api", Source: "Helm", Object: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "api"}, "spec": map[string]any{"externalIPs": []any{"010.0.0.1"}}}}}
	result, _ := Scan(input, "1.35", "1.36")
	strict := checkByID(t, result, "strict-ip-cidr-validation")
	if strict.Status != CheckReview || !strings.Contains(strict.Summary, "1 non-canonical") {
		t.Fatal("non-canonical source IP must require review")
	}
	input.Namespaces = []string{"default"}
	result, _ = Scan(input, "1.35", "1.36")
	strict = checkByID(t, result, "strict-ip-cidr-validation")
	if strict.Status != CheckReview || !strings.Contains(strict.Summary, "1 non-canonical") || strings.Contains(strict.Summary, "No invalid") || strict.Caveat == "" {
		t.Fatalf("scoped strict-IP findings must keep their actionable summary and coverage note: %+v", strict)
	}
	input = completeInput()
	input.Namespaces = []string{"default"}
	result, _ = Scan(input, "1.35", "1.36")
	strict = checkByID(t, result, "strict-ip-cidr-validation")
	if strict.Status != CheckUnknown || !strings.Contains(strict.Summary, "coverage is incomplete") {
		t.Fatalf("scoped strict-IP clean result must remain incomplete: %+v", strict)
	}

	networkPolicy := &unstructured.Unstructured{Object: map[string]any{
		"kind": "NetworkPolicy",
		"spec": map[string]any{
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{
					"ipBlock": map[string]any{"cidr": "010.0.0.0/24", "except": []any{"010.0.0.1/32"}},
				}},
			}},
			"egress": []any{map[string]any{
				"to": []any{map[string]any{
					"ipBlock": map[string]any{"cidr": "010.0.1.0/24"},
				}},
			}},
		},
	}}
	candidates := strictNetworkCandidates(networkPolicy)
	wantPaths := []string{
		"spec.egress[0].to[0].ipBlock.cidr",
		"spec.ingress[0].from[0].ipBlock.cidr",
		"spec.ingress[0].from[0].ipBlock.except[0]",
	}
	if len(candidates) != len(wantPaths) {
		t.Fatalf("NetworkPolicy candidates = %+v, want paths %v", candidates, wantPaths)
	}
	for i, want := range wantPaths {
		if candidates[i].path != want {
			t.Fatalf("NetworkPolicy candidate %d path = %q, want %q", i, candidates[i].path, want)
		}
	}

	input = completeInput()
	input.Platform = "gke"
	input.Namespaces = []string{"default"}
	result, _ = Scan(input, "1.34", "1.35")
	gke := checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckUnknown || gke.Caveat == "" || !strings.Contains(gke.Summary, "coverage is incomplete") {
		t.Fatalf("scoped GKE scan with no exec probes = %+v, want incomplete scoped evidence", gke)
	}

	input.Namespaces = nil
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}}}}}}}}}
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs", Controller: boolPtr(true)}}}}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", Controller: boolPtr(true)}}}}}
	input.Events = []*corev1.Event{{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "api-1"}, Message: "Liveness probe failed: command timed out"}}
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckBlocked || gke.Findings[0].Title != "Exec probe already timing out" {
		t.Fatal("correlated GKE timeout event must block")
	}

	input.Events = []*corev1.Event{}
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckReview || gke.Findings[0].Title != "Exec probe relies on default one-second timeout" {
		t.Fatalf("static GKE timeout exposure must have a distinct review title: %+v", gke)
	}

	input.Deployments[0].Spec.Template.Spec.InitContainers = input.Deployments[0].Spec.Template.Spec.Containers
	input.Deployments[0].Spec.Template.Spec.Containers = nil
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if len(gke.Findings) != 1 || !strings.Contains(gke.Findings[0].Evidence.Path, ".initContainers[api].") {
		t.Fatalf("init-container probe evidence path = %+v, want initContainers", gke.Findings)
	}
	input.Deployments[0].Spec.Template.Spec.Containers = input.Deployments[0].Spec.Template.Spec.InitContainers
	input.Deployments[0].Spec.Template.Spec.InitContainers = nil

	input.Namespaces = []string{"default"}
	input.Deployments[0].Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = 2
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckUnknown || gke.Inspected != 1 || !strings.Contains(gke.Summary, "Exec probe timeouts are configured") || strings.Contains(gke.Summary, "No risky") {
		t.Fatalf("scoped configured GKE probes must retain the unprovable-duration explanation: %+v", gke)
	}
}

func TestStrictNetworkCandidatesIgnoreHeadlessServiceSentinel(t *testing.T) {
	service := &unstructured.Unstructured{Object: map[string]any{
		"kind": "Service",
		"spec": map[string]any{"clusterIP": "None", "clusterIPs": []any{"None"}},
	}}
	if candidates := strictNetworkCandidates(service); len(candidates) != 0 {
		t.Fatalf("headless Service candidates = %+v, want none", candidates)
	}
}

func TestGKEExecProbeRetainsReadableFindingsWithPartialWorkloadCoverage(t *testing.T) {
	input := completeInput()
	input.Platform = "gke"
	input.Pods = nil
	input.Deployments = []*appsv1.Deployment{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "api", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}},
		}}}}},
	}}
	result, err := Scan(input, "1.34", "1.35")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, result, "gke-exec-probe-timeout")
	if check.Status != CheckReview || len(check.Findings) != 1 || check.Caveat == "" {
		t.Fatalf("GKE check = %+v, want retained finding plus partial-evidence caveat", check)
	}
}

func TestGKEExecProbeUnknownPlatformPreservesUncertainty(t *testing.T) {
	input := completeInput()
	input.Platform = "unknown"
	result, err := Scan(input, "1.34", "1.35")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, result, "gke-exec-probe-timeout")
	if check.Status != CheckUnknown || !strings.Contains(check.Summary, "platform detection was unavailable") {
		t.Fatalf("unknown-platform GKE check = %+v, want explicit uncertainty", check)
	}
}

func TestGKEExecProbeEventDoesNotCrossResourceKinds(t *testing.T) {
	input := completeInput()
	input.Platform = "gke"
	input.Deployments = []*appsv1.Deployment{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "api", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}},
		}}}}},
	}}
	input.Events = []*corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "api"},
		Message:        "Liveness probe failed: command timed out",
	}}
	result, err := Scan(input, "1.34", "1.35")
	if err != nil {
		t.Fatal(err)
	}
	check := checkByID(t, result, "gke-exec-probe-timeout")
	if check.Status != CheckReview || check.Findings[0].Title != "Exec probe relies on default one-second timeout" {
		t.Fatalf("GKE check = %+v, Pod event must not mark same-named Deployment as timed out", check)
	}
}

func boolPtr(value bool) *bool { return &value }
