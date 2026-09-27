package opencost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKubecostCandidateRequiresClusterData(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"empty allocation", `{"code":200,"data":[{}]}`, 200, true},
		{"cluster allocation", `{"code":200,"data":[{"test":{"properties":{"cluster":"test"},"totalCost":1}}]}`, 200, false},
		{"unauthorized", `{"error":"unauthorized"}`, 401, true},
		{"malformed", `not-json`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-API-KEY") != "candidate-key" {
					t.Error("candidate key missing")
				}
				if !strings.Contains(r.URL.Query().Get("filter"), "cluster") {
					t.Error("candidate probe was not cluster filtered")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			err := ProbeCandidate(context.Background(), ManagerConfig{Source: SourceKubecost, URL: server.URL, APIKey: "candidate-key", ClusterID: "test"})
			if (err != nil) != tc.wantError {
				t.Fatalf("probe error = %v", err)
			}
			if tc.status == 401 && !errors.Is(err, ErrKubecostAuthentication) {
				t.Fatalf("authentication failure lost: %v", err)
			}
		})
	}
}
