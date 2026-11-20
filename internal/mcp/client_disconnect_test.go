package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

const clientGivesUpAfter = 200 * time.Millisecond

// timeToolCalls reports how long each tools/call ran on server, measured
// outside every handler, so it is the time Radar spent on the call whether or
// not anyone was still waiting for the answer.
func timeToolCalls(server *mcpsdk.Server) <-chan time.Duration {
	took := make(chan time.Duration, 4)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			start := time.Now()
			result, err := next(ctx, method, req)
			if method == "tools/call" {
				took <- time.Since(start)
			}
			return result, err
		}
	})
	return took
}

// callAndGiveUp calls tool from a client on version and stops waiting after
// clientGivesUpAfter, the way an agent with a tool timeout does.
func callAndGiveUp(t *testing.T, endpoint, version, tool string, args map[string]any) {
	t.Helper()
	session := connectAtVersion(t, endpoint, version)
	ctx, cancel := context.WithTimeout(context.Background(), clientGivesUpAfter)
	defer cancel()
	_, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err == nil {
		t.Fatalf("%s answered within %v; the test needs a call that outlives the client", tool, clientGivesUpAfter)
	}
}

func awaitDuration(t *testing.T, took <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case d := <-took:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the tool call never returned on the server")
		return 0
	}
}

// installColdWidgets serves a custom kind whose informer starts but can't
// sync: its list in namespace "cold" is held until the test ends, so list_resources
// waits out its full sync budget unless its context ends first. With
// holdProbe, Radar's first access probe of the kind is held too, so the read
// never gets as far as starting the informer.
// It returns a func that releases the held lists; call it before closing a
// server whose handler may still be waiting on them.
func installColdWidgets(t *testing.T, holdProbe bool) (releaseLists func()) {
	t.Helper()
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cold"}},
	)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)

	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"namespace": "cold", "name": "w1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, widget)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLists = func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLists)
	var mu sync.Mutex
	nsLists := 0
	// The first list in "cold" is the access probe and answers at once; the
	// informer's own list after it is held.
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if holdProbe {
			<-release
			return false, nil, nil
		}
		if a.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("radar may not list widgets cluster-wide"))
		}
		mu.Lock()
		nsLists++
		n := nsLists
		mu.Unlock()
		if n > 1 {
			<-release
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	previousWait := dynamicSyncWait
	dynamicSyncWait = 1500 * time.Millisecond
	t.Cleanup(func() { dynamicSyncWait = previousWait })
	return releaseLists
}

// A client on protocol 2026-07-28 cancels a call by closing its POST. When it
// gives up on a list_resources call that is waiting for a cold kind to load,
// Radar stops waiting too. Older clients don't cancel by disconnecting, so
// their calls run as before.
func TestListResourcesStopsWhenANewProtocolClientLeaves(t *testing.T) {
	for _, tc := range []struct {
		version    string
		stopsEarly bool
	}{
		{version: discoverProtocolVersion, stopsEarly: true},
		{version: "2025-11-25", stopsEarly: false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			installColdWidgets(t, false)
			server := newServer(false, false, "")
			took := timeToolCalls(server)
			httpServer := httptest.NewServer(handlerForServer(server))
			defer httpServer.Close()

			callAndGiveUp(t, httpServer.URL, tc.version, "list_resources",
				map[string]any{"kind": "widgets", "group": "example.test", "namespace": "cold"})
			ran := awaitDuration(t, took)

			if tc.stopsEarly && ran > 800*time.Millisecond {
				t.Fatalf("list_resources ran %v after a %v client left; it should stop with the client", ran, clientGivesUpAfter)
			}
			if !tc.stopsEarly && ran < dynamicSyncWait-100*time.Millisecond {
				t.Fatalf("list_resources ran %v for a %s client; disconnecting must not cancel on that protocol", ran, tc.version)
			}
		})
	}
}

