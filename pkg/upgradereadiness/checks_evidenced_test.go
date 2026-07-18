package upgradereadiness

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCrossedReleaseCatalog(t *testing.T) {
	result, err := Scan(completeInput(), "1.34", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 17 {
		t.Fatalf("checks = %d, want 17", len(result.Checks))
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

func TestAdmissionAndCRDBackendSemantics(t *testing.T) {
	input := completeInput()
	input.AdmissionWebhookConfigurations = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "policy"},
		"webhooks": []any{map[string]any{"name": "policy.example", "clientConfig": map[string]any{"service": map[string]any{"namespace": "default", "name": "missing"}}}},
	}}}
	result, _ := Scan(input, "1.35", "1.36")
	if checkByID(t, result, "admission-webhook-readiness").Status != CheckBlocked {
		t.Fatal("fail-closed webhook with no backend must block")
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

func TestStrictSourceValidationAndGKEProbeEvidence(t *testing.T) {
	input := completeInput()
	input.ManifestResources[0] = ManifestResource{APIVersion: "v1", Kind: "Service", Namespace: "default", Name: "api", Source: "Helm", Object: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "api"}, "spec": map[string]any{"externalIPs": []any{"010.0.0.1"}}}}}
	result, _ := Scan(input, "1.35", "1.36")
	if checkByID(t, result, "strict-ip-cidr-validation").Status != CheckWarning {
		t.Fatal("non-canonical source IP must require review")
	}

	input = completeInput()
	input.Platform = "gke"
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}}}}}}}}}
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs", Controller: boolPtr(true)}}}}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", Controller: boolPtr(true)}}}}}
	input.Events = []*corev1.Event{{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "api-1"}, Message: "Liveness probe failed: command timed out"}}
	result, _ = Scan(input, "1.34", "1.35")
	if checkByID(t, result, "gke-exec-probe-timeout").Status != CheckBlocked {
		t.Fatal("correlated GKE timeout event must block")
	}
}

func boolPtr(value bool) *bool { return &value }
