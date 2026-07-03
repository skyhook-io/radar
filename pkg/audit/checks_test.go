package audit

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ptr[T any](v T) *T { return &v }

func findingNames(findings []Finding, checkID string) map[string]bool {
	names := map[string]bool{}
	for _, f := range findings {
		if f.CheckID == checkID {
			names[f.Name] = true
		}
	}
	return names
}

func findingResourceKeys(findings []Finding, checkID string) map[string]bool {
	keys := map[string]bool{}
	for _, f := range findings {
		if f.CheckID == checkID {
			keys[f.Kind+"/"+f.Namespace+"/"+f.Name] = true
		}
	}
	return keys
}

func TestRunChecks_Empty(t *testing.T) {
	results := RunChecks(&CheckInput{})
	if len(results.Findings) != 0 {
		t.Errorf("expected no findings for empty input, got %d", len(results.Findings))
	}
}

func TestRunChecks_Nil(t *testing.T) {
	results := RunChecks(nil)
	if results == nil {
		t.Fatal("expected non-nil results for nil input")
	}
}

func TestSecurityChecks(t *testing.T) {
	input := &CheckInput{
		// non-nil = authoritative inventory (nil now means RBAC-denied and
		// skips the SA-dependent automount check)
		ServiceAccounts: []*corev1.ServiceAccount{},
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "insecure-app", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "insecure"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						HostNetwork: true,
						Containers: []corev1.Container{{
							Name:  "app",
							Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr(true),
							},
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	findingsByCheck := map[string]Finding{}
	for _, f := range results.Findings {
		findingsByCheck[f.CheckID] = f
	}

	// Should flag: hostNetwork, privileged, runAsRoot, privilegeEscalation, readOnlyRootFs, automountServiceAccountToken
	for _, expected := range []string{"hostNetwork", "privileged", "runAsRoot", "privilegeEscalation", "readOnlyRootFs", "automountServiceAccountToken"} {
		if _, ok := findingsByCheck[expected]; !ok {
			t.Errorf("expected finding for check %q, not found", expected)
		}
	}

	// Verify they're attributed to the Deployment, not a Pod
	for _, f := range results.Findings {
		if f.Kind != "Deployment" {
			t.Errorf("expected findings attributed to Deployment, got %q", f.Kind)
		}
	}
}

func TestSecurityChecks_Secure(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "secure-app", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "secure"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
							MaxSkew: 1, TopologyKey: "kubernetes.io/hostname",
							WhenUnsatisfiable: corev1.DoNotSchedule,
							LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "secure"}},
						}},
						Containers: []corev1.Container{{
							Name:  "app",
							Image: "nginx:1.25",
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
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt(8080)}}},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt(8080)}}},
						}},
					},
				},
			},
		}},
		PodDisruptionBudgets: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "secure-pdb", Namespace: "default"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "secure"}},
			},
		}},
	}

	results := RunChecks(input)

	// A well-configured deployment should have zero security/reliability/efficiency findings
	securityFindings := 0
	for _, f := range results.Findings {
		if f.Category == CategorySecurity || f.Category == CategoryReliability || f.Category == CategoryEfficiency {
			securityFindings++
			t.Errorf("unexpected finding: [%s] %s - %s", f.CheckID, f.Category, f.Message)
		}
	}
}

func TestSecurityChecks_RunAsNonRootInheritedFromPod(t *testing.T) {
	// Pod-level PodSecurityContext.RunAsNonRoot=true should satisfy the
	// runAsRoot check for containers that don't set it themselves.
	// Regression for https://github.com/skyhook-io/radar/issues/484
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-nonroot", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
	}
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "runAsRoot" {
			t.Errorf("runAsRoot flagged despite pod-level RunAsNonRoot=true: %s", f.Message)
		}
	}
}

func TestSecurityChecks_RunAsUserNonZeroSatisfiesNonRoot(t *testing.T) {
	// A non-zero runAsUser at the pod level also means the container
	// doesn't run as root, even without RunAsNonRoot being set.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-uid", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(1000))},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
	}
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "runAsRoot" {
			t.Errorf("runAsRoot flagged despite pod-level RunAsUser=1000: %s", f.Message)
		}
	}
}

func TestSecurityChecks_ContainerOverridesPod(t *testing.T) {
	// Container-level RunAsNonRoot=false must override pod-level true.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "override", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(false)},
						}},
					},
				},
			},
		}},
	}
	found := false
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "runAsRoot" {
			found = true
		}
	}
	if !found {
		t.Error("expected runAsRoot finding when container overrides pod with RunAsNonRoot=false")
	}
}

func TestSecurityChecks_AutomountFromServiceAccount(t *testing.T) {
	// Pod doesn't set AutomountServiceAccountToken; its ServiceAccount sets
	// it to false. No finding should be emitted.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "sa-noauto", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName: "restricted",
						SecurityContext:    &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
		ServiceAccounts: []*corev1.ServiceAccount{{
			ObjectMeta:                   metav1.ObjectMeta{Name: "restricted", Namespace: "team"},
			AutomountServiceAccountToken: ptr(false),
		}},
	}
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "automountServiceAccountToken" {
			t.Errorf("automount flagged despite SA setting false: %s", f.Message)
		}
	}
}

func TestSecurityChecks_PodOverridesServiceAccountAutomount(t *testing.T) {
	// SA says false, pod explicitly says true — pod wins, finding emitted.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "override-auto", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName:           "restricted",
						AutomountServiceAccountToken: ptr(true),
						SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
		ServiceAccounts: []*corev1.ServiceAccount{{
			ObjectMeta:                   metav1.ObjectMeta{Name: "restricted", Namespace: "team"},
			AutomountServiceAccountToken: ptr(false),
		}},
	}
	found := false
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "automountServiceAccountToken" {
			found = true
		}
	}
	if !found {
		t.Error("expected automount finding when pod explicitly sets it to true")
	}
}

func TestEfficiencyChecks_LimitRangeDefaults(t *testing.T) {
	// Namespace has a LimitRange with container defaults — the containers
	// below don't set requests/limits, but admission would fill them in, so
	// no efficiency findings should be emitted.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "no-explicit", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
					},
				},
			},
		}},
		LimitRanges: []*corev1.LimitRange{{
			ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "team"},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				}},
			},
		}},
	}
	for _, f := range RunChecks(input).Findings {
		switch f.CheckID {
		case "cpuRequestMissing", "memoryRequestMissing", "cpuLimitMissing", "memoryLimitMissing":
			t.Errorf("efficiency check flagged despite LimitRange defaults: %s", f.Message)
		}
	}
}

func TestEfficiencyChecks_LimitRangePodTypeDoesNotSuppress(t *testing.T) {
	// LimitRanges with Type=Pod apply to aggregate pod limits, not to
	// container defaults — container-level findings must still fire.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-limits", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
					},
				},
			},
		}},
		LimitRanges: []*corev1.LimitRange{{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-scope", Namespace: "team"},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypePod,
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				}},
			},
		}},
	}
	need := map[string]bool{"cpuRequestMissing": true, "memoryRequestMissing": true, "cpuLimitMissing": true, "memoryLimitMissing": true}
	for _, f := range RunChecks(input).Findings {
		delete(need, f.CheckID)
	}
	if len(need) > 0 {
		t.Errorf("LimitType=Pod should not suppress container findings; missing: %v", need)
	}
}

func TestEfficiencyChecks_LimitRangeMaxDoesNotSuppress(t *testing.T) {
	// LimitRange items with Max/Min but no Default/DefaultRequest enforce
	// constraints — they do not inject values, so missing-request/limit
	// findings must still fire.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "max-only", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
					},
				},
			},
		}},
		LimitRanges: []*corev1.LimitRange{{
			ObjectMeta: metav1.ObjectMeta{Name: "max-only", Namespace: "team"},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				}},
			},
		}},
	}
	need := map[string]bool{"cpuRequestMissing": true, "memoryRequestMissing": true, "cpuLimitMissing": true, "memoryLimitMissing": true}
	for _, f := range RunChecks(input).Findings {
		delete(need, f.CheckID)
	}
	if len(need) > 0 {
		t.Errorf("LimitRange.Max-only should not suppress missing-resource findings; missing: %v", need)
	}
}

