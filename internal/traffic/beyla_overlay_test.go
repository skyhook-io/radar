package traffic

import (
	"context"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLikelyOverlay(t *testing.T) {
	cases := []struct {
		src, dst, transport string
		want                bool
	}{
		{"Node", "Node", "UDP", true},
		{"node", "node", "udp", true},
		{"Node", "Node", "TCP", false},
		{"Node", "Node", "", false}, // transport not exported: UDP cannot be told apart
		{"Node", "Deployment", "UDP", false},
		{"Deployment", "Deployment", "UDP", false},
	}
	for _, c := range cases {
		if got := likelyOverlay(c.src, c.dst, c.transport); got != c.want {
			t.Errorf("likelyOverlay(%q, %q, %q) = %t, want %t", c.src, c.dst, c.transport, got, c.want)
		}
	}
}

func TestBeylaSource_GetFlows_MarksNodeToNodeUDPAsLikelyOverlay(t *testing.T) {
	node := func(src, dst, transport string) map[string]string {
		return map[string]string{
			"k8s_src_owner_name": src, "k8s_src_owner_type": "Node",
			"k8s_dst_owner_name": dst, "k8s_dst_owner_type": "Node", "transport": transport,
		}
	}
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = func(_ context.Context, query string) (*prom.QueryResult, error) {
		if !strings.Contains(query, "beyla_network_flow_bytes_total") {
			return emptyResult(), nil
		}
		if strings.Contains(query, `direction="unknown"`) {
			// Encapsulated pod traffic between nodes: no initiator either way.
			return promResult("vector", promSeries(node("node-a", "node-b", "UDP"), 100.0)), nil
		}
		return promResult("vector",
			promSeries(node("node-a", "node-c", "UDP"), 50.0),
			promSeries(node("node-a", "node-c", "TCP"), 20.0),
		), nil
	}

	resp, err := src.GetFlows(context.Background(), FlowOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	marked := map[string]bool{}
	for _, f := range resp.Flows {
		marked[f.Destination.Name+"|"+f.Protocol] = f.LikelyOverlay
	}
	if !marked["node-b|udp"] || !marked["node-c|udp"] {
		t.Errorf("node-to-node UDP must be marked on both paths: %v", marked)
	}
	if marked["node-c|tcp"] {
		t.Errorf("node-to-node TCP is not overlay: %v", marked)
	}
	for _, a := range AggregateFlows(resp.Flows) {
		if a.Destination.Name == "node-b" && !a.LikelyOverlay {
			t.Error("the aggregate edge must keep the marking")
		}
	}
}
