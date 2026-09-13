package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestAttributionChurnRetainsOnlyUnchangedPairs(t *testing.T) {
	old := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "old"}, {Name: "api-1", UID: "unchanged"}}
	current := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "new"}, old[1], {Name: "api-2", UID: "added"}}
	key := "shop/Deployment/api"
	expires := time.Now().Add(time.Minute)
	entry := &workloadAttributionEntry{identity: fmt.Sprint(old), started: time.Now(), expires: expires, result: workloadAttributions{"cpu": {Pods: old, UID: true}}}
	c := &Client{workloadAttributionEntries: map[string]*workloadAttributionEntry{key: entry}}
	result, state := c.automaticWorkloadAttributions(PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: current})
	if state != "available" || len(result["cpu"].Pods) != 1 || result["cpu"].Pods[0] != old[1] {
		t.Fatalf("wrong retained population: %s %+v", state, result)
	}
	if len(entry.result["cpu"].Pods) != 2 || entry.expires != expires {
		t.Fatal("mutated prior evidence or extended expiry")
	}
	entry.expires = time.Now().Add(-time.Second)
	if result, _ := c.automaticWorkloadAttributions(PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: current}); result != nil {
		t.Fatal("reused expired intersection")
	}
}

func TestConnectionChangeClearsAttributionTimers(t *testing.T) {
	Initialize(nil, nil, "test")
	defer Reset()
	for _, change := range []func(){func() { SetManualURL("http://changed:9090") }, func() { SetHeaders(map[string]string{"X-Scope-OrgID": "changed"}) }, Reset} {
		ctx, cancel := context.WithCancel(context.Background())
		c := GetClient()
		c.mu.Lock()
		c.workloadAttributionEntries = map[string]*workloadAttributionEntry{"recent": {started: time.Now(), cancel: cancel}}
		c.mu.Unlock()
		change()
		c.mu.RLock()
		remaining := len(c.workloadAttributionEntries)
		c.mu.RUnlock()
		if ctx.Err() == nil || remaining != 0 {
			cancel()
			t.Fatal("connection retained old probe or churn timer")
		}
	}
}

func TestAttributionOversizeIsExplained(t *testing.T) {
	var pods []prom.WorkloadPodIdentity
	for i := 0; i < 100; i++ {
		pods = append(pods, prom.WorkloadPodIdentity{Name: strings.Repeat("a", 200) + fmt.Sprint(i), UID: "030a7597-c1fc-48b0-9bb4-683489285358"})
	}
	client := prom.NewClient(attributionTransport(func(string) ([]byte, error) { t.Fatal("oversized probe reached transport"); return nil, nil }))
	plans, err := probeWorkloadAttributions(context.Background(), client, PodScope{Namespace: "shop", Identities: pods})
	if err != nil || len(plans) != 5 {
		t.Fatalf("unexpected result: %+v %v", plans, err)
	}
	for _, plan := range plans {
		if plan.State != "unavailable" || !strings.Contains(plan.Reason, "query limit") {
			t.Fatalf("missing limit explanation: %+v", plan)
		}
	}
}

func TestAttributionWaitsForDiscoveryBeforeProbeDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []prom.Series{}
		if r.FormValue("query") == "up" {
			rows = append(rows, prom.Series{Labels: map[string]string{"job": "fixture"}})
		}
		_, _ = w.Write(attributionEvidence(rows, ""))
	}))
	defer server.Close()
	select {
	case discoveryGate <- struct{}{}:
	default:
		t.Fatal("discovery gate busy")
	}
	held := true
	defer func() {
		if held {
			<-discoveryGate
		}
	}()
	c := &Client{manualURL: server.URL, httpClient: server.Client()}
	defer func() { c.mu.Lock(); c.retired = true; c.cancelWorkloadAttributionsLocked(); c.mu.Unlock() }()
	scope := PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}}}
	c.automaticWorkloadAttributions(scope)
	time.Sleep(8200 * time.Millisecond)
	if _, state := c.automaticWorkloadAttributions(scope); state != "detecting" {
		t.Fatalf("discovery became probe error: %s", state)
	}
	<-discoveryGate
	held = false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, state := c.automaticWorkloadAttributions(scope)
		if state == "available" {
			return
		}
		if state == "error" {
			t.Fatal("discovery success left an error lockout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("attribution did not finish after discovery")
}

func TestAttributionUIDIsApplicationNotCollector(t *testing.T) {
	pods := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}}
	rows := []prom.Series{{Labels: map[string]string{"pod": "collector", "k8s_pod_name": "api-0", "k8s_pod_uid": pods[0].UID, "job": "custom"}}}
	if got := matchingUIDPods(pods, rows, true); len(got) != 1 {
		t.Fatalf("matching application missing: %v", got)
	}
	rows[0].Labels["k8s_pod_uid"] = "0d709b51-f624-4d0a-b7dd-79e17402acb9"
	if got := matchingUIDPods(pods, rows, true); len(got) != 0 {
		t.Fatalf("foreign UID included: %v", got)
	}
}

