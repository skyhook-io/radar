package audit

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	networkingv1 "k8s.io/api/networking/v1"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func secureContainer(name string, readiness bool) corev1.Container {
	c := corev1.Container{
		Name: name, Image: "app:v1",
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr(true),
			ReadOnlyRootFilesystem:   ptr(true),
			AllowPrivilegeEscalation: ptr(false),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt(8080)}}},
	}
	if readiness {
		c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt(8080)}}}
	}
	return c
}

func deploymentInNS(name, ns string, replicas int32, containers ...corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr(false),
					Containers:                   containers,
				},
			},
		},
	}
}

// TestCheckCounts_PodSpecFamilyCountsWorkloads pins the subject unit of the
// pod-spec family: a 3-container workload where 2 containers fail one check
// is ONE evaluated subject with ONE merged finding — evaluated=1, passed=0 —
// while sibling checks the same workload satisfies show passed=1.
func TestCheckCounts_PodSpecFamilyCountsWorkloads(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts: []*corev1.ServiceAccount{},
		LimitRanges:     []*corev1.LimitRange{},
		Deployments: []*appsv1.Deployment{
			deploymentInNS("web", "prod", 1,
				secureContainer("app", false),
				secureContainer("sidecar", false),
				secureContainer("probe-ok", true),
			),
		},
	}
	results := RunChecks(input)

	merged := 0
	for _, f := range results.Findings {
		if f.CheckID == "readinessProbeMissing" {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("expected 1 merged readinessProbeMissing finding, got %d", merged)
	}

	if got := results.CheckCounts["readinessProbeMissing"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("readinessProbeMissing counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	for _, id := range []string{"livenessProbeMissing", "runAsRoot", "privileged", "cpuLimitMissing", "dockerSocketMount"} {
		if got := results.CheckCounts[id]; got != (CheckCount{Evaluated: 1, Passed: 1}) {
			t.Errorf("%s counts = %+v, want {Evaluated:1 Passed:1}", id, got)
		}
	}
	if byNS := results.EvaluatedByNamespace["readinessProbeMissing"]; byNS["prod"] != 1 {
		t.Errorf("evaluatedByNamespace[readinessProbeMissing][prod] = %d, want 1", byNS["prod"])
	}
}

// TestCheckCounts_MissingInputs pins the "couldn't check ≠ passing" rule:
// nil prerequisite inputs surface in MissingInputs and their checks are
// absent from CheckCounts entirely.
func TestCheckCounts_MissingInputs(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{
			deploymentInNS("web", "prod", 3, secureContainer("app", true)),
		},
		// PodDisruptionBudgets, ConfigMaps, PodMetrics all nil = unavailable.
	}
	results := RunChecks(input)

	for _, id := range []string{"missingPDB", "orphanConfigMapSecret", "secretInConfigMap", "resourceUtilization"} {
		if _, ok := results.CheckCounts[id]; ok {
			t.Errorf("%s must not appear in CheckCounts when its input is nil", id)
		}
	}
	// Every nil prerequisite is declared, including the relationship-check
	// and pod-spec-family inventories added by the per-check gating.
	want := map[string]bool{
		"poddisruptionbudgets": true, "configmaps": true, "podmetrics": true,
		"serviceaccounts": true, "limitranges": true, "secrets": true,
		"pods": true, "services": true, "ingresses": true,
		"horizontalpodautoscalers": true,
		"statefulsets": true, "daemonsets": true,
		"jobs": true, "cronjobs": true,
	}
	if len(results.MissingInputs) != len(want) {
		t.Fatalf("MissingInputs = %v, want exactly %v", results.MissingInputs, want)
	}
	for _, in := range results.MissingInputs {
		if !want[in] {
			t.Errorf("unexpected missing input %q", in)
		}
	}

	// Same scan with the inputs present (but empty) — the checks run and count.
	input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{}
	input.ConfigMaps = []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "prod"}}}
	input.PodMetrics = []PodMetricsInput{}
	input.ServiceAccounts = []*corev1.ServiceAccount{}
	input.LimitRanges = []*corev1.LimitRange{}
	input.Pods = []*corev1.Pod{}
	input.Services = []*corev1.Service{}
	input.Ingresses = []*networkingv1.Ingress{}
	input.Secrets = []*corev1.Secret{}
	input.HorizontalPodAutoscalers = []*autoscalingv2.HorizontalPodAutoscaler{}
	input.StatefulSets = []*appsv1.StatefulSet{}
	input.DaemonSets = []*appsv1.DaemonSet{}
	input.Jobs = []*batchv1.Job{}
	input.CronJobs = []*batchv1.CronJob{}
	results = RunChecks(input)
	if len(results.MissingInputs) != 0 {
		t.Errorf("MissingInputs = %v, want none when inputs are non-nil", results.MissingInputs)
	}
	if got := results.CheckCounts["missingPDB"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("missingPDB counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	if got := results.CheckCounts["secretInConfigMap"]; got != (CheckCount{Evaluated: 1, Passed: 1}) {
		t.Errorf("secretInConfigMap counts = %+v, want {Evaluated:1 Passed:1}", got)
	}
}