func TestEfficiencyChecks_LimitRangePartialDefaults(t *testing.T) {
	// LimitRange sets only DefaultRequest.cpu — only cpuRequestMissing should
	// be suppressed; the other three findings must still fire.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "partial", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
					},
				},
			},
		}},
		LimitRanges: []*corev1.LimitRange{{
			ObjectMeta: metav1.ObjectMeta{Name: "cpu-req-only", Namespace: "team"},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("100m"),
					},
				}},
			},
		}},
	}
	flagged := map[string]bool{}
	for _, f := range RunChecks(input).Findings {
		flagged[f.CheckID] = true
	}
	if flagged["cpuRequestMissing"] {
		t.Error("cpuRequestMissing should be suppressed by LimitRange DefaultRequest.cpu")
	}
	for _, id := range []string{"memoryRequestMissing", "cpuLimitMissing", "memoryLimitMissing"} {
		if !flagged[id] {
			t.Errorf("%s should still fire — LimitRange covered only cpu request", id)
		}
	}
}

func TestSecurityChecks_AutomountDefaultServiceAccount(t *testing.T) {
	// Pod doesn't set ServiceAccountName — implicit "default" SA applies.
	// If the default SA has automount=false, no finding should fire.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "implicit-default", Namespace: "team"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
						Containers: []corev1.Container{{
							Name: "app", Image: "nginx:1.25",
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
		ServiceAccounts: []*corev1.ServiceAccount{{
			ObjectMeta:                   metav1.ObjectMeta{Name: "default", Namespace: "team"},
			AutomountServiceAccountToken: ptr(false),
		}},
	}
	for _, f := range RunChecks(input).Findings {
		if f.CheckID == "automountServiceAccountToken" {
			t.Errorf("automount flagged despite implicit default SA with automount=false: %s", f.Message)
		}
	}
}

func TestReliabilityChecks(t *testing.T) {
	input := &CheckInput{
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "single-replica", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "single"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{{
							Name:  "app",
							Image: "myapp:latest",
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             ptr(true),
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	checks := map[string]bool{}
	for _, f := range results.Findings {
		checks[f.CheckID] = true
	}

	if !checks["singleReplica"] {
		t.Error("expected singleReplica finding")
	}
	if !checks["imageTagLatest"] {
		t.Error("expected imageTagLatest finding")
	}
	if !checks["pullPolicyNotAlways"] {
		t.Error("expected pullPolicyNotAlways finding")
	}
}

func TestSingleReplica_SkippedWithHPA(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "autoscaled", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "auto"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{{
			ObjectMeta: metav1.ObjectMeta{Name: "autoscaled-hpa", Namespace: "default"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
					Kind: "Deployment", Name: "autoscaled",
				},
			},
		}},
	}

	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "singleReplica" {
			t.Error("singleReplica should not fire when HPA targets the deployment")
		}
	}
}

func TestEfficiencyChecks(t *testing.T) {
	input := &CheckInput{
		LimitRanges: []*corev1.LimitRange{},
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "no-resources", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nores"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{{
							Name:  "app",
							Image: "app:v1",
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             ptr(true),
								ReadOnlyRootFilesystem:   ptr(true),
								AllowPrivilegeEscalation: ptr(false),
							},
							// No resources set
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	checks := map[string]bool{}
	for _, f := range results.Findings {
		checks[f.CheckID] = true
	}

	for _, expected := range []string{"cpuRequestMissing", "memoryRequestMissing", "cpuLimitMissing", "memoryLimitMissing"} {
		if !checks[expected] {
			t.Errorf("expected finding for check %q", expected)
		}
	}
}

func TestServiceNoMatchingPods(t *testing.T) {
	input := &CheckInput{
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan-svc", Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "nonexistent"},
			},
		}},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: "default", Labels: map[string]string{"app": "other"}},
		}},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "serviceNoMatchingPods" {
			found = true
		}
	}
	if !found {
		t.Error("expected serviceNoMatchingPods finding")
	}
}

func TestIngressNoMatchingService(t *testing.T) {
	input := &CheckInput{
		Ingresses: []*networkingv1.Ingress{{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-ingress", Namespace: "default"},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{
					Host: "example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{{
								Path: "/",
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: "missing-service",
									},
								},
							}},
						},
					},
				}},
			},
		}},
		Services: []*corev1.Service{}, // no services
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "ingressNoMatchingService" {
			found = true
		}
	}
	if !found {
		t.Error("expected ingressNoMatchingService finding")
	}
}

func TestBarePodChecked(t *testing.T) {
	input := &CheckInput{
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "bare-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "app",
					Image: "nginx",
				}},
			},
			// No OwnerReferences — bare pod
		}},
	}

	results := RunChecks(input)
	if len(results.Findings) == 0 {
		t.Error("expected findings for bare pod with no security context or probes")
	}
	for _, f := range results.Findings {
		if f.Kind != "Pod" {
			t.Errorf("bare pod findings should have Kind=Pod, got %q", f.Kind)
		}
	}
}

func TestOwnedPodNotChecked(t *testing.T) {
	input := &CheckInput{
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "owned-pod", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "my-rs"}},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			},
		}},
	}

	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.Kind == "Pod" {
			t.Error("owned pods should not produce findings (workload checks cover them)")
		}
	}
}

func TestImageTag(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"nginx:1.25", "1.25"},
		{"nginx:latest", "latest"},
		{"nginx", ""},
		{"gcr.io/project/image:v2", "v2"},
		{"image@sha256:abc123", "sha256:abc123"},
	}
	for _, tt := range tests {
		got := imageTag(tt.image)
		if got != tt.want {
			t.Errorf("imageTag(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}

func TestDangerousCapabilities(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "cap-app", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cap"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "app",
							Image: "app:v1",
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"SYS_ADMIN", "NET_BIND_SERVICE"},
								},
							},
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "dangerousCapabilities" {
			found = true
			if f.Severity != SeverityDanger {
				t.Errorf("dangerousCapabilities should be danger severity, got %q", f.Severity)
			}
		}
	}
	if !found {
		t.Error("expected dangerousCapabilities finding for SYS_ADMIN")
	}
}

func TestMissingPDB(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "multi-replica", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "multi"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		PodDisruptionBudgets: []*policyv1.PodDisruptionBudget{}, // empty = listed but none exist
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "missingPDB" {
			found = true
		}
	}
	if !found {
		t.Error("expected missingPDB finding for multi-replica deployment without PDB")
	}
}

func TestMissingPDB_CoveredByPDB(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "covered", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "covered"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		PodDisruptionBudgets: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pdb", Namespace: "default"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{IntVal: 2},
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "covered"}},
			},
		}},
	}

	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "missingPDB" {
			t.Error("missingPDB should not fire when PDB covers the deployment")
		}
	}
}

func TestMissingPDB_CrossNamespaceNotCovered(t *testing.T) {
	// PDB in namespace "monitoring" should NOT suppress findings for
	// a Deployment in namespace "production" even if labels match.
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "prod-app", Namespace: "production"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		PodDisruptionBudgets: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "wrong-ns-pdb", Namespace: "monitoring"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{IntVal: 2},
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			},
		}},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "missingPDB" && f.Namespace == "production" {
			found = true
		}
	}
	if !found {
		t.Error("expected missingPDB finding — PDB in different namespace should not cover the deployment")
	}
}

