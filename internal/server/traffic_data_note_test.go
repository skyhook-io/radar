package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/pkg/traffic"
)

func TestTrafficPayloadDataNote(t *testing.T) {
	for name, payload := range map[string]func(*traffic.FlowsResponse, []traffic.Flow) map[string]any{
		"flows": func(response *traffic.FlowsResponse, flows []traffic.Flow) map[string]any {
			return trafficFlowsPayload(response, flows, false)
		},
		"records": trafficRecordsPayload,
	} {
		t.Run(name, func(t *testing.T) {
			flows := []traffic.Flow{
				{Source: traffic.Endpoint{Namespace: "a"}},
				{Source: traffic.Endpoint{Namespace: "b"}},
			}
			for _, tc := range []struct {
				name       string
				note       string
				namespaces []string
				source     []traffic.Flow
				wantNote   bool
			}{
				{"carried through", "Reported bytes may overlap.", nil, flows, true},
				{"all filtered away", "Reported bytes may overlap.", []string{"c"}, flows, false},
				{"some remain", "Reported bytes may overlap.", []string{"a"}, flows, true},
				{"source returned none", "Reported bytes may overlap.", nil, nil, true},
				{"absent", "", nil, flows, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response := &traffic.FlowsResponse{Flows: tc.source, DataNote: tc.note}
					s := &Server{}
					r := httptest.NewRequest(http.MethodGet, "/", nil)
					visible := s.visibleFlows(r, response.Flows, tc.namespaces, traffic.DefaultFlowOptions())
					encoded, err := json.Marshal(payload(response, visible))
					if err != nil {
						t.Fatal(err)
					}
					var decoded map[string]any
					if err := json.Unmarshal(encoded, &decoded); err != nil {
						t.Fatal(err)
					}
					note, present := decoded["dataNote"]
					if present != tc.wantNote || present && note != tc.note {
						t.Errorf("dataNote = %v (present %v), want %q (present %v)", note, present, tc.note, tc.wantNote)
					}
					for _, field := range []string{"warning", "warningKind"} {
						if _, present := decoded[field]; present {
							t.Errorf("a data note must not set %s", field)
						}
					}
				})
			}
		})
	}
}
