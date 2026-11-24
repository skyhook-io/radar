package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRightsizingOwnerEligibilityPreservesPrimaryProof(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "web", UID: "local"}
	info := func(uid, cluster string) prom.Series {
		return prom.Series{Labels: map[string]string{"namespace": "shop", "pod": "web", "uid": uid, "cluster": cluster}}
	}
	for _, tc := range []struct {
		name     string
		data     []prom.Series
		eligible bool
		primary  string
	}{
		{"primary proof", []prom.Series{info("local", "a")}, false, "a"},
		{"foreign UID", []prom.Series{info("foreign", "a")}, false, ""},
		{"primary conflict", []prom.Series{info("local", "a"), info("foreign", "a")}, false, ""},
		{"UID-less labeled inventory", []prom.Series{info("", "a")}, true, ""},
		{"unlabeled", []prom.Series{info("", "")}, false, ""},
		{"mixed labeled and unlabeled", []prom.Series{info("", "local"), info("", "")}, false, ""},
		{"empty", nil, false, ""},
		{"truncated", make([]prom.Series, 257), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := prom.NewClient(attributionTransport(func(query string) ([]byte, error) {
				if !strings.Contains(query, "kube_pod_info") || strings.Contains(query, "kube_pod_owner") {
					t.Fatalf("primary issued alternate query: %s", query)
				}
				return attributionEvidence(tc.data, ""), nil
			}))
			got, err := probeHistoricalPartition(context.Background(), c, "shop", []prom.WorkloadPodIdentity{pod})
			if err != nil || (len(got.uidlessAnchors) > 0) != tc.eligible || got.scope.ClusterLabels["cluster"] != tc.primary {
				t.Fatalf("proof=%+v err=%v", got, err)
			}
		})
	}
}

