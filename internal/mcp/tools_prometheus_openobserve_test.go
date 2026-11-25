package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// setupOpenObserveRelay serves metrics the way an OpenObserve connection
// does: through a relay whose transport scopes every request to one cluster.
func setupOpenObserveRelay(t *testing.T) *fakeProm {
	t.Helper()
	f := &fakeProm{}
	srv := httptest.NewServer(f)
	prometheus.Initialize(nil, nil, "test-ctx")
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: oo.IDLabel, Value: "uid-1"}}}
	if err := prometheus.EnableRelay(prometheus.RelayConfig{
		Key:      "test-openobserve",
		Endpoint: srv.URL,
		Address:  "OpenObserve " + srv.URL,
		Source:   prom.MetricsSource{Kind: "openobserve", Via: "local", MatchedBy: "id"},
		NewTransport: func(time.Duration, int64) prom.Transport {
			return &oo.RewritingTransport{Inner: prom.NewHTTPTransport(srv.URL, "", nil), Options: opts}
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		prometheus.DisableRelay()
		srv.Close()
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "")
	})
	return f
}

func TestHandleQueryPrometheus_OpenObserveRefusesScalar(t *testing.T) {
	setupOpenObserveRelay(t)
	_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "scalar(sum(up))"})
	if err == nil {
		t.Fatal("scalar() over an OpenObserve relay answered")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "OpenObserve-backed metrics don't support scalar()") || !strings.Contains(msg, "sum(...)") {
		t.Fatalf("error = %q, want the scalar refusal telling the model what to query instead", msg)
	}
}
