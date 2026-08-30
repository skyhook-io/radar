package k8s

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestIsMoreStableVersion(t *testing.T) {
	tests := []struct {
		newVer string
		oldVer string
		want   bool
	}{
		{"v1", "v1alpha1", true},
		{"v1", "v1beta1", true},
		{"v1beta1", "v1alpha1", true},
		{"v1alpha1", "v1beta1", false},
		{"v1beta1", "v1", false},
		{"v2", "v1", true},
		{"v1", "v2", false},
		{"v1beta2", "v1beta1", true},
		{"v1beta1", "v1beta2", false},
	}
	for _, tt := range tests {
		t.Run(tt.newVer+"_vs_"+tt.oldVer, func(t *testing.T) {
			got := isMoreStableVersion(tt.newVer, tt.oldVer)
			if got != tt.want {
				t.Errorf("isMoreStableVersion(%q, %q) = %v, want %v", tt.newVer, tt.oldVer, got, tt.want)
			}
		})
	}
}

func TestGetGVRWithGroup_DisambiguatesSameKind(t *testing.T) {
	// Create a fake clientset with two CRDs sharing the same Kind but different groups
	client := fakeclientset.NewSimpleClientset()
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "argoproj.io/v1alpha1",
			APIResources: []metav1.APIResource{
				{Name: "applications", Kind: "Application", Namespaced: true, Verbs: metav1.Verbs{"list", "watch", "get"}},
			},
		},
		{
			GroupVersion: "app.k8s.io/v1beta1",
			APIResources: []metav1.APIResource{
				{Name: "applications", Kind: "Application", Namespaced: true, Verbs: metav1.Verbs{"list", "watch", "get"}},
			},
		},
	}

	// Build ResourceDiscovery via the real constructor
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery failed: %v", err)
	}
	d := &ResourceDiscovery{ResourceDiscovery: core}

	// GetGVRWithGroup should return the correct group
	gvr, ok := d.GetGVRWithGroup("Application", "argoproj.io")
	if !ok {
		t.Fatal("expected to find Application in argoproj.io")
	}
	if gvr.Group != "argoproj.io" {
		t.Errorf("expected group argoproj.io, got %s", gvr.Group)
	}
	if gvr.Version != "v1alpha1" {
		t.Errorf("expected version v1alpha1, got %s", gvr.Version)
	}

	gvr, ok = d.GetGVRWithGroup("Application", "app.k8s.io")
	if !ok {
		t.Fatal("expected to find Application in app.k8s.io")
	}
	if gvr.Group != "app.k8s.io" {
		t.Errorf("expected group app.k8s.io, got %s", gvr.Group)
	}
	if gvr.Version != "v1beta1" {
		t.Errorf("expected version v1beta1, got %s", gvr.Version)
	}

	// Non-existent group should return false
	_, ok = d.GetGVRWithGroup("Application", "nonexistent.io")
	if ok {
		t.Error("expected not to find Application in nonexistent.io")
	}
}

// A failed attempt must leave discovery initializable. Discovery starts in
// parallel with the caches, so it can arrive before the client exists; if that
// attempt consumed the one initialization, the next call would report success
// with discovery still nil — a subsystem that never came up, reported as up.
func TestInitResourceDiscoveryStaysRetryableAfterAMissingClient(t *testing.T) {
	clientMu.Lock()
	savedClient := discoveryClient
	discoveryClient = nil
	clientMu.Unlock()
	discoveryMu.Lock()
	savedDiscovery := resourceDiscovery
	savedBinding := resourceDiscoveryClient
	resourceDiscovery = nil
	resourceDiscoveryClient = nil
	discoveryMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		discoveryClient = savedClient
		clientMu.Unlock()
		discoveryMu.Lock()
		resourceDiscovery = savedDiscovery
		resourceDiscoveryClient = savedBinding
		discoveryMu.Unlock()
	})

	if err := InitResourceDiscovery(); err == nil {
		t.Fatal("expected an error when no discovery client exists")
	}
	if GetResourceDiscovery() != nil {
		t.Fatal("a failed init must not publish a discovery singleton")
	}

	// The retry must reach the body again. A sync.Once here returns nil on the
	// second call — the failure spent the attempt and is now reported as
	// success — so erroring twice is what proves retryability.
	if err := InitResourceDiscovery(); err == nil {
		t.Fatal("a second attempt with no client must fail again, not report the spent attempt as success")
	}

	// The telling part: the failure did not spend the attempt.
	fake := fakeclientset.NewSimpleClientset()
	fakeDisc, ok := fake.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("expected a fake discovery client")
	}
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("building discovery against a fake client: %v", err)
	}
	discoveryMu.Lock()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	resourceDiscoveryClient = GetDiscoveryClient()
	discoveryMu.Unlock()

	if err := InitResourceDiscovery(); err != nil {
		t.Errorf("init must succeed once a client exists, got %v", err)
	}
	if GetResourceDiscovery() == nil {
		t.Error("discovery should be initialized after a retry")
	}
}