func TestRightsizingOwnerPartitionChecksEveryUIDLessSource(t *testing.T) {
	at := time.Now()
	pod := prom.WorkloadPodIdentity{Name: "db-0", UID: "local"}
	node := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.NodeSpec{ProviderID: "provider://" + name}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{SystemUUID: "uuid-" + name}}}
	}
	nodes := []*corev1.Node{node("node1"), node("node2")}
	podRow := func(family, uid, cluster, owner string) prom.Series {
		l := map[string]string{"namespace": "shop", "pod": pod.Name, "cluster": cluster, "job": "ksm", "instance": "ksm-1"}
		if uid != "" {
			l["uid"], l["job"], l["instance"] = uid, "opencost", "cost-1"
		}
		if owner != "" {
			l["owner_name"], l["owner_kind"] = owner, "StatefulSet"
		}
		return viewRow(family, l)
	}
	exporter := func(instance string) prom.Series {
		return viewRow("sources", map[string]string{"cluster": "local", "job": "ksm", "instance": instance})
	}
	nodeRow := func(instance, node, provider, uuid string) prom.Series {
		return viewRow("nodes", map[string]string{"cluster": "local", "job": "ksm", "instance": instance, "node": node, "provider_id": provider, "system_uuid": uuid})
	}
	live1 := nodeRow("ksm-1", "node1", "provider://node1", "UUID-NODE1")
	live2 := nodeRow("ksm-1", "node2", "provider://node2", "uuid-node2")
	weak := podRow("kube_pod_info", "", "local", "")
	owner := podRow(metricPodOwner, "local", "local", "db")
	weakOwner := podRow(metricPodOwner, "", "local", "db")
	normalRows := []prom.Series{weak, weakOwner, owner}
	normalSources := []prom.Series{exporter("ksm-1"), live1, live2}
	crowdedSources := []prom.Series{live1, live2}
	for i := 0; i < 65; i++ {
		row := exporter("ksm-1")
		row.Labels["cluster_name"] = fmt.Sprintf("alias-%d", i)
		crowdedSources = append(crowdedSources, row)
	}
	cases := []struct {
		name               string
		anchors, inventory []prom.Series
		verified           bool
	}{
		{"API UID and provider IDs agree across different exporters", normalRows, normalSources, true},
		{"distinct partition foreign UID", append([]prom.Series{podRow(metricPodOwner, "foreign", "other", "db")}, normalRows...), normalSources, true},
		{"another UID under same partition", append([]prom.Series{podRow(metricPodOwner, "other", "local", "db")}, normalRows...), normalSources, false},
		{"same local UID across partitions", append([]prom.Series{podRow(metricPodOwner, "local", "other", "db")}, normalRows...), normalSources, false},
		{"two controlling owners for one live UID", append([]prom.Series{podRow(metricPodOwner, "local", "local", "foreign")}, normalRows...), normalSources, false},
		{"different UID-less controller", append([]prom.Series{podRow(metricPodOwner, "", "local", "foreign")}, normalRows...), normalSources, false},
		{"missing strong owner", []prom.Series{weak, weakOwner}, normalSources, false},
		{"source audit dropped anchor reporter", normalRows, []prom.Series{live1, live2}, false},
		{"missing local node from reporter", normalRows, []prom.Series{exporter("ksm-1"), live1}, false},
		{"another exporter under same cluster label", normalRows, append([]prom.Series{exporter("foreign")}, normalSources...), false},
		{"same-name node with different cloud identity", normalRows, []prom.Series{exporter("ksm-1"), live1, nodeRow("ksm-1", "node2", "provider://foreign", "uuid-node2")}, false},
		{"reported UUID contradicts provider match", normalRows, []prom.Series{exporter("ksm-1"), live1, nodeRow("ksm-1", "node2", "provider://node2", "uuid-foreign")}, false},
		{"unrecognized node at same source", normalRows, append([]prom.Series{nodeRow("ksm-1", "other", "provider://other", "")}, normalSources...), false},
		{"another reporter has contrary stable identity", normalRows, append([]prom.Series{nodeRow("ksm-2", "other", "provider://other", "")}, normalSources...), false},
		{"truncated source list before source-key deduplication", normalRows, crowdedSources, false},
		{"anchor overflow", make([]prom.Series, 257), normalSources, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				data := tc.anchors
				if strings.Contains(query, "kube_node_info") {
					for _, s := range []string{`kube_node_info{cluster="local"}`, `kube_pod_info{cluster="local",uid=""}`, `topk(65,`, `topk(1025,`} {
						if !strings.Contains(query, s) {
							t.Fatalf("incomplete reporter audit: %s", query)
						}
					}
					data = tc.inventory
				}
				return &prom.QueryResult{ResultType: "vector", Series: data}, nil
			}}
			got, err := rightsizingOwnerPartition(context.Background(), q, "shop", []prom.WorkloadPodIdentity{pod}, nodes, at)
			if err != nil || (got["cluster"] == "local") != tc.verified || !tc.verified && len(got) != 0 {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestRightsizingRetiredInventoryReporters(t *testing.T) {
	span := 7 * 24 * time.Hour
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "live"}, Spec: corev1.NodeSpec{ProviderID: "provider://live"}}
	row := func(tag, reporter, nodeName, provider string) prom.Series {
		return viewRow(tag, map[string]string{"cluster": "local", "job": "ksm", "instance": reporter, "node": nodeName, "provider_id": provider})
	}
	for _, tc := range []struct {
		name    string
		retired []prom.Series
		want    bool
	}{
		{"retired source reported a live and a deleted node", []prom.Series{row("nodes", "retired", "live", "provider://live"), row("nodes", "retired", "gone", "provider://gone")}, true},
		{"retired source only reported unrelated nodes", []prom.Series{row("nodes", "retired", "foreign", "provider://foreign")}, false},
		{"retired source reported a conflicting live node", []prom.Series{row("nodes", "retired", "live", "provider://live"), row("nodes", "retired", "live", "provider://foreign")}, false},
		{"retired source omitted node identity", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				if strings.HasPrefix(query, "topk(1025,count by (provider_id)") {
					return &prom.QueryResult{ResultType: "vector", Series: []prom.Series{{Labels: map[string]string{"provider_id": "provider://live"}}}}, nil
				}
				if strings.HasPrefix(query, "topk(1,max_over_time") {
					return &prom.QueryResult{ResultType: "vector"}, nil
				}
				for _, s := range []string{`present_over_time(kube_node_info{cluster="local"}[604800s])`, `present_over_time(kube_pod_info{cluster="local",uid=""}[604800s])`, `present_over_time(kube_pod_owner{cluster="local",uid="",owner_is_controller="true"}[604800s])`} {
					if !strings.Contains(query, s) {
						t.Fatalf("missing retained source scope %q", s)
					}
				}
				rows := []prom.Series{row("sources", "now", "", ""), row("sources", "retired", "", ""), row("nodes", "now", "live", "provider://live")}
				return &prom.QueryResult{ResultType: "vector", Series: append(rows, tc.retired...)}, nil
			}}
			got, err := rightsizingSourcesMatchNodes(context.Background(), q, time.Now(), span, map[string]string{"cluster": "local"}, nil, []*corev1.Node{node})
			if got != tc.want || err != nil {
				t.Fatalf("verified=%v err=%v", got, err)
			}
		})
	}
}

func TestRightsizingOwnerPartitionQueryBound(t *testing.T) {
	names := podNames(100, 253)
	anchors := make([]prom.WorkloadPodIdentity, len(names))
	for i, name := range names {
		anchors[i] = prom.WorkloadPodIdentity{Name: name, UID: fmt.Sprint(i)}
	}
	queries, maxBytes := 0, 0
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		queries++
		maxBytes = max(maxBytes, len(query))
		if len(query) > prom.MaxWorkloadQueryBytes {
			t.Fatalf("oversized query: %d", len(query))
		}
		if strings.Contains(query, "kube_pod_owner") && strings.Contains(query, names[99]) {
			return &prom.QueryResult{ResultType: "vector", Annotations: prom.QueryAnnotations{Warnings: []string{"partial"}}}, nil
		}
		return &prom.QueryResult{ResultType: "vector"}, nil
	}}
	got, err := rightsizingOwnerPartition(context.Background(), q, "shop", anchors, []*corev1.Node{{}}, time.Now())
	if len(got) != 0 || err == nil || queries < 2 {
		t.Fatalf("partial proof: %v, err=%v, queries=%d", got, err, queries)
	}
	t.Logf("100 maximum-length Pod names: %d requests, largest %d bytes", queries, maxBytes)
}

