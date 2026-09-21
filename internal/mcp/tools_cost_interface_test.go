package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func TestCostInterfaceThroughMCP(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query().Get("query")
		if r.URL.Path != "/api/v1/query_range" && (strings.Contains(query, "container_cpu_allocation") || strings.Contains(query, "container_memory_allocation_bytes")) {
			value := "2"
			if strings.Contains(query, "container_memory_allocation_bytes") {
				value = "1"
			}
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"namespace":"app"},"value":[1700000000,%q]}]}}`, value)
			return
		}
		if strings.HasPrefix(query, "sum(") && strings.Contains(query, "node_total_hourly_cost") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"10"]}]}}`)
			return
		}
		if r.URL.Path != "/api/v1/query_range" && strings.Contains(r.URL.Query().Get("query"), "node_total_hourly_cost") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"node":"expensive"},"value":[1700000000,"9"]},{"metric":{"node":"cheap"},"value":[1700000000,"1"]}]}}`)
			return
		}
		if r.URL.Path != "/api/v1/query_range" {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`)
			return
		}
		series := make([]any, 0, 9)
		for i := 0; i < 9; i++ {
			namespace := fmt.Sprintf("ns-%d", i)
			if i == 8 {
				namespace = "other"
			}
			series = append(series, map[string]any{"metric": map[string]string{"namespace": namespace}, "values": [][]any{{1700000000, fmt.Sprint(i + 1)}, {1700003600, fmt.Sprint(i + 2)}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": series}})
	}))
	defer source.Close()
	original := opencost.ConfigSnapshot()
	if err := opencost.Configure(opencost.ManagerConfig{Source: opencost.SourcePrometheus}); err != nil {
		t.Fatal(err)
	}
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(source.URL)
	t.Cleanup(func() {
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
		_ = opencost.Configure(original)
	})
	session := connectTestServer(t)
	var summaries []any
	for _, mode := range []string{"omitted", "false", "true"} {
		args := map[string]any{"view": "trend", "range": "6h"}
		if mode != "omitted" {
			args["include_points"] = mode == "true"
		}
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_cost", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		raw := renderContent(result.Content)
		if result.IsError {
			t.Fatal(raw)
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if response["available"] != true || response["seriesCapped"] != true || response["truncated"] == true || response["namespaceCount"] != float64(9) {
			t.Fatalf("incorrect aggregation: %s", raw)
		}
		total := response["trendTotal"].(map[string]any)
		if total["minHourlyCost"] != float64(45) || total["peakHourlyCost"] != float64(54) || total["peakAt"] != "2023-11-14T23:13:20Z" {
			t.Fatalf("missing or incorrect total extrema: %s", raw)
		}
		rows := response["series"].([]any)
		realOther, remainder := false, false
		for _, item := range rows {
			row := item.(map[string]any)
			if row["minHourlyCost"] != row["startHourlyCost"] || row["peakHourlyCost"] != row["endHourlyCost"] || row["peakAt"] != "2023-11-14T23:13:20Z" {
				t.Fatalf("missing or incorrect series extrema: %s", raw)
			}
			_, points := row["dataPoints"]
			if points != (mode == "true") {
				t.Fatalf("mode=%s points=%v: %s", mode, points, raw)
			}
			if points && len(row["dataPoints"].([]any)) != 2 {
				t.Fatal("raw samples lost")
			}
			if row["type"] == "namespace" && row["namespace"] == "other" {
				realOther = true
			}
			if row["type"] == "remainder" {
				remainder = true
				if _, named := row["namespace"]; named {
					t.Fatal("remainder impersonates a namespace")
				}
			}
			delete(row, "dataPoints")
		}
		if !strings.Contains(raw, "Of 9 namespaces in scope, 8 have named series; the remaining 1") {
			t.Fatalf("ambiguous aggregation counts: %s", raw)
		}
		if !realOther || !remainder {
			t.Fatalf("missing namespace/remainder distinction: %s", raw)
		}
		if mode != "true" && !strings.Contains(raw, "include_points=true") {
			t.Fatalf("missing retry hint: %s", raw)
		}
		summaries = append(summaries, map[string]any{"series": rows, "total": response["trendTotal"]})
	}
	if !reflect.DeepEqual(summaries[0], summaries[1]) || !reflect.DeepEqual(summaries[0], summaries[2]) {
		t.Fatalf("include_points changed summaries: %#v", summaries)
	}
	// Defaulted view-specific fields must not leak into summary calls.
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_cost", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("default summary rejected: %v %v", result, err)
	}
	for _, namespace := range []string{"", "app"} {
		args := map[string]any{"view": "summary"}
		if namespace != "" {
			args["namespace"] = namespace
		}
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_cost", Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("summary: %v %v", result, err)
		}
		var wire map[string]any
		raw := renderContent(result.Content)
		if err := json.Unmarshal([]byte(raw), &wire); err != nil {
			t.Fatal(err)
		}
		if wire["available"] != true {
			t.Fatalf("unavailable summary: %s", raw)
		}
		totals := wire["totals"].(map[string]any)
		for _, key := range []string{"unallocatedHourlyCost", "efficiencyPercent"} {
			if _, ok := totals[key]; !ok {
				t.Fatalf("missing explicit %s: %s", key, raw)
			}
		}
		row := wire["namespaces"].([]any)[0].(map[string]any)
		for _, key := range []string{"cpuHourlyCost", "memoryHourlyCost", "efficiencyPercent"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("missing explicit %s: %s", key, raw)
			}
		}
		if namespace != "" && !reflect.DeepEqual(wire["effectiveNamespaces"], []any{namespace}) {
			t.Fatalf("missing effective scope: %s", raw)
		}
		for _, key := range []string{"namespaceScope", "clusterEfficiency", "efficiency", "cpuCost", "memoryCost", "storageCost", "networkCost", "unallocatedCost", "unusedRequestCost"} {
			if strings.Contains(raw, `"`+key+`":`) {
				t.Fatalf("stale field %s: %s", key, raw)
			}
		}

		if totals["allocatedHourlyCost"] != float64(3) || totals["allocatedMonthlyProjection"] != float64(2190) {
			t.Fatalf("wrong allocation: %s", raw)
		}
		if namespace == "" {
			if totals["nodeHourlyCost"] != float64(10) || totals["nodeMonthlyProjection"] != float64(7300) {
				t.Fatalf("node basis replaced allocation: %s", raw)
			}
		} else {
			for _, key := range []string{"nodeHourlyCost", "nodeMonthlyProjection"} {
				value, exists := totals[key]
				if !exists || value != nil {
					t.Fatalf("scoped %s must be explicit null: %s", key, raw)
				}
			}
		}
		for _, key := range []string{"hourlyCost", "hourlyCostBasis", "projectedMonthlyCost"} {
			if _, exists := totals[key]; exists {
				t.Fatalf("ambiguous total %s still emitted", key)
			}
		}
	}

	for _, name := range []string{"cheap", "missing"} {
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_cost", Arguments: map[string]any{"view": "nodes", "name": name, "limit": 1}})
		if err != nil || result.IsError {
			t.Fatalf("node selection: %v %v", result, err)
		}
		var response struct {
			Available bool
			NodeCount int
			Truncated bool
			Reason    string
			Nodes     []nodeCostRow
		}
		raw := renderContent(result.Content)
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if name == "missing" {
			if response.Available || response.Reason != "node_not_found" {
				t.Fatalf("no match: %s", raw)
			}
			continue
		}
		if !response.Available || response.NodeCount != 1 || response.Truncated || len(response.Nodes) != 1 || response.Nodes[0].Name != name {
			t.Fatalf("selection after cap: %s", raw)
		}
		var wire map[string]any
		json.Unmarshal([]byte(raw), &wire)
		totals := wire["totals"].(map[string]any)
		if totals["nodeHourlyCost"] != float64(1) || totals["nodeMonthlyProjection"] != float64(730) || totals["allocatedHourlyCost"] != nil {
			t.Fatalf("selected totals: %s", raw)
		}
	}

}

