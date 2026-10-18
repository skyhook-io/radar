package mcp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

const testListFailureAfter = 300 * time.Millisecond

// failingLists fakes an API whose lists fail per namespace on demand, and
// whose watches the test can break so the informers relist.
type failingLists struct {
	mu       sync.Mutex
	failures map[string]error
	watchers map[string][]*watch.FakeWatcher
}

func newFailingLists() *failingLists {
	return &failingLists{failures: map[string]error{}, watchers: map[string][]*watch.FakeWatcher{}}
}

func (f *failingLists) fail(namespace string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failures, namespace)
		return
	}
	f.failures[namespace] = err
	// Each watch ends with an error, so its informer relists.
	for _, w := range f.watchers[namespace] {
		w.Error(&metav1.Status{Status: metav1.StatusFailure, Code: http.StatusInternalServerError, Message: "watch broke"})
	}
	f.watchers[namespace] = nil
}

func (f *failingLists) failure(namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures[namespace]
}

func (f *failingLists) install(client *ktesting.Fake, resource string) {
	client.PrependReactor("list", resource, func(action ktesting.Action) (bool, runtime.Object, error) {
		if err := f.failure(action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	client.PrependWatchReactor(resource, func(action ktesting.Action) (bool, watch.Interface, error) {
		// A WatchList carries the initial list, so it fails the same way.
		if err := f.failure(action.GetNamespace()); err != nil {
			return true, nil, err
		}
		w := watch.NewFake()
		f.mu.Lock()
		f.watchers[action.GetNamespace()] = append(f.watchers[action.GetNamespace()], w)
		f.mu.Unlock()
		return true, w, nil
	})
}

func (f *failingLists) probe(_ context.Context, _, namespace string) error {
	return f.failure(namespace)
}

func conversionFailure() error {
	return apierrors.NewInternalError(errors.New("conversion webhook for widgets.example.test failed: service not found"))
}

func waitForRead(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func errCode(err error) string {
	if err == nil {
		return ""
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	return code
}

// A typed kind's namespace whose lists keep failing is a gap with its rows
// withheld: kind_stale for a server error, radar_forbidden once Kubernetes
// refuses Radar, and read again once its lists succeed.
func TestResourceReadsStaleTypedNamespace(t *testing.T) {
	lists := newFailingLists()
	client := fake.NewClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "alpha-config", Namespace: "alpha"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "beta-config", Namespace: "beta"}},
	)
	lists.install(&client.Fake, "configmaps")
	t.Cleanup(k8s.SetTestListFailureTracking(testListFailureAfter, lists.probe))
	if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}}, map[string][]string{"configmaps": {"alpha", "beta"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	waitForRead(t, "configmaps to load", func() bool {
		return k8s.GetResourceCache().AssessKindRead("configmaps", []string{"alpha", "beta"}).Complete
	})

	list := func(namespace string) (*listWireCoverage, []map[string]any, error) {
		result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "ConfigMap", Namespace: namespace, Context: "none"})
		if err != nil {
			return nil, nil, err
		}
		rows, coverage := listBody(t, result)
		return &coverage, rows, nil
	}
	get := func(name string) error {
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "ConfigMap", Namespace: "beta", Name: name, Context: "none"})
		return err
	}

	lists.fail("beta", conversionFailure())
	waitForRead(t, "beta to read stale", func() bool {
		coverage, _, err := list("")
		return err == nil && slices.Contains(coverage.Codes, "kind_stale")
	})
	coverage, rows, _ := list("")
	if coverage.Status != "partial" || !slices.Equal(coverage.GapNamespaces["kind_stale"], []string{"beta"}) || !slices.Equal(coverage.Read, []string{"alpha"}) {
		t.Fatalf("all namespaces while beta is stale = %+v", coverage)
	}
	for _, row := range rows {
		if row["namespace"] == "beta" {
			t.Fatalf("a stale namespace's rows were served: %v", row)
		}
	}
	if !strings.Contains(coverage.Summary, "Radar's copy of configmaps there stopped updating, so Radar doesn't use it") {
		t.Fatalf("summary doesn't explain the stale namespace: %s", coverage.Summary)
	}
	_, _, err := list("beta")
	if errCode(err) != "kind_stale" || !strings.Contains(err.Error(), `Radar's copy of configmaps in namespace "beta" has stopped updating, so Radar doesn't use it`) ||
		!strings.Contains(err.Error(), "conversion webhook is down") || strings.Contains(err.Error(), "service not found") {
		t.Fatalf("list of the stale namespace = %v", err)
	}
	// A stale store never confirms an absence.
	if err := get("no-such-config"); errCode(err) != "kind_stale" {
		t.Fatalf("get in the stale namespace = %v", err)
	}

	lists.fail("beta", apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("system:serviceaccount:radar:radar cannot list")))
	waitForRead(t, "beta to read refused", func() bool {
		return errCode(get("beta-config")) == "radar_forbidden"
	})
	if err := get("beta-config"); strings.Contains(err.Error(), "system:serviceaccount") || !strings.Contains(err.Error(), "no longer uses its copy") {
		t.Fatalf("refusal = %v", err)
	}

	lists.fail("beta", nil)
	waitForRead(t, "beta to recover", func() bool {
		coverage, _, err := list("")
		return err == nil && coverage.Status == "complete"
	})
	if err := get("beta-config"); err != nil {
		t.Fatalf("get after recovery = %v", err)
	}
}

// A custom resource whose lists keep failing (a conversion webhook down)
// reads kind_stale for both list and get, and recovers.
func TestResourceReadsStaleCustomResource(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "w1", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
	lists := newFailingLists()
	lists.install(&dyn.Fake, "widgets")
	t.Cleanup(k8s.SetTestListFailureTracking(testListFailureAfter, nil))
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	get := func() error {
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "Widget", Group: gvr.Group, Namespace: "alpha", Name: "w1", Context: "none"})
		return err
	}
	waitForRead(t, "widgets to load", func() bool { return get() == nil })

	// The cluster-wide informer lists at namespace "".
	lists.fail("", conversionFailure())
	waitForRead(t, "widgets to read stale", func() bool { return errCode(get()) == "kind_stale" })
	_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "Widget", Group: gvr.Group, Context: "none"})
	if errCode(err) != "kind_stale" {
		t.Fatalf("list while stale = %v", err)
	}

	lists.fail("", nil)
	waitForRead(t, "widgets to recover", func() bool { return get() == nil })
}
