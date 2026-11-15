package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Without a session, only the kinds the config-reference family answers
// about say it went unanswered; a Service's drawer has nothing to report.
func TestUnansweredConfigFamilyReportsOnlyForItsKinds(t *testing.T) {
	ResetTestState()
	answer := func(obj runtime.Object) *topology.Relationships {
		fetched, ok := OwnershipObservation(obj)
		if !ok {
			t.Fatalf("no observation of %T", obj)
		}
		rel := &topology.Relationships{}
		AnswerRelationships(rel, fetched, obj, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
		return rel
	}
	svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}
	if rel := answer(svc); rel.Coverage != nil {
		t.Errorf("a Service drawer reports the config family: %+v", rel.Coverage)
	}
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "settings"}}
	rel := answer(cm)
	if c, ok := rel.Coverage[topology.ConfigReferenceFamily]; !ok || len(c.NotChecked) == 0 {
		t.Errorf("a ConfigMap drawer without a session = %+v; want the family saying why it went unanswered", rel.Coverage)
	}
}
