package k8s

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExtractAPIVersionRestoresTypedIdentity(t *testing.T) {
	if got := extractAPIVersion("Deployment", &appsv1.Deployment{}); got != "apps/v1" {
		t.Fatalf("typed Deployment apiVersion = %q, want apps/v1", got)
	}
	custom := &unstructured.Unstructured{}
	custom.SetAPIVersion("batch.volcano.sh/v1alpha1")
	if got := extractAPIVersion("Job", custom); got != "batch.volcano.sh/v1alpha1" {
		t.Fatalf("dynamic Job apiVersion = %q, want exact custom version", got)
	}
	if got := extractAPIVersion("Widget", struct{}{}); got != "" {
		t.Fatalf("unknown typed object apiVersion = %q, want unknown", got)
	}
}

func TestTypedInformerKindsHaveCanonicalAPIVersions(t *testing.T) {
	for _, lister := range k8score.AllKindListers() {
		if _, ok := resourceid.BuiltinAPIVersion(lister.Kind()); !ok {
			t.Errorf("typed informer kind %q has no canonical apiVersion", lister.Kind())
		}
	}
}
