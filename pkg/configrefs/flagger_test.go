package configrefs

import (
	"testing"

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
