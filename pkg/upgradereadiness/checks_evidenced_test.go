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
	input.Nodes[0].Status.NodeInfo.ContainerRuntimeVersion = "cri-o://1.35.0"
	input.NodeRuntimeEvidence[0].MetricsAvailable = false
	result, _ = Scan(input, "1.35", "1.36")
	if checkByID(t, result, "container-runtime-support").Status != CheckUnknown {
		t.Fatal("unrecognized CRI without metric must be incomplete")
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
	webhooks := input.AdmissionWebhookConfigurations[0].Object["webhooks"].([]any)
	webhooks[0].(map[string]any)["failurePolicy"] = "Ignore"
	result, _ = Scan(input, "1.35", "1.36")
	admission = checkByID(t, result, "admission-webhook-readiness")
	if admission.Status != CheckWarning || admission.Findings[0].Title != "Fail-open webhook backend unavailable" || !strings.Contains(admission.Findings[0].Impact, "fail open") {
		t.Fatalf("fail-open webhook with no backend must warn with a concrete impact: %+v", admission)
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

	input.AdmissionWebhookConfigurations[0].Object["webhooks"] = []any{map[string]any{
		"name": "policy.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "policy-system", "name": "policy"}},
	}}
	admission = scanAdmissionWebhookReadiness(input)
	finalizeCheck(&admission)
	if admission.Status != CheckUnknown || len(admission.Findings) != 0 || admission.Caveat == "" {
		t.Fatalf("unverified admission backend = %+v, want incomplete without a false blocker", admission)
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
	input.Services = append(input.Services, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "policy-system"}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "converter", Namespace: "policy-system"}})
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

	input = completeInput()
	input.Platform = "gke"
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}}}}}}}}}
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs", Controller: boolPtr(true)}}}}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", Controller: boolPtr(true)}}}}}
	input.Events = []*corev1.Event{{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "api-1"}, Message: "Liveness probe failed: command timed out"}}
	result, _ = Scan(input, "1.34", "1.35")
	gke := checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckBlocked || gke.Findings[0].Title != "Exec probe already timing out" {
		t.Fatal("correlated GKE timeout event must block")
	}

	input.Events = []*corev1.Event{}
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckReview || gke.Findings[0].Title != "Exec probe relies on default one-second timeout" {
		t.Fatalf("static GKE timeout exposure must have a distinct review title: %+v", gke)
	}

	input.Namespaces = []string{"default"}
	input.Deployments[0].Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = 2
	result, _ = Scan(input, "1.34", "1.35")
	gke = checkByID(t, result, "gke-exec-probe-timeout")
	if gke.Status != CheckUnknown || !strings.Contains(gke.Summary, "Exec probe timeouts are configured") || strings.Contains(gke.Summary, "No risky") {
		t.Fatalf("scoped configured GKE probes must retain the unprovable-duration explanation: %+v", gke)
	}
}

func boolPtr(value bool) *bool { return &value }