type attributionTransport func(string) ([]byte, error)

func (f attributionTransport) Do(_ context.Context, _, _ string, params url.Values) ([]byte, error) {
	return f(params.Get("query"))
}
func (attributionTransport) Address() string { return "fixture" }

func attributionEvidence(rows []prom.Series, warning string) []byte {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, map[string]any{"metric": row.Labels, "value": []any{100, "1"}})
	}
	body := map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}}
	if warning != "" {
		body[warning] = []string{"partial response"}
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func TestProbeAttributionSources(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	scope := PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}}
	for _, tc := range []struct {
		name, warning                            string
		uid, partition, failKSM, duplicateBucket bool
		wantUID                                  bool
		wantState                                string
	}{
		{name: "UID without KSM", uid: true, failKSM: true, wantUID: true},
		{name: "partition", partition: true},
		{name: "no identity"},
		{name: "KSM failure", failKSM: true, wantState: "error"},
		{name: "partial warning", warning: "warnings", wantState: "probe error"},
		{name: "partial info", warning: "infos", wantState: "probe error"},
		{name: "duplicate histogram replica", uid: true, duplicateBucket: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := map[string]string{"__name__": "http_server_request_duration_seconds_count", "k8s_pod_name": pod.Name, "job": "custom"}
			if tc.uid {
				labels["k8s_pod_uid"] = pod.UID
			}
			if tc.partition {
				labels["cluster"] = "local"
			}
			rows := []prom.Series{{Labels: labels}}
			if tc.duplicateBucket {
				rows = append(rows, prom.Series{Labels: map[string]string{"__name__": "http_server_request_duration_seconds_bucket", "k8s_pod_name": pod.Name, "k8s_pod_uid": pod.UID, "job": "custom", "replica": "other"}})
			}
			client := prom.NewClient(attributionTransport(func(query string) ([]byte, error) {
				if strings.Contains(query, "kube_pod_info") {
					if tc.failKSM {
						return nil, errors.New("denied")
					}
					if tc.partition {
						return attributionEvidence([]prom.Series{{Labels: map[string]string{"pod": pod.Name, "uid": pod.UID, "cluster": "local"}}}, ""), nil
					}
					return attributionEvidence(nil, ""), nil
				}
				return attributionEvidence(rows, tc.warning), nil
			}))
			got, err := probeWorkloadAttributions(context.Background(), client, scope)
			if tc.wantState == "probe error" {
				if err == nil {
					t.Fatal("accepted partial evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			plan := got["beyla"]
			if plan.UID != tc.wantUID || plan.State != tc.wantState {
				t.Fatalf("plan: %+v", plan)
			}
			if tc.partition && (plan.Scope.ClusterLabels["cluster"] != "local" || len(plan.Pods) != 1) {
				t.Fatalf("missing partition: %+v", plan)
			}
			if !tc.wantUID && !tc.partition && len(plan.Pods) != 0 {
				t.Fatalf("invented attribution: %+v", plan)
			}
		})
	}
}

func TestProbeFamilyBoundIsIndependent(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	var rows []prom.Series
	for i := 0; i < 1025; i++ {
		rows = append(rows, prom.Series{Labels: map[string]string{"__name__": "container_memory_working_set_bytes", "pod": pod.Name, "id": fmt.Sprintf("/foreign/%d", i)}})
	}
	rows = append(rows, prom.Series{Labels: map[string]string{"__name__": "http_server_request_duration_seconds_count", "k8s_pod_name": pod.Name, "k8s_pod_uid": pod.UID, "job": "custom"}})
	client := prom.NewClient(attributionTransport(func(query string) ([]byte, error) { return attributionEvidence(rows, ""), nil }))
	got, err := probeWorkloadAttributions(context.Background(), client, PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}})
	if err != nil || !got["beyla"].UID || len(got["memory"].Pods) != 0 || !strings.Contains(got["memory"].Reason, "Too many") {
		t.Fatalf("family bound poisoned another source: %+v %v", got, err)
	}
}

