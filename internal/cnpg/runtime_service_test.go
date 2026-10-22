package cnpg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestClusterRuntimeMemoRequiresGrantsAndIsolatesCallerContextAndPodUID(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/pg/status") {
			_, _ = io.WriteString(w, cnpgStatusFixture)
			return
		}
		_, _ = io.WriteString(w, cnpgMetricsFixture)
	}))
	defer api.Close()
	proxy, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	cluster := cnpgActionCluster(nil)
	newCache := func(uid string) *k8s.ResourceCache {
		pod := cnpgActionPod("pg-1", uid, true)
		pod.Spec.Containers = []corev1.Container{{Name: "postgres"}}
		core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: k8sfake.NewSimpleClientset(pod), ResourceTypes: map[string]bool{"pods": true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(core.Stop)
		return &k8s.ResourceCache{ResourceCache: core}
	}
	cache := newCache("original-pod")
	inventoryAllowed, proxyAllowed := true, true
	grantChecks := 0
	type callerKey struct{}
	ctx := context.WithValue(context.Background(), callerKey{}, "alice")
	reader := newTestReader(nil)
	reader.Identity = t.Name() + "/east/alice"
	reader.Clients.Proxy = proxy
	reader.Observations.Cluster = func(ctx context.Context, namespace, name string, grants ...auth.Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error) {
		grantChecks++
		if ctx.Value(callerKey{}) != "alice" {
			t.Error("caller context lost before authorization")
		}
		if len(grants) != 1 || grants[0] != GrantListPods {
			t.Errorf("inventory grants=%v", grants)
		}
		if !inventoryAllowed {
			return nil, nil, &ReadFailure{Status: http.StatusForbidden, Message: "no Pod grant"}
		}
		return cache, cluster, nil
	}
	reader.Access.Permission = func(ctx context.Context, g auth.Grant) string {
		if ctx.Value(callerKey{}) != "alice" {
			t.Error("caller context lost before proxy authorization")
		}
		if g != grantGetPodsProxy.In("db") {
			t.Errorf("proxy grant=%v", g)
		}
		if !proxyAllowed {
			return integration.PermissionDenied
		}
		return integration.PermissionAllowed
	}
	read := func(wantCalls int32) *CNPGClusterRuntimeResponse {
		t.Helper()
		got, err := reader.ClusterRuntime(ctx, "db", "pg")
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != wantCalls {
			t.Fatalf("proxy calls=%d, want %d", calls.Load(), wantCalls)
		}
		return got
	}
	read(2)
	read(2)
	if grantChecks != 2 {
		t.Fatalf("memo skipped inventory authorization: checks=%d", grantChecks)
	}
	proxyAllowed = false
	denied := read(2)
	if denied.Instances[0].Status.State != runtimeStateDenied || denied.Instances[0].Status.CNPGInstanceStatusFacts != nil {
		t.Fatalf("memo disclosed denied status: %+v", denied)
	}
	inventoryAllowed = false
	if _, err := reader.ClusterRuntime(ctx, "db", "pg"); err == nil {
		t.Fatal("memo bypassed revoked inventory grant")
	}
	if calls.Load() != 2 {
		t.Fatal("denied inventory triggered a proxy read")
	}
	inventoryAllowed, proxyAllowed = true, true
	for i, identity := range []string{"east/bob", "west/alice"} {
		reader.Identity = t.Name() + "/" + identity
		read(int32(4 + i*2))
	}
	cache = newCache("replacement-pod")
	got := read(8)
	if got.Instances[0].PodUID != "replacement-pod" {
		t.Fatal("replacement instance retained old Pod identity")
	}
	if grantChecks < 7 {
		t.Fatal(fmt.Sprintf("expected authorization before every read, got %d", grantChecks))
	}
}
