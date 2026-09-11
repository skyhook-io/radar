package audit

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func annotated(d *appsv1.Deployment, value string) *appsv1.Deployment {
	d.Annotations = map[string]string{IgnoreChecksAnnotation: value}
	return d
}

func TestIgnoreAnnotation_SuppressesNamedCheckOnlyForThatResource(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			annotated(deploymentInNS("solo-ignored", "prod", 1, secureContainer("app", false)), "singleReplica"),
			deploymentInNS("solo-flagged", "prod", 1, secureContainer("app", false)),
		},
	}
	results := RunChecks(input)

	single := findingNames(results.Findings, "singleReplica")
	if single["solo-ignored"] {
		t.Error("singleReplica finding should be suppressed on annotated deployment")
	}
	if !single["solo-flagged"] {
		t.Error("singleReplica finding missing on unannotated deployment")
	}
	// Other checks on the annotated resource are unaffected.
	if !findingNames(results.Findings, "readinessProbeMissing")["solo-ignored"] {
		t.Error("readinessProbeMissing should still fire on the annotated deployment")
	}
}

func TestIgnoreAnnotation_MultipleChecksCommaSeparated(t *testing.T) {
	d := deploymentInNS("hostnet", "prod", 1, secureContainer("app", false))
	d.Spec.Template.Spec.HostNetwork = true
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments:              []*appsv1.Deployment{annotated(d, "singleReplica, hostNetwork")},
	}
	results := RunChecks(input)

	for _, id := range []string{"singleReplica", "hostNetwork"} {
		if findingNames(results.Findings, id)["hostnet"] {
			t.Errorf("%s finding should be suppressed by comma-separated annotation", id)
		}
	}
	if !findingNames(results.Findings, "readinessProbeMissing")["hostnet"] {
		t.Error("readinessProbeMissing should still fire on the annotated deployment")
	}
}

func TestIgnoreAnnotation_UnknownCheckNameIsHarmless(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			annotated(deploymentInNS("solo", "prod", 1, secureContainer("app", true)), "notARealCheck"),
		},
	}
	results := RunChecks(input)

	if !findingNames(results.Findings, "singleReplica")["solo"] {
		t.Error("unknown check name in annotation must not suppress anything")
	}
}

func TestIgnoreAnnotation_ExcludedFromCounts(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			annotated(deploymentInNS("solo-ignored", "prod", 1, secureContainer("a", true)), "singleReplica"),
			deploymentInNS("solo-flagged", "prod", 1, secureContainer("a", true)),
			deploymentInNS("scaled", "prod", 2, secureContainer("a", true)),
		},
	}
	results := RunChecks(input)

	// solo-ignored is excluded from the denominator: 2 evaluated (solo-flagged
	// failing, scaled passing), not 3.
	if got := results.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 2, Passed: 1}) {
		t.Errorf("singleReplica counts = %+v, want {Evaluated:2 Passed:1}", got)
	}
	if got := results.EvaluatedByNamespace["singleReplica"]["prod"]; got != 2 {
		t.Errorf("EvaluatedByNamespace[singleReplica][prod] = %d, want 2", got)
	}
}

func TestIgnoreAnnotation_MultiContainerDecrementsEvaluatedOnce(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			// Two containers both missing the readiness probe → two raw
			// findings for one (resource, check) pair pre-merge.
			annotated(deploymentInNS("multi", "prod", 2, secureContainer("a", false), secureContainer("b", false)), "readinessProbeMissing"),
			deploymentInNS("ok", "prod", 2, secureContainer("a", true)),
		},
	}
	results := RunChecks(input)

	if findingNames(results.Findings, "readinessProbeMissing")["multi"] {
		t.Error("readinessProbeMissing should be suppressed on annotated multi-container deployment")
	}
	if got := results.CheckCounts["readinessProbeMissing"]; got != (CheckCount{Evaluated: 1, Passed: 1}) {
		t.Errorf("readinessProbeMissing counts = %+v, want {Evaluated:1 Passed:1}", got)
	}
}

