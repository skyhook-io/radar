package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestRightsizingScanThroughSizeLimitedTenantProxy(t *testing.T) {
	const prefix = "/gateway/select/42/prometheus"
	at := time.Unix(1740000000, 0)
	ctx := context.Background()
	var partial atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.RequestURI) > 8192 {
			http.Error(w, "request target too long", http.StatusRequestURITooLong)
			return
		}
		if r.Header.Get("X-Scope-OrgID") != "team-a" || r.Header.Get("Authorization") != "Bearer fixture" {
			http.Error(w, "missing tenant or auth", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		query := r.Form.Get("query")
		if query == "up" {
			fmt.Fprint(w, authProbeBody)
			return
		}
		if query == "" || len(query) > prom.MaxWorkloadQueryBytes {
			t.Errorf("unexpected query size %d", len(query))
		}
		var annotations string
		switch query {
		case "vector(1)":
			if r.Method != http.MethodGet {
				t.Errorf("small query used %s", r.Method)
			}
		default:
			if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.PostForm.Get("query") != query {
				t.Errorf("large query used %s with %d bytes of URL parameters", r.Method, len(r.URL.RawQuery))
			}
			if partial.Load() {
				annotations = `"isPartial":true,`
			}
		}
		switch r.URL.Path {
		case prefix + "/api/v1/query_range":
			if r.Form.Get("start") != "1739395200" || r.Form.Get("end") != "1740000000" || r.Form.Get("step") != "300" {
				t.Error("range changed")
			}
			fmt.Fprintf(w, `{"status":"success",%s"data":{"resultType":"matrix","result":[]}}`, annotations)
		case prefix + "/api/v1/query":
			if r.Form.Get("time") != "1740000000" {
				t.Error("instant query lost its evaluation time")
			}
			fmt.Fprintf(w, `{"status":"success",%s"data":{"resultType":"vector","result":[]}}`, annotations)
		default:
			t.Errorf("wrong backend path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL + "/gateway", basePath: "/select/42/prometheus", manualURL: srv.URL,
		httpClient: srv.Client(), headers: map[string]string{"X-Scope-OrgID": "team-a", "Authorization": "Bearer fixture"},
	}
	scan, err := client.newScanQuerier(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []func(context.Context, string) (*prom.QueryResult, error){
		scan.Query,
		func(ctx context.Context, q string) (*prom.QueryResult, error) {
			return scan.QueryRange(ctx, q, at.Add(-7*24*time.Hour), at, 5*time.Minute)
		},
	} {
		if _, err := run(ctx, "vector(1)"); err != nil {
			t.Fatal(err)
		}
	}

	// This expression fits the GET budget until the configured URL path and
	// tenant base path are included.
	path := "/api/v1/query"
	params := url.Values{"query": {""}, "time": {"1740000000"}}
	query := strings.Repeat("x", scanGETRequestTargetBytes-len(path)-1-len(params.Encode()))
	partial.Store(true)
	if _, err := requireComplete(scan.Query(ctx, query)); !errors.Is(err, prom.ErrPartialResponse) {
		t.Fatalf("partial instant query returned %v", err)
	}

	var batch []scanWorkload
	kinds := []string{"Deployment", "DaemonSet", "StatefulSet"}
	for i := range 46 {
		batch = append(batch, scanTestWorkload(kinds[i%len(kinds)], fmt.Sprintf("team-%02d", i), fmt.Sprintf("payments-service-production-%02d", i), "app"))
	}
	original := buildRightsizingScanQueries(batch, ViewScope{})["throttle"]
	if len(original) <= prom.MaxWorkloadQueryBytes {
		t.Fatal("fixture must split on the store's raw PromQL budget")
	}
	runRange := func(ctx context.Context, query string) (*prom.QueryResult, error) {
		return requireComplete(scan.QueryRange(ctx, query, at.Add(-7*24*time.Hour), at, 5*time.Minute))
	}
	partial.Store(false)
	if _, err := runScanQuery(ctx, batch, ViewScope{}, "throttle", original, runRange); err != nil {
		t.Fatal(err)
	}
	partial.Store(true)
	_, err = runScanQuery(ctx, batch, ViewScope{}, "throttle", original, runRange)
	if !errors.Is(err, prom.ErrPartialResponse) {
		t.Fatalf("split throttle query returned %v", err)
	}
}