func TestGroupByResource_SortingAndCounts(t *testing.T) {
	findings := []Finding{
		{Kind: "Deployment", Namespace: "default", Name: "app-a", CheckID: "cpuLimitMissing", Category: CategoryEfficiency, Severity: SeverityWarning, Message: "no cpu limit"},
		{Kind: "Deployment", Namespace: "default", Name: "app-b", CheckID: "runAsRoot", Category: CategorySecurity, Severity: SeverityDanger, Message: "runs as root"},
		{Kind: "Deployment", Namespace: "default", Name: "app-b", CheckID: "cpuLimitMissing", Category: CategoryEfficiency, Severity: SeverityWarning, Message: "no cpu limit"},
		{Kind: "Deployment", Namespace: "default", Name: "app-c", CheckID: "cpuLimitMissing", Category: CategoryEfficiency, Severity: SeverityWarning, Message: "no cpu limit"},
		{Kind: "Deployment", Namespace: "default", Name: "app-c", CheckID: "memoryLimitMissing", Category: CategoryEfficiency, Severity: SeverityWarning, Message: "no mem limit"},
	}

	groups := GroupByResource(findings)

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// app-b has 1 danger → should be first
	if groups[0].Name != "app-b" {
		t.Errorf("expected first group to be app-b (has danger), got %s", groups[0].Name)
	}
	if groups[0].Danger != 1 || groups[0].Warning != 1 {
		t.Errorf("app-b: expected 1 danger + 1 warning, got %d danger + %d warning", groups[0].Danger, groups[0].Warning)
	}

	// app-c has 2 warnings → should be before app-a (1 warning)
	if groups[1].Name != "app-c" {
		t.Errorf("expected second group to be app-c (2 warnings), got %s", groups[1].Name)
	}
	if groups[1].Warning != 2 {
		t.Errorf("app-c: expected 2 warnings, got %d", groups[1].Warning)
	}

	// app-a has 1 warning → last
	if groups[2].Name != "app-a" {
		t.Errorf("expected third group to be app-a (1 warning), got %s", groups[2].Name)
	}
}

func TestGroupByResource_Empty(t *testing.T) {
	groups := GroupByResource(nil)
	if len(groups) != 0 {
		t.Errorf("expected 0 groups for nil input, got %d", len(groups))
	}
}

func TestBuildResults_MergesMultiContainerFindings(t *testing.T) {
	// Two containers in the same deployment both lack probes
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "multi"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{
							{
								Name: "app", Image: "app:v1",
								SecurityContext: &corev1.SecurityContext{
									RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false),
								},
							},
							{
								Name: "sidecar", Image: "sidecar:v1",
								SecurityContext: &corev1.SecurityContext{
									RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false),
								},
							},
						},
					},
				},
			},
		}},
	}

	results := RunChecks(input)

	// Both containers lack probes — should be merged into one finding per checkID
	probeFindings := 0
	for _, f := range results.Findings {
		if f.CheckID == "readinessProbeMissing" {
			probeFindings++
			// Merged message should mention both containers
			if !contains(f.Message, "app") || !contains(f.Message, "sidecar") {
				t.Errorf("merged readinessProbeMissing should mention both containers, got: %s", f.Message)
			}
		}
	}
	if probeFindings != 1 {
		t.Errorf("expected 1 merged readinessProbeMissing finding, got %d", probeFindings)
	}
}

func TestRegistryCompleteness(t *testing.T) {
	// Create a maximally-insecure input that triggers every check
	input := &CheckInput{
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "default"},
			Spec: corev1.PodSpec{
				HostNetwork: true, HostPID: true, HostIPC: true,
				Containers: []corev1.Container{{
					Name: "c", Image: "nginx",
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{"SYS_ADMIN"},
						},
					},
				}},
			},
		}},
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "d"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "nginx:latest"}},
					},
				},
			},
		}},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "nope"}},
		}},
		Ingresses: []*networkingv1.Ingress{{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-ing", Namespace: "default"},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{{
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{Name: "missing"},
								},
							}},
						},
					},
				}},
			},
		}},
	}

	results := RunChecks(input)

	// Every checkID that fired must have a registry entry
	seen := make(map[string]bool)
	for _, f := range results.Findings {
		seen[f.CheckID] = true
	}
	for checkID := range seen {
		if _, ok := CheckRegistry[checkID]; !ok {
			t.Errorf("checkID %q has no entry in CheckRegistry", checkID)
		}
	}

	// Verify the Checks map in results is populated
	for checkID := range seen {
		if _, ok := results.Checks[checkID]; !ok {
			t.Errorf("checkID %q missing from results.Checks map", checkID)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ============================================================================
// New check tests
// ============================================================================

func TestInsecureCapabilities(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "cap-test", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cap"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{{
							Name: "app", Image: "app:v1",
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false),
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"NET_RAW", "SYS_PTRACE", "NET_BIND_SERVICE"},
								},
							},
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	checks := map[string]bool{}
	for _, f := range results.Findings {
		checks[f.CheckID] = true
	}

	if !checks["insecureCapabilities"] {
		t.Error("expected insecureCapabilities finding for NET_RAW/SYS_PTRACE")
	}
	// NET_BIND_SERVICE should NOT be flagged
	for _, f := range results.Findings {
		if f.CheckID == "insecureCapabilities" && containsStr(f.Message, "NET_BIND_SERVICE") {
			t.Error("NET_BIND_SERVICE should not be flagged as insecure")
		}
	}
	// dangerousCapabilities should NOT fire (no SYS_ADMIN/NET_ADMIN/ALL)
	if checks["dangerousCapabilities"] {
		t.Error("dangerousCapabilities should not fire for NET_RAW/SYS_PTRACE")
	}
}

func TestMissingTopologySpread(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "no-spread", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ns"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "missingTopologySpread" {
			found = true
		}
	}
	if !found {
		t.Error("expected missingTopologySpread for 3-replica deployment without constraints")
	}
}

func TestMissingTopologySpread_SingleReplica(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "single", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "s"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
	}

	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "missingTopologySpread" {
			t.Error("missingTopologySpread should not fire for single-replica deployment")
		}
	}
}

func TestPodHARisk(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		Pods: []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "default", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "node-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web-2", Namespace: "default", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "node-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web-3", Namespace: "default", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "node-1"}},
		},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "podHARisk" {
			found = true
		}
	}
	if !found {
		t.Error("expected podHARisk when all 3 pods are on the same node")
	}
}

func TestPodHARisk_Distributed(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
				},
			},
		}},
		Pods: []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "default", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "node-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web-2", Namespace: "default", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "node-2"}},
		},
	}

	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "podHARisk" {
			t.Error("podHARisk should not fire when pods are on different nodes")
		}
	}
}

func TestOrphanConfigMapSecret(t *testing.T) {
	input := &CheckInput{
		Ingresses: []*networkingv1.Ingress{},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "app", Image: "app:v1",
					Env: []corev1.EnvVar{{
						Name: "DB_URL",
						ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
							},
						},
					}},
				}},
			},
		}},
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "orphan-config", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: "default"}}, // system — should be skipped
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "orphan-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "sa-token", Namespace: "default"}, Type: corev1.SecretTypeServiceAccountToken}, // should be skipped
		},
	}

	results := RunChecks(input)
	orphans := map[string]bool{}
	messages := map[string]string{}
	for _, f := range results.Findings {
		if f.CheckID == "orphanConfigMapSecret" {
			orphans[f.Name] = true
			messages[f.Name] = f.Message
		}
	}

	if !orphans["orphan-config"] {
		t.Error("expected orphan finding for orphan-config")
	}
	if !orphans["orphan-secret"] {
		t.Error("expected orphan finding for orphan-secret")
	}
	if orphans["app-config"] {
		t.Error("app-config is referenced, should not be flagged as orphan")
	}
	if orphans["kube-root-ca.crt"] {
		t.Error("kube-root-ca.crt should be skipped")
	}
	if orphans["sa-token"] {
		t.Error("service account token secrets should be skipped")
	}
	if strings.Contains(messages["orphan-config"], "any pod") || strings.Contains(messages["orphan-secret"], "any pod") {
		t.Errorf("orphan messages should mention broadened references, got %q / %q", messages["orphan-config"], messages["orphan-secret"])
	}
}

