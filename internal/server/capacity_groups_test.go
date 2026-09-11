package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// capacityConfigMapCache is a capacityConfigMapSource over an in-memory
// indexer, so each coverage verdict can be driven without an informer.
type capacityConfigMapCache struct {
	clusterWide bool
	namespaces  []string
	ready       bool
	lister      corelisters.ConfigMapLister
}

func (c capacityConfigMapCache) IsKindClusterWide(string) bool           { return c.clusterWide }
func (c capacityConfigMapCache) KindNamespaces(string) []string          { return c.namespaces }
func (c capacityConfigMapCache) IsKindReady(string) bool                 { return c.ready }
func (c capacityConfigMapCache) ConfigMaps() corelisters.ConfigMapLister { return c.lister }

func capacityConfigMapLister(t *testing.T, payload *string) corelisters.ConfigMapLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if payload != nil {
		if err := indexer.Add(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: autoscalerStatusNamespace, Name: autoscalerStatusConfigMapName},
			Data:       map[string]string{"status": *payload},
		}); err != nil {
			t.Fatalf("seed ConfigMap: %v", err)
		}
	}
	return corelisters.NewConfigMapLister(indexer)
}

// TestCapacityAutoscalerStatusCoverageVerdictsMatchDetection pins the pairing
// the groups engine depends on. Denied, out-of-scope, unreadable and
// unparseable all yield a nil Status, so only the detection distinguishes them
// from a genuinely absent ConfigMap — and rendering any of them as "no capacity
// manager detected" would state a cluster fact from a read that never happened.
func TestCapacityAutoscalerStatusCoverageVerdictsMatchDetection(t *testing.T) {
	readable := capacityConfigMapCache{clusterWide: true, ready: true}
	garbage := "not a cluster-autoscaler status payload"

	tests := []struct {
		name          string
		allowed       bool
		cache         capacityConfigMapSource
		wantStatus    capacityapi.CoverageStatus
		wantReason    string
		wantDetection capacitymodel.AutoscalerDetection
	}{
		{
			name:          "denied",
			cache:         withLister(readable, capacityConfigMapLister(t, nil)),
			wantStatus:    capacityapi.CoverageDenied,
			wantReason:    "autoscaler_status_configmap_denied",
			wantDetection: capacitymodel.AutoscalerUnavailable,
		},
		{
			name:          "cache scope",
			allowed:       true,
			cache:         withLister(capacityConfigMapCache{ready: true, namespaces: []string{"default"}}, capacityConfigMapLister(t, nil)),
			wantStatus:    capacityapi.CoverageUnavailable,
			wantReason:    "autoscaler_status_cache_scope",
			wantDetection: capacitymodel.AutoscalerUnavailable,
		},
		{
			name:          "parse failed",
			allowed:       true,
			cache:         withLister(readable, capacityConfigMapLister(t, &garbage)),
			wantStatus:    capacityapi.CoverageError,
			wantReason:    "autoscaler_status_parse_failed",
			wantDetection: capacitymodel.AutoscalerUnavailable,
		},
		{
			name:          "not published",
			allowed:       true,
			cache:         withLister(readable, capacityConfigMapLister(t, nil)),
			wantStatus:    capacityapi.CoverageUnavailable,
			wantReason:    "autoscaler_status_not_published",
			wantDetection: capacitymodel.AutoscalerNotPublished,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, coverage, detection := capacityAutoscalerStatus(test.allowed, test.cache)
			if status != nil {
				t.Fatalf("status = %#v, want nil", status)
			}
			if coverage.Status != test.wantStatus || coverage.ReasonCode != test.wantReason {
				t.Fatalf("coverage = %q/%q, want %q/%q", coverage.Status, coverage.ReasonCode, test.wantStatus, test.wantReason)
			}
			if detection != test.wantDetection {
				t.Fatalf("detection = %v, want %v", detection, test.wantDetection)
			}
			// Only kube-system was ever probed; the verdict must say so rather
			// than read as a cluster-wide claim.
			if len(coverage.Namespaces) != 1 || coverage.Namespaces[0] != autoscalerStatusNamespace {
				t.Fatalf("coverage namespaces = %v, want [%s]", coverage.Namespaces, autoscalerStatusNamespace)
			}
		})
	}
}

func TestCapacityAutoscalerStatusObservedPayload(t *testing.T) {
	payload := "Cluster-autoscaler status at 2026-07-18 22:01:54.093779908 +0000 UTC:\n" +
		"Cluster-wide:\n  Health:      Healthy (ready=1 registered=1)\n"
	status, coverage, detection := capacityAutoscalerStatus(true, withLister(
		capacityConfigMapCache{clusterWide: true, ready: true},
		capacityConfigMapLister(t, &payload),
	))
	if status == nil {
		t.Fatal("parsed payload returned no status")
	}
	if coverage.Status != capacityapi.CoverageAvailable {
		t.Fatalf("coverage = %#v, want available", coverage)
	}
	if detection != capacitymodel.AutoscalerObserved {
		t.Fatalf("detection = %v, want observed", detection)
	}
}

