package configrefs

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKEDAAuthenticationReferences(t *testing.T) {
	for _, kind := range []string{"ScaledObject", "ScaledJob"} {
		t.Run(kind, func(t *testing.T) {
			scaler := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "keda.sh/v1alpha1", "kind": kind,
				"metadata": map[string]any{"name": "worker", "namespace": "team"},
				"spec": map[string]any{"triggers": []any{
					map[string]any{"authenticationRef": map[string]any{"name": "credentials"}},
					map[string]any{"authenticationRef": map[string]any{"name": "credentials", "kind": "TriggerAuthentication"}},
					map[string]any{"authenticationRef": map[string]any{"name": "credentials", "kind": "ClusterTriggerAuthentication"}},
					map[string]any{"authenticationRef": map[string]any{"name": "bad", "kind": "Secret"}},
					map[string]any{"authenticationRef": map[string]any{"kind": "TriggerAuthentication"}},
					map[string]any{},
				}},
			}}
			before := scaler.DeepCopy()
			want := []resourceid.Ref{resourceid.NewRef("keda.sh", "TriggerAuthentication", "team", "credentials"), resourceid.NewRef("keda.sh", "ClusterTriggerAuthentication", "", "credentials")}
			if got := KEDAAuthenticationReferences(scaler); !reflect.DeepEqual(got, want) {
				t.Fatalf("references = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(scaler, before) {
				t.Fatal("mutated scaler")
			}
			scaler.SetAPIVersion("other.example/v1")
			if got := KEDAAuthenticationReferences(scaler); len(got) != 0 {
				t.Fatalf("non-KEDA source: %+v", got)
			}
		})
	}
	if got := KEDAAuthenticationReferences(nil); len(got) != 0 {
		t.Fatal(got)
	}
}
