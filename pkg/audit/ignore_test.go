package audit

import (
	"reflect"
	"testing"
	"time"

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

func TestIgnoreAnnotation_ReportsIgnoredSubjects(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			// Two containers → two raw findings, but one ignored subject.
			annotated(deploymentInNS("multi", "prod", 2, secureContainer("a", false), secureContainer("b", false)), "readinessProbeMissing,singleReplica"),
			annotated(deploymentInNS("solo", "dev", 1, secureContainer("a", true)), "singleReplica"),
			// Passing subjects produce no finding, so nothing is ignored.
			annotated(deploymentInNS("ok", "prod", 2, secureContainer("a", true)), "readinessProbeMissing"),
		},
	}
	results := RunChecks(input)

	if results.IgnoreChecksAnnotation != IgnoreChecksAnnotation {
		t.Errorf("IgnoreChecksAnnotation = %q, want %q", results.IgnoreChecksAnnotation, IgnoreChecksAnnotation)
	}
	want := map[string]map[string]int{
		"readinessProbeMissing": {"prod": 1},
		"singleReplica":         {"dev": 1},
	}
	if !reflect.DeepEqual(results.AnnotationIgnoredByNamespace, want) {
		t.Errorf("AnnotationIgnoredByNamespace = %v, want %v", results.AnnotationIgnoredByNamespace, want)
	}

	filtered := ApplySettings(results, []string{"dev"}, []string{"readinessProbeMissing"})
	if len(filtered.AnnotationIgnoredByNamespace) != 0 {
		t.Errorf("ignored namespaces and disabled checks must drop their counts, got %v", filtered.AnnotationIgnoredByNamespace)
	}
	if filtered.IgnoreChecksAnnotation != IgnoreChecksAnnotation {
		t.Error("ApplySettings must keep IgnoreChecksAnnotation")
	}
	if !reflect.DeepEqual(results.AnnotationIgnoredByNamespace, want) {
		t.Error("ApplySettings must not mutate the cached scan's map")
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

func annotatedNamespace(name, value string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{IgnoreChecksAnnotation: value}}}
}

func TestNamespaceIgnoreAnnotationDropsFindingsInThatNamespace(t *testing.T) {
	soloA := deploymentInNS("solo-a", "a", 1, secureContainer("app", false), secureContainer("side", false))
	soloA.Spec.Template.Spec.HostNetwork = true
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			soloA,
			deploymentInNS("solo-b", "b", 1, secureContainer("app", false)),
		},
		Namespaces: []*corev1.Namespace{
			annotatedNamespace("a", "singleReplica, readinessProbeMissing"),
			{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
		},
	}
	results := RunChecks(input)

	single := findingNames(results.Findings, "singleReplica")
	if single["solo-a"] {
		t.Error("singleReplica should be suppressed in the annotated namespace")
	}
	if !single["solo-b"] {
		t.Error("singleReplica must still fire in the unannotated namespace")
	}
	if !findingNames(results.Findings, "hostNetwork")["solo-a"] {
		t.Error("checks not named on the namespace must still fire")
	}
	if got := results.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("singleReplica counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	// Two containers fail readinessProbeMissing: still one subject.
	if got := results.CheckCounts["readinessProbeMissing"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("readinessProbeMissing counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	want := map[string]map[string]int{"singleReplica": {"a": 1}, "readinessProbeMissing": {"a": 1}}
	if !reflect.DeepEqual(results.AnnotationIgnoredByNamespace, want) {
		t.Errorf("AnnotationIgnoredByNamespace = %v, want %v", results.AnnotationIgnoredByNamespace, want)
	}
}

func TestNamespaceIgnoreAndResourceIgnoreUnion(t *testing.T) {
	d := deploymentInNS("hostnet", "a", 1, secureContainer("app", false))
	d.Spec.Template.Spec.HostNetwork = true
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments:              []*appsv1.Deployment{annotated(d, "hostNetwork")},
		Namespaces:               []*corev1.Namespace{annotatedNamespace("a", "singleReplica")},
	}
	results := RunChecks(input)

	for _, id := range []string{"singleReplica", "hostNetwork"} {
		if findingNames(results.Findings, id)["hostnet"] {
			t.Errorf("%s should be suppressed by the union of resource and namespace annotations", id)
		}
	}
	if !findingNames(results.Findings, "readinessProbeMissing")["hostnet"] {
		t.Error("readinessProbeMissing is named on neither and must still fire")
	}
}

func TestNamespaceIgnoreSkipsClusterScoped(t *testing.T) {
	input := storageInput()
	input.PersistentVolumes[0].Status.Phase = corev1.VolumeReleased
	input.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(time.Now().Add(-72 * time.Hour)))
	input.Namespaces = []*corev1.Namespace{annotatedNamespace("app", "releasedPV"), annotatedNamespace("", "releasedPV")}
	results := RunChecks(input)

	if !findingNames(results.Findings, "releasedPV")["disk"] {
		t.Errorf("a cluster-scoped PV finding must ignore Namespace annotations: %+v", results.Findings)
	}
}