func TestRightsizingOwnerViewScope(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-0", UID: "live-uid"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Spec: corev1.NodeSpec{ProviderID: "provider://node"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod, node)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, tc := range []struct {
		name, view        string
		primary, filtered bool
		want              ViewScopeState
		alternate         bool
	}{
		{"rightsizing without per-family labels", rightsizingScopeView, false, false, ViewScopeUnlabeled, true},
		{"optional query warning", rightsizingScopeView, false, false, ViewScopeUnlabeled, true},
		{"rightsizing with matching family", rightsizingScopeView, false, true, ViewScopeVerified, true},
		{"normal historical scope stays independent", rightsizingScopeView, true, true, ViewScopeVerified, false},
		{"unrelated view cannot obtain owner proof", "other", false, false, ViewScopeUnlabeled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var alternate atomic.Bool
			store := &viewScopeStore{answer: func(q string) []byte {
				var out []prom.Series
				switch {
				case strings.HasPrefix(q, "topk(1025,count by (provider_id)"):
					out = []prom.Series{{Labels: map[string]string{"provider_id": "provider://node"}}}
				case strings.HasPrefix(q, "topk(1,max_over_time"):
				case strings.HasPrefix(q, "topk(257,"):
					l := map[string]string{"namespace": "shop", "pod": "api-0", "cluster": "a"}
					if tc.primary {
						l["uid"] = "live-uid"
					}
					out = []prom.Series{{Labels: l}}
				case strings.Contains(q, `"sources"`):
					out = []prom.Series{viewRow("sources", map[string]string{"cluster": "a", "job": "ksm", "instance": "ksm-1"}), viewRow("nodes", map[string]string{"cluster": "a", "job": "ksm", "instance": "ksm-1", "node": "node", "provider_id": "provider://node"})}
				case strings.Contains(q, "topk(257,"):
					alternate.Store(true)
					if tc.name == "optional query warning" {
						return attributionEvidence(nil, "warnings")
					}
					out = []prom.Series{viewRow("kube_pod_info", map[string]string{"cluster": "a", "namespace": "shop", "pod": "api-0", "job": "ksm", "instance": "ksm-1"}), viewRow(metricPodOwner, map[string]string{"cluster": "a", "namespace": "shop", "pod": "api-0", "uid": "live-uid", "owner_kind": "ReplicaSet", "owner_name": "api"})}
				case strings.Contains(q, `cluster="a"`):
					if strings.Contains(q, ownerMetric.Name) {
						out = append(out, viewRow(ownerMetric.Name, clusterA))
					}
					if tc.filtered && strings.Contains(q, cpuMetric.Name) {
						out = append(out, viewRow(cpuMetric.Name, clusterA))
					}
				default:
					if strings.Contains(q, cpuMetric.Name) {
						out = append(out, viewRow(cpuMetric.Name, nil), viewAnchorRow(cpuMetric.Name))
					}
					if strings.Contains(q, ownerMetric.Name) {
						out = append(out, viewRow(ownerMetric.Name, clusterA), viewAnchorRow(ownerMetric.Name))
					}
				}
				return attributionEvidence(out, "")
			}}
			c := newViewScopeClient(t, store)
			req := viewRequest(true, cpuMetric, ownerMetric)
			req.View, req.Namespace = tc.view, "shop"
			result, err := c.ResolveViewScope(context.Background(), req, k8s.GetResourceCache())
			if err != nil || result.State != tc.want || alternate.Load() != tc.alternate || result.Withheld != (tc.want == ViewScopeLabelMissing) {
				t.Fatalf("scope=%+v err=%v alternate=%t", result, err, alternate.Load())
			}
			if tc.want == ViewScopeUnlabeled && (result.Withheld || result.Filter(cpuMetric.Name) != "" || len(result.Wire().Labels) > 0 || result.Wire().State != ViewScopeUnlabeled) {
				t.Fatalf("baseline local result was promoted to fleet evidence: %+v", result)
			}
			if tc.alternate && c.workloadPartition != nil {
				t.Fatalf("rightsizing proof leaked into shared historical memo: %+v", c.workloadPartition)
			}
		})
	}
}

func TestRightsizingOwnerAuditQueryBound(t *testing.T) {
	q := &fakeViewQuerier{answer: func(string) (*prom.QueryResult, error) { t.Fatal("oversize query sent"); return nil, nil }}
	ok, err := rightsizingSourcesMatchNodes(context.Background(), q, time.Now(), 0, map[string]string{"cluster": strings.Repeat("x", prom.MaxWorkloadQueryBytes)}, nil, nil)
	if ok || !errors.Is(err, prom.ErrWorkloadQueryTooLarge) {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
}
