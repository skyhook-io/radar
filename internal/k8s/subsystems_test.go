package k8s

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestInitAllSubsystemsPropagatesTimelineInitFailure(t *testing.T) {
	timelineErr := errors.New("timeline init failed")
	RegisterTimelineFuncs(nil, func() error {
		return timelineErr
	})
	t.Cleanup(func() {
		RegisterTimelineFuncs(nil, nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := InitAllSubsystems(ctx, func(string) {})
	if !errors.Is(err, timelineErr) {
		t.Fatalf("InitAllSubsystems error = %v, want timeline error %v", err, timelineErr)
	}
}

func TestTrackedSwitchDiscoveryWaitHasOwnBudget(t *testing.T) {
	for _, state := range []string{"current", "replaced", "replaced before discovery returns", "operation cancelled"} {
		t.Run(state, func(t *testing.T) {
			withFreshTestConnection(t)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "serving", Namespace: "default"}}
			if err := InitTestResourceCache(fake.NewSimpleClientset(pod)); err != nil {
				t.Fatal(err)
			}
			cache := GetResourceCache()
			t.Cleanup(ResetResourceCache)
			t.Cleanup(ResetDynamicResourceCache)
			t.Cleanup(ResetPolicyReportIndex)
			t.Cleanup(InvalidateServerVersionCache)

			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			started := make(chan struct{}, 1)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api" {
					_, _ = w.Write([]byte(`{"kind":"APIVersions","apiVersion":"v1","versions":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
			}))
			t.Cleanup(api.Close)
			t.Cleanup(unblock)
			cfg := &rest.Config{Host: api.URL}
			clientMu.Lock()
			prev := installedConn.Load()
			installedConn.Store(copyTestConnectionLocked())
			installedConn.Load().clients.discovery = discovery.NewDiscoveryClientForConfigOrDie(cfg)
			installedConn.Load().clients.dynamic = dynamic.NewForConfigOrDie(cfg)
			clientMu.Unlock()
			t.Cleanup(func() {
				clientMu.Lock()
				installedConn.Store(prev)
				clientMu.Unlock()
			})
			t.Cleanup(ResetMetricsHistory)

			// Registered before the late dynamic cache exists, as the SSE
			// broadcaster's topology refresh is.
			crdComplete := make(chan struct{}, 1)
			OnCRDDiscoveryComplete(func() {
				select {
				case crdComplete <- struct{}{}:
				default:
				}
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const budget = 100 * time.Millisecond
			done := make(chan error, 1)
			go func() { done <- initAllSubsystems(ctx, func(string) {}, budget, nil) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("no discovery request")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("discovery returned %v", err)
				}
			case <-time.After(budget + 500*time.Millisecond):
				t.Fatal("stalled discovery held the switch past its own budget")
			}
			cancel() // The switch releases its init context on success.

			if _, err := GetResourceCache().Pods().Pods("default").Get("serving"); err != nil {
				t.Fatalf("reading typed pod during discovery: %v", err)
			}
			info, err := GetClusterInfo(t.Context())
			if err != nil || info.CRDDiscoveryStatus != string(k8score.CRDDiscoveryInProgress) {
				t.Fatalf("cluster info during discovery = %+v, %v", info, err)
			}
			topo, err := topology.NewBuilder(NewTopologyResourceProvider(cache)).Build(topology.DefaultBuildOptions())
			if err != nil || topo.CRDDiscoveryStatus != string(k8score.CRDDiscoveryInProgress) {
				t.Fatalf("topology during discovery = %+v, %v", topo, err)
			}
			if GetDynamicResourceCache() != nil {
				t.Fatal("dynamic cache initialized before API discovery finished")
			}
			switch state {
			case "replaced":
				ResetResourceCache()
				ResetResourceDiscovery()
				if err := InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
					t.Fatal(err)
				}
				// The replacement owns its own cache state; close it while installed.
				t.Cleanup(ResetResourceCache)
				// Retiring the cache releases the waiter while discovery is still stalled.
				for deadline := time.Now().Add(3 * time.Second); cache.apiDiscoveryPending.Load(); {
					if time.Now().After(deadline) {
						t.Fatal("a stalled discovery kept the retired cache's waiter alive")
					}
					time.Sleep(time.Millisecond)
				}
			case "replaced before discovery returns":
				// Leave the old core running so discovery completion, rather
				// than cache.Done(), wakes the waiter after replacement.
				ResetResourceDiscovery()
				t.Cleanup(cache.Stop)
			case "operation cancelled":
				// A rescope whose connectivity check fails cancels the operation
				// but leaves this cache serving.
				CancelOngoingOperations()
			}
			unblock()
			for deadline := time.Now().Add(3 * time.Second); cache.apiDiscoveryPending.Load(); {
				if time.Now().After(deadline) {
					t.Fatal("late discovery did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			if state == "replaced" || state == "replaced before discovery returns" {
				if GetResourceDiscovery() != nil || GetDynamicResourceCache() != nil {
					t.Fatal("late discovery installed state for a replaced connection")
				}
				return
			}
			dc := GetDynamicResourceCache()
			if GetResourceDiscovery() == nil || dc == nil {
				t.Fatal("late discovery did not install discovery and the dynamic cache")
			}
			for deadline := time.Now().Add(3 * time.Second); dc.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete; {
				if time.Now().After(deadline) {
					t.Fatal("late CRD warmup did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-crdComplete:
			case <-time.After(3 * time.Second):
				t.Fatal("CRD discovery completion callback did not fire on the late dynamic cache")
			}
		})
	}
}
