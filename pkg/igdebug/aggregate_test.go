package igdebug

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestParseProcessesFiltersPinnedContainer(t *testing.T) {
	raw := []byte(`[
		{"comm":"nginx","pid":42,"parent":{"comm":"containerd-shim","pid":10},"creds":{"user":"root","uid":0},"runtime":{"containerId":"wanted"}},
		{"comm":"sidecar","pid":84,"runtime":{"containerId":"other"}}
	]`)
	rows, err := ParseProcesses(raw, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Command != "nginx" || rows[0].ParentPID != 10 {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestParseConnectionsMapsStateAndEndpoint(t *testing.T) {
	raw := []byte(`[{
		"src":{"addr":"10.0.0.2","port":4444,"proto":"TCP","k8s":{"kind":"pod","namespace":"app","name":"client"}},
		"dst":{"addr":"10.0.0.3","port":5432,"proto":"TCP","k8s":{"kind":"svc","namespace":"app","name":"postgres"}},
		"state":5,"runtime":{"containerId":"wanted"}
	}]`)
	rows, err := ParseConnections(raw, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "FIN_WAIT2" || rows[0].Remote.Resource == nil || rows[0].Remote.Resource.Name != "postgres" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestParseConnectionsUsesUDPStateVocabulary(t *testing.T) {
	raw := []byte(`[
		{"src":{"addr":"10.0.0.2","port":5353,"proto":"UDP"},"dst":{"addr":"0.0.0.0","port":0,"proto":"UDP"},"state":7,"runtime":{"containerId":"wanted"}},
		{"src":{"addr":"10.0.0.2","port":5354,"proto":"UDP"},"dst":{"addr":"10.0.0.10","port":53,"proto":"UDP"},"state":1,"runtime":{"containerId":"wanted"}}
	]`)
	rows, err := ParseConnections(raw, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].State != "UNCONNECTED" || rows[1].State != "CONNECTED" {
		t.Fatalf("unexpected UDP states: %#v", rows)
	}
}

func TestDNSAggregatorUsesReleasedResponseFieldsAndTracksUnmatched(t *testing.T) {
	agg := NewDNSAggregator("wanted")
	events := []map[string]any{
		{"id": "a1", "name": "db.app.svc.cluster.local.", "qtype": "A", "qr": "Q", "runtime": map[string]any{"containerId": "wanted"}, "src": endpointFixture("10.0.0.2", 42000, "UDP"), "dst": endpointFixture("10.0.0.10", 53, "UDP")},
		{"id": "a1", "name": "db.app.svc.cluster.local.", "qtype": "A", "qr": "R", "rcode": "Success", "addresses": "10.0.1.9", "latency_ns_raw": 2_500_000, "runtime": map[string]any{"containerId": "wanted"}, "src": endpointFixture("10.0.0.10", 53, "UDP"), "dst": endpointFixture("10.0.0.2", 42000, "UDP")},
		{"id": "b2", "name": "missing.app.svc.cluster.local.", "qtype": "A", "qr": "Q", "runtime": map[string]any{"containerId": "wanted"}, "src": endpointFixture("10.0.0.2", 43000, "UDP"), "dst": endpointFixture("10.0.0.10", 53, "UDP")},
		{"id": "ignored", "name": "other", "qtype": "A", "qr": "Q", "runtime": map[string]any{"containerId": "other"}},
	}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if _, err := agg.Add(raw); err != nil {
			t.Fatal(err)
		}
	}
	groups, unmatched, count, truncated := agg.Result()
	if count != 3 || unmatched != 1 || len(groups) != 1 {
		t.Fatalf("count=%d unmatched=%d groups=%#v", count, unmatched, groups)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if groups[0].AverageLatencyMs != 2.5 || groups[0].ResponseCode != "Success" || len(groups[0].Addresses) != 1 {
		t.Fatalf("unexpected group: %#v", groups[0])
	}
}

func TestDNSV0541LiveShapeFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/dns-v0.54.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	agg := NewDNSAggregator("wanted")
	for _, event := range events {
		if _, err := agg.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	groups, _, count, _ := agg.Result()
	if count != 3 || len(groups) != 2 || groups[0].ResponseCode != "NameError" || groups[1].ResponseCode != "Success" {
		t.Fatalf("count=%d groups=%#v", count, groups)
	}
}

func TestDNSAggregatorRejectsMalformedEvent(t *testing.T) {
	agg := NewDNSAggregator("wanted")
	if _, err := agg.Add([]byte(`{"id":`)); err == nil {
		t.Fatal("expected malformed event error")
	}
}

func TestDNSAggregatorDoesNotCountReplyBeforeQueryAsUnmatched(t *testing.T) {
	agg := NewDNSAggregator("wanted")
	events := []map[string]any{
		{"id": "a1", "name": "db.app.svc.cluster.local.", "qtype": "A", "qr": "R", "rcode": "Success", "latency_ns_raw": 1_000_000, "runtime": map[string]any{"containerId": "wanted"}, "src": endpointFixture("10.0.0.10", 53, "UDP"), "dst": endpointFixture("10.0.0.2", 42000, "UDP")},
		{"id": "a1", "name": "db.app.svc.cluster.local.", "qtype": "A", "qr": "Q", "runtime": map[string]any{"containerId": "wanted"}, "src": endpointFixture("10.0.0.2", 42000, "UDP"), "dst": endpointFixture("10.0.0.10", 53, "UDP")},
	}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if _, err := agg.Add(raw); err != nil {
			t.Fatal(err)
		}
	}
	groups, unmatched, count, _ := agg.Result()
	if count != 2 || unmatched != 0 || len(groups) != 1 {
		t.Fatalf("count=%d unmatched=%d groups=%#v", count, unmatched, groups)
	}
}

func TestDNSAggregatorAveragesOnlyMeasuredLatencies(t *testing.T) {
	agg := NewDNSAggregator("wanted")
	for _, latency := range []int64{0, 2_000_000} {
		raw, _ := json.Marshal(map[string]any{
			"id": "a1", "name": "db.app.svc.cluster.local.", "qtype": "A", "qr": "R", "rcode": "Success",
			"latency_ns_raw": latency, "runtime": map[string]any{"containerId": "wanted"},
			"src": endpointFixture("10.0.0.10", 53, "UDP"), "dst": endpointFixture("10.0.0.2", 42000, "UDP"),
		})
		if _, err := agg.Add(raw); err != nil {
			t.Fatal(err)
		}
	}
	groups, _, _, _ := agg.Result()
	if len(groups) != 1 || groups[0].Count != 2 || groups[0].AverageLatencyMs != 2 {
		t.Fatalf("unexpected groups: %#v", groups)
	}
}

func TestDNSAggregatorCapsRetainedState(t *testing.T) {
	agg := NewDNSAggregator("wanted")
	for index := 0; index <= maxDNSPending; index++ {
		raw, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("query-%d", index), "name": "pending.example.", "qtype": "A", "qr": "Q",
			"runtime": map[string]any{"containerId": "wanted"},
			"src":     endpointFixture("10.0.0.2", index, "UDP"), "dst": endpointFixture("10.0.0.10", 53, "UDP"),
		})
		if _, err := agg.Add(raw); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index <= maxDNSGroups; index++ {
		raw, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("reply-%d", index), "name": fmt.Sprintf("group-%d.example.", index), "qtype": "A", "qr": "R", "rcode": "Success",
			"addresses": "10.0.0.20", "runtime": map[string]any{"containerId": "wanted"},
			"src": endpointFixture("10.0.0.10", 53, "UDP"), "dst": endpointFixture("10.0.0.2", index, "UDP"),
		})
		if _, err := agg.Add(raw); err != nil {
			t.Fatal(err)
		}
	}
	groups, unmatched, _, truncated := agg.Result()
	if !truncated || unmatched != maxDNSPending || len(groups) != maxDNSGroups {
		t.Fatalf("truncated=%v unmatched=%d groups=%d", truncated, unmatched, len(groups))
	}
}

func endpointFixture(address string, port int, protocol string) map[string]any {
	return map[string]any{"addr": address, "port": port, "proto": protocol}
}
