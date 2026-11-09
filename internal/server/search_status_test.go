package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchStatusPreservesAuthorizedIssuesWithoutOwnership(t *testing.T) {
	withOwnershipDiscovery(t)
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "visible"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "hidden"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "visible", Name: "status-probe-pod", UID: "visible-pod"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "hidden", Name: "status-probe-hidden", UID: "hidden-pod"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "visible", Name: "status-probe-secret"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "status-probe-node", UID: "worker"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour))}}}},
	))
	var gets atomic.Int32
	fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
		if a.Verb == "get" && (a.Resource == "pods" || a.Resource == "nodes") {
			gets.Add(1)
		}
		return a.Resource != "secrets"
	})
	env := newAuthTestServer(t)
	var rebuilds atomic.Int32
	env.srv.broadcaster.rebuildTopology = func() bool { rebuilds.Add(1); return false }
	type status struct {
		health string
		issues int
	}
	for _, nodes := range []bool{true, false} {
		var want map[string]status
		for _, mode := range []string{"summary", "status", "none"} {
			user := fmt.Sprintf("status-%v-%s", nodes, mode)
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"visible"}}
			perms.SetCanI("list", "", "nodes", "", nodes)
			env.srv.permCache.Set(user, nil, perms)
			gets.Store(0)
			rebuilds.Store(0)
			resp := env.authGet(t, "/api/search?q=status-probe&include=none&context="+mode, user, "")
			var result search.Result
			err := json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status=%d err=%v", mode, resp.StatusCode, err)
			}
			wantHits := 1
			if nodes {
				wantHits++
			}
			if len(result.Hits) != wantHits {
				t.Fatalf("nodes=%v mode=%s: hits %+v", nodes, mode, result.Hits)
			}
			got := map[string]status{}
			for _, hit := range result.Hits {
				if hit.Namespace == "hidden" || hit.Kind == "Secret" || hit.Kind == "Node" && !nodes {
					t.Fatalf("mode=%s exposed an unreadable hit: %+v", mode, hit)
				}
				if mode == "none" {
					if hit.SummaryContext != nil {
						t.Fatalf("context=none emitted enrichment: %+v", hit)
					}
					continue
				}
				ctx := hit.SummaryContext
				if ctx == nil || ctx.Health == "" || ctx.IssueCount == 0 {
					t.Fatalf("mode=%s lost known status: %+v", mode, hit)
				}
				if mode == "status" && (ctx.ManagedBy != nil || ctx.Scanned != nil || len(ctx.Omitted) != 0) {
					t.Fatalf("status mode requested navigation: %+v", ctx)
				}
				got[hit.Kind+"/"+hit.Name] = status{ctx.Health, ctx.IssueCount}
			}
			if mode == "summary" {
				want = got
				if gets.Load() == 0 {
					t.Fatal("summary control did not read ownership")
				}
			} else {
				if gets.Load() != 0 || rebuilds.Load() != 0 {
					t.Fatalf("%s requested ownership/graph: gets=%d rebuilds=%d", mode, gets.Load(), rebuilds.Load())
				}
				if mode == "status" && !reflect.DeepEqual(got, want) {
					t.Fatalf("authorized status changed: got %+v want %+v", got, want)
				}
			}
		}
	}
}
