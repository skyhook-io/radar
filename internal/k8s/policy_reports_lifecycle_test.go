package k8s

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

func installPolicyReportCache(t *testing.T) (*Connection, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{orReports: "ReportList"})
	resources := []APIResource{
		{Group: "kyverno.io", Version: "v1", Name: "policies", Kind: "Policy", Namespaced: true},
		{Group: orReports.Group, Version: orReports.Version, Name: orReports.Resource, Kind: "Report", Namespaced: true, Verbs: []string{"list", "watch"}},
	}
	if err := InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatal(err)
	}
	conn := Current()
	t.Cleanup(conn.closeDynamicResourceCache)
	return conn, client
}

func TestClosedConnectionDropsPolicyReportWarmup(t *testing.T) {
	for _, outcome := range []string{"ready", "deferred"} {
		t.Run(outcome, func(t *testing.T) {
			withFreshTestConnection(t)
			a, client := installPolicyReportCache(t)
			started, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			blockProbe := sync.OnceFunc(func() { close(started); <-release })
			client.PrependReactor("list", "reports", func(clienttesting.Action) (bool, runtime.Object, error) {
				blockProbe()
				if outcome == "deferred" {
					return true, nil, errors.New("report probe unavailable")
				}
				return false, nil, nil
			})
			done := make(chan struct{})
			go func() { defer close(done); a.warmupPolicyReports() }()
			select {
			case <-started:
			case <-done:
				t.Fatal("warmup finished without probing the report cache")
			}
			a.closeDynamicResourceCache()
			b, _ := installPolicyReportCache(t)
			bDone := make(chan struct{})
			go func() { defer close(bDone); b.warmupPolicyReports() }()
			select {
			case <-bDone:
			case <-time.After(3 * time.Second):
				t.Fatal("replacement warmup waited on the retired connection")
			}
			index, status := GetPolicyReportIndex(), GetPolicyReportStatus()
			if index == nil || status.Status != KyvernoStatusReady || !status.LiveUpdates || !reflect.DeepEqual(status.WatchedGroups, []string{orReports.Group}) {
				t.Fatalf("replacement did not publish its own ready index: %+v", status)
			}
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("retired warmup did not finish after the report probe returned")
			}
			if a.PolicyReportIndex() != nil || a.KyvernoStatus() != KyvernoStatusWarmup || a.PolicyReportStatus().Status != KyvernoStatusWarmup {
				t.Fatalf("closed connection published late: %+v", a.PolicyReportStatus())
			}
			if GetPolicyReportIndex() != index || !reflect.DeepEqual(GetPolicyReportStatus(), status) {
				t.Fatalf("late warmup replaced the serving index or status: %+v", GetPolicyReportStatus())
			}
		})
	}
}

func TestClosedConnectionDropsDebouncedPolicyReportRebuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withFreshTestConnection(t)
		a := Current()
		s := &a.dynamicCacheState().reports
		s.published.Store(&policyReportSnapshot{index: policyreports.NewIndex(), status: PolicyReportStatus{Status: KyvernoStatusReady}})
		listing, release := make(chan struct{}), make(chan struct{})
		s.scheduleRebuild(func([]schema.GroupVersionResource) []*unstructured.Unstructured {
			close(listing)
			<-release
			return nil
		})
		<-listing
		a.closeDynamicResourceCache()
		restoreTestConnection(a.fresh())
		Current().warmupPolicyReports()
		close(release)
		synctest.Wait()
		if a.PolicyReportIndex() != nil || GetPolicyReportIndex() != nil {
			t.Fatal("debounced rebuild published after close")
		}
		if status := GetPolicyReportStatus(); status.Status != KyvernoStatusNotInstalled || status.ReasonCode != ReasonNoDiscovery {
			t.Fatalf("replacement lost its own warmup decision: %+v", status)
		}
	})
}

func TestPolicyReportInformerRebuildsItsOwnCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withFreshTestConnection(t)
		a, client := installPolicyReportCache(t)
		a.warmupPolicyReports()
		initial := a.PolicyReportIndex()
		if initial == nil {
			t.Fatal("warmup did not publish the initial index")
		}
		// Deliver A's informer event with B installed but before A's teardown.
		b, _ := installPolicyReportCache(t)
		b.warmupPolicyReports()
		replacement := b.PolicyReportIndex()
		report := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "openreports.io/v1alpha1", "kind": "Report",
			"metadata": map[string]any{"name": "report", "namespace": "shop"},
			"scope":    map[string]any{"apiVersion": "v1", "kind": "Pod", "name": "web"},
			"results":  []any{map[string]any{"policy": "limits", "rule": "cpu", "result": "fail"}},
		}}
		if _, err := client.Resource(orReports).Namespace("shop").Create(t.Context(), report, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(rebuildDebounce)
		synctest.Wait()
		next := a.PolicyReportIndex()
		if next == initial || len(next.FindingsFor("", "Pod", "shop", "web")) != 1 {
			t.Fatal("A's informer did not rebuild from A's reports")
		}
		if replacement == nil || GetPolicyReportIndex() != replacement || replacement.Size() != 0 {
			t.Fatal("A's event changed B's index")
		}
	})
}

func TestPolicyReportStatusReadsDiscoveryPendingOnItsConnection(t *testing.T) {
	withFreshTestConnection(t)
	a := Current()
	typed := &ResourceCache{}
	typed.apiDiscoveryPending.Store(true)
	a.cacheState().promoted.Store(typed)
	installPolicyReportCache(t)
	if status := a.PolicyReportStatus(); status.Status != KyvernoStatusWarmup || status.ReasonCode != ReasonNoDiscovery || status.ObservedCount != -1 {
		t.Fatalf("replacement dynamic cache masked pending discovery: %+v", status)
	}
}

func TestReadmissionKeepsPolicyReportIndex(t *testing.T) {
	withFreshTestConnection(t)
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	conn, _ := installPolicyReportCache(t)
	conn.warmupPolicyReports()
	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	first, idx, status := ServingConnectionID(), GetPolicyReportIndex(), GetPolicyReportStatus()
	if idx == nil || status.Status != KyvernoStatusReady {
		t.Fatalf("warmup did not publish the initial index: %+v", status)
	}
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	if second := ServingConnectionID(); second == "" || second == first || GetPolicyReportIndex() != idx || !reflect.DeepEqual(GetPolicyReportStatus(), status) {
		t.Fatal("re-admission did not keep the index and status under a new ID")
	}
	ResetAllSubsystems()
	if Current() == conn || Current().Dynamic() != conn.Dynamic() || GetPolicyReportIndex() != nil || GetKyvernoStatus() != KyvernoStatusWarmup {
		t.Fatal("rebuild on the same clients did not start with an undecided, empty index")
	}
}