// TestApplySettings_RecomputesCountsOnCopies pins the two ApplySettings
// invariants: ignored namespaces and disabled checks leave both the findings
// AND the denominators, and the raw (cached) results are never mutated.
func TestApplySettings_RecomputesCountsOnCopies(t *testing.T) {
	input := &CheckInput{
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			deploymentInNS("web", "prod", 1, secureContainer("app", true)),
			deploymentInNS("big", "prod", 3, secureContainer("app", true)),
			deploymentInNS("job", "dev", 1, secureContainer("app", true)),
		},
	}
	raw := RunChecks(input)

	if got := raw.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 3, Passed: 1}) {
		t.Fatalf("raw singleReplica counts = %+v, want {Evaluated:3 Passed:1}", got)
	}
	if got := raw.CheckCounts["missingTopologySpread"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Fatalf("raw missingTopologySpread counts = %+v, want {Evaluated:1 Passed:0}", got)
	}

	filtered := ApplySettings(raw, []string{"dev"}, []string{"missingTopologySpread"})

	if got := filtered.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 2, Passed: 1}) {
		t.Errorf("filtered singleReplica counts = %+v, want {Evaluated:2 Passed:1}", got)
	}
	if _, ok := filtered.EvaluatedByNamespace["singleReplica"]["dev"]; ok {
		t.Error("ignored namespace must be removed from EvaluatedByNamespace")
	}
	for _, f := range filtered.Findings {
		if f.Namespace == "dev" {
			t.Errorf("finding in ignored namespace survived: %+v", f)
		}
	}
	if _, ok := filtered.CheckCounts["missingTopologySpread"]; ok {
		t.Error("disabled check must be dropped from CheckCounts")
	}
	if _, ok := filtered.EvaluatedByNamespace["missingTopologySpread"]; ok {
		t.Error("disabled check must be dropped from EvaluatedByNamespace")
	}
	for _, f := range filtered.Findings {
		if f.CheckID == "missingTopologySpread" {
			t.Errorf("finding for disabled check survived: %+v", f)
		}
	}

	// Raw results back the server's shared 5s cache — they must be intact.
	if got := raw.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 3, Passed: 1}) {
		t.Errorf("raw CheckCounts mutated by ApplySettings: %+v", got)
	}
	if raw.EvaluatedByNamespace["singleReplica"]["dev"] != 1 {
		t.Errorf("raw EvaluatedByNamespace mutated by ApplySettings: %+v", raw.EvaluatedByNamespace["singleReplica"])
	}
	if _, ok := raw.CheckCounts["missingTopologySpread"]; !ok {
		t.Error("raw CheckCounts lost a check after ApplySettings")
	}
}

// TestCheckCounts_PassingArithmetic pins the summary rollups: Summary.Passing
// is the sum of per-check passed, and each category's Passing is the same sum
// restricted to that category's checks (via CheckRegistry).
func TestCheckCounts_PassingArithmetic(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{
			deploymentInNS("web", "prod", 1, secureContainer("app", true)),
			deploymentInNS("api", "prod", 1, secureContainer("app", false)),
		},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "prod"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "nope"}},
		}},
	}
	results := RunChecks(input)

	total := 0
	byCategory := map[string]int{}
	for id, cc := range results.CheckCounts {
		total += cc.Passed
		meta, ok := CheckRegistry[id]
		if !ok {
			t.Fatalf("check %q missing from CheckRegistry", id)
		}
		byCategory[meta.Category] += cc.Passed
	}
	if results.Summary.Passing != total {
		t.Errorf("Summary.Passing = %d, want %d (sum of per-check passed)", results.Summary.Passing, total)
	}
	for cat, cs := range results.Summary.Categories {
		if cs.Passing != byCategory[cat] {
			t.Errorf("category %s Passing = %d, want %d", cat, cs.Passing, byCategory[cat])
		}
	}
	if total == 0 {
		t.Error("expected a non-zero passing total for this fixture")
	}
}

func TestAutomount_PodLevelExplicitEvaluatesWithoutSAInventory(t *testing.T) {
	on := true
	input := &CheckInput{
		LimitRanges: []*corev1.LimitRange{},
		Deployments: []*appsv1.Deployment{
			deploymentInNS("web", "prod", 3, secureContainer("app", true)),
		},
	}
	input.Deployments[0].Spec.Template.Spec.AutomountServiceAccountToken = &on
	// ServiceAccounts deliberately nil: pod-level explicit value decides.
	results := RunChecks(input)
	cc, ok := results.CheckCounts["automountServiceAccountToken"]
	if !ok || cc.Evaluated != 1 || cc.Passed != 0 {
		t.Errorf("automount counts = %+v ok=%v, want evaluated 1 passed 0", cc, ok)
	}
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "automountServiceAccountToken" {
			found = true
		}
	}
	if !found {
		t.Errorf("pod-level explicit automount not detected with nil SA inventory")
	}
}

func TestAutomount_NamespaceScopedSAInventoryLimitsEvaluation(t *testing.T) {
	input := &CheckInput{
		LimitRanges:              []*corev1.LimitRange{},
		ServiceAccounts:          []*corev1.ServiceAccount{},
		ServiceAccountsNamespace: "covered",
		Deployments: []*appsv1.Deployment{
			deploymentInNS("in-scope", "covered", 3, secureContainer("app", true)),
			deploymentInNS("out-of-scope", "elsewhere", 3, secureContainer("app", true)),
		},
	}
	// The fixture sets pod-level automount explicitly; the point here is the
	// SA-inventory-dependent path, so unset it on both.
	input.Deployments[0].Spec.Template.Spec.AutomountServiceAccountToken = nil
	input.Deployments[1].Spec.Template.Spec.AutomountServiceAccountToken = nil
	results := RunChecks(input)
	byNS := results.EvaluatedByNamespace["automountServiceAccountToken"]
	if byNS["covered"] != 1 {
		t.Errorf("covered ns evaluated = %d, want 1", byNS["covered"])
	}
	if byNS["elsewhere"] != 0 {
		t.Errorf("elsewhere evaluated = %d, want 0 (SA inventory doesn't cover it)", byNS["elsewhere"])
	}
	for _, f := range results.Findings {
		if f.CheckID == "automountServiceAccountToken" && f.Namespace == "elsewhere" {
			t.Errorf("false automount finding outside SA inventory coverage")
		}
	}
}
