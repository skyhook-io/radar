package prometheus

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// TestRawQueryRefusalIsTheCallersError: a query refused as bad input, by
// Radar Hub's relay or by the local OpenObserve scoping, answers 400 with the
// refusal; a failed backend still answers 502.
func TestRawQueryRefusalIsTheCallersError(t *testing.T) {
	SetAuthGate(nil)
	scoped := func(inner prom.Transport) func(time.Duration, int64) prom.Transport {
		return func(time.Duration, int64) prom.Transport {
			return &oo.RewritingTransport{Inner: inner, Options: oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: "uid-1"}}}}
		}
	}
	relayRefusal := &prom.RefusedError{Message: "OpenObserve refused the query: parse error", Err: &prom.HTTPError{StatusCode: http.StatusBadRequest}}
	for _, tc := range []struct {
		name      string
		transport func(time.Duration, int64) prom.Transport
		query     string
		status    int
		message   string
	}{
		{"relay refusal", (&fakeRelayTransport{err: relayRefusal}).build, "up", http.StatusBadRequest, "OpenObserve refused the query: parse error"},
		{"local cluster label write", scoped(relayTransportFunc{&fakeRelayTransport{}}), `label_replace(up, "radar_cluster_uid", "x", "", "")`, http.StatusBadRequest, "Queries may not write a cluster label"},
		{"local parse error", scoped(relayTransportFunc{&fakeRelayTransport{}}), `sum(`, http.StatusBadRequest, ""},
		{"backend failure", (&fakeRelayTransport{err: &prom.HTTPError{StatusCode: http.StatusInternalServerError}}).build, "up", http.StatusBadGateway, "Prometheus query failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := withRelayTestClient(t)
			cfg := testRelayConfig(&fakeRelayTransport{}, nil)
			cfg.NewTransport = tc.transport
			if err := client.EnableRelay(cfg); err != nil {
				t.Fatal(err)
			}
			for _, typ := range []string{"instant", "range"} {
				rec := getMetrics(t, metricsRouter(client), "/prometheus/query?type="+typ+"&query="+url.QueryEscape(tc.query))
				var body struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				if rec.Code != tc.status || !strings.HasPrefix(body.Error, tc.message) {
					t.Fatalf("%s: %d %q, want %d %q", typ, rec.Code, body.Error, tc.status, tc.message)
				}
			}
		})
	}
}