func TestAttributionTwoLabelPartition(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	rows := []prom.Series{
		{Labels: map[string]string{"pod": pod.Name, "uid": pod.UID, "cluster": "a", "cluster_id": "1"}},
		{Labels: map[string]string{"pod": pod.Name, "uid": "foreign", "cluster": "a", "cluster_id": "2"}},
		{Labels: map[string]string{"pod": pod.Name, "uid": "foreign", "cluster": "b", "cluster_id": "1"}},
	}
	labels, pods := identifyPartition([]prom.WorkloadPodIdentity{pod}, rows)
	if len(labels) != 2 || len(pods) != 1 {
		t.Fatalf("two-label partition: %v %v", labels, pods)
	}
}

func TestAttributionPartitionRejectsSourceUIDConflict(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	for _, beyla := range []bool{true, false} {
		family, key := "container_cpu_usage_seconds_total", "cpu"
		labels := map[string]string{"pod": pod.Name, "cluster": "local", "id": "/kubepods/pod0d709b51-f624-4d0a-b7dd-79e17402acb9/container", "job": "custom"}
		if beyla {
			family, key = "http_server_request_duration_seconds_count", "beyla"
			labels["k8s_pod_name"], labels["k8s_pod_uid"] = pod.Name, "0d709b51-f624-4d0a-b7dd-79e17402acb9"
		}
		labels["__name__"] = family
		client := prom.NewClient(attributionTransport(func(query string) ([]byte, error) {
			if strings.Contains(query, "kube_pod_info") {
				return attributionEvidence([]prom.Series{{Labels: map[string]string{"pod": pod.Name, "uid": pod.UID, "cluster": "local"}}}, ""), nil
			}
			return attributionEvidence([]prom.Series{{Labels: labels}}, ""), nil
		}))
		plans, err := probeWorkloadAttributions(context.Background(), client, PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}})
		if err != nil || len(plans[key].Pods) != 0 || !strings.Contains(plans[key].Reason, "contradict") {
			t.Fatalf("accepted contradictory %s: %+v %v", key, plans, err)
		}
	}
}

func TestAttributionMemoIdentityAndGeneration(t *testing.T) {
	scope := PodScope{Namespace: "shop", Kind: "StatefulSet", Name: "api", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "original"}}}
	key := "shop/StatefulSet/api"
	for _, tc := range []struct {
		name                                 string
		changeUID, changeGeneration, retired bool
		want                                 string
	}{
		{name: "same identity", want: "available"},
		{name: "same name new UID", changeUID: true, want: "detecting"},
		{name: "new connection", changeGeneration: true, want: "detecting"},
		{name: "retired", retired: true, want: "detecting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entry := &workloadAttributionEntry{identity: fmt.Sprint(scope.Identities), started: time.Now(), expires: time.Now().Add(time.Minute), result: workloadAttributions{"cpu": {UID: true}}, cancel: cancel}
			c := &Client{retired: tc.retired, workloadAttributionEntries: map[string]*workloadAttributionEntry{key: entry}}
			request := scope
			if tc.changeUID {
				request.Identities = []prom.WorkloadPodIdentity{{Name: "api-0", UID: "replacement"}}
			}
			if tc.changeGeneration {
				c.discoveryGen++
			}
			got, state := c.automaticWorkloadAttributions(request)
			if state != tc.want {
				t.Fatalf("state %s want %s", state, tc.want)
			}
			if tc.want != "available" && got != nil {
				t.Fatal("old evidence escaped")
			}
			if (tc.changeUID || tc.changeGeneration) && ctx.Err() == nil {
				t.Fatal("superseded probe not canceled")
			}
		})
	}
}

func TestAttributionMemoAdmissionAndRefresh(t *testing.T) {
	scope := PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "uid"}}}
	entries := map[string]*workloadAttributionEntry{}
	for i := 0; i < 2; i++ {
		entries[fmt.Sprint(i)] = &workloadAttributionEntry{cancel: func() {}}
	}
	c := &Client{workloadAttributionEntries: entries}
	if _, state := c.automaticWorkloadAttributions(scope); state != "detecting" || len(entries) != 2 {
		t.Fatal("admission limit not enforced")
	}
	entries["shop/Deployment/api"] = &workloadAttributionEntry{identity: fmt.Sprint(scope.Identities), started: time.Now().Add(-4 * time.Minute), expires: time.Now().Add(30 * time.Second), result: workloadAttributions{"cpu": {UID: true}}}
	if result, state := c.automaticWorkloadAttributions(scope); state != "available" || result == nil {
		t.Fatal("refresh admission blanked valid charts")
	}
	entries["shop/Deployment/api"].expires = time.Now().Add(-time.Second)
	if result, state := c.automaticWorkloadAttributions(scope); state != "detecting" || result != nil {
		t.Fatal("expired evidence escaped")
	}
}

