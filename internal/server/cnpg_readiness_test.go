package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCNPGReadAdapterRequiresTypedInitialSync(t *testing.T) {
	controller := true
	clusterOwner := metav1.OwnerReference{APIVersion: cnpgsvc.Group + "/v1", Kind: "Cluster", Name: "orders", UID: "cluster-uid", Controller: &controller}
	client := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "orders-1", Namespace: "pg", Labels: map[string]string{"cnpg.io/cluster": "orders"}, OwnerReferences: []metav1.OwnerReference{clusterOwner}}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "orders-join", Namespace: "pg", UID: "job-uid", OwnerReferences: []metav1.OwnerReference{clusterOwner}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "join-pod", Namespace: "pg", Labels: map[string]string{"cnpg.io/cluster": "orders", "cnpg.io/jobRole": "join"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "orders-join", UID: "job-uid", Controller: &controller}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "pg", Labels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}}},
	)
	release := make(chan struct{})
	client.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case <-release:
			return false, nil, nil
		default:
			return true, nil, errors.New("initial list held until test release")
		}
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:              client,
		ResourceTypes:       map[string]bool{"pods": true, "jobs": true, "deployments": true, "services": true},
		ResourceScopes:      map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "pg"}, "jobs": {Enabled: true, Namespace: "pg"}, "deployments": {Enabled: true, Namespace: "pg"}, "services": {Enabled: true, Namespace: "pg"}},
		DeferredTypes:       map[string]bool{},
		MinimalSet:          map[string]bool{"pods": true, "services": true},
		PatienceWindow:      20 * time.Millisecond,
		SyncTimeout:         200 * time.Millisecond,
		DeferredSyncTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	r := httptest.NewRequest(http.MethodGet, "/api/cnpg/workspace?namespace=pg", nil)
	reader := (&Server{}).cnpgReader(r).Reader
	reader.Observations.Cache, reader.Observations.Connected, reader.Observations.Discovery = cache, true, nil
	reader.Observations.Issues = func(context.Context, []string) []issues.Issue { return nil }
	reader.Observations.WorkspaceRead = func(_ context.Context, _ *k8s.ResourceCache, kind integration.WorkspaceKind, _, _ []string) (integration.KindAccess, []*unstructured.Unstructured) {
		var objects []*unstructured.Unstructured
		if kind.Key == "clusters" {
			objects = []*unstructured.Unstructured{withUID(cnpgObj(cnpgsvc.Group+"/v1", "Cluster", "pg", "orders", nil, nil), "cluster-uid")}
		}
		return integration.KindAccess{State: integration.KindCoverageFull, All: true}, objects
	}
	ctx := context.Background()
	deniedServer := newAuthServer(auth.Config{Mode: "proxy"})
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	for _, kind := range []struct{ group, resource string }{{"", "pods"}, {"batch", "jobs"}, {"apps", "deployments"}, {"", "services"}} {
		permissions.SetCanI("list", kind.group, kind.resource, "", false)
		permissions.SetCanI("list", kind.group, kind.resource, "pg", false)
	}
	deniedServer.permCache.Set("denied", nil, permissions)
	deniedRequest := requestWithUser(http.MethodGet, "/", &auth.User{Username: "denied"})
	deniedReader := deniedServer.cnpgReader(deniedRequest).Reader
	for _, stage := range []struct {
		name      string
		readiness k8s.KindReadiness
		state     string
		count     int
	}{
		{"pending after startup backstop", k8s.KindPending, integration.KindCoverageSyncing, 0},
		{"failed", k8s.KindFailed, integration.KindCoverageError, 0},
		{"late ready", k8s.KindReady, integration.KindCoverageFull, 1},
	} {
		t.Run(stage.name, func(t *testing.T) {
			if stage.readiness == k8s.KindReady {
				close(release)
			}
			waitFor(t, 45*time.Second, func() bool {
				return cache.KindReadinessFor("pods") == stage.readiness && cache.KindReadinessFor("jobs") == stage.readiness && cache.KindReadinessFor("deployments") == stage.readiness && cache.KindReadinessFor("services") == stage.readiness
			})
			response := reader.Workspace(ctx, cache, []string{"pg"}, "fixture")
			if response.Coverage["pods"].State != stage.state || len(response.Objects["pods"]) != stage.count || response.JobCoverage.State != stage.state || len(response.JobPods) != stage.count {
				t.Fatalf("workspace typed sources: Pods=%#v/%d Jobs=%#v/%d", response.Coverage["pods"], len(response.Objects["pods"]), response.JobCoverage, len(response.JobPods))
			}
			operator, err := reader.Operator(ctx)
			operatorState := stage.state
			if stage.readiness == k8s.KindReady {
				operatorState = integration.KindCoveragePartial
			}
			if err != nil || operator.Coverage["deployments"].State != operatorState || operator.Coverage["services"].State != operatorState || len(operator.Components) != stage.count {
				t.Fatalf("operator read = %#v, %v", operator, err)
			}
			for _, kind := range []struct{ group, resource string }{{"", "pods"}, {"batch", "jobs"}, {"apps", "deployments"}, {"", "services"}} {
				denied, _ := deniedReader.Observations.TypedScope(deniedRequest.Context(), cache, []string{"pg"}, kind.group, kind.resource)
				if denied.State != integration.KindCoverageDenied {
					t.Fatalf("readiness masked %s caller denial: %#v", kind.resource, denied)
				}
				uncached, _ := reader.Observations.TypedScope(ctx, cache, []string{"other"}, kind.group, kind.resource)
				if uncached.State != integration.KindCoverageUncached {
					t.Fatalf("readiness masked %s uncovered scope: %#v", kind.resource, uncached)
				}
			}
		})
	}
}
