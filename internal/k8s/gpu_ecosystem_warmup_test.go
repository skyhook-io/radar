package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestGPUEcosystemRegistryIsInSupportedWarmupCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "gpu-ecosystem-demo", "resources.tsv"))
	if err != nil {
		t.Fatalf("read GPU ecosystem registry: %v", err)
	}

	type catalogKey struct {
		group    string
		resource string
	}
	type lookupKey struct {
		group string
		kind  string
	}
	catalog := make(map[catalogKey]supportedCRDResource, len(supportedCRDFallbacks))
	lookups := make(map[lookupKey]supportedCRDResource, len(supportedCRDFallbacks))
	for _, candidate := range supportedCRDFallbacks {
		key := catalogKey{group: candidate.Group, resource: candidate.Resource}
		if previous, exists := catalog[key]; exists {
			t.Fatalf("duplicate supported dynamic resource %s.%s: %s and %s", candidate.Resource, candidate.Group, previous.Kind, candidate.Kind)
		}
		catalog[key] = candidate
		lookup := lookupKey{group: candidate.Group, kind: candidate.Kind}
		if previous, exists := lookups[lookup]; exists {
			t.Fatalf("ambiguous supported dynamic kind %s.%s: %s and %s", candidate.Kind, candidate.Group, previous.Resource, candidate.Resource)
		}
		lookups[lookup] = candidate
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 || strings.TrimPrefix(lines[0], "# ") != "logical_id\tgroup\tplural\tkind\tscope\tapi_version\tscenario\tcrd_url" {
		t.Fatalf("unexpected GPU ecosystem registry header: %q", lines[0])
	}
	for lineNumber, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 8 {
			t.Fatalf("registry line %d has %d fields, want 8", lineNumber+2, len(fields))
		}
		group, resource, kind, scope, version := fields[1], fields[2], fields[3], fields[4], fields[5]
		candidate, ok := catalog[catalogKey{group: group, resource: resource}]
		if !ok {
			t.Errorf("%s.%s is curated but absent from supportedCRDFallbacks", resource, group)
			continue
		}
		if candidate.Kind != kind {
			t.Errorf("%s.%s kind = %s, want %s", resource, group, candidate.Kind, kind)
		}
		if candidate.Namespaced != (scope == "Namespaced") {
			t.Errorf("%s.%s namespaced = %t, registry scope = %s", resource, group, candidate.Namespaced, scope)
		}
		if len(candidate.Versions) == 0 || candidate.Versions[0] != version {
			t.Errorf("%s.%s preferred fallback version = %v, want registry version %s first", resource, group, candidate.Versions, version)
		}
	}
}

func TestRecognizedLargeResourceWarmsWhileUnknownLargeResourceDefers(t *testing.T) {
	ResetTestDynamicState()
	t.Cleanup(ResetTestDynamicState)

	recognized := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	listOnly := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "localqueues"}
	unknown := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}

	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			recognized: "WorkloadList",
			unknown:    "WidgetList",
		},
	)
	dynamicClient.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		remaining := int64(101)
		list := &unstructured.UnstructuredList{}
		list.SetRemainingItemCount(&remaining)
		return true, list, nil
	})
	if err := InitTestDynamicResourceCache(dynamicClient, []APIResource{
		{Group: recognized.Group, Version: recognized.Version, Name: recognized.Resource, Kind: "Workload", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: listOnly.Group, Version: listOnly.Version, Name: listOnly.Resource, Kind: "LocalQueue", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}},
		{Group: unknown.Group, Version: unknown.Version, Name: unknown.Resource, Kind: "Widget", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	cache := GetDynamicResourceCache()

	WarmupCommonCRDs()
	if !cache.WaitForSync(recognized, 3*time.Second) {
		t.Fatal("recognized Workload informer did not sync")
	}
	if got := cache.Observation(recognized); got.State != k8score.DynamicObservationWatched || got.Origin != k8score.DynamicObservationOriginWarmup {
		t.Fatalf("recognized large resource observation = %+v, want watched warmup", got)
	}
	if got := cache.Observation(listOnly); got.State != k8score.DynamicObservationUnsupported || got.ReasonCode != "list_watch_unsupported" {
		t.Fatalf("recognized list-only resource observation = %+v, want unsupported", got)
	}

	cache.DiscoverAllCRDs()
	deadline := time.Now().Add(3 * time.Second)
	for cache.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cache.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		t.Fatal("CRD discovery did not complete")
	}
	if got := cache.Observation(unknown); got.State != k8score.DynamicObservationDeferred || got.ReasonCode != "resource_count_exceeds_eager_limit" {
		t.Fatalf("unknown large resource observation = %+v, want size-gated deferred", got)
	}
	if got := cache.Observation(recognized); got.Origin != k8score.DynamicObservationOriginWarmup {
		t.Fatalf("full discovery replaced recognized warmup origin: %+v", got)
	}
}
