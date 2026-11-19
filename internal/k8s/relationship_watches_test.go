package k8s

import (
	"context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/topology"
	"go/ast"
	"go/parser"
	"go/token"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRelationshipKindsHaveFallbacks(t *testing.T) {
	for _, k := range relationshipWatchKinds() {
		found := false
		for _, c := range supportedCRDFallbacks {
			if c.Group == k.Group && c.Kind == k.Kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("relationship kind %s has no supportedCRDFallbacks entry", k)
		}
	}
}

type recordingRelationshipProvider struct {
	*k8score.ResourceDiscovery
	listed  map[schema.GroupVersionResource]bool
	objects map[schema.GroupVersionResource][]*unstructured.Unstructured
}

func (p *recordingRelationshipProvider) List(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	p.listed[gvr] = true
	return p.objects[gvr], nil
}
func (p *recordingRelationshipProvider) ListNamespaces(gvr schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	return p.List(gvr, "")
}
func (p *recordingRelationshipProvider) Get(schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (p *recordingRelationshipProvider) GetWatchedResources() []schema.GroupVersionResource {
	return nil
}
func (p *recordingRelationshipProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}

func TestRelationshipActivationAgainstBuilder(t *testing.T) {
	client := fakeclientset.NewSimpleClientset()
	// Serve the entire supported catalog, including kinds outside the activation
	// set, so a missing declaration cannot hide itself from the builder.
	for _, c := range supportedCRDFallbacks {
		client.Fake.Resources = append(client.Fake.Resources, &metav1.APIResourceList{
			GroupVersion: schema.GroupVersion{Group: c.Group, Version: c.Versions[0]}.String(),
			APIResources: []metav1.APIResource{{Name: c.Resource, Kind: c.Kind, Namespaced: c.Namespaced, Verbs: []string{"get", "list", "watch"}}},
		})
	}
	discovery, err := k8score.NewResourceDiscovery(client.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	// Independent graph lookup literals keep a missing fallback from making
	// the builder silently skip that API in this fixture.
	files, err := filepath.Glob("../../pkg/topology/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "GetGVRWithGroup" {
				return true
			}
			kind, ok := call.Args[0].(*ast.BasicLit)
			if !ok || kind.Kind != token.STRING {
				return true
			}
			group, ok := call.Args[1].(*ast.BasicLit)
			if !ok || group.Kind != token.STRING {
				return true
			}
			k, _ := strconv.Unquote(kind.Value)
			g, _ := strconv.Unquote(group.Value)
			if _, exists := discovery.GetGVRWithGroup(k, g); !exists {
				gvr, _ := meta.UnsafeGuessKindToResource(schema.GroupVersionKind{Group: g, Version: "v1", Kind: k})
				discovery.AddAPIResource(k8score.APIResource{Group: g, Version: "v1", Kind: k, Name: gvr.Resource, IsCRD: true, Namespaced: true, Verbs: []string{"get", "list", "watch"}})
			}
			return true
		})
	}
	p := &recordingRelationshipProvider{ResourceDiscovery: discovery, listed: map[schema.GroupVersionResource]bool{}, objects: map[schema.GroupVersionResource][]*unstructured.Unstructured{}}
	// NodeClass listing is data-dependent: exercise every named provider.
	pools, _ := discovery.GetGVRWithGroup("NodePool", "karpenter.sh")
	for _, k := range relations.Kinds() {
		if k.Kind != "NodeClass" && !strings.HasSuffix(k.Kind, "NodeClass") {
			continue
		}
		p.objects[pools] = append(p.objects[pools], &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": k.Group},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": k.Group, "kind": k.Kind, "name": "default"}}}},
		}})
	}
	rc, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{k8score.Pods: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	opts := topology.DefaultBuildOptions()
	opts.ForRelationshipCache = true
	opts.IncludeReplicaSets = true
	if _, err := topology.NewBuilder(NewTopologyResourceProvider(&ResourceCache{ResourceCache: rc})).WithDynamic(p).Build(opts); err != nil {
		t.Fatal(err)
	}
	declared := map[schema.GroupVersionResource]bool{}
	var newWatches, missed []string
	for _, k := range relationshipWatchKinds() {
		gvr, ok := discovery.GetGVRWithGroup(k.Kind, k.Group)
		if !ok {
			t.Fatalf("fixture does not serve %s", k)
		}
		declared[gvr] = true
		if !p.listed[gvr] {
			newWatches = append(newWatches, gvr.String())
		}
	}
	for gvr := range p.listed {
		if !declared[gvr] {
			missed = append(missed, gvr.String())
		}
	}
	slices.Sort(newWatches)
	slices.Sort(missed)
	t.Logf("Declared but graph does not list (%d):\n%s", len(newWatches), strings.Join(newWatches, "\n"))
	t.Logf("Graph lists but not declared (%d):\n%s", len(missed), strings.Join(missed, "\n"))
	raw, err := os.ReadFile("testdata/relationship-watch-additions.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if !reflect.DeepEqual(newWatches, want) {
		t.Errorf("activation expansion changed: got %v, want %v", newWatches, want)
	}
	if len(missed) != 0 {
		t.Errorf("graph starts undeclared watches: %v", missed)
	}
	for _, k := range relations.Kinds() {
		t.Logf("Declared: %s/%s", k.Group, k.Kind)
	}
}

func TestRelationshipWarmupBeforeDeferredGateAndAfterReplacement(t *testing.T) {
	client := fakeclientset.NewSimpleClientset()
	client.Fake.Resources = []*metav1.APIResourceList{{GroupVersion: "keda.sh/v1alpha1", APIResources: []metav1.APIResource{{Name: "scaledjobs", Kind: "ScaledJob", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}}}
	discovery, err := k8score.NewResourceDiscovery(client.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	makeCache := func() *k8score.DynamicResourceCache {
		c, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{Discovery: discovery, RequiredWatchKinds: relationshipWatchKinds(), DynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{fatKnownCRD: "ScaledJobList"})})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Stop)
		return c
	}
	old := makeCache()
	current := old
	stillCurrent := func() bool { return current == old }
	gate := make(chan struct{})
	finished := make(chan struct{})
	runCRDWarmupSequence(context.Background(), nil, stillCurrent, old.StartWatchReconciler)
	go func() {
		runCRDWarmupSequence(context.Background(), gate, stillCurrent, func() { t.Error("replaced cache ran deferred warmup") })
		close(finished)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !old.IsSynced(fatKnownCRD) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !old.IsSynced(fatKnownCRD) {
		t.Fatal("relationship activation waited for deferred gate")
	}
	old.Stop()
	current = makeCache()
	runCRDWarmupSequence(context.Background(), nil, stillCurrent, func() { t.Error("stale startup passed the stillCurrent guard") })
	runCRDWarmupSequence(context.Background(), nil, func() bool { return current != old }, current.StartWatchReconciler)
	deadline = time.Now().Add(2 * time.Second)
	for !current.IsSynced(fatKnownCRD) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !current.IsSynced(fatKnownCRD) {
		t.Fatal("replacement cache did not activate")
	}
	close(gate)
	<-finished
}
