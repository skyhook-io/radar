package audit

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ptr[T any](v T) *T { return &v }

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
	for _, f := range results.Findings {
		if f.CheckID == "orphanConfigMapSecret" {
			orphans[f.Name] = true
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
}

func TestDeprecatedAPIVersion(t *testing.T) {
	input := &CheckInput{
		ClusterVersion: "1.30",
		ServedAPIs: []string{
			"apps/v1",                    // stable — should not flag
			"batch/v1beta1",              // deprecated, removed in 1.25 — should flag
			"policy/v1beta1",             // deprecated, removed in 1.25 — should flag
			"networking.k8s.io/v1",       // stable — should not flag
		},
	}

	results := RunChecks(input)
	deprecated := 0
	for _, f := range results.Findings {
		if f.CheckID == "deprecatedAPIVersion" {
			deprecated++
		}
	}
	// batch/v1beta1 has CronJob, policy/v1beta1 has PDB + PSP = at least 3 entries
	if deprecated < 3 {
		t.Errorf("expected at least 3 deprecatedAPIVersion findings, got %d", deprecated)
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
			{Namespace: "default", Name: "waste-pod", CPUUsage: 5, CPURequest: 1000, MemoryUsage: 10 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024},       // 0.5% CPU, 2% memory — waste
			{Namespace: "default", Name: "hot-pod", CPUUsage: 950, CPURequest: 1000, MemoryUsage: 480 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024},        // 95% CPU, 94% memory — risk
			{Namespace: "default", Name: "normal-pod", CPUUsage: 500, CPURequest: 1000, MemoryUsage: 256 * 1024 * 1024, MemoryRequest: 512 * 1024 * 1024},     // 50% — fine
			{Namespace: "default", Name: "no-request-pod", CPUUsage: 100, CPURequest: 0, MemoryUsage: 128 * 1024 * 1024, MemoryRequest: 0},                     // no requests — skip
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
	if found["app-config"] {
		t.Error("app-config should not be flagged (no sensitive keys)")
	}
}
