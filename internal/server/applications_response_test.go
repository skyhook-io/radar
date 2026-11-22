package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestApplicationsRetainAttachmentsAboveTopologyDisplayLimit(t *testing.T) {
	selector := map[string]string{"app": "api"}
	objects := testNamespaces("prod")
	objects = append(objects,
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: selector}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}},
			{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
		}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: selector}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "settings"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "data"}},
	)
	for i := range topology.LargeClusterThreshold {
		objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: fmt.Sprintf("idle-%d", i)}})
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	cache := k8s.GetResourceCache()
	display, err := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).Build(topology.DefaultBuildOptions())
	if err != nil || !display.RequiresNamespaceFilter || len(display.Nodes) != 0 {
		t.Fatalf("display guard not exercised: topology=%+v err=%v", display, err)
	}
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	for _, namespaces := range [][]string{nil, {"prod"}} {
		for range 2 {
			resp, err := listApplicationsForRequest(context.Background(), namespaces, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Applications) != 1 {
				t.Fatalf("rows=%d", len(resp.Applications))
			}
			row := resp.Applications[0]
			rel := row.Relationships
			if rel == nil || len(rel.ServiceRefs) != 1 || rel.ServiceRefs[0].Name != "api" || rel.Configs != 1 || len(rel.ConfigRefs) != 1 || rel.ConfigRefs[0].Name != "settings" || rel.Storage != 1 || len(rel.StorageRefs) != 1 || rel.StorageRefs[0].Name != "data" {
				t.Fatalf("namespaces=%v attachments=%+v", namespaces, rel)
			}
			if row.WorkloadClass != "service" || row.Workloads[0].WorkloadClass != "service" {
				t.Fatalf("exposure classification=%s/%s", row.WorkloadClass, row.Workloads[0].WorkloadClass)
			}
		}
	}
}

func queuedApplicationsBuildFixture(t *testing.T) {
	t.Helper()
	objects := append(testNamespaces("prod"), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}})
	useTestResourceCache(t, fake.NewClientset(objects...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
}

// A cold build queued behind a slow one gets its whole budget once admitted.
func TestApplicationsQueuedBuildGetsFullBudget(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	prev := applicationsBuildBudget
	applicationsBuildBudget = 300 * time.Millisecond
	t.Cleanup(func() { applicationsBuildBudget = prev })

	applicationsBuildSlot <- struct{}{} // a slow build holds the slot
	done := make(chan error, 1)
	go func() {
		_, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false)
		done <- err
	}()
	time.Sleep(2 * applicationsBuildBudget)
	<-applicationsBuildSlot
	if err := <-done; err != nil {
		t.Fatalf("queued build failed: %v", err)
	}
}

// A request that ends while its build is still queued reads as a busy
// build, not an internal error.
func TestApplicationsQueuedBuildRequestEnds(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	applicationsBuildSlot <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := listApplicationsForRequest(ctx, []string{"prod"}, false); !errors.Is(err, errResourceCacheUnavailable) {
		t.Fatalf("err = %v, want errResourceCacheUnavailable", err)
	}
	releaseQueuedApplicationsBuild(t)
}

// releaseQueuedApplicationsBuild frees the held slot and waits out the build
// that was queued behind it, which outlives the requests that started it.
func releaseQueuedApplicationsBuild(t *testing.T) {
	t.Helper()
	<-applicationsBuildSlot
	if _, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false); err != nil {
		t.Fatalf("queued build failed after the slot freed: %v", err)
	}
}

// startQueuedApplicationsRequest starts a request for the queued fixture's key
// and gives it time to join the build waiting for the held slot.
func startQueuedApplicationsRequest(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := listApplicationsForRequest(ctx, []string{"prod"}, false)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	return done
}

// The request that started a shared build ending doesn't fail the requests
// waiting on the same build.
func TestApplicationsBuildOutlivesTheRequestThatStartedIt(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	applicationsBuildSlot <- struct{}{}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := startQueuedApplicationsRequest(leaderCtx)
	follower := startQueuedApplicationsRequest(context.Background())
	cancelLeader()
	if err := <-leader; !errors.Is(err, errResourceCacheUnavailable) {
		t.Fatalf("canceled request err = %v, want errResourceCacheUnavailable", err)
	}
	<-applicationsBuildSlot
	if err := <-follower; err != nil {
		t.Fatalf("waiting request failed with the canceled one: %v", err)
	}
}

