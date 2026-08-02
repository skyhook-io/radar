package rolloutdiag

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		wantApplicable bool
		wantRisk       bool
	}{
		{
			name:           "percentage values",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(stringValue("0%"), stringValue("100%"))),
			wantApplicable: true,
			wantRisk:       true,
		},
		{
			name:           "integer values equal replicas",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), intValue(3))),
			wantApplicable: true,
			wantRisk:       true,
		},
		{
			name:           "max unavailable exceeds replicas",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), intValue(4))),
			wantApplicable: true,
			wantRisk:       true,
		},
		{
			name:           "unset strategy type defaults to rolling update",
			deployment:     testDeployment(replicaPtr(3), "", rollingUpdate(intValue(0), stringValue("100%"))),
			wantApplicable: true,
			wantRisk:       true,
		},
		{
			name:           "nil rolling update uses safe defaults",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, nil),
			wantApplicable: true,
		},
		{
			name:       "recreate is explicit full replacement",
			deployment: testDeployment(replicaPtr(3), appsv1.RecreateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("100%"))),
		},
		{
			name:       "unknown strategy is not applicable",
			deployment: testDeployment(replicaPtr(3), appsv1.DeploymentStrategyType("BlueGreen"), rollingUpdate(intValue(0), stringValue("100%"))),
		},
		{
			name:           "nil max surge uses default",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(nil, stringValue("100%"))),
			wantApplicable: true,
		},
		{
			name:           "nil max unavailable uses default",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), nil)),
			wantApplicable: true,
		},
		{
			name:           "positive percentage surge rounds up",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(stringValue("1%"), stringValue("100%"))),
			wantApplicable: true,
		},
		{
			name:           "default-sized surge preserves overlap",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(stringValue("25%"), stringValue("100%"))),
			wantApplicable: true,
		},
		{
			name:           "partial max unavailable preserves capacity",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("50%"))),
			wantApplicable: true,
		},
		{
			name:           "both zero follows controller fencepost rule",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), intValue(0))),
			wantApplicable: true,
		},
		{
			name:       "one replica is handled by redundancy audit",
			deployment: testDeployment(replicaPtr(1), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("100%"))),
		},
		{
			name:       "zero replicas has no rollout availability",
			deployment: testDeployment(replicaPtr(0), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("100%"))),
		},
		{
			name:       "nil replicas defaults to one",
			deployment: testDeployment(nil, appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("100%"))),
		},
		{
			name:           "invalid surge value",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(stringValue("invalid"), stringValue("100%"))),
			wantApplicable: true,
		},
		{
			name:           "invalid unavailable value",
			deployment:     testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("invalid"))),
			wantApplicable: true,
		},
		{name: "nil deployment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Applicable(tt.deployment); got != tt.wantApplicable {
				t.Fatalf("Applicable() = %v, want %v", got, tt.wantApplicable)
			}
			if got := Analyze(tt.deployment); (got != nil) != tt.wantRisk {
				t.Fatalf("Analyze() = %+v, want risk %v", got, tt.wantRisk)
			}
		})
	}
}

func TestAnalyzeRiskFactsAndCopy(t *testing.T) {
	deployment := testDeployment(replicaPtr(3), appsv1.RollingUpdateDeploymentStrategyType, rollingUpdate(intValue(0), stringValue("100%")))
	deployment.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "PASSWORD", Value: "sensitive-value"}},
	}}

	risk := Analyze(deployment)
	if risk == nil {
		t.Fatal("Analyze() returned nil")
	}
	if risk.Reason != reasonAllReplicasUnavailableWithoutSurge {
		t.Errorf("Reason = %q", risk.Reason)
	}
	if risk.Replicas != 3 || risk.MaxSurge != "0" || risk.MaxUnavailable != "100%" {
		t.Errorf("source facts = %+v", risk)
	}
	if risk.ResolvedMaxSurge != 0 || risk.ResolvedMaxUnavailable != 3 {
		t.Errorf("resolved facts = %+v", risk)
	}
	for _, want := range []string{"maxUnavailable=100%", "maxSurge=0", "permit", "can drop", "no old-version fallback"} {
		if !strings.Contains(risk.Message, want) {
			t.Errorf("Message %q missing %q", risk.Message, want)
		}
	}
	for _, forbidden := range []string{"guarantee", "sensitive-value", "PASSWORD"} {
		if strings.Contains(risk.Message, forbidden) {
			t.Errorf("Message %q contains %q", risk.Message, forbidden)
		}
	}
}

func testDeployment(replicas *int32, strategyType appsv1.DeploymentStrategyType, update *appsv1.RollingUpdateDeployment) *appsv1.Deployment {
	return &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Replicas: replicas,
		Strategy: appsv1.DeploymentStrategy{Type: strategyType, RollingUpdate: update},
	}}
}

func rollingUpdate(maxSurge, maxUnavailable *intstr.IntOrString) *appsv1.RollingUpdateDeployment {
	return &appsv1.RollingUpdateDeployment{MaxSurge: maxSurge, MaxUnavailable: maxUnavailable}
}

func replicaPtr(value int32) *int32 {
	return &value
}

func intValue(value int) *intstr.IntOrString {
	result := intstr.FromInt32(int32(value))
	return &result
}

func stringValue(value string) *intstr.IntOrString {
	result := intstr.FromString(value)
	return &result
}
