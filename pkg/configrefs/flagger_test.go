package configrefs

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFlaggerAnalysisFollowsNativePreference(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec map[string]any
		want string
	}{
		{"current", map[string]any{"analysis": map[string]any{"interval": "1m"}}, "1m"},
		{"served deprecated field", map[string]any{"canaryAnalysis": map[string]any{"interval": "2m"}}, "2m"},
		{"current wins", map[string]any{"analysis": map[string]any{"interval": "1m"}, "canaryAnalysis": map[string]any{"interval": "2m"}}, "1m"},
		{"empty current wins", map[string]any{"analysis": map[string]any{}, "canaryAnalysis": map[string]any{"interval": "2m"}}, ""},
		{"null current", map[string]any{"analysis": nil, "canaryAnalysis": map[string]any{"interval": "2m"}}, "2m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"spec": tc.spec}}
			got, _, _ := unstructured.NestedString(FlaggerAnalysis(u), "interval")
			if got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
	if FlaggerAnalysis(nil) != nil {
		t.Fatal("nil Canary has analysis")
	}
}

func TestFlaggerNativeTargetReference(t *testing.T) {
	for _, tc := range []struct{ kind, version, group string }{
		{"Deployment", "ignored.example/v1", "apps"},
		{"DaemonSet", "apps/v1", "apps"},
		{"Service", "v1", ""},
		{"Service", "serving.knative.dev/v1", "serving.knative.dev"},
		{"Service", "serving.knative.dev/v1beta1", ""},
	} {
		t.Run(tc.kind+tc.version, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"namespace": "release"},
				"spec": map[string]any{"targetRef": map[string]any{"apiVersion": tc.version, "kind": tc.kind, "name": "app", "namespace": "ignored"}},
			}}
			ref, ok := FlaggerTargetReference(u)
			if !ok || ref != (resourceid.Ref{Group: tc.group, Kind: tc.kind, Namespace: "release", Name: "app"}) {
				t.Fatalf("reference=%+v/%v", ref, ok)
			}
			u.SetAPIVersion("other.example/v1")
			if _, ok := FlaggerTargetReference(u); ok {
				t.Fatal("foreign group received native target semantics")
			}
		})
	}
}

func TestFlaggerMetricTemplateReferencesFollowNativeClient(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"namespace": "release"},
		"spec": map[string]any{"analysis": map[string]any{"metrics": []any{
			map[string]any{"name": "builtin"},
			map[string]any{"query": "inline"},
			map[string]any{"templateRef": map[string]any{"name": "latency", "apiVersion": "other.example/v1", "kind": "Unrelated"}},
			map[string]any{"templateRef": map[string]any{"name": "latency"}},
			map[string]any{"templateRef": map[string]any{"name": "latency", "namespace": "metrics"}},
		}}},
	}}
	want := []resourceid.Ref{
		{Group: "flagger.app", Kind: "MetricTemplate", Namespace: "release", Name: "latency"},
		{Group: "flagger.app", Kind: "MetricTemplate", Namespace: "metrics", Name: "latency"},
	}
	if got := FlaggerMetricTemplateReferences(u); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v; want %+v", got, want)
	}
	u.SetAPIVersion("other.example/v1")
	if len(FlaggerMetricTemplateReferences(u)) != 0 {
		t.Fatal("foreign group received template semantics")
	}
}

func TestFlaggerProviderSecretStaysWithTemplate(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "MetricTemplate", "metadata": map[string]any{"namespace": "metrics"},
		"spec": map[string]any{"provider": map[string]any{"secretRef": map[string]any{"name": "credentials", "namespace": "ignored"}}},
	}}
	got, ok := FlaggerProviderSecretReference(u)
	if !ok || got != (resourceid.Ref{Kind: "Secret", Namespace: "metrics", Name: "credentials"}) {
		t.Fatalf("got %+v/%v", got, ok)
	}
	u.SetKind("Canary")
	if _, ok := FlaggerProviderSecretReference(u); ok {
		t.Fatal("non-template received provider Secret semantics")
	}
}