func TestOrphanConfigMapSecretSkipsKnownPlatformArtifacts(t *testing.T) {
	input := &CheckInput{
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "aws-auth", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "amazon-vpc-cni", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "extension-apiserver-authentication", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-legacy-service-account-token-tracking", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster-autoscaler-status", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster-kubestore", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "clustermetrics", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "gke-common-webhook-heartbeat", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "gke-common-webhook-lock", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "ingress-uid", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "konnectivity-agent-autoscaler-config", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "kube-dns-autoscaler", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "kubedns-config-images", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "pdcsi-metrics-collector-config-map", Namespace: "kube-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "config-images", Namespace: "gmp-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "scheduled-jobs", Namespace: "gmp-system"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster-autoscaler-status", Namespace: "cluster-autoscaler"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-cmd-params-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "argocd-dex-cm", Namespace: "argocd"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-gpg-keys-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-notifications-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-rbac-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-ssh-known-hosts-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-tls-certs-cm",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "ingress-nginx-controller",
				Namespace: "ingress-nginx",
				Labels:    map[string]string{"app.kubernetes.io/name": "ingress-nginx"},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "argo-rollouts-config", Namespace: "argo-rollouts"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "argo-rollouts-notification-configmap", Namespace: "argo-rollouts"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "kyverno",
				Namespace: "kyverno",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "kyverno"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "kyverno-metrics",
				Namespace: "kyverno",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "kyverno"},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "aws-auth", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "argocd-rbac-cm", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx-controller", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "ordinary-helm-config",
				Namespace: "default",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-prometheus-stack-alertmanager-overview",
				Namespace: "monitoring",
				Labels:    map[string]string{"grafana_dashboard": "1"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-prometheus-stack-grafana-datasource",
				Namespace: "monitoring",
				Labels:    map[string]string{"grafana_datasource": "true"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-prometheus-stack-rules",
				Namespace: "monitoring",
				Labels:    map[string]string{"prometheus_rule": "yes"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "fluentd-extra-config",
				Namespace: "logging",
				Labels:    map[string]string{"fluentd_config": "enabled"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "k8sgpt-dynamic-config",
				Namespace: "k8sgpt",
				Labels:    map[string]string{"k8sgpt.ai/dynamically-loaded": "true"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:        "datadog-operator-lock",
				Namespace:   "datadog",
				Annotations: map[string]string{"control-plane.alpha.kubernetes.io/leader": `{"holderIdentity":"datadog-operator"}`},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argo-workflows-workflow-controller-configmap",
				Namespace: "argo-workflows",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argo-workflows"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "cnpg-controller-manager-config",
				Namespace: "cloud-native-pg",
				Labels:    map[string]string{"app.kubernetes.io/name": "cloudnative-pg"},
			}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "sealed-secrets-keyabc",
				Namespace: "sealed-secrets",
				Labels:    map[string]string{"sealedsecrets.bitnami.com/sealed-secrets-key": "active"},
			}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "repo-creds",
				Namespace: "argocd",
				Labels:    map[string]string{"argocd.argoproj.io/secret-type": "repo-creds"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-secret",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-notifications-secret",
				Namespace: "argocd",
				Labels:    map[string]string{"app.kubernetes.io/part-of": "argocd"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "cert-manager-webhook-ca",
				Namespace: "cert-manager",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "cert-manager-webhook"},
			}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{
				Name:        "cert-manager-other-webhook-ca",
				Namespace:   "cert-manager",
				Annotations: map[string]string{"cert-manager.io/allow-direct-injection": "true"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "letsencrypt-account-key",
				Namespace: "cert-manager",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "cert-manager"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "alertmanager", Namespace: "gmp-public"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "webhook-tls",
				Namespace: "gmp-system",
				Labels: map[string]string{
					"addonmanager.kubernetes.io/mode":  "Reconcile",
					"components.gke.io/component-name": "managed-prometheus",
				},
			}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{Name: "crossplane-root-ca", Namespace: "crossplane-system"}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-webhook-ca", Namespace: "default"}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "letsencrypt-account-key",
				Namespace: "default",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "ordinary-helm-secret",
				Namespace: "default",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingResourceKeys(RunChecks(input).Findings, "orphanConfigMapSecret")
	for _, key := range []string{
		"ConfigMap/kube-system/aws-auth",
		"ConfigMap/kube-system/amazon-vpc-cni",
		"ConfigMap/kube-system/extension-apiserver-authentication",
		"ConfigMap/kube-system/kube-apiserver-legacy-service-account-token-tracking",
		"ConfigMap/kube-system/cluster-autoscaler-status",
		"ConfigMap/kube-system/cluster-kubestore",
		"ConfigMap/kube-system/clustermetrics",
		"ConfigMap/kube-system/gke-common-webhook-heartbeat",
		"ConfigMap/kube-system/gke-common-webhook-lock",
		"ConfigMap/kube-system/ingress-uid",
		"ConfigMap/kube-system/konnectivity-agent-autoscaler-config",
		"ConfigMap/kube-system/kube-dns-autoscaler",
		"ConfigMap/kube-system/kubedns-config-images",
		"ConfigMap/kube-system/pdcsi-metrics-collector-config-map",
		"ConfigMap/gmp-system/config-images",
		"ConfigMap/gmp-system/scheduled-jobs",
		"ConfigMap/cluster-autoscaler/cluster-autoscaler-status",
		"ConfigMap/argocd/argocd-cm",
		"ConfigMap/argocd/argocd-cmd-params-cm",
		"ConfigMap/argocd/argocd-dex-cm",
		"ConfigMap/argocd/argocd-gpg-keys-cm",
		"ConfigMap/argocd/argocd-notifications-cm",
		"ConfigMap/argocd/argocd-rbac-cm",
		"ConfigMap/argocd/argocd-ssh-known-hosts-cm",
		"ConfigMap/argocd/argocd-tls-certs-cm",
		"ConfigMap/ingress-nginx/ingress-nginx-controller",
		"ConfigMap/argo-rollouts/argo-rollouts-config",
		"ConfigMap/argo-rollouts/argo-rollouts-notification-configmap",
		"ConfigMap/kyverno/kyverno",
		"ConfigMap/kyverno/kyverno-metrics",
		"ConfigMap/monitoring/kube-prometheus-stack-alertmanager-overview",
		"ConfigMap/monitoring/kube-prometheus-stack-grafana-datasource",
		"ConfigMap/monitoring/kube-prometheus-stack-rules",
		"ConfigMap/logging/fluentd-extra-config",
		"ConfigMap/k8sgpt/k8sgpt-dynamic-config",
		"ConfigMap/datadog/datadog-operator-lock",
		"ConfigMap/argo-workflows/argo-workflows-workflow-controller-configmap",
		"ConfigMap/cloud-native-pg/cnpg-controller-manager-config",
		"Secret/sealed-secrets/sealed-secrets-keyabc",
		"Secret/argocd/repo-creds",
		"Secret/argocd/argocd-secret",
		"Secret/argocd/argocd-notifications-secret",
		"Secret/cert-manager/cert-manager-webhook-ca",
		"Secret/cert-manager/cert-manager-other-webhook-ca",
		"Secret/cert-manager/letsencrypt-account-key",
		"Secret/gmp-public/alertmanager",
		"Secret/gmp-system/webhook-tls",
		"Secret/crossplane-system/crossplane-root-ca",
	} {
		if orphans[key] {
			t.Errorf("%s should not be flagged as orphan", key)
		}
	}
	for _, key := range []string{
		"ConfigMap/default/aws-auth",
		"ConfigMap/default/argocd-rbac-cm",
		"ConfigMap/default/ingress-nginx-controller",
		"ConfigMap/default/ordinary-helm-config",
		"Secret/default/cert-manager-webhook-ca",
		"Secret/default/letsencrypt-account-key",
		"Secret/default/ordinary-helm-secret",
	} {
		if !orphans[key] {
			t.Errorf("%s should still be flagged as orphan", key)
		}
	}
}

func TestOrphanConfigMapSecretKnownPlatformArtifactNegativeCases(t *testing.T) {
	input := &CheckInput{
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "argocd-rbac-cm", Namespace: "custom-argocd"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "kyverno-metrics", Namespace: "kyverno"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "ingress-nginx-controller",
				Namespace: "ingress-nginx",
				Labels:    map[string]string{"app.kubernetes.io/name": "not-ingress-nginx"},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "koala-grafana-dashboards", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "disabled-grafana-dashboard",
				Namespace: "default",
				Labels:    map[string]string{"grafana_dashboard": "false"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "disabled-k8sgpt-dynamic-config",
				Namespace: "k8sgpt",
				Labels:    map[string]string{"k8sgpt.ai/dynamically-loaded": "false"},
			}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:        "empty-leader-lock",
				Namespace:   "default",
				Annotations: map[string]string{"control-plane.alpha.kubernetes.io/leader": ""},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "argo-workflows-workflow-controller-configmap", Namespace: "argo-workflows"}},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "cnpg-controller-manager-config",
				Namespace: "cloud-native-pg",
				Labels:    map[string]string{"app.kubernetes.io/name": "not-cloudnative-pg"},
			}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "argocd-secret", Namespace: "argocd"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "cert-manager-webhook-ca",
				Namespace: "cert-manager",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "letsencrypt-account-key",
				Namespace: "cert-manager",
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "webhook-tls", Namespace: "gmp-system"}, Type: corev1.SecretTypeTLS},
			{ObjectMeta: metav1.ObjectMeta{
				Name:      "webhook-tls",
				Namespace: "default",
				Labels: map[string]string{
					"addonmanager.kubernetes.io/mode":  "Reconcile",
					"components.gke.io/component-name": "managed-prometheus",
				},
			}, Type: corev1.SecretTypeTLS},
		},
	}

	orphans := findingResourceKeys(RunChecks(input).Findings, "orphanConfigMapSecret")
	for _, key := range []string{
		"ConfigMap/custom-argocd/argocd-rbac-cm",
		"ConfigMap/kyverno/kyverno-metrics",
		"ConfigMap/ingress-nginx/ingress-nginx-controller",
		"ConfigMap/default/koala-grafana-dashboards",
		"ConfigMap/default/disabled-grafana-dashboard",
		"ConfigMap/k8sgpt/disabled-k8sgpt-dynamic-config",
		"ConfigMap/default/empty-leader-lock",
		"ConfigMap/argo-workflows/argo-workflows-workflow-controller-configmap",
		"ConfigMap/cloud-native-pg/cnpg-controller-manager-config",
		"Secret/argocd/argocd-secret",
		"Secret/cert-manager/cert-manager-webhook-ca",
		"Secret/cert-manager/letsencrypt-account-key",
		"Secret/gmp-system/webhook-tls",
		"Secret/default/webhook-tls",
	} {
		if !orphans[key] {
			t.Errorf("%s should still be flagged as orphan", key)
		}
	}
}

