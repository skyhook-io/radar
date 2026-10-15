package cnpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestClusterHistoryPreservesPVCIsolationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason string
		err                 error
	}{
		{"ambiguous", cnpgHistoryStateAmbiguous, "more than one cluster identity", prometheuspkg.ErrScopeAmbiguous},
		{"mismatch", cnpgHistoryStateScopeMismatch, "do not appear", prometheuspkg.ErrScopeMismatch},
		{"query error", historyStateError, "Prometheus query failed", errors.New("connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := cnpgActionCluster(nil)
			pod := cnpgActionPod("pg-1", "pod-uid", true)
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "db", Labels: map[string]string{clusterLabel: "pg", instanceNameLabel: "pg-1"}, OwnerReferences: pod.OwnerReferences}}
			core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: k8sfake.NewSimpleClientset(pod, pvc), ResourceTypes: map[string]bool{"pods": true, "persistentvolumeclaims": true}})
			if err != nil {
				t.Fatal(err)
			}
			defer core.Stop()
			reader := newTestReader(nil)
			reader.Access.MetricsRead = func(context.Context, string, string, string, string) bool { return true }
			reader.Metrics.Connection = func(context.Context) (bool, error) { return true, nil }
			reader.Metrics.CNPGScope = func(context.Context, string, string, []prom.WorkloadPodIdentity, time.Duration) (string, prometheuspkg.SeriesIsolation, error) {
				return "", prometheuspkg.SeriesIsolation{}, nil
			}
			reader.Metrics.PVCScope = func(context.Context, string, []string, []prom.WorkloadPodIdentity, time.Duration) (string, prometheuspkg.SeriesIsolation, error) {
				return "", prometheuspkg.SeriesIsolation{}, tc.err
			}
			called := false
			reader.Metrics.History = func(_ context.Context, req prometheuspkg.CNPGHistoryRequest) ([]prometheuspkg.CNPGHistoryChart, error) {
				called = true
				if req.PVCScopeState != tc.state || !strings.Contains(req.PVCReason, tc.reason) {
					t.Fatalf("history request lost failure: %+v", req)
				}
				return nil, nil
			}
			rng, _ := prometheuspkg.ParseCNPGHistoryRange("1h")
			reader.ClusterHistory(context.Background(), &k8s.ResourceCache{ResourceCache: core}, cluster, rng)
			if !called {
				t.Fatal("history reader did not receive the PVC scope result")
			}
		})
	}
}