func TestCostAndRightsizingSchemaRejections(t *testing.T) {
	session := connectTestServer(t)
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"get_cost", map[string]any{"view": "invalid"}, "view"},
		{"get_cost", map[string]any{"view": "trend", "range": "30d"}, "range"},
		{"get_cost", map[string]any{"limit": 0}, "limit"},
		{"get_cost", map[string]any{"limit": -1}, "limit"},
		{"get_cost", map[string]any{"include_points": false}, "include_points"},
		{"get_cost", map[string]any{"include_points": true}, "include_points"},
		{"get_cost", map[string]any{"include_points": nil}, "include_points"},
		{"get_rightsizing", map[string]any{"scope": "cluster", "limit": 101}, "limit"},
		{"get_rightsizing", map[string]any{"scope": "cluster", "classification": "balanced"}, "classification"},
		{"get_rightsizing", map[string]any{"scope": "workload", "classification": "increase"}, "classification"},
		{"get_rightsizing", map[string]any{"scope": "namespace", "namespaces": []string{}}, "namespaces"},
		{"get_rightsizing", map[string]any{"scope": "namespace", "namespaces": nil}, "namespaces"},
		{"get_rightsizing", map[string]any{"scope": "namespace", "namespaces": []string{""}}, "namespaces"},
	} {
		t.Run(tc.name+fmt.Sprint(tc.args), func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(renderContent(result.Content), tc.field) {
				t.Fatalf("expected actionable %s rejection: %+v", tc.field, result)
			}
		})
	}
}
