package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestReadsWhileCustomResourcesPending(t *testing.T) {
	useTestResourceCache(t, testFakeClient)
	cache := k8s.GetResourceCache()
	cache.SetTestAPIDiscoveryPending(true)

	t.Run("issues", func(t *testing.T) {
		rec := httptest.NewRecorder()
		testServerSrv.handleIssues(rec, httptest.NewRequest(http.MethodGet, "/api/issues", nil))
		var body issuesapi.Response
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("issues: %d, %v", rec.Code, err)
		}
		if !slices.ContainsFunc(body.UncheckedSources, func(gap issuesapi.UncheckedSource) bool {
			return gap.Source == issuesapi.UncheckedSourceCustomResources && gap.Reason == issuesapi.UncheckedReasonSyncPending && len(gap.Kinds) == 0
		}) {
			t.Errorf("missing discovery gap in %+v", body.UncheckedSources)
		}
		if got := resourceIssuesCoverage("Widget", "example.com", "default"); got != resourceIssuesCoverageSyncing {
			t.Errorf("Widget issue coverage = %s, want syncing", got)
		}
	})
	t.Run("dashboard", func(t *testing.T) {
		rec := httptest.NewRecorder()
		testServerSrv.handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
		body := decodeDashboard(t, rec.Result())
		if body.ResourceCounts.Pods.Total == 0 || !slices.Contains(body.PartialData, "custom resources") || body.NetworkPolicyCoverage != nil {
			t.Errorf("pods = %d, partialData = %v, networkPolicyCoverage = %+v", body.ResourceCounts.Pods.Total, body.PartialData, body.NetworkPolicyCoverage)
		}
		if body.Audit == nil || !slices.Contains(body.Audit.MissingInputs, "custom resources") {
			t.Errorf("audit missing inputs = %+v", body.Audit)
		}
	})
	for _, path := range []string{"/api/api-resources", "/api/dashboard/crds", "/api/cnpg/workspace", "/api/velero/workspace", "/api/velero/schedules/default/nightly/activity", "/api/velero/runs/backups"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(testServer.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body struct {
				Code readreason.Code `json:"error_code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusServiceUnavailable || body.Code != readreason.KindSyncPending {
				t.Errorf("status = %d, code = %q, decode error = %v", resp.StatusCode, body.Code, err)
			}
		})
	}
}
