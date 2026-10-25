package k8s

import (
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
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

// fluxDiscovery installs a discovery singleton over a fake whose OCIRepository
// is served at version, with a short TTL so it expires quickly.
func fluxDiscovery(t *testing.T, version string) (*fakediscovery.FakeDiscovery, *ResourceDiscovery) {
	t.Helper()
	withCleanDiscoveryGlobals(t)
	fakeDisc := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = servedOCIRepository(version)
	core, err := k8score.NewResourceDiscovery(fakeDisc, k8score.WithDiscoveryCacheTTL(time.Millisecond))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	d := &ResourceDiscovery{ResourceDiscovery: core}
	discoveryMu.Lock()
	resourceDiscovery = d
	discoveryMu.Unlock()
	return fakeDisc, d
}

func servedOCIRepository(version string) []*metav1.APIResourceList {
	return []*metav1.APIResourceList{{
		GroupVersion: "source.toolkit.fluxcd.io/" + version,
		APIResources: []metav1.APIResource{{Name: "ocirepositories", Kind: "OCIRepository", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch", "patch"}}},
	}}
}

func waitForServedVersion(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		gvr, ok := ServedGVR("source.toolkit.fluxcd.io", "OCIRepository")
		if ok && gvr.Version == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %s, got %v %v", want, gvr, ok)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A CRD upgrade can retire the version discovery last saw. Write paths resolve
// through ServedGVR, so it must refresh expired discovery itself rather than
// wait for some read path to do it.
func TestServedGVRRefreshesExpiredDiscovery(t *testing.T) {
	fakeDisc, d := fluxDiscovery(t, "v1beta2")
	if gvr, ok := d.GetGVRWithGroup("OCIRepository", "source.toolkit.fluxcd.io"); !ok || gvr.Version != "v1beta2" {
		t.Fatalf("before upgrade: %v %v", gvr, ok)
	}
	// Changed before the first ServedGVR, so no background refresh reads the
	// fake while it is written.
	fakeDisc.Resources = servedOCIRepository("v1")
	waitForServedVersion(t, "v1")
}

// A stalled discovery endpoint must not hold a write action hostage, and must
// not pile up a goroutine per call: callers get the cached answer while one
// refresh runs, and a later expiry can refresh again.
func TestServedGVRDoesNotWaitForASlowRefresh(t *testing.T) {
	fakeDisc, d := fluxDiscovery(t, "v1")
	var stalled atomic.Bool
	stalled.Store(true)
	release := make(chan struct{})
	fakeDisc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		if stalled.Load() {
			<-release
		}
		return false, nil, nil
	})
	time.Sleep(5 * time.Millisecond)

	before := goruntime.NumGoroutine()
	done := make(chan string, 1)
	go func() {
		for range 200 {
			if gvr, ok := ServedGVR("source.toolkit.fluxcd.io", "OCIRepository"); !ok || gvr.Version != "v1" {
				done <- "want the cached v1"
				return
			}
		}
		done <- ""
	}()
	select {
	case msg := <-done:
		if msg != "" {
			t.Fatal(msg)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("ServedGVR waited on a stalled discovery refresh")
	}
	if grew := goruntime.NumGoroutine() - before; grew > 3 {
		t.Errorf("%d goroutines parked behind one stalled refresh", grew)
	}

	stalled.Store(false)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for d.backgroundRefresh.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the stalled refresh never released its slot")
		}
		time.Sleep(time.Millisecond)
	}
	fakeDisc.Resources = servedOCIRepository("v2")
	waitForServedVersion(t, "v2")
}

// A kind installed after discovery's last refresh resolves on the first
// lookup that misses it, and further misses don't refresh again within the
// interval.
func TestLookupRefreshingOnMiss(t *testing.T) {
	fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	core, err := k8score.NewResourceDiscovery(fakeDiscovery, k8score.WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d := &ResourceDiscovery{ResourceDiscovery: core}
	widgets := func(d *ResourceDiscovery) (schema.GroupVersionResource, bool) {
		return d.GetGVRWithGroup("Widget", "example.test")
	}
	if _, ok := d.GetGVRWithGroup("Widget", "example.test"); ok {
		t.Fatal("widget resolved before it was installed")
	}
	fakeDiscovery.Resources = []*metav1.APIResourceList{{
		GroupVersion: "example.test/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}}},
	}}
	before := len(fakeDiscovery.Actions())
	if gvr, ok := d.LookupRefreshingOnMiss(widgets); !ok || gvr.Resource != "widgets" {
		t.Fatalf("installed kind = %v, %v", gvr, ok)
	}
	refreshed := len(fakeDiscovery.Actions())
	if refreshed == before {
		t.Fatal("a miss must refresh discovery")
	}
	if _, ok := d.LookupRefreshingOnMiss(widgets); !ok || len(fakeDiscovery.Actions()) != refreshed {
		t.Fatal("a hit must not refresh discovery")
	}
	if _, ok := d.LookupRefreshingOnMiss(func(d *ResourceDiscovery) (schema.GroupVersionResource, bool) { return d.GetGVR("gadgets") }); ok || len(fakeDiscovery.Actions()) != refreshed {
		t.Fatal("a miss within the interval must not refresh discovery again")
	}
	if _, ok := (*ResourceDiscovery)(nil).LookupRefreshingOnMiss(widgets); ok {
		t.Fatal("nil discovery resolved a kind")
	}
}