func withLister(base capacityConfigMapCache, lister corelisters.ConfigMapLister) capacityConfigMapCache {
	base.lister = lister
	return base
}

// TestCapacityClusterIdentityDetectsContextSwitch pins the coherence guard the
// assembled response depends on: every cluster singleton is re-read here, so a
// mid-request kubeconfig switch must fail the comparison rather than let pool
// specs from one cluster be stitched to nodes from another.
func TestCapacityClusterIdentityDetectsContextSwitch(t *testing.T) {
	identity := currentCapacityClusterIdentity()
	if !identity.stillCurrent() {
		t.Fatal("a freshly captured identity must compare equal to the live one")
	}

	previous := k8s.SetTestContextName(identity.contextName + "-switched")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	if identity.stillCurrent() {
		t.Fatal("context switch went undetected — the response would mix two clusters")
	}

	k8s.SetTestContextName(previous)
	if !identity.stillCurrent() {
		t.Fatal("identity must compare equal again once the context is restored")
	}
}

// switchCapacityContextAfterFirstRead makes the cluster identity change exactly
// once, after the request has captured it — the mid-flight kubeconfig switch a
// test cannot otherwise time deterministically.
func switchCapacityContextAfterFirstRead(t *testing.T) {
	t.Helper()
	previous := capacityClusterIdentityNow
	t.Cleanup(func() { capacityClusterIdentityNow = previous })
	reads := 0
	capacityClusterIdentityNow = func() capacityClusterIdentity {
		identity := previous()
		if reads > 0 {
			identity.contextName += "-switched"
		}
		reads++
		return identity
	}
}

// TestCapacityResponsesFailClosedOnMidRequestContextSwitch drives the real
// handlers. The Karpenterless/denied path is the one that matters most: it
// returns from the loader early and then loads nodes, pods and autoscaler
// groups afterwards, so a gate that only guarded the fully-available return
// would serve the previous cluster's fleet under the new cluster's name.
func TestCapacityResponsesFailClosedOnMidRequestContextSwitch(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	t.Run("karpenterless denied overview", func(t *testing.T) {
		env := newAuthTestServer(t)
		permissions := &auth.UserPermissions{AllowedNamespaces: nil}
		permissions.SetCanI("list", karpenter.Group, "nodepools", "", false)
		permissions.SetCanI("list", "", "nodes", "", true)
		permissions.SetCanI("list", "", "pods", "", false)
		permissions.SetCanI("list", "", "pods", "default", false)
		permissions.SetCanI("list", "", "pods", "broken", false)
		permissions.SetCanI("get", "", "configmaps", "kube-system", false)
		env.srv.permCache.Set("alice", nil, permissions)

		// Baseline: this path serves 200 when the cluster holds still.
		resp := env.authGet(t, "/api/capacity", "alice", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("baseline status = %d, want 200", resp.StatusCode)
		}

		switchCapacityContextAfterFirstRead(t)
		assertCapacityContextSwitchFailsClosed(t, env.authGet(t, "/api/capacity", "alice", ""))
	})

	for _, path := range []string{
		"/api/capacity",
		"/api/capacity/pools",
		"/api/capacity/pools/general",
		"/api/capacity/pools/general/members?type=node",
		"/api/capacity/demand",
		"/api/capacity/activity",
	} {
		t.Run(path, func(t *testing.T) {
			switchCapacityContextAfterFirstRead(t)
			assertCapacityContextSwitchFailsClosed(t, get(t, path))
		})
	}
}

// TestCapacityResponseMetaStampsFromCapturedIdentity pins property 2 of the
// writeCapacityResponse invariant: the response's cluster naming comes from
// the captured snapshot, never from a second read of the globals. The seam
// returns a STABLE identity whose naming deliberately disagrees with the live
// cluster state — the gate passes (capture == re-check), so the only way the
// response can carry the seam's names is by stamping them from the capture.
func TestCapacityResponseMetaStampsFromCapturedIdentity(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	previous := capacityClusterIdentityNow
	t.Cleanup(func() { capacityClusterIdentityNow = previous })
	capacityClusterIdentityNow = func() capacityClusterIdentity {
		identity := previous()
		identity.activeClusterContext = "captured-context"
		identity.clusterName = "captured-cluster"
		return identity
	}

	resp := get(t, "/api/capacity")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	var decoded struct {
		ClusterContext struct {
			ContextName string `json:"contextName"`
			ClusterName string `json:"clusterName"`
		} `json:"clusterContext"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ClusterContext.ContextName != "captured-context" || decoded.ClusterContext.ClusterName != "captured-cluster" {
		t.Fatalf("clusterContext = %+v, want the CAPTURED identity's naming — a divergent value means the meta re-read the globals", decoded.ClusterContext)
	}
}

func assertCapacityContextSwitchFailsClosed(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a response assembled across a context switch mixes two clusters; body = %s", resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if message, _ := decoded["error"].(string); message != "Cluster context changed while the capacity response was being assembled" {
		t.Fatalf("error = %q, want the context-change message", message)
	}
}