func TestOrphanConfigMapSecretPrecision(t *testing.T) {
	controller := true
	ownerRefs := []metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "Widget", Name: "owner", Controller: &controller}}
	replicas := int32(0)

	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "app:v1",
						EnvFrom: []corev1.EnvFromSource{{
							ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "deploy-env"}},
						}, {
							SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "deploy-secret"}},
						}},
					}},
				}},
			},
		}},
		StatefulSets: []*appsv1.StatefulSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "stateful", Namespace: "default"},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "app:v1"}},
				Volumes: []corev1.Volume{{
					Name:         "config",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "stateful-volume"}}},
				}},
			}}},
		}},
		DaemonSets: []*appsv1.DaemonSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "agent", Image: "agent:v1"}},
				Volumes: []corev1.Volume{{
					Name: "projected",
					VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
						ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "daemon-projected"}},
					}, {
						Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "daemon-projected-secret"}},
					}}}},
				}},
			}}},
		}},
		Jobs: []*batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "default"},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "job",
					Image: "job:v1",
					Env: []corev1.EnvVar{{
						Name: "JOB_CFG",
						ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "job-config"},
						}},
					}},
				}},
			}}},
		}},
		CronJobs: []*batchv1.CronJob{{
			ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "default"},
			Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "cron", Image: "cron:v1"}},
				Volumes: []corev1.Volume{{
					Name: "csi",
					VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
						Driver:               "secrets-store.csi.k8s.io",
						NodePublishSecretRef: &corev1.LocalObjectReference{Name: "csi-secret"},
					}},
				}},
			}}}}},
		}},
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "deploy-env", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "stateful-volume", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "daemon-projected", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "job-config", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "owned-config", Namespace: "default", OwnerReferences: ownerRefs}},
			{ObjectMeta: metav1.ObjectMeta{Name: "actual-orphan-config", Namespace: "default"}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "deploy-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "daemon-projected-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "csi-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "owned-secret", Namespace: "default", OwnerReferences: ownerRefs}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "actual-orphan-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingNames(RunChecks(input).Findings, "orphanConfigMapSecret")
	if !orphans["actual-orphan-config"] || !orphans["actual-orphan-secret"] {
		t.Fatalf("expected only explicit orphans, got %+v", orphans)
	}
	for _, name := range []string{
		"deploy-env", "deploy-secret", "stateful-volume", "daemon-projected",
		"daemon-projected-secret", "job-config", "csi-secret", "owned-config", "owned-secret",
	} {
		if orphans[name] {
			t.Errorf("%s should not be flagged as orphan", name)
		}
	}
}

func TestOrphanConfigMapSecretTerminalJobsDoNotSuppressFindings(t *testing.T) {
	input := &CheckInput{
		Jobs: []*batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "finished", Namespace: "default"},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "job",
					Image: "job:v1",
					Env: []corev1.EnvVar{{
						Name: "JOB_CFG",
						ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "finished-job-config"},
						}},
					}, {
						Name: "JOB_SECRET",
						ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "finished-job-secret"},
						}},
					}},
				}},
			}}},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}}},
		}},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "finished-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "job",
					Image: "job:v1",
					Env: []corev1.EnvVar{{
						Name: "JOB_CFG",
						ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "finished-job-config"},
						}},
					}, {
						Name: "JOB_SECRET",
						ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "finished-job-secret"},
						}},
					}},
				}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
		}},
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "finished-job-config", Namespace: "default"}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "finished-job-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingNames(RunChecks(input).Findings, "orphanConfigMapSecret")
	if !orphans["finished-job-config"] || !orphans["finished-job-secret"] {
		t.Fatalf("terminal Job references should not suppress orphan findings, got %+v", orphans)
	}
}

func TestOrphanConfigMapSecretServiceAccountImagePullSecrets(t *testing.T) {
	input := &CheckInput{
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "implicit-default", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "app:v1"}},
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{Name: "explicit-builder", Namespace: "default"},
			Spec: corev1.PodSpec{
				ServiceAccountName: "builder",
				Containers:         []corev1.Container{{Name: "app", Image: "app:v1"}},
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{Name: "direct-override", Namespace: "default"},
			Spec: corev1.PodSpec{
				ServiceAccountName: "override",
				ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "direct-pull-secret"}},
				Containers:         []corev1.Container{{Name: "app", Image: "app:v1"}},
			},
		}},
		ServiceAccounts: []*corev1.ServiceAccount{{
			ObjectMeta:       metav1.ObjectMeta{Name: "default", Namespace: "default"},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "default-pull-secret"}},
		}, {
			ObjectMeta:       metav1.ObjectMeta{Name: "builder", Namespace: "default"},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "builder-pull-secret"}},
		}, {
			ObjectMeta:       metav1.ObjectMeta{Name: "override", Namespace: "default"},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "overridden-sa-pull-secret"}},
		}},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "default-pull-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "builder-pull-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "direct-pull-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "overridden-sa-pull-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingNames(RunChecks(input).Findings, "orphanConfigMapSecret")
	for _, name := range []string{"default-pull-secret", "builder-pull-secret", "direct-pull-secret"} {
		if orphans[name] {
			t.Errorf("%s should be counted as used", name)
		}
	}
	if !orphans["overridden-sa-pull-secret"] {
		t.Fatalf("SA imagePullSecret should not count as used when the pod specifies direct imagePullSecrets, got %+v", orphans)
	}
}