func TestAttributionFailedRefreshPreservesOriginalExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"job": "up"}}}, ""))
			return
		}
		http.Error(w, "transient", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	scope := PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}}}
	key := "shop/Deployment/api"
	expiry := time.Now().Add(45 * time.Second)
	entry := &workloadAttributionEntry{identity: fmt.Sprint(scope.Identities), started: time.Now().Add(-4 * time.Minute), expires: expiry, result: workloadAttributions{"cpu": {UID: true, Pods: scope.Identities}}}
	c := &Client{baseURL: server.URL, httpClient: server.Client(), workloadAttributionEntries: map[string]*workloadAttributionEntry{key: entry}}
	if _, state := c.automaticWorkloadAttributions(scope); state != "available" {
		t.Fatalf("refresh blanked charts: %s", state)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.RLock()
		current := c.workloadAttributionEntries[key]
		finished := current.cancel == nil
		c.mu.RUnlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	got, state := c.automaticWorkloadAttributions(scope)
	if state != "available" || !got["cpu"].UID {
		t.Fatalf("failed refresh lost valid evidence: %s %+v", state, got)
	}
	c.mu.RLock()
	gotExpiry := c.workloadAttributionEntries[key].expires
	c.mu.RUnlock()
	if !gotExpiry.Equal(expiry) {
		t.Fatal("failed refresh extended trust")
	}
}

func TestAttributionPartitionConflicts(t *testing.T) {
	pods := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}}
	local := prom.Series{Labels: map[string]string{"pod": "api-0", "uid": pods[0].UID, "cluster": "local"}}
	foreign := prom.Series{Labels: map[string]string{"pod": "api-0", "uid": "foreign", "cluster": "foreign"}}
	labels, matched := identifyPartition(pods, []prom.Series{local, foreign})
	if labels["cluster"] != "local" || len(matched) != 1 {
		t.Fatalf("failed partition: %v %v", labels, matched)
	}
	foreign.Labels["cluster"] = "local"
	if labels, _ := identifyPartition(pods, []prom.Series{local, foreign}); len(labels) != 0 {
		t.Fatalf("accepted colliding partition %v", labels)
	}
	delete(local.Labels, "cluster")
	if labels, _ := identifyPartition(pods, []prom.Series{local}); len(labels) != 0 {
		t.Fatalf("invented local trust %v", labels)
	}
}

func TestNegativeAttributionRemainsVisibleDuringRefresh(t *testing.T) {
	scope := PodScope{Namespace: "shop", Kind: "Deployment", Name: "api", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}}}
	for _, positive := range []bool{false, true} {
		plan := workloadAttribution{Reason: "No matching identities."}
		if positive {
			plan.Pods = scope.Identities
			plan.UID = true
		}
		expired := time.Now().Add(-time.Second)
		entry := &workloadAttributionEntry{identity: fmt.Sprint(scope.Identities), started: time.Now(), expires: expired, result: workloadAttributions{"cpu": plan}, cancel: func() {}}
		c := &Client{workloadAttributionEntries: map[string]*workloadAttributionEntry{"shop/Deployment/api": entry}}
		got, state := c.automaticWorkloadAttributions(scope)
		if positive {
			if state != "detecting" || got != nil {
				t.Fatal("expired positive evidence reused")
			}
		} else if state != "available" || got["cpu"].Reason != plan.Reason {
			t.Fatal("negative refresh blanked explanation")
		}
		if !entry.expires.Equal(expired) {
			t.Fatal("trust expiry extended")
		}
		c.discoveryGen++
		if got, state = c.automaticWorkloadAttributions(scope); state != "detecting" || got != nil {
			t.Fatal("reused result across connections")
		}
	}
}

func TestAttributionDoesNotAddHAReplicas(t *testing.T) {
	rows := []prom.Series{{Labels: map[string]string{"job": "beyla", "prometheus_replica": "a"}}, {Labels: map[string]string{"job": "beyla", "prometheus_replica": "b"}}}
	if _, ok := attributionJob(rows, nil, true); ok {
		t.Fatal("accepted ambiguous HA replicas")
	}
	if job, ok := attributionJob(rows[:1], nil, true); !ok || job != `job="beyla"` {
		t.Fatalf("single population: %q %v", job, ok)
	}
}