func TestNilNamespacesUnchanged(t *testing.T) {
	newInput := func() *CheckInput {
		return &CheckInput{
			ServiceAccounts:          []*corev1.ServiceAccount{},
			HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
			Deployments: []*appsv1.Deployment{
				annotated(deploymentInNS("solo-ignored", "a", 1, secureContainer("app", false)), "singleReplica"),
				deploymentInNS("solo-flagged", "a", 1, secureContainer("app", false)),
			},
		}
	}
	without := RunChecks(newInput())
	withEmpty := newInput()
	withEmpty.Namespaces = []*corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}
	got := RunChecks(withEmpty)
	got.NamespaceIgnoreChecks = false
	if !reflect.DeepEqual(got, without) {
		t.Errorf("unannotated Namespaces changed results:\n got %+v\nwant %+v", got, without)
	}
	if !findingNames(without.Findings, "singleReplica")["solo-flagged"] {
		t.Error("without Namespaces the unannotated deployment must keep its finding")
	}
}

func TestNamespaceIgnoreSameCheckOnResourceAndNamespaceCountsOnce(t *testing.T) {
	input := &CheckInput{
		ServiceAccounts:          []*corev1.ServiceAccount{},
		HorizontalPodAutoscalers: []*autoscalingv2.HorizontalPodAutoscaler{},
		Deployments: []*appsv1.Deployment{
			annotated(deploymentInNS("both", "a", 1, secureContainer("app", true)), "singleReplica"),
			deploymentInNS("sibling", "b", 1, secureContainer("app", true)),
		},
		Namespaces: []*corev1.Namespace{annotatedNamespace("a", "singleReplica"), {ObjectMeta: metav1.ObjectMeta{Name: "b"}}},
	}
	results := RunChecks(input)

	if got := results.CheckCounts["singleReplica"]; got != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Errorf("singleReplica counts = %+v, want {Evaluated:1 Passed:0}", got)
	}
	want := map[string]map[string]int{"singleReplica": {"a": 1}}
	if !reflect.DeepEqual(results.AnnotationIgnoredByNamespace, want) {
		t.Errorf("AnnotationIgnoredByNamespace = %v, want %v", results.AnnotationIgnoredByNamespace, want)
	}
}

func TestNamespaceIgnoreChecksFlag(t *testing.T) {
	if RunChecks(&CheckInput{}).NamespaceIgnoreChecks {
		t.Error("NamespaceIgnoreChecks must be unset without Namespaces")
	}
	results := RunChecks(&CheckInput{Namespaces: []*corev1.Namespace{}})
	if !results.NamespaceIgnoreChecks {
		t.Error("NamespaceIgnoreChecks must be set when Namespaces were provided")
	}
	if !ApplySettings(results, []string{"a"}, []string{"singleReplica"}).NamespaceIgnoreChecks {
		t.Error("ApplySettings must keep NamespaceIgnoreChecks")
	}
}
