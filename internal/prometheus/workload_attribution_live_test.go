package prometheus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestWorkloadAttributionVictoriaMetrics(t *testing.T) {
	endpoint := os.Getenv("RADAR_TEST_ATTRIBUTION_VM_URL")
	if endpoint == "" {
		t.Skip("set RADAR_TEST_ATTRIBUTION_VM_URL to a disposable loopback VictoriaMetrics fixture")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture must be a disposable loopback server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	end := time.Now().Truncate(time.Second).Add(-30 * time.Second)
	ns := fmt.Sprintf("radar-attribution-%d", time.Now().UnixNano())
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	var body bytes.Buffer
	for _, cluster := range []string{"local", "foreign"} {
		uid, rate := pod.UID, float64(1)
		if cluster == "foreign" {
			uid, rate = "0d709b51-f624-4d0a-b7dd-79e17402acb9", 100
		}
		for _, family := range []string{"http_server_request_duration_seconds_count", "istio_requests_total", "kube_pod_info", "container_cpu_usage_seconds_total"} {
			labels := map[string]string{"__name__": family, "namespace": ns, "pod": pod.Name, "cluster": cluster, "job": "custom"}
			switch family {
			case "container_cpu_usage_seconds_total":
				labels["container"], labels["id"] = "app", "/kubepods/burstable/pod"+uid+"/container"
			case "http_server_request_duration_seconds_count":
				labels["k8s_namespace_name"], labels["k8s_pod_name"], labels["k8s_pod_uid"] = ns, pod.Name, uid
				labels["http_response_status_code"] = "200"
			case "istio_requests_total":
				labels["reporter"], labels["request_protocol"], labels["destination_workload_namespace"], labels["response_code"] = "destination", "http", ns, "200"
			case "kube_pod_info":
				labels["uid"] = uid
			}
			var timestamps []int64
			var values []float64
			for i := 0; i <= 40; i++ {
				timestamps = append(timestamps, end.Add(time.Duration(i-40)*15*time.Second).UnixMilli())
				value := float64(i) * 15 * rate
				if family == "kube_pod_info" {
					value = 1
				}
				values = append(values, value)
			}
			if err := json.NewEncoder(&body).Encode(map[string]any{"metric": labels, "values": values, "timestamps": timestamps}); err != nil {
				t.Fatal(err)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/v1/import", &body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("import status %d", response.StatusCode)
	}
	// Imported samples become queryable on the fixture's flush interval.
	flush, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/internal/force_flush", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(flush)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	client := prom.NewClient(prom.NewHTTPTransport(endpoint, "", nil))
	plans, err := probeWorkloadAttributions(ctx, client, PodScope{Namespace: ns, Identities: []prom.WorkloadPodIdentity{pod}})
	if err != nil {
		t.Fatal(err)
	}
	if !plans["beyla"].UID || plans["istio"].Scope.ClusterLabels["cluster"] != "local" {
		t.Fatalf("attribution: %+v", plans)
	}
	for _, source := range []prom.RequestSource{prom.RequestSourceBeyla, prom.RequestSourceIstio} {
		plan := plans[string(source)]
		selection := identitySelection(ns, plan.Pods)
		var queries prom.RequestQueries
		if plan.UID {
			queries, err = prom.BuildIdentityRequestQueries(time.Minute, selection, plan.Pods, source, plan.Job)
		} else {
			queries, err = prom.BuildRequestQueries(time.Minute, selection, plan.Scope, source, plan.Job)
		}
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.QueryWorkloadRange(ctx, queries.Rate, end.Add(-time.Minute), end, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Series) != 1 || len(result.Series[0].DataPoints) == 0 {
			t.Fatalf("%s empty: %+v; %s", source, result, queries.Rate)
		}
		for _, point := range result.Series[0].DataPoints {
			if math.Abs(point.Value-1) > 0.00001 {
				t.Fatalf("%s included foreign population: %v; %s", source, point.Value, strings.TrimSpace(queries.Rate))
			}
		}
	}
	query, err := prom.BuildIdentityWorkloadResourceQuery(time.Minute, prom.SelectPods(ns, []string{pod.Name}), []prom.WorkloadPodIdentity{pod}, prom.CategoryCPU)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.QueryWorkloadRange(ctx, query, end.Add(-time.Minute), end, time.Minute)
	if err != nil || len(result.Series) != 1 || len(result.Series[0].DataPoints) == 0 {
		t.Fatalf("cAdvisor compact query: %+v %v", result, err)
	}
	for _, point := range result.Series[0].DataPoints {
		if math.Abs(point.Value-1) > 0.00001 {
			t.Fatalf("cAdvisor foreign UID escaped: %v", point.Value)
		}
	}
}
