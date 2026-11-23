package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"

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

// failingThenAnsweringDiscovery fails every discovery until answer is set.
type failingThenAnsweringDiscovery struct {
	discovery.DiscoveryInterface
	answer atomic.Bool
}

func (d *failingThenAnsweringDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	if !d.answer.Load() {
		return nil, nil, errors.New("Get \"https://cluster/api\": context deadline exceeded")
	}
	return nil, []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"list", "watch"}},
	}}}, nil
}

// A discovery that failed outright leaves no catalog. Every cluster serves
// built-in kinds, so the catalog says discovery is pending (which the UI
// retries) and asks for a refresh, rather than answering an empty list.
func TestAPIResourcesAfterFailedDiscovery(t *testing.T) {
	useTestResourceCache(t, testFakeClient)
	disc := &failingThenAnsweringDiscovery{}
	restore, err := k8s.SetTestResourceDiscovery(disc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	disc.answer.Store(true) // the cluster answers from now on

	get := func() (int, []byte) {
		resp, err := http.Get(testServer.URL + "/api/api-resources")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if code, body := get(); code != http.StatusServiceUnavailable || !strings.Contains(string(body), string(readreason.KindSyncPending)) {
		t.Fatalf("empty catalog after a failed discovery: %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := get()
		if code == http.StatusOK && strings.Contains(string(body), `"pods"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog never recovered: %d %s", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