// Before a cold kind's informer exists, Radar first probes whether it may
// list the kind at all. A client that leaves while that probe is still out
// ends the call too, whether it named a namespace or not.
func TestListResourcesStopsDuringTheFirstProbeWhenANewProtocolClientLeaves(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"one namespace":  {"kind": "widgets", "group": "example.test", "namespace": "cold"},
		"all namespaces": {"kind": "widgets", "group": "example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			releaseLists := installColdWidgets(t, true)
			server := newServer(false, false, "")
			took := timeToolCalls(server)
			httpServer := httptest.NewServer(handlerForServer(server))
			defer httpServer.Close()
			defer releaseLists()

			callAndGiveUp(t, httpServer.URL, discoverProtocolVersion, "list_resources", args)
			select {
			case ran := <-took:
				if ran > 800*time.Millisecond {
					t.Fatalf("list_resources ran %v after a %v client left; it should stop with the client", ran, clientGivesUpAfter)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("list_resources still waiting on the access probe 3s after a %v client left", clientGivesUpAfter)
			}
		})
	}
}

// A call whose own deadline passes during that first probe reports the
// deadline, the same as one that passes during the sync wait, not a failed
// read of the kind.
func TestListResourcesReportsItsDeadlineDuringTheFirstProbe(t *testing.T) {
	releaseLists := installColdWidgets(t, true)
	defer releaseLists()
	for name, input := range map[string]listResourcesInput{
		"one namespace":  {Kind: "widgets", Group: "example.test", Namespace: "cold"},
		"all namespaces": {Kind: "widgets", Group: "example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := handleListResources(ctx, &mcpsdk.CallToolRequest{}, input)
				done <- err
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				releaseLists()
				t.Fatal("list_resources was still waiting on the access probe 2s after its 100ms deadline")
			}
			var classified *readError
			if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &classified) {
				t.Fatalf("err = %v, want the caller's deadline", err)
			}
		})
	}
}

// Write tools keep running when the client leaves. A deployment scale whose
// PATCH is still in flight when a 2026-07-28 client disconnects must reach the
// API server with a live request and finish.
func TestWriteToolKeepsRunningWhenANewProtocolClientLeaves(t *testing.T) {
	patchArrived := make(chan struct{})
	patchCancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePatch := func() { releaseOnce.Do(func() { close(release) }) }
	patched := make(chan struct{})
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/namespaces/shop/deployments/web") {
			http.NotFound(w, r)
			return
		}
		// Reading the body lets the server notice the client hanging up.
		_, _ = io.Copy(io.Discard, r.Body)
		close(patchArrived)
		select {
		case <-r.Context().Done():
			close(patchCancelled)
			return
		case <-release:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"shop"},"spec":{"replicas":3}}`)
		close(patched)
	}))
	defer apiServer.Close()

	dyn, err := dynamic.NewForConfig(&rest.Config{Host: apiServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments",
		Namespaced: true, Verbs: []string{"get", "list", "watch", "patch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	server := newFullServer()
	took := timeToolCalls(server)
	httpServer := httptest.NewServer(handlerForServer(server))
	defer httpServer.Close()
	// Runs before either server closes, so a failing run can't leave the
	// PATCH, and with it both servers, waiting forever.
	defer releasePatch()

	callAndGiveUp(t, httpServer.URL, discoverProtocolVersion, "manage_workload",
		map[string]any{"action": "scale", "kind": "deployment", "namespace": "shop", "name": "web", "replicas": 3})

	select {
	case <-patchArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the scale PATCH never reached the API server")
	}
	// The client has gone. Give a cancellation time to arrive before letting
	// the PATCH finish.
	select {
	case <-patchCancelled:
		t.Fatal("the client leaving cancelled manage_workload's PATCH mid-flight")
	case <-time.After(500 * time.Millisecond):
	}
	releasePatch()
	select {
	case <-patched:
	case <-patchCancelled:
		t.Fatal("the client leaving cancelled manage_workload's PATCH mid-flight")
	case <-time.After(5 * time.Second):
		t.Fatal("the scale PATCH never finished")
	}
	awaitDuration(t, took)
}