func annotatedUnstructured(u *unstructured.Unstructured, value string) *unstructured.Unstructured {
	u.SetAnnotations(map[string]string{IgnoreChecksAnnotation: value})
	return u
}

// CRD subjects are keyed by the group audit findings actually carry ("" — CRD
// kinds are absent from the builtin table), not by the object's real API group.
func TestIgnoreAnnotation_CRDSubject(t *testing.T) {
	const g = "traefik.io"
	route := annotatedUnstructured(
		traefikRoute(g, "IngressRoute", "app", "r1", []map[string]any{{"name": "missing-svc"}}, nil),
		"traefikRouteMissingService",
	)
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		AllServices:              []*corev1.Service{},
		IngressRoutes:            []*unstructured.Unstructured{route},
	}
	results := RunChecks(input)

	if findingNames(results.Findings, "traefikRouteMissingService")["r1"] {
		t.Error("traefikRouteMissingService should be suppressed on the annotated IngressRoute")
	}
	if got := results.CheckCounts["traefikRouteMissingService"].Evaluated; got != 0 {
		t.Errorf("Evaluated = %d, want 0 — the ignored route must leave the denominator", got)
	}
}

// The opt-out is per-resource: annotating one CRD subject must not silence the
// same check on its unannotated siblings, nor steal their evaluated tally.
func TestIgnoreAnnotation_CRDSubjectScopedToResource(t *testing.T) {
	const g = "traefik.io"
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		AllServices:              []*corev1.Service{},
		IngressRoutes: []*unstructured.Unstructured{
			annotatedUnstructured(
				traefikRoute(g, "IngressRoute", "app", "ignored", []map[string]any{{"name": "missing-a"}}, nil),
				"traefikRouteMissingService",
			),
			traefikRoute(g, "IngressRoute", "app", "flagged", []map[string]any{{"name": "missing-b"}}, nil),
		},
	}
	results := RunChecks(input)

	names := findingNames(results.Findings, "traefikRouteMissingService")
	if names["ignored"] {
		t.Error("annotated route should be suppressed")
	}
	if !names["flagged"] {
		t.Error("unannotated sibling route must still be flagged")
	}
	if got := results.CheckCounts["traefikRouteMissingService"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	if got := results.EvaluatedByNamespace["traefikRouteMissingService"]["app"]; got != 1 {
		t.Errorf("EvaluatedByNamespace[app] = %d, want 1 — the sibling's tally must survive", got)
	}
}

// A CRD kind whose name collides with a builtin (CNPG Cluster) must still be
// keyed under "" like every other CRD subject.
func TestIgnoreAnnotation_CRDSubjectCollidingKind(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name": "db", "namespace": "prod",
			"annotations": map[string]any{IgnoreChecksAnnotation: "cnpgNoDeclarativeBackup"},
		},
	}}
	input := &CheckInput{
		ServiceAccounts:                   []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers:          []*autoscalingv2.HorizontalPodAutoscaler{},
		CNPGClusters:                      []*unstructured.Unstructured{cluster},
		CNPGScheduledBackupsAuthoritative: true,
	}
	results := RunChecks(input)

	if findingNames(results.Findings, "cnpgNoDeclarativeBackup")["db"] {
		t.Error("cnpgNoDeclarativeBackup should be suppressed on the annotated CNPG Cluster")
	}
}

func TestIgnoreAnnotation_NonWorkloadSubject(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts: []*corev1.ServiceAccount{},
		Pods:            []*corev1.Pod{},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "headless-tool",
				Namespace:   "prod",
				Annotations: map[string]string{IgnoreChecksAnnotation: "serviceNoMatchingPods"},
			},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "nothing"}},
		}},
	}
	results := RunChecks(input)

	if findingNames(results.Findings, "serviceNoMatchingPods")["headless-tool"] {
		t.Error("serviceNoMatchingPods should be suppressed on annotated Service")
	}
}
