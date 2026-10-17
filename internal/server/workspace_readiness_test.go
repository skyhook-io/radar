package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestWorkspaceTypedSourcesRequireInitialSync(t *testing.T) {
	controller := true
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pg"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "pg"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "4"), corev1.ResourceMemory: mustQty(t, "8Gi")}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "pg"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "orders-1", Namespace: "pg", Labels: map[string]string{"cnpg.io/cluster": "orders"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "orders", UID: "orders-uid", Controller: &controller}},
		}, Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "500m")}}}}}},
	)
	release := make(chan struct{})
	client.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.GetResource().Resource {
		case "pods", "nodes", "deployments":
			select {
			case <-release:
			default:
				return true, nil, errors.New("initial list held until test release")
			}
		}
		return false, nil, nil
	})
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestPromotedSyncingCache(client, 5*time.Second, 3*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	srv := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/capacity?namespace=pg", nil)
	deniedServer := newAuthServer(auth.Config{Mode: "proxy"})
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	permissions.SetCanI("list", "", "nodes", "", false)
	permissions.SetCanI("list", "", "pods", "", false)
	permissions.SetCanI("list", "", "pods", "pg", false)
	permissions.SetCanI("list", "apps", "deployments", "", false)
	permissions.SetCanI("list", "apps", "deployments", "pg", false)
	deniedServer.permCache.Set("denied", nil, permissions)
	deniedRequest := requestWithUser(http.MethodGet, "/api/capacity?namespace=pg", &auth.User{Username: "denied"})

	for _, stage := range []struct {
		name      string
		readiness k8s.KindReadiness
		capacity  capacityapi.CoverageStatus
		count     int
	}{
		{"pending", k8s.KindPending, capacityapi.CoverageSyncing, 0},
		{"failed", k8s.KindFailed, capacityapi.CoverageError, 0},
		{"late ready", k8s.KindReady, capacityapi.CoverageAvailable, 1},
	} {
		t.Run(stage.name, func(t *testing.T) {
			if stage.readiness == k8s.KindReady {
				close(release)
			}
			waitFor(t, 45*time.Second, func() bool {
				return cache.KindReadinessFor("pods") == stage.readiness && cache.KindReadinessFor("nodes") == stage.readiness && cache.KindReadinessFor("deployments") == stage.readiness
			})
			vitalsResponse := httptest.NewRecorder()
			srv.handleVitals(vitalsResponse, httptest.NewRequest(http.MethodGet, "/api/vitals?namespaces=pg", nil))
			var vitals VitalsResponse
			if err := json.Unmarshal(vitalsResponse.Body.Bytes(), &vitals); err != nil {
				t.Fatal(err)
			}
			if stage.readiness != k8s.KindReady {
				if vitals.Completeness.Complete || !slices.Contains(vitals.Completeness.Pending, "Pod") {
					t.Fatalf("unread Vitals must report pending Pod evidence: %+v", vitals.Completeness)
				}
			} else if vitals.Pods.Total != 1 || vitals.CPU == nil || vitals.CPU.RequestsMillis != 500 {
				t.Fatalf("late-ready Vitals lost observed requests: %+v", vitals)
			}
			meta := capacityapi.NewResponseMeta(time.Now())
			if got := len(srv.loadCapacityPods(r, &meta, time.Now())); got != stage.count {
				t.Fatalf("Capacity Pods = %d, want %d", got, stage.count)
			}
			if got := len(srv.loadCapacityNodes(r, &meta, time.Now())); got != stage.count {
				t.Fatalf("Capacity Nodes = %d, want %d", got, stage.count)
			}
			if !reflect.DeepEqual(meta.Coverage[capacityapi.CoverageWorkloads], meta.Coverage[capacityapi.CoveragePods]) {
				t.Fatal("workload coverage diverged from the Pods it derives from")
			}
			for _, source := range []capacityapi.CoverageSource{capacityapi.CoveragePods, capacityapi.CoverageNodes} {
				coverage := meta.Coverage[source]
				if coverage.Status != stage.capacity {
					t.Fatalf("%s coverage = %#v, want %s", source, coverage, stage.capacity)
				}
				if stage.readiness != k8s.KindReady {
					if coverage.ItemCount != nil || coverage.ObservedAt != nil || capacityCoverageObserved(coverage) {
						t.Fatalf("unread %s reported an observation: %#v", source, coverage)
					}
				} else if coverage.ItemCount == nil || *coverage.ItemCount != 1 || coverage.ObservedAt == nil {
					t.Fatalf("ready %s did not establish its observed row: %#v", source, coverage)
				}
			}
			deniedMeta := capacityapi.NewResponseMeta(time.Now())
			deniedServer.loadCapacityPods(deniedRequest, &deniedMeta, time.Now())
			deniedServer.loadCapacityNodes(deniedRequest, &deniedMeta, time.Now())
			for _, source := range []capacityapi.CoverageSource{capacityapi.CoveragePods, capacityapi.CoverageNodes} {
				if deniedMeta.Coverage[source].Status != capacityapi.CoverageDenied {
					t.Fatalf("readiness masked %s denial: %#v", source, deniedMeta.Coverage[source])
				}
			}
			if empty, _ := srv.cnpgReader(r).Observations.TypedScope(r.Context(), cache, []string{}, "", "pods"); empty.State != integration.KindCoverageDenied {
				t.Fatalf("empty namespace access widened: %#v", empty)
			}
			if stage.readiness == k8s.KindReady {
				emptyMeta := capacityapi.NewResponseMeta(time.Now())
				emptyRequest := httptest.NewRequest(http.MethodGet, "/api/capacity?namespace=empty", nil)
				if pods := srv.loadCapacityPods(emptyRequest, &emptyMeta, time.Now()); len(pods) != 0 {
					t.Fatalf("empty namespace returned %d Pods", len(pods))
				}
				coverage := emptyMeta.Coverage[capacityapi.CoveragePods]
				if coverage.Status != capacityapi.CoverageAvailable || coverage.ItemCount == nil || *coverage.ItemCount != 0 {
					t.Fatalf("ready empty scope must establish zero: %#v", coverage)
				}
			}
		})
	}
}

func TestWorkspaceDisabledTypedSourcesRemainUnavailable(t *testing.T) {
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"services": {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/capacity?namespace=pg", nil)
	meta := capacityapi.NewResponseMeta(time.Now())
	srv.loadCapacityPods(r, &meta, time.Now())
	srv.loadCapacityNodes(r, &meta, time.Now())
	for _, source := range []capacityapi.CoverageSource{capacityapi.CoveragePods, capacityapi.CoverageNodes} {
		coverage := meta.Coverage[source]
		if coverage.Status != capacityapi.CoverageUnavailable || coverage.ItemCount != nil || coverage.ObservedAt != nil {
			t.Fatalf("disabled %s reported loading or an observation: %#v", source, coverage)
		}
		wantReason := "pod_cache_unavailable"
		if source == capacityapi.CoverageNodes {
			wantReason = "node_cache_unavailable"
		}
		if coverage.ReasonCode != wantReason {
			t.Fatalf("disabled %s reason = %q, want %q", source, coverage.ReasonCode, wantReason)
		}
	}
	if access, _ := srv.cnpgReader(r).Observations.TypedScope(r.Context(), k8s.GetResourceCache(), []string{"pg"}, "", "pods"); access.State != integration.KindCoverageUncached {
		t.Fatalf("disabled Pods must read as uncached, not full or syncing: %#v", access)
	}
}
