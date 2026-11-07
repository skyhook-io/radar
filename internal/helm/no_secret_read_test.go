package helm

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/k8score"
)

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// recordingObjects is a cache holding stamped Secrets, recording every list.
// The embedded nil Provider panics on a method the read starts to need, so a
// new read path has to be added here on purpose.
type recordingObjects struct {
	search.Provider
	typed         map[string][]runtime.Object
	dynamic       map[schema.GroupVersionResource][]*unstructured.Unstructured
	listedTyped   []string
	listedDynamic []string
}

func (p *recordingObjects) ListTyped(resource string, _ []string) ([]runtime.Object, error) {
	p.listedTyped = append(p.listedTyped, resource)
	return p.typed[resource], nil
}

func (p *recordingObjects) ListDynamic(_ context.Context, gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	p.listedDynamic = append(p.listedDynamic, gvr.Resource)
	return p.dynamic[gvr], nil
}

func (p *recordingObjects) DynamicResources() ([]schema.GroupVersionResource, error) {
	return []schema.GroupVersionResource{secretsGVR}, nil
}

func (p *recordingObjects) PlanTyped(_ string, _ []string) readplan.Plan {
	return readplan.Plan{Partitions: []readplan.Partition{{Extent: readplan.AllNamespaces, Verdict: readplan.Verdict{State: readplan.Readable}}}}
}

func (p *recordingObjects) PlanDynamic(gvr schema.GroupVersionResource, namespaces []string, clusterScoped bool) readplan.Plan {
	return readplan.Dynamic(p, gvr, namespaces, clusterScoped)
}

func (p *recordingObjects) NamespaceObservation(schema.GroupVersionResource, string) k8score.DynamicResourceObservation {
	return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
}

func (p *recordingObjects) KindForGVR(gvr schema.GroupVersionResource) string {
	if gvr == secretsGVR {
		return "Secret"
	}
	return ""
}

func (p *recordingObjects) DynamicCacheStopped() bool                                { return false }
func (p *recordingObjects) PrepareDynamicObservations([]schema.GroupVersionResource) {}
func (p *recordingObjects) KnownMissingNamespaces([]string) []string                 { return nil }
func (p *recordingObjects) Current() bool                                            { return true }

func stampedMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: "apps", Name: name, Annotations: map[string]string{
		releaseNameAnnotation:      "web",
		releaseNamespaceAnnotation: "apps",
	}}
}

// TestObservedReadsNeverListSecrets covers both observed routes end to end:
// the promise is that no Secret is read, which a response can't show.
func TestObservedReadsNeverListSecrets(t *testing.T) {
	newProvider := func() *recordingObjects {
		return &recordingObjects{
			typed: map[string][]runtime.Object{
				"secrets":    {&corev1.Secret{ObjectMeta: stampedMeta("sh.helm.release.v1.web.v1")}},
				"configmaps": {&corev1.ConfigMap{ObjectMeta: stampedMeta("cfg")}},
			},
			dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
				secretsGVR: {{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": "apps", "name": "other"}}}},
			},
		}
	}
	assertNoSecretRead := func(t *testing.T, p *recordingObjects) {
		t.Helper()
		if slices.Contains(p.listedTyped, "secrets") || slices.Contains(p.listedDynamic, "secrets") {
			t.Fatalf("Secrets were listed: typed %v, dynamic %v", p.listedTyped, p.listedDynamic)
		}
		if !slices.Contains(p.listedTyped, "configmaps") {
			t.Fatalf("the read listed nothing, so this proves nothing: %v", p.listedTyped)
		}
	}

	t.Run("list", func(t *testing.T) {
		p := newProvider()
		inventory, err := (&Client{}).ReadInventory(context.Background(), InventoryRequest{StorageNamespaces: []string{}, Scope: &ObjectScope{Provider: p}})
		if err != nil {
			t.Fatal(err)
		}
		if len(inventory.Observed) != 1 || inventory.Observed[0].Name != "web" || inventory.Observed[0].ResourceCount != 1 {
			t.Fatalf("observed = %+v, want web with only its ConfigMap", inventory.Observed)
		}
		assertNoSecretRead(t, p)
	})

	t.Run("detail", func(t *testing.T) {
		p := newProvider()
		detail, err := GetObservedRelease(context.Background(), &ObjectScope{Provider: p}, "apps", "web")
		if err != nil {
			t.Fatal(err)
		}
		if detail == nil || detail.ResourceCount != 1 || detail.Resources[0].Kind != "ConfigMap" {
			t.Fatalf("detail = %+v, want web with only its ConfigMap", detail)
		}
		assertNoSecretRead(t, p)
	})
}