// A request waiting on a build another request started leaves on its own
// deadline rather than staying until the build ends.
func TestApplicationsBuildWaiterLeavesOnItsOwnDeadline(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	applicationsBuildSlot <- struct{}{}
	leader := startQueuedApplicationsRequest(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	select {
	case err := <-startQueuedApplicationsRequest(ctx):
		if !errors.Is(err, errResourceCacheUnavailable) {
			t.Fatalf("waiter err = %v, want errResourceCacheUnavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter stayed past its deadline while the build was queued")
	}
	<-applicationsBuildSlot
	if err := <-leader; err != nil {
		t.Fatalf("starting request failed: %v", err)
	}
}

// A build that never gets its slot gives up on its own, whatever its callers'
// contexts allow.
func TestApplicationsQueuedBuildWaitIsBounded(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	prev := applicationsBuildQueueWait
	applicationsBuildQueueWait = 50 * time.Millisecond
	t.Cleanup(func() { applicationsBuildQueueWait = prev })
	applicationsBuildSlot <- struct{}{}
	t.Cleanup(func() { <-applicationsBuildSlot })
	select {
	case err := <-startQueuedApplicationsRequest(context.Background()):
		if !errors.Is(err, errResourceCacheUnavailable) {
			t.Fatalf("err = %v, want errResourceCacheUnavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued build waited past its bound")
	}
}

func listApplicationsStatus() int {
	w := httptest.NewRecorder()
	(&Server{}).handleListApplications(w, httptest.NewRequest(http.MethodGet, "/api/applications?namespaces=prod", nil))
	return w.Code
}

func assertNoApplicationsCached(t *testing.T) {
	t.Helper()
	applicationsCacheMu.Lock()
	defer applicationsCacheMu.Unlock()
	if len(applicationsCache) != 0 {
		t.Fatalf("an unfinished build was cached: %v", applicationsCache)
	}
}

// A build that runs out of its budget answers busy, which the UI retries, not
// an internal error.
func TestApplicationsBuildOutOfTimeAnswersBusy(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	prev := applicationsBuildBudget
	applicationsBuildBudget = time.Nanosecond
	t.Cleanup(func() { applicationsBuildBudget = prev })
	if got := listApplicationsStatus(); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
	assertNoApplicationsCached(t)
}

// enrichmentBlockedFixture is the queued fixture with an Argo CD Application
// kind whose list blocks until release, which holds a build inside the Argo CD
// enrichment. entered closes once a build gets there.
func enrichmentBlockedFixture(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}})...))
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ApplicationList"})
	reached, unblock := make(chan struct{}), make(chan struct{})
	var reachedOnce, unblockOnce sync.Once
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		reachedOnce.Do(func() { close(reached) })
		<-unblock
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Application", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	release = func() {
		unblockOnce.Do(func() { close(unblock) })
		// The blocked list belongs to a watch start that outlives the build.
		_ = k8s.GetDynamicResourceCache().EnsureWatching(gvr)
	}
	t.Cleanup(release)
	return reached, release
}

// A build that runs out of time inside the Argo CD enrichment would answer
// rows without their sources: it answers busy and caches nothing.
func TestApplicationsBuildOutOfTimeInEnrichmentIsNotPublished(t *testing.T) {
	entered, release := enrichmentBlockedFixture(t)
	prev := applicationsBuildBudget
	applicationsBuildBudget = time.Second
	t.Cleanup(func() { applicationsBuildBudget = prev })
	status := listApplicationsStatus()
	select {
	case <-entered:
	default:
		t.Fatal("the build ran out of time before the enrichment")
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	assertNoApplicationsCached(t)
	release()
}

// A context switch while the enrichment reads the dynamic cache would mix two
// clusters in one answer: the build answers busy and caches nothing.
func TestApplicationsBuildAcrossAContextSwitchIsNotPublished(t *testing.T) {
	entered, release := enrichmentBlockedFixture(t)
	status := make(chan int, 1)
	go func() { status <- listApplicationsStatus() }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the build never reached the enrichment")
	}
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(testNamespaces("prod")...)); err != nil {
		t.Fatal(err)
	}
	release()
	if got := <-status; got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
	assertNoApplicationsCached(t)
}

// Rows grouped while a manager kind is still loading can regroup once it has
// loaded: they are answered, not cached, and the first build after the load
// is cached.
func TestApplicationsBuildWhileAManagerKindLoadsIsNotCached(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}})...))
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ApplicationList"})
	loaded := make(chan struct{})
	var loadOnce sync.Once
	load := func() { loadOnce.Do(func() { close(loaded) }) }
	// The first list is the access probe; the informer's own list waits.
	var lists atomic.Int32
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) > 1 {
			select {
			case <-loaded:
			case <-time.After(10 * time.Second):
			}
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Application", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(load)
	addApplicationTypedDiscovery()
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if dc.WaitForSync(gvr, 10*time.Millisecond) {
		t.Fatal("Applications loaded before the build could see them loading")
	}

	resp, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false)
	if err != nil || len(resp.Applications) != 1 {
		t.Fatalf("rows while loading = %+v, err = %v", resp, err)
	}
	assertNoApplicationsCached(t)

	load()
	if !dc.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("Applications never loaded")
	}
	if _, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false); err != nil {
		t.Fatal(err)
	}
	applicationsCacheMu.Lock()
	cached := len(applicationsCache)
	applicationsCacheMu.Unlock()
	if cached != 1 {
		t.Fatalf("cached builds after the load = %d, want 1", cached)
	}
}

// A build that panics after it is admitted fails every request waiting on it
// with a 500 that doesn't carry the panic, not the process, and leaves its slot
// free and nothing cached: the next build, of these rows or others, succeeds.
func TestApplicationsBuildPanicFailsItsWaiters(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	t.Cleanup(func() { applicationsBuildTestHooks.joined, applicationsBuildTestHooks.admitted = nil, nil })
	joined, release := make(chan struct{}, 2), make(chan struct{})
	applicationsBuildTestHooks.joined = func() { joined <- struct{}{} }
	applicationsBuildTestHooks.admitted = func() {
		<-release
		panic("secret build detail")
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			w := httptest.NewRecorder()
			(&Server{}).handleListApplications(w, httptest.NewRequest(http.MethodGet, "/api/applications?namespaces=prod", nil))
			responses <- w
		}()
	}
	<-joined
	<-joined
	close(release)
	for range 2 {
		w := <-responses
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret build detail") {
			t.Fatalf("waiter answered %d %q, want a 500 without the panic", w.Code, w.Body.String())
		}
	}
	applicationsBuildTestHooks.joined, applicationsBuildTestHooks.admitted = nil, nil
	if held := len(applicationsBuildSlot); held != 0 {
		t.Fatalf("the panicked build left the slot held")
	}
	assertNoApplicationsCached(t)

	if status := listApplicationsStatus(); status != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", status)
	}
	if _, err := listApplicationsForRequest(context.Background(), nil, false); err != nil {
		t.Fatalf("another key's build failed: %v", err)
	}
}
