package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/mcp"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// REST and MCP search share one engine but each handler builds its own
// options, so the same query must report the same coverage, spelled the same,
// on both surfaces.
func TestSearchCoverageMatchesAcrossRESTAndMCP(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	reports := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "reports"}
	probes := schema.GroupVersionResource{Group: "retry.example.io", Version: "v1", Resource: "probewidgets"}
	var probeAnswered atomic.Bool
	listKinds := map[schema.GroupVersionResource]string{widgets: "WidgetList", reports: "ReportList", probes: "ProbeWidgetList"}
	resources := []k8s.APIResource{
		{Group: widgets.Group, Version: widgets.Version, Name: widgets.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: probes.Group, Version: probes.Version, Name: probes.Resource, Kind: "ProbeWidget", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: reports.Group, Version: reports.Version, Name: reports.Resource, Kind: "Report", Namespaced: true, Verbs: []string{"get", "list"}},
	}
	// 25 cluster-scoped kinds Radar's own identity may not list: more than a
	// broad search would ever name.
	var gadgets []schema.GroupVersionResource
	var gadgetTerms []string
	for i := range 25 {
		gvr := schema.GroupVersionResource{Group: "gadgets.example.io", Version: "v1", Resource: fmt.Sprintf("gadget%02ds", i)}
		gadgets = append(gadgets, gvr)
		gadgetTerms = append(gadgetTerms, fmt.Sprintf("kind:Gadget%02d", i))
		listKinds[gvr] = fmt.Sprintf("Gadget%02dList", i)
		resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: fmt.Sprintf("Gadget%02d", i), Verbs: []string{"get", "list", "watch"}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		resource := action.GetResource()
		if resource == probes {
			if action.GetNamespace() == "" {
				return true, nil, apierrors.NewForbidden(resource.GroupResource(), "", errors.New("collector scope"))
			}
			if !probeAnswered.Load() {
				return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
			}
			return false, nil, nil
		}
		if resource.Group != "gadgets.example.io" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(resource.GroupResource(), "", errors.New("collector cannot list"))
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	dc.Stop()
	core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: k8s.GetResourceDiscovery().ResourceDiscovery, NamespaceFallbacks: []string{"a"}, ProbeRetryBackoff: []time.Duration{2 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	dc.DynamicResourceCache = core
	for _, gvr := range gadgets {
		if err := dc.EnsureWatching(gvr); err == nil {
			t.Fatalf("%s: collector list should be denied", gvr.Resource)
		}
	}
	// Until CRD discovery completes every search reports the catalog as
	// loading; finish it, and the widget watch it starts, first.
	discovered := make(chan struct{})
	dc.OnCRDDiscoveryComplete(func() { close(discovered) })
	dc.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(10 * time.Second):
		t.Fatal("CRD discovery did not complete")
	}
	if err := dc.EnsureWatching(widgets); err != nil || !dc.WaitForSync(widgets, 5*time.Second) {
		t.Fatalf("widgets did not sync: %v", err)
	}
	srv := server.New(server.Config{DevMode: true, MCPHandler: mcp.NewHandler()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "search-parity", Version: "test"}, nil).Connect(context.Background(), &mcpsdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var deniedGadgets []unsearchedEntry
	for i := range 25 {
		deniedGadgets = append(deniedGadgets, unsearchedEntry{Kind: fmt.Sprintf("Gadget%02d", i), Group: "gadgets.example.io", Reason: "radar_forbidden"})
	}
	for _, tc := range []struct {
		query  string
		want   []unsearchedEntry
		before func()
	}{
		// Unwatched kinds, list-only APIs and kinds Radar's own identity may
		// not list are the search's scope: a broad query names none of them.
		{"robusta", []unsearchedEntry{}, nil},
		{"kind:Widget robusta", []unsearchedEntry{}, nil},
		{"kind:NoSuchKindXyz robusta", []unsearchedEntry{{Kind: "NoSuchKindXyz", Reason: "kind_unknown"}}, nil},
		{"kind:Report robusta", []unsearchedEntry{{Kind: "Report", Group: "example.io", Reason: "kind_not_watchable"}}, nil},
		{strings.Join(gadgetTerms, " ") + " robusta", deniedGadgets, nil},
		{"robusta", []unsearchedEntry{{Kind: "*", Reason: "kind_sync_pending", Count: 1}}, func() {
			if err := dc.EnsureWatching(probes); err == nil {
				t.Fatal("unanswered walk unexpectedly granted")
			}
		}},
		{"kind:ProbeWidget robusta", []unsearchedEntry{{Kind: "ProbeWidget", Group: probes.Group, Reason: "kind_sync_pending"}}, nil},
		{"robusta", []unsearchedEntry{{Kind: "ProbeWidget", Group: probes.Group, Reason: "read_error"}}, func() {
			deadline := time.Now().Add(4 * time.Second)
			for dc.Observation(probes).ProbeRetrying && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if dc.Observation(probes).ProbeRetrying {
				t.Fatal("retry did not exhaust")
			}
		}},
		{"kind:ProbeWidget robusta", []unsearchedEntry{{Kind: "ProbeWidget", Group: probes.Group, Reason: "read_error"}}, nil},
		{"robusta", []unsearchedEntry{}, func() {
			probeAnswered.Store(true)
			if err := dc.EnsureWatchingContext(t.Context(), probes, "a"); err != nil {
				t.Fatal(err)
			}
			if !dc.WaitForSync(probes, time.Second) {
				t.Fatal("answered namespace did not sync")
			}
		}},
	} {
		if tc.before != nil {
			tc.before()
		}
		resp, err := http.Get(ts.URL + "/api/search?context=none&include=none&q=" + url.QueryEscape(tc.query))
		if err != nil {
			t.Fatal(err)
		}
		var rest map[string]json.RawMessage
		err = json.NewDecoder(resp.Body).Decode(&rest)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: REST search: status %d: %v", tc.query, resp.StatusCode, err)
		}
		if _, ok := rest["coverageHint"]; ok {
			t.Fatalf("%s: coverageHint leaked into REST", tc.query)
		}
		var restResult search.Result
		if err := json.Unmarshal(mustJSON(t, rest), &restResult); err != nil {
			t.Fatal(err)
		}

		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "search", Arguments: map[string]any{"query": tc.query, "include": "none", "context": "none"}})
		if err != nil || result.IsError || len(result.Content) == 0 {
			t.Fatalf("%s: MCP search failed: %v %+v", tc.query, err, result)
		}
		text, ok := result.Content[0].(*mcpsdk.TextContent)
		if !ok {
			t.Fatalf("%s: MCP search returned %T", tc.query, result.Content[0])
		}
		var viaMCP struct {
			search.Result
			CoverageHint string `json:"coverageHint"`
		}
		if err := json.Unmarshal([]byte(text.Text), &viaMCP); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(unsearched(restResult), tc.want) || partial(restResult) != (len(tc.want) > 0) {
			t.Fatalf("%s: REST coverage %+v (partial=%v), want %+v", tc.query, unsearched(restResult), partial(restResult), tc.want)
		}
		if !reflect.DeepEqual(unsearched(restResult), unsearched(viaMCP.Result)) || partial(restResult) != partial(viaMCP.Result) {
			t.Fatalf("%s: REST coverage %+v differs from MCP %+v", tc.query, unsearched(restResult), unsearched(viaMCP.Result))
		}
		// A partial result always explains itself; so does a narrowed broad
		// search that found nothing, which is no proof of absence.
		explains := partial(viaMCP.Result) || (len(viaMCP.Coverage.NarrowedBy) > 0 && len(viaMCP.Hits) == 0)
		if (viaMCP.CoverageHint != "") != explains {
			t.Fatalf("%s: MCP partial=%v with coverageHint %q", tc.query, partial(viaMCP.Result), viaMCP.CoverageHint)
		}
		for _, gap := range tc.want {
			if gap.Reason == "kind_sync_pending" && !strings.Contains(viaMCP.CoverageHint, "retry in a few seconds") {
				t.Fatalf("pending hint: %q", viaMCP.CoverageHint)
			}
			if gap.Reason == "read_error" {
				want := "Couldn't read ProbeWidget (retry.example.io); matches of that kind may be missing."
				if len(search.Parse(tc.query).KindFilter) == 0 {
					want += " Retry with kind:ProbeWidget before concluding absence."
				}
				if viaMCP.CoverageHint != want {
					t.Fatalf("read-error hint: %q, want %q", viaMCP.CoverageHint, want)
				}
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
