package server

import (
	"encoding/json"
	"testing"

	"github.com/skyhook-io/radar/pkg/traffic"
)

// The flows handler hand-builds its JSON payload, so a field the source sets can
// be silently dropped on the way out. WarningKind decides whether the client
// retries, and a dropped one is indistinguishable from a source that never set
// it — which means a permanent condition gets retried forever.
func TestTrafficFlowsPayloadKeepsWarningKind(t *testing.T) {
	response := &traffic.FlowsResponse{
		Source:      "beyla",
		Flows:       []traffic.Flow{},
		Warning:     "Beyla is not exporting dst.port and transport.",
		WarningKind: traffic.WarningPartial,
	}

	encoded, err := json.Marshal(trafficFlowsPayload(response, response.Flows))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded["warning"] == "" || decoded["warning"] == nil {
		t.Fatal("warning missing from the payload")
	}
	if got := decoded["warningKind"]; got != traffic.WarningPartial {
		t.Errorf("warningKind = %v, want %q — the client cannot tell a permanent condition from a hiccup without it",
			got, traffic.WarningPartial)
	}

	// A source that sets no kind must not gain one: absent means transient, and
	// inventing a value here would change how existing sources are retried.
	plain := &traffic.FlowsResponse{Source: "caretta", Flows: []traffic.Flow{}, Warning: "port-forward not ready"}
	if _, ok := trafficFlowsPayload(plain, plain.Flows)["warningKind"]; ok {
		t.Error("warningKind should be absent when the source did not set one")
	}
}

// A partial-data warning describes the flows it arrived with. When namespace
// filtering removes all of them, it describes edges this user cannot see.
func TestTrafficFlowsPayloadDropsWarningWhenFilteringRemovedEverything(t *testing.T) {
	response := &traffic.FlowsResponse{
		Source:      "beyla",
		Flows:       []traffic.Flow{{Source: traffic.Endpoint{Namespace: "other"}}},
		Warning:     "Beyla is not exporting dst.port and transport.",
		WarningKind: traffic.WarningPartial,
	}

	// Everything the source returned was filtered out.
	payload := trafficFlowsPayload(response, []traffic.Flow{})
	if _, ok := payload["warning"]; ok {
		t.Errorf("warning should be dropped: it qualifies flows the user cannot see, got %q", payload["warning"])
	}

	// The source itself returned nothing: the warning is the explanation for the
	// empty result and must survive.
	empty := &traffic.FlowsResponse{
		Source:      "beyla",
		Flows:       []traffic.Flow{},
		Warning:     "Some traffic is not shown: Beyla reports it as direction=unknown on both sides.",
		WarningKind: traffic.WarningPartial,
	}
	if _, ok := trafficFlowsPayload(empty, []traffic.Flow{})["warning"]; !ok {
		t.Error("an empty result needs its explanation kept")
	}

	// A transient warning beside flows is about figures missing from them, so it
	// goes with them — and kept on an empty payload it would have the client poll
	// every few seconds for traffic this user will never be shown.
	transient := &traffic.FlowsResponse{
		Source:      "beyla",
		Flows:       []traffic.Flow{{Source: traffic.Endpoint{Namespace: "other"}}},
		Warning:     "Beyla metrics are incomplete: HTTP 5xx error rates could not be read from Prometheus.",
		WarningKind: traffic.WarningTransient,
	}
	if _, ok := trafficFlowsPayload(transient, []traffic.Flow{})["warning"]; ok {
		t.Error("a transient warning about filtered-out flows must be dropped with them")
	}

	// A failed fetch returns no flows at all, so nothing was filtered and the
	// warning is the whole answer.
	failed := &traffic.FlowsResponse{
		Source:      "beyla",
		Flows:       []traffic.Flow{},
		Warning:     "Failed to query Beyla metrics: connection refused",
		WarningKind: traffic.WarningTransient,
	}
	if _, ok := trafficFlowsPayload(failed, []traffic.Flow{})["warning"]; !ok {
		t.Error("a failed fetch must keep its warning")
	}
}