func TestOrphanConfigMapSecretEphemeralContainerRefs(t *testing.T) {
	input := &CheckInput{
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "debugged", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "app:v1"}},
				EphemeralContainers: []corev1.EphemeralContainer{{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{
						Name:  "debugger",
						Image: "debug:v1",
						Env: []corev1.EnvVar{{
							Name: "DEBUG_CONFIG",
							ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "debug-config"},
							}},
						}},
						EnvFrom: []corev1.EnvFromSource{{
							SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "debug-secret"}},
						}},
					},
				}},
			},
		}},
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "debug-config", Namespace: "default"}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "debug-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingNames(RunChecks(input).Findings, "orphanConfigMapSecret")
	if orphans["debug-config"] || orphans["debug-secret"] {
		t.Fatalf("ephemeral container refs should be counted as used, got %+v", orphans)
	}
}

func TestOrphanConfigMapSecretAdditionalRefs(t *testing.T) {
	input := &CheckInput{
		ConfigObjectRefs: []ConfigObjectRef{
			{Kind: "ConfigMap", Namespace: "default", Name: "crd-config"},
			{Kind: "Secret", Namespace: "default", Name: "crd-secret"},
			{Kind: "Service", Namespace: "default", Name: "ignored"},
			{Kind: "Secret", Namespace: "", Name: "ignored-no-namespace"},
		},
		ConfigMaps: []*corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "crd-config", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "actual-orphan-config", Namespace: "default"}},
		},
		Secrets: []*corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: "crd-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
			{ObjectMeta: metav1.ObjectMeta{Name: "actual-orphan-secret", Namespace: "default"}, Type: corev1.SecretTypeOpaque},
		},
	}

	orphans := findingNames(RunChecks(input).Findings, "orphanConfigMapSecret")
	if orphans["crd-config"] || orphans["crd-secret"] {
		t.Fatalf("additional refs should suppress orphan findings, got %+v", orphans)
	}
	if !orphans["actual-orphan-config"] || !orphans["actual-orphan-secret"] {
		t.Fatalf("unreferenced resources should still be flagged, got %+v", orphans)
	}
}

func TestDeprecatedAPIVersion(t *testing.T) {
	input := &CheckInput{
		ClusterVersion: "1.30",
		ServedAPIs: []string{
			"apps/v1",              // stable — should not flag
			"batch/v1beta1",        // deprecated, removed in 1.25 — should flag
			"policy/v1beta1",       // deprecated, removed in 1.25 — should flag
			"networking.k8s.io/v1", // stable — should not flag
		},
	}

	results := RunChecks(input)
	deprecated := 0
	for _, f := range results.Findings {
		if f.CheckID == "deprecatedAPIVersion" {
			deprecated++
		}
	}
	// Findings merge per served group/version now (evaluated unit == failure
	// unit): batch/v1beta1 + policy/v1beta1 = 2 merged findings; policy's
	// message carries both PDB and PSP deprecations.
	if deprecated != 2 {
		t.Errorf("expected 2 merged deprecatedAPIVersion findings, got %d", deprecated)
	}
	for _, f := range results.Findings {
		if f.CheckID == "deprecatedAPIVersion" && f.Name == "policy/v1beta1" {
			if !strings.Contains(f.Message, "PodDisruptionBudget") || !strings.Contains(f.Message, "PodSecurityPolicy") {
				t.Errorf("policy/v1beta1 merged message missing a kind: %q", f.Message)
			}
		}
	}
}

func TestDeprecatedAPIVersion_NoServedAPIs(t *testing.T) {
	input := &CheckInput{
		ClusterVersion: "1.30",
		// No ServedAPIs — check should be skipped
	}
	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "deprecatedAPIVersion" {
			t.Error("deprecatedAPIVersion should not fire when ServedAPIs is empty")
		}
	}
}

func TestResourceUtilization(t *testing.T) {
	input := &CheckInput{
		PodMetrics: []PodMetricsInput{
			{Namespace: "default", Name: "waste-pod", CPUUsage: 5, CPURequest: 1000, MemoryUsage: 10 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024},     // 0.5% CPU, 2% memory — waste
			{Namespace: "default", Name: "hot-pod", CPUUsage: 950, CPURequest: 1000, MemoryUsage: 480 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024},    // 95% CPU, 94% memory — risk
			{Namespace: "default", Name: "normal-pod", CPUUsage: 500, CPURequest: 1000, MemoryUsage: 256 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024}, // 50% — fine
			{Namespace: "default", Name: "no-request-pod", CPUUsage: 100, CPURequest: 0, MemoryUsage: 128 * 1024 * 1024, MemoryRequest: 0},                // no requests — skip
		},
	}

	results := RunChecks(input)
	pods := map[string]int{} // pod name → finding count
	for _, f := range results.Findings {
		if f.CheckID == "resourceUtilization" {
			pods[f.Name]++
		}
	}

	if pods["waste-pod"] < 1 {
		t.Error("expected utilization finding for waste-pod (under-utilized)")
	}
	if pods["hot-pod"] < 1 {
		t.Error("expected utilization finding for hot-pod (over-utilized)")
	}
	if pods["normal-pod"] > 0 {
		t.Error("normal-pod at 50% utilization should not be flagged")
	}
	if pods["no-request-pod"] > 0 {
		t.Error("pod with no requests should not be flagged")
	}
}

func TestResourceUtilization_Empty(t *testing.T) {
	input := &CheckInput{}
	results := RunChecks(input)
	for _, f := range results.Findings {
		if f.CheckID == "resourceUtilization" {
			t.Error("resourceUtilization should not fire when no metrics provided")
		}
	}
}

func TestDockerSocketMount(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "ci-runner", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ci"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{{
							Name: "runner", Image: "runner:v1",
							SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false)},
						}},
						Volumes: []corev1.Volume{{
							Name:         "docker-sock",
							VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/run/docker.sock"}},
						}},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	found := false
	for _, f := range results.Findings {
		if f.CheckID == "dockerSocketMount" {
			found = true
			if f.Severity != SeverityDanger {
				t.Errorf("dockerSocketMount should be danger, got %s", f.Severity)
			}
		}
	}
	if !found {
		t.Error("expected dockerSocketMount finding for /var/run/docker.sock volume")
	}
}

func TestSensitiveHostPath(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "log"}},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr(false),
						Containers: []corev1.Container{{
							Name: "log", Image: "log:v1",
							SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false)},
						}},
						Volumes: []corev1.Volume{
							{Name: "host-etc", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc"}}},
							{Name: "app-data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data/app"}}},
						},
					},
				},
			},
		}},
	}

	results := RunChecks(input)
	checks := map[string]bool{}
	for _, f := range results.Findings {
		if f.CheckID == "sensitiveHostPath" {
			checks[f.Message] = true
		}
	}

	// /etc should be flagged
	foundEtc := false
	for msg := range checks {
		if containsStr(msg, "/etc") {
			foundEtc = true
		}
	}
	if !foundEtc {
		t.Error("expected sensitiveHostPath finding for /etc")
	}

	// /data/app should NOT be flagged
	for msg := range checks {
		if containsStr(msg, "/data") {
			t.Error("/data/app should not be flagged as sensitive host path")
		}
	}
}

