package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/rest"
)

func TestHubMetricsDecisionIsVisibleToConnectAndNextAdmission(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := connectionruntime.New(nil, connectionruntime.Settings{})
	if !k8s.AdmitTestIntegrations(runtime.Build) {
		t.Fatal("could not admit the fixture")
	}
	t.Cleanup(k8s.RetireServingConnection)
	s := server.New(server.Config{IntegrationRuntime: runtime, ListenAddress: "127.0.0.1"})
	t.Cleanup(s.Stop)
	old := connectionruntime.SetFor(k8s.Current()).Metrics
	decision := cloud.MetricsSourceDecision{
		Response: cloud.MetricsSourceResponse{Source: cloud.MetricsSourceOpenObserve, MatchedBy: "id"},
		Key:      "hub-relay",
		Endpoint: "https://hub.example/relay",
		NewTransport: func(time.Duration, int64) prom.Transport {
			return prom.NewHTTPTransport("https://hub.example", "", nil)
		},
	}
	ApplyHubMetricsSource(runtime, decision)
	check := func(warning string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/prometheus/connect", nil))
		var status prom.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || rec.Code != http.StatusOK ||
			!status.Connected || status.Source == nil || status.Source.Via != "hub" ||
			status.Source.Warning != warning || status.Address != cloud.MetricsRelayAddress {
			t.Fatalf("Connect did not return the Hub member: %d %s", rec.Code, rec.Body)
		}
	}
	check("")
	if !old.Closed() {
		t.Fatal("the Hub decision left the previous member live")
	}
	before := connectionruntime.SetFor(k8s.Current())
	decision.Response.Warning = "new relay metadata"
	ApplyHubMetricsSource(runtime, decision)
	check(decision.Response.Warning)
	after := connectionruntime.SetFor(k8s.Current())
	if after.Metrics != before.Metrics || after.Metrics.Closed() || after.Traffic != before.Traffic || after.Cost != before.Cost {
		t.Fatal("updating relay metadata replaced a serving member")
	}
	decision.Key = ""
	decision.Response.Warning = "invalid relay metadata"
	ApplyHubMetricsSource(runtime, decision)
	check("new relay metadata")
	k8s.RetireServingConnection()
	if !k8s.AdmitTestIntegrations(runtime.Build) {
		t.Fatal("could not readmit the fixture")
	}
	check("new relay metadata")
}
