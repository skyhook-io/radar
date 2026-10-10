package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/traffic"
)

func TestTrafficFlowOptions(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/traffic/flows?since=1m&excludeNamespaces=kube-system,+cert-manager,,&excludeHost=true", nil)
	opts, err := trafficFlowOptions(r, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Since != time.Minute || opts.Namespace != "" || len(opts.Namespaces) != 2 {
		t.Errorf("since=%v namespace=%q namespaces=%v, want 1m, no single namespace, both namespaces passed on", opts.Since, opts.Namespace, opts.Namespaces)
	}
	if strings.Join(opts.ExcludeNamespaces, ",") != "kube-system,cert-manager" || !opts.ExcludeHost {
		t.Errorf("exclusions = %v host=%v", opts.ExcludeNamespaces, opts.ExcludeHost)
	}

	single, _ := trafficFlowOptions(httptest.NewRequest(http.MethodGet, "/api/traffic/flows", nil), []string{"a"})
	if single.Namespace != "a" || single.ExcludeHost {
		t.Errorf("single namespace = %q host=%v, want the namespace for sources that filter on one", single.Namespace, single.ExcludeHost)
	}

	if _, err := trafficFlowOptions(httptest.NewRequest(http.MethodGet, "/api/traffic/flows?since=soon", nil), nil); err == nil {
		t.Error("an unparseable window must be rejected")
	}
}

// The flows response used to carry every record, which on a large Hubble
// install was tens of megabytes. The graph's aggregation still covers them
// all; only the records sent along are capped.
func TestTrafficFlowsPayloadCapsTheRecordSample(t *testing.T) {
	now := time.Now()
	var flows []traffic.Flow
	for i := range trafficFlowSample + 250 {
		flows = append(flows, traffic.Flow{
			Source:      traffic.Endpoint{Namespace: "a", Name: fmt.Sprintf("p%d", i%7)},
			Destination: traffic.Endpoint{Namespace: "a", Name: "db"},
			Connections: 1,
			LastSeen:    now.Add(-time.Duration(i) * time.Second),
		})
	}
	payload := trafficFlowsPayload(&traffic.FlowsResponse{Source: "hubble", FlowLimit: 50000}, flows)

	sample := payload["flows"].([]traffic.Flow)
	if len(sample) != trafficFlowSample || payload["flowsTotal"] != len(flows) {
		t.Fatalf("sample=%d total=%v, want %d of %d", len(sample), payload["flowsTotal"], trafficFlowSample, len(flows))
	}
	if !sample[0].LastSeen.Equal(now) || sample[len(sample)-1].LastSeen.Before(flows[trafficFlowSample-1].LastSeen) {
		t.Error("the sample must be the newest records, newest first")
	}
	var aggregated int64
	for _, a := range payload["aggregated"].([]traffic.AggregatedFlow) {
		aggregated += a.Connections
	}
	if aggregated != int64(len(flows)) {
		t.Errorf("aggregation covers %d connections, want all %d records", aggregated, len(flows))
	}
	if payload["flowLimit"] != 50000 {
		t.Errorf("flowLimit = %v, want the source's total cap passed through", payload["flowLimit"])
	}
}

func TestVisibleFlowsAppliesScopeExclusionsAndSelection(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	pod := func(ns, name string) traffic.Endpoint {
		return traffic.Endpoint{Namespace: ns, Name: name, Kind: traffic.EndpointKindPod}
	}
	flows := []traffic.Flow{
		{Source: pod("a", "web"), Destination: pod("a", "db")},
		{Source: pod("a", "web"), Destination: pod("kube-system", "dns")},
		{Source: pod("b", "x"), Destination: pod("b", "y")},
		{Source: pod("a", "web"), Destination: pod("a", "cache")},
	}
	opts := traffic.FlowOptions{
		ExcludeNamespaces: []string{"kube-system"},
		Match: &traffic.FlowMatch{Pairs: []traffic.EndpointPair{
			{Source: traffic.EndpointRef{Namespace: "a", Name: "web"}, Destination: traffic.EndpointRef{Namespace: "a", Name: "db"}},
			{Source: traffic.EndpointRef{Namespace: "b", Name: "x"}, Destination: traffic.EndpointRef{Namespace: "b", Name: "y"}},
		}},
	}
	got := s.visibleFlows(r, flows, []string{"a"}, opts)
	if len(got) != 1 || got[0].Destination.Name != "db" {
		t.Errorf("got %v, want only a/web→a/db: b is outside the caller's namespaces, kube-system is excluded, cache is not selected", got)
	}
}

func TestHandleGetTrafficRecordsRejectsBadSelections(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name  string
		match string
		want  int
	}{
		{"missing", "", http.StatusBadRequest},
		{"not JSON", "{", http.StatusBadRequest},
		{"empty", `{"endpoints":[]}`, http.StatusBadRequest},
		{"too large", `{"endpoints":[{"name":"` + strings.Repeat("x", maxTrafficMatchBytes) + `"}]}`, http.StatusRequestEntityTooLarge},
	} {
		w := httptest.NewRecorder()
		s.handleGetTrafficRecords(w, httptest.NewRequest(http.MethodGet, "/api/traffic/flows/records?match="+url.QueryEscape(tc.match), nil))
		if w.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}