func TestSecretInConfigMap(t *testing.T) {
	input := &CheckInput{
		ConfigMaps: []*corev1.ConfigMap{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
				Data:       map[string]string{"app_name": "myapp", "log_level": "info"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "db-config", Namespace: "default"},
				Data:       map[string]string{"db_host": "postgres", "db_password": "hunter2"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "short-client-secret", Namespace: "default"},
				Data:       map[string]string{"oauth_client_secret": "hunter2"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "literal-secret", Namespace: "default"},
				Data:       map[string]string{"secret": "hunter2"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "auth-config", Namespace: "default"},
				Data: map[string]string{
					"auth_mode":                      "oidc",
					"google_application_credentials": "/var/run/secrets/google/key.json",
					"credential_url":                 "https://metadata.google.internal/token",
					"token_file":                     "/var/run/secrets/token",
					"token_ttl":                      "3600",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "secret-reference-config", Namespace: "default"},
				Data: map[string]string{
					"client_secret_name": "oauth-client-credentials",
					"secret_name":        "db-secret",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "embedded-url-credentials", Namespace: "default"},
				Data:       map[string]string{"credentials": `{"token_uri":"https://oauth2.googleapis.com/token","private_key_id":"abc123DEF456ghi789JKL"}`},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "connection-url-credentials", Namespace: "default"},
				Data:       map[string]string{"db_credentials": "postgres://app:s3cretpass@db:5432/app"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "password-only-url-credentials", Namespace: "default"},
				Data:       map[string]string{"cache_credentials": "redis://:s3cretpass@redis:6379/0"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "token-config", Namespace: "default"},
				Data:       map[string]string{"api_token": "qH7mN2pR9sT4uV6wX8yZ0aB1cD3eF5gH"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bearer-config", Namespace: "default"},
				Data:       map[string]string{"authorization_header": "Bearer qH7mN2pR9sT4uV6wX8yZ0aB1cD3eF5gH"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "basic-config", Namespace: "default"},
				Data:       map[string]string{"authorization_header": "Basic c3VwZXJzZWNyZXQ6cGFzc3dvcmQxMjM="},
			},
		},
	}

	results := RunChecks(input)
	found := map[string]bool{}
	for _, f := range results.Findings {
		if f.CheckID == "secretInConfigMap" {
			found[f.Name] = true
		}
	}

	if !found["db-config"] {
		t.Error("expected secretInConfigMap finding for db-config (has db_password key)")
	}
	if !found["short-client-secret"] {
		t.Error("expected secretInConfigMap finding for short client secret key")
	}
	if !found["literal-secret"] {
		t.Error("expected secretInConfigMap finding for literal secret key")
	}
	if found["app-config"] {
		t.Error("app-config should not be flagged (no sensitive keys)")
	}
	if found["auth-config"] {
		t.Error("auth-config should not be flagged for low-information auth/token settings")
	}
	if found["secret-reference-config"] {
		t.Error("secret-reference-config should not be flagged for Secret name references")
	}
	if !found["embedded-url-credentials"] {
		t.Error("expected secretInConfigMap finding for credential-looking value that embeds a URL")
	}
	if !found["connection-url-credentials"] {
		t.Error("expected secretInConfigMap finding for credential URL with embedded userinfo")
	}
	if !found["password-only-url-credentials"] {
		t.Error("expected secretInConfigMap finding for credential URL with password-only userinfo")
	}
	if !found["token-config"] {
		t.Error("expected secretInConfigMap finding for token-config (token-looking value)")
	}
	if !found["bearer-config"] {
		t.Error("expected secretInConfigMap finding for bearer-config (bearer token-looking value)")
	}
	if !found["basic-config"] {
		t.Error("expected secretInConfigMap finding for basic-config (basic auth value)")
	}
}

// TestCheckStuckTerminating pins the lifecycle check's age-tier mapping.
// Cluster Audit + per-resource GitOps Issue must agree on what counts
// as "stuck" so an operator looking at both surfaces sees consistent
// severity for the same resource.
func TestCheckStuckTerminating(t *testing.T) {
	now := time.Now()
	mkPod := func(name string, ago time.Duration, finalizers []string) *corev1.Pod {
		dt := metav1.NewTime(now.Add(-ago))
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "default",
				DeletionTimestamp: &dt,
				Finalizers:        finalizers,
			},
		}
	}

	input := &CheckInput{
		Pods: []*corev1.Pod{
			// Healthy pod (no deletionTimestamp) — must not be flagged.
			{ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "default"}},
			// Just deleted, within window — must not be flagged.
			mkPod("recently-deleted", 30*time.Second, nil),
			// 4m59s — under threshold, must not be flagged.
			mkPod("under-threshold", 4*time.Minute+59*time.Second, nil),
			// 6 minutes — warning tier.
			mkPod("warning", 6*time.Minute, []string{"example.io/cleanup"}),
			// 45 minutes — danger tier.
			mkPod("danger", 45*time.Minute, []string{"finalizers.fluxcd.io"}),
		},
	}

	results := RunChecks(input)

	bySeverity := map[string]map[string]string{} // severity → name → message
	for _, f := range results.Findings {
		if f.CheckID != "stuckTerminating" {
			continue
		}
		if bySeverity[f.Severity] == nil {
			bySeverity[f.Severity] = map[string]string{}
		}
		bySeverity[f.Severity][f.Name] = f.Message
	}

	if _, found := bySeverity["warning"]["healthy"]; found {
		t.Error("healthy pod should not be flagged")
	}
	if _, found := bySeverity["warning"]["recently-deleted"]; found {
		t.Error("pod within cleanup window should not be flagged")
	}
	if _, found := bySeverity["warning"]["under-threshold"]; found {
		t.Error("pod under 5min threshold should not be flagged")
	}
	msg, ok := bySeverity["warning"]["warning"]
	if !ok {
		t.Fatal("expected warning-tier finding for 6min-old terminating pod")
	}
	if !strings.Contains(msg, "example.io/cleanup") {
		t.Errorf("expected warning message to name finalizer; got %q", msg)
	}
	dangerMsg, ok := bySeverity["danger"]["danger"]
	if !ok {
		t.Fatal("expected danger-tier finding for 45min-old terminating pod")
	}
	if !strings.Contains(dangerMsg, "finalizers.fluxcd.io") {
		t.Errorf("expected danger message to name finalizer; got %q", dangerMsg)
	}
}

// TestCheckStuckTerminating_AllKinds asserts the check fires for *every*
// typed slice in CheckInput, not just Pods. The implementation has 11
// near-identical for-loops that each call emit() with a hardcoded kind
// string. A copy-paste bug (omitting one slice during a refactor, or
// passing the wrong kind label to emit()) would silently regress
// coverage for that kind without any other test catching it. One
// terminating fixture per kind, all set 45min old → all should report
// danger-tier with their correct Kind field.
func TestCheckStuckTerminating_AllKinds(t *testing.T) {
	now := time.Now()
	dt := metav1.NewTime(now.Add(-45 * time.Minute))
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			DeletionTimestamp: &dt,
			Finalizers:        []string{"example.io/cleanup"},
		}
	}

	input := &CheckInput{
		Pods:                     []*corev1.Pod{{ObjectMeta: meta("pod-x")}},
		Deployments:              []*appsv1.Deployment{{ObjectMeta: meta("deploy-x")}},
		StatefulSets:             []*appsv1.StatefulSet{{ObjectMeta: meta("sts-x")}},
		DaemonSets:               []*appsv1.DaemonSet{{ObjectMeta: meta("ds-x")}},
		Services:                 []*corev1.Service{{ObjectMeta: meta("svc-x")}},
		Ingresses:                []*networkingv1.Ingress{{ObjectMeta: meta("ing-x")}},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{{ObjectMeta: meta("hpa-x")}},
		PodDisruptionBudgets:     []*policyv1.PodDisruptionBudget{{ObjectMeta: meta("pdb-x")}},
		ConfigMaps:               []*corev1.ConfigMap{{ObjectMeta: meta("cm-x")}},
		Secrets:                  []*corev1.Secret{{ObjectMeta: meta("secret-x")}},
		ServiceAccounts:          []*corev1.ServiceAccount{{ObjectMeta: meta("sa-x")}},
	}

	// Call the check directly. RunChecks would run the full chain
	// (pod-spec checks, PDB matcher, etc.) which expect richer fixtures
	// (Selector, container specs) than this test cares about. The audit
	// dispatch is itself covered by RunChecks tests; this one targets
	// only the stuckTerminating loop completeness.
	findings := checkStuckTerminating(newEvalTracker(), input)
	byKindAndName := map[string]string{} // "Kind/name" → severity
	for _, f := range findings {
		if f.CheckID != "stuckTerminating" {
			continue
		}
		byKindAndName[f.Kind+"/"+f.Name] = f.Severity
	}

	wantPairs := map[string]string{
		"Pod/pod-x":                     SeverityDanger,
		"Deployment/deploy-x":           SeverityDanger,
		"StatefulSet/sts-x":             SeverityDanger,
		"DaemonSet/ds-x":                SeverityDanger,
		"Service/svc-x":                 SeverityDanger,
		"Ingress/ing-x":                 SeverityDanger,
		"HorizontalPodAutoscaler/hpa-x": SeverityDanger,
		"PodDisruptionBudget/pdb-x":     SeverityDanger,
		"ConfigMap/cm-x":                SeverityDanger,
		"Secret/secret-x":               SeverityDanger,
		"ServiceAccount/sa-x":           SeverityDanger,
	}
	for k, want := range wantPairs {
		got, ok := byKindAndName[k]
		if !ok {
			t.Errorf("missing finding for %s — kind not flagged when terminating", k)
			continue
		}
		if got != want {
			t.Errorf("%s: severity = %q, want %q", k, got, want)
		}
	}
}

