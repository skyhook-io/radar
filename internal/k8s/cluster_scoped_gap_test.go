package k8s

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// namespaceUser reads one namespace and counts the kind checks it is asked.
type namespaceUser struct{ checks *int }

func (u namespaceUser) CanRead(resourceid.GroupKind, string) ownership.Decision {
	*u.checks++
	return ownership.Allowed
}
func (namespaceUser) Namespaces() ([]string, bool) { return []string{"shop"}, false }

// A Node's drawer searches every cluster-scoped kind for its dependents, and
// each kind Radar doesn't collect leaves a gap. Those gaps name no object, so
// they cost a namespace-limited user no permission check each: the checks
// don't grow with the kinds Radar doesn't collect.
func TestNodeDrawerChecksDontGrowWithUncollectedClusterKinds(t *testing.T) {
	checks := func(uncollected int) int {
		node := &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker"}}
		core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(node), ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Nodes: true}, DeferredTypes: map[string]bool{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(core.Stop)
		discovery := buildFakeDiscoveryCore(t)
		verbs := metav1.Verbs{"delete", "get", "list", "watch"}
		discovery.AddAPIResource(k8score.APIResource{Name: "pods", Kind: "Pod", Version: "v1", Namespaced: true, Verbs: verbs})
		discovery.AddAPIResource(k8score.APIResource{Name: "nodes", Kind: "Node", Version: "v1", Verbs: verbs})
		for i := range uncollected {
			discovery.AddAPIResource(k8score.APIResource{Name: fmt.Sprintf("things%d", i), Kind: fmt.Sprintf("Thing%d", i), Group: "example.io", Version: "v1", Verbs: verbs})
		}
		previous := ownershipSource
		ownershipSource = cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: core, Discovery: discovery} })
		t.Cleanup(func() { ownershipSource = previous })
		n := 0
		access := func(*ownership.Catalog) ownership.Access { return namespaceUser{&n} }
		rel, _, _ := AnswerRelationships(&ResourceCache{ResourceCache: core}, node, access, access)
		if rel == nil || rel.Ownership == nil {
			t.Fatalf("no answer: %+v", rel)
		}
		return n
	}
	none, five := checks(0), checks(5)
	t.Logf("kind checks: %d with no uncollected cluster-scoped kinds, %d with five", none, five)
	if five != none {
		t.Fatalf("a Node's drawer made %d kind checks with no uncollected cluster-scoped kinds and %d with five", none, five)
	}
}
