package audit

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/configrefs"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func completeOrphanEvidence(input *CheckInput) *CheckInput {
	evidence := &ConfigReferenceEvidence{Refs: CollectConfigObjectRefs(input), CompleteNamespaces: map[string][]string{}, ReflectionsComplete: map[string]bool{"ConfigMap": true, "Secret": true}}
	for _, cm := range input.ConfigMaps {
		evidence.CompleteNamespaces["ConfigMap"] = append(evidence.CompleteNamespaces["ConfigMap"], cm.Namespace)
		evidence.Objects = append(evidence.Objects, configrefs.Metadata("ConfigMap", cm))
	}
	for _, sec := range input.Secrets {
		evidence.CompleteNamespaces["Secret"] = append(evidence.CompleteNamespaces["Secret"], sec.Namespace)
		evidence.Objects = append(evidence.Objects, configrefs.Metadata("Secret", sec))
	}
	input.ConfigReferenceEvidence = evidence
	return input
}

func TestOrphanEvidenceDoesNotInferAbsence(t *testing.T) {
	input := &CheckInput{Secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: "app"}}}}
	unknown := RunChecks(input)
	if unknown.CheckCounts["orphanConfigMapSecret"].Evaluated != 0 || !slices.Contains(unknown.MissingInputs, "secret-references") {
		t.Fatalf("unknown evaluated or not disclosed: %+v", unknown)
	}
	completeOrphanEvidence(input)
	complete := RunChecks(input)
	if complete.CheckCounts["orphanConfigMapSecret"] != (CheckCount{Evaluated: 1, Passed: 0}) {
		t.Fatal("complete evidence must establish unused")
	}
	input.ConfigReferenceEvidence.CompleteNamespaces = nil
	input.ConfigReferenceEvidence.Refs = []ConfigObjectRef{{Kind: "Secret", Namespace: "app", Name: "candidate"}}
	used := RunChecks(input)
	if used.CheckCounts["orphanConfigMapSecret"] != (CheckCount{Evaluated: 1, Passed: 1}) || slices.Contains(used.MissingInputs, "secret-references") {
		t.Fatal("known use must win over missing unrelated evidence")
	}
}

func TestOrphanEvidenceIsKindAndNamespaceSpecific(t *testing.T) {
	input := &CheckInput{ConfigMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "app"}}}, Secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "app"}}, {ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "other"}}}, ConfigReferenceEvidence: &ConfigReferenceEvidence{CompleteNamespaces: map[string][]string{"Secret": {"app"}}}}
	result := RunChecks(input)
	if result.CheckCounts["orphanConfigMapSecret"] != (CheckCount{Evaluated: 1}) {
		t.Fatal("coverage widened")
	}
	if !slices.Contains(result.MissingInputs, "secret-references") || !slices.Contains(result.MissingInputs, "configmap-references") {
		t.Fatal("missing kinds not disclosed")
	}
	for _, f := range result.Findings {
		if f.CheckID == "orphanConfigMapSecret" && (f.Kind != "Secret" || f.Namespace != "app") {
			t.Fatal("unknown subject reported unused")
		}
	}
}

func TestOrphanReflectionEvidence(t *testing.T) {
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"}}}
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source", "reflector.v1.k8s.emberstack.com/auto-reflects": "true"}}}
	input := completeOrphanEvidence(&CheckInput{Secrets: []*corev1.Secret{source, mirror}})
	input.ConfigReferenceEvidence.ReflectionsComplete["Secret"] = false
	result := RunChecks(input)
	if result.CheckCounts["orphanConfigMapSecret"].Evaluated != 0 || !slices.Contains(result.MissingInputs, "secret-references") {
		t.Fatal("automatic mirror proves neither consumption nor source absence")
	}
	input.ConfigReferenceEvidence.Refs = []ConfigObjectRef{{Kind: "Secret", Namespace: "edge", Name: "mirror"}}
	result = RunChecks(input)
	if result.CheckCounts["orphanConfigMapSecret"] != (CheckCount{Evaluated: 2, Passed: 2}) {
		t.Fatal("observed mirror consumption did not propagate")
	}
	input.ConfigReferenceEvidence.Refs = nil
	input.ConfigReferenceEvidence.ReflectionsComplete["Secret"] = true
	result = RunChecks(input)
	if result.CheckCounts["orphanConfigMapSecret"] != (CheckCount{Evaluated: 1}) {
		t.Fatal("unused automatic mirror should be exempt without making source used")
	}
}