// TestCheckCrossplaneStuck pins the severity ramp and condition-priority
// rules for stuck MR/XR/Claim resources. The 5min/30min thresholds are
// shared with stuckTerminating; if either is retuned, retune both so the
// audit page reports consistent severity across stuck-resource categories.
func TestCheckCrossplaneStuck(t *testing.T) {
	now := time.Now()
	mr := func(name string, ttype, treason, tmessage string, transitionAgo time.Duration, paused bool) *unstructured.Unstructured {
		annotations := map[string]interface{}{}
		if paused {
			annotations["crossplane.io/paused"] = "true"
		}
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kubernetes.crossplane.io/v1alpha1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name":        name,
				"annotations": annotations,
			},
			"spec": map[string]interface{}{
				"providerConfigRef": map[string]interface{}{"name": "default"},
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":               ttype,
						"status":             "False",
						"reason":             treason,
						"message":            tmessage,
						"lastTransitionTime": now.Add(-transitionAgo).Format(time.RFC3339),
					},
				},
			},
		}}
		return u
	}

	healthy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubernetes.crossplane.io/v1alpha1",
		"kind":       "Object",
		"metadata":   map[string]interface{}{"name": "healthy"},
		"spec":       map[string]interface{}{"providerConfigRef": map[string]interface{}{"name": "default"}},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "lastTransitionTime": now.Format(time.RFC3339)},
				map[string]interface{}{"type": "Synced", "status": "True", "lastTransitionTime": now.Format(time.RFC3339)},
			},
		},
	}}

	input := &CheckInput{
		ManagedResources: []*unstructured.Unstructured{
			healthy,
			mr("under-threshold", "Ready", "Pending", "still converging", 4*time.Minute+59*time.Second, false),
			mr("warn-ready", "Ready", "ProviderConfigNotReady", "auth error from cloud", 6*time.Minute, false),
			mr("warn-synced", "Synced", "ReconcileError", "schema rejected by provider", 6*time.Minute, false),
			mr("danger", "Ready", "BackendError", "quota exceeded", 45*time.Minute, false),
			mr("paused-ignored", "Ready", "ProviderConfigNotReady", "auth error", 45*time.Minute, true),
		},
	}

	results := RunChecks(input)
	bySeverity := map[string]map[string]Finding{}
	for _, f := range results.Findings {
		if f.CheckID != "crossplaneStuck" {
			continue
		}
		if bySeverity[f.Severity] == nil {
			bySeverity[f.Severity] = map[string]Finding{}
		}
		bySeverity[f.Severity][f.Name] = f
	}

	if _, found := bySeverity["warning"]["healthy"]; found {
		t.Error("healthy MR should not be flagged")
	}
	if _, found := bySeverity["warning"]["under-threshold"]; found {
		t.Error("MR within 5min window should not be flagged")
	}
	if _, found := bySeverity["warning"]["paused-ignored"]; found {
		t.Error("paused MR should not be flagged regardless of age — operator intent")
	}
	if _, found := bySeverity["danger"]["paused-ignored"]; found {
		t.Error("paused MR should not be flagged regardless of age — operator intent")
	}

	warnReady, ok := bySeverity["warning"]["warn-ready"]
	if !ok {
		t.Fatal("expected warning-tier finding for 6min-old Ready=False MR")
	}
	if !strings.Contains(warnReady.Message, "Ready=False") {
		t.Errorf("expected message to name Ready=False; got %q", warnReady.Message)
	}
	if !strings.Contains(warnReady.Message, "auth error from cloud") {
		t.Errorf("expected message to include the upstream cloud error; got %q", warnReady.Message)
	}

	warnSynced, ok := bySeverity["warning"]["warn-synced"]
	if !ok {
		t.Fatal("expected warning-tier finding for 6min-old Synced=False MR")
	}
	if !strings.Contains(warnSynced.Message, "Synced=False") {
		t.Errorf("expected message to name Synced=False; got %q", warnSynced.Message)
	}

	danger, ok := bySeverity["danger"]["danger"]
	if !ok {
		t.Fatal("expected danger-tier finding for 45min-old MR")
	}
	if !strings.Contains(danger.Message, "quota exceeded") {
		t.Errorf("expected danger message to include cloud error; got %q", danger.Message)
	}
}

// TestCheckCrossplaneStuck_SyncedPriority verifies Synced=False takes
// precedence over Ready=False when both are present. Synced=False usually
// indicates a configuration error (the actionable thing); Ready=False is
// often a downstream consequence. Operators fixing Synced first resolves
// both — surfacing Synced=False in the finding tells them where to look.
func TestCheckCrossplaneStuck_SyncedPriority(t *testing.T) {
	now := time.Now()
	bothFalse := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubernetes.crossplane.io/v1alpha1",
		"kind":       "Object",
		"metadata":   map[string]interface{}{"name": "both"},
		"spec":       map[string]interface{}{"providerConfigRef": map[string]interface{}{"name": "default"}},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "False", "reason": "ProviderConfigNotReady", "message": "ready msg", "lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339)},
				map[string]interface{}{"type": "Synced", "status": "False", "reason": "ReconcileError", "message": "synced msg", "lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339)},
			},
		},
	}}
	input := &CheckInput{ManagedResources: []*unstructured.Unstructured{bothFalse}}
	results := RunChecks(input)
	var found *Finding
	for i := range results.Findings {
		if results.Findings[i].CheckID == "crossplaneStuck" && results.Findings[i].Name == "both" {
			found = &results.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a crossplaneStuck finding")
	}
	if !strings.Contains(found.Message, "Synced=False") {
		t.Errorf("expected Synced=False to win over Ready=False; got message %q", found.Message)
	}
	if !strings.Contains(found.Message, "synced msg") {
		t.Errorf("expected Synced message body in finding; got %q", found.Message)
	}
}

// TestCheckCrossplaneStuck_Composites checks that XRs/Claims are scanned too.
func TestCheckCrossplaneStuck_Composites(t *testing.T) {
	now := time.Now()
	xr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "demo.example.io/v1alpha1",
		"kind":       "AppBundle",
		"metadata":   map[string]interface{}{"name": "broken-xr"},
		"spec": map[string]interface{}{
			"crossplane": map[string]interface{}{
				"resourceRefs": []interface{}{},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Synced", "status": "False", "reason": "ComposeResources", "message": "composition error", "lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339)},
			},
		},
	}}
	input := &CheckInput{CompositeResources: []*unstructured.Unstructured{xr}}
	results := RunChecks(input)
	var found *Finding
	for i := range results.Findings {
		if results.Findings[i].CheckID == "crossplaneStuck" && results.Findings[i].Name == "broken-xr" {
			found = &results.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected crossplaneStuck finding for composite resource")
	}
	if found.Kind != "AppBundle" {
		t.Errorf("expected Kind=AppBundle, got %q", found.Kind)
	}
}