func buildFakeDiscoveryCore(t *testing.T) *k8score.ResourceDiscovery {
	t.Helper()
	fake := fakeclientset.NewSimpleClientset()
	fakeDisc, ok := fake.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("expected a fake discovery client")
	}
	core, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("building discovery against a fake client: %v", err)
	}
	return core
}

func withCleanDiscoveryGlobals(t *testing.T) {
	t.Helper()
	clientMu.Lock()
	savedClient := discoveryClient
	clientMu.Unlock()
	discoveryMu.Lock()
	savedDiscovery := resourceDiscovery
	savedBinding := resourceDiscoveryClient
	savedEpoch := discoveryEpoch
	resourceDiscovery = nil
	resourceDiscoveryClient = nil
	discoveryMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		discoveryClient = savedClient
		clientMu.Unlock()
		discoveryMu.Lock()
		resourceDiscovery = savedDiscovery
		resourceDiscoveryClient = savedBinding
		discoveryEpoch = savedEpoch
		discoveryMu.Unlock()
	})
}

// A context switch resets discovery BEFORE it swaps the client, so an init
// that snapshots in that gap pairs the current epoch with the previous
// cluster's client. The epoch alone cannot catch that; the publish step must
// notice the client swap and discard the build.
func TestPublishDiscardsABuildWhoseClientWasSwappedOut(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	previousClusterClient := &discovery.DiscoveryClient{}
	newClusterClient := &discovery.DiscoveryClient{}

	clientMu.Lock()
	discoveryClient = previousClusterClient
	clientMu.Unlock()
	discoveryMu.Lock()
	epoch := discoveryEpoch
	discoveryMu.Unlock()

	// The context switch's step 2: the client swaps with no epoch bump.
	clientMu.Lock()
	discoveryClient = newClusterClient
	clientMu.Unlock()

	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, previousClusterClient)
	if GetResourceDiscovery() != nil {
		t.Fatal("a build from a swapped-out client must not be published — it describes the previous cluster")
	}

	// Control: the same call with a current snapshot installs, so the discard
	// above was the guard firing, not a vacuously broken publish.
	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, newClusterClient)
	if GetResourceDiscovery() == nil {
		t.Fatal("a build whose snapshot is still current must be published")
	}
}

func TestPublishDiscardsABuildFromBeforeAReset(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	client := &discovery.DiscoveryClient{}
	clientMu.Lock()
	discoveryClient = client
	clientMu.Unlock()
	discoveryMu.Lock()
	epoch := discoveryEpoch
	discoveryMu.Unlock()

	ResetResourceDiscovery()

	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, client)
	if GetResourceDiscovery() != nil {
		t.Fatal("a build from before a reset must not be published")
	}
}

func TestPublishDoesNotClobberAnAlreadyPublishedSingleton(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	client := &discovery.DiscoveryClient{}
	clientMu.Lock()
	discoveryClient = client
	clientMu.Unlock()
	discoveryMu.Lock()
	epoch := discoveryEpoch
	discoveryMu.Unlock()

	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, client)
	first := GetResourceDiscovery()
	if first == nil {
		t.Fatal("first publish should install")
	}
	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, client)
	if GetResourceDiscovery() != first {
		t.Fatal("a second concurrent build must not replace the one already published")
	}
}

// The gap between a context switch's reset and its client swap has two
// orderings. If the swap lands before the stale build publishes, the pointer
// check in publishResourceDiscovery discards it. This test pins the other one:
// the stale build snapshots AND publishes entirely inside the gap, where the
// old client is still the active one and no publish-time check can tell it
// from a legitimate build. The binding recorded at publish is the defense —
// the moment the swap lands, the singleton stops being served, and the new
// cluster's build replaces it.
func TestSingletonPublishedInTheResetSwapGapDiesWithTheSwap(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	previousClusterClient := &discovery.DiscoveryClient{}
	newClusterClient := &discovery.DiscoveryClient{}

	clientMu.Lock()
	discoveryClient = previousClusterClient
	clientMu.Unlock()
	ResetResourceDiscovery()
	discoveryMu.Lock()
	epoch := discoveryEpoch
	discoveryMu.Unlock()

	stale := buildFakeDiscoveryCore(t)
	publishResourceDiscovery(stale, epoch, previousClusterClient)
	if GetResourceDiscovery() == nil {
		t.Fatal("sanity: inside the gap the stale publish is indistinguishable from a valid one and must install")
	}

	clientMu.Lock()
	discoveryClient = newClusterClient
	clientMu.Unlock()

	if GetResourceDiscovery() != nil {
		t.Fatal("a singleton built against a swapped-out client must not be served")
	}

	publishResourceDiscovery(buildFakeDiscoveryCore(t), epoch, newClusterClient)
	got := GetResourceDiscovery()
	if got == nil {
		t.Fatal("the new cluster's build must replace the stale singleton")
	}
	if got.ResourceDiscovery == stale {
		t.Fatal("the served singleton is still the stale build")
	}
}
