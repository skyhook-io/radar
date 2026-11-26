package openobserve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]StoreVersion{
		"v0.14.7":    {Raw: "v0.14.7", Major: 0, Minor: 14, Patch: 7},
		"1.1.2":      {Raw: "1.1.2", Major: 1, Minor: 1, Patch: 2},
		"v1.2.0-rc3": {Raw: "v1.2.0-rc3", Major: 1, Minor: 2},
		"v1.2":       {Raw: "v1.2", Major: 1, Minor: 2},
	} {
		got, ok := ParseVersion(in)
		if !ok || got != want {
			t.Errorf("ParseVersion(%q) = %+v, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "v", "latest", "1", "1.x.0", "1.2.3.4"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}

// fakeConfigServer answers OpenObserve's two config endpoints: the
// authenticated per-organization one (v1.1 and later) and the public one
// older versions report their version in.
func fakeConfigServer(t *testing.T, orgVersion, publicVersion string) (*prom.HTTPTransport, *[]string) {
	t.Helper()
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/default/config" && orgVersion != "":
			auth = append(auth, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"version":"` + orgVersion + `","build_type":"opensource"}`))
		case r.URL.Path == "/config" && publicVersion != "":
			_, _ = w.Write([]byte(`{"version":"` + publicVersion + `","commit_hash":"x"}`))
		case r.URL.Path == "/config":
			// v1.1's public bootstrap config has no version.
			_, _ = w.Write([]byte(`{"build_type":"opensource","commit_hash":"x"}`))
		case r.URL.Path == "/api/default/prometheus/api/v1/query", r.URL.Path == "/api/default/prometheus/api/v1/query_range":
			_, _ = w.Write(vectorBody(nil))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	conn := Connection{BaseURL: srv.URL, Org: "default", User: "u@example.com", Token: "tok"}
	tr, err := conn.NewHTTPTransport(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return tr, &auth
}

func TestDetectStoreBehaviour(t *testing.T) {
	cases := []struct {
		name          string
		org, public   string
		want          StoreBehaviour
		wantErr       bool
		wantOrgAuthed bool
	}{
		{name: "v0.14.7 (public config)", public: "v0.14.7", want: StoreBehaviour{Version: "v0.14.7", LegacyFilter: true, LegacyKnown: true, LabelValuesUnscoped: true}},
		{name: "v1.0.0", public: "v1.0.0", want: StoreBehaviour{Version: "v1.0.0", LegacyFilter: true, LegacyKnown: true, LabelValuesUnscoped: true}},
		{name: "v1.1.2 (org config)", org: "v1.1.2", want: StoreBehaviour{Version: "v1.1.2", LegacyFilter: true, LegacyKnown: true}, wantOrgAuthed: true},
		{name: "v1.2.0", org: "v1.2.0", want: StoreBehaviour{Version: "v1.2.0", LegacyKnown: true}, wantOrgAuthed: true},
		// No version anywhere and nothing to probe with: every guard stays on.
		{name: "unknown", want: StoreBehaviour{LegacyFilter: true, LabelValuesUnscoped: true}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, auth := fakeConfigServer(t, tc.org, tc.public)
			got, err := DetectStoreBehaviour(context.Background(), tr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("behaviour = %+v, want %+v", got, tc.want)
			}
			if tc.wantOrgAuthed && (len(*auth) == 0 || (*auth)[0] == "") {
				t.Fatal("the organization config was read without credentials")
			}
			legacy, lerr := LegacyFilterBehaviour(context.Background(), tr)
			if tc.want.Version != "" && (lerr != nil || legacy != tc.want.LegacyFilter) {
				t.Fatalf("LegacyFilterBehaviour = %v, %v", legacy, lerr)
			}
		})
	}
}

// Without a version, the probe decides the filter guard, and label lookups
// stay refused even when the probe says the store filters.
func TestDetectStoreBehaviourProbeFallback(t *testing.T) {
	modern := &routeStore{routes: []route{
		{match: []string{"radar_probe_never_written"}},
		{match: []string{"kube_pod_info"}, rows: []map[string]string{{}}},
	}}
	got, err := DetectStoreBehaviour(context.Background(), modern)
	if !errors.Is(err, ErrVersionUnknown) {
		t.Fatalf("err = %v", err)
	}
	if got != (StoreBehaviour{LegacyKnown: true, LabelValuesUnscoped: true}) {
		t.Fatalf("behaviour = %+v", got)
	}
}

// Older OpenObserve answers the instant probe empty while range queries
// answer; the probe and family detection read the range answer.
func TestProbesFallBackToRangeQueries(t *testing.T) {
	legacy := &routeStore{routes: []route{{rangeOnly: true, match: []string{"kube_pod_info"}, rows: []map[string]string{{}}}}}
	if got, err := LegacyFilterBehaviour(context.Background(), legacy); err != nil || !got {
		t.Fatalf("legacy = %v, %v", got, err)
	}

	ooc := []map[string]string{{"cluster": "oo-b"}}
	store := &routeStore{routes: []route{
		{rangeOnly: true, match: []string{"container_cpu_usage_seconds_total"}, rows: ooc},
		{rangeOnly: true, match: []string{"container_memory_working_set_bytes"}, rows: ooc},
	}}
	missing, err := DetectFamilies(context.Background(), store, clusterOpts)
	if err != nil {
		t.Fatal(err)
	}
	want := []Family{FamilyThrottling, FamilyNetwork, FamilyFilesystem, FamilyRestarts, FamilyVolumes, FamilyNodes, FamilyHPA}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v", missing)
	}
}

func TestRewritingTransportLabelValuesUnscoped(t *testing.T) {
	f := &fakeStore{body: `{"status":"success","data":["a"]}`}
	rt := &RewritingTransport{Inner: f, Options: uidOpts, LabelValuesUnscoped: true}
	for _, path := range []string{"/api/v1/label/pod/values", "/api/v1/labels"} {
		_, err := rt.Do(context.Background(), http.MethodGet, path, url.Values{"match[]": {`kube_pod_info{namespace="demo"}`}})
		if !errors.Is(err, ErrLabelLookupsUnscoped) || !errors.Is(err, prom.ErrQueryRefused) {
			t.Errorf("%s: err = %v", path, err)
		}
	}
	if len(f.queries) != 0 {
		t.Fatalf("label lookup reached the store")
	}
	// Queries and series lookups, whose answers are filtered, still work.
	f.body = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	if _, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"count by (pod) (kube_pod_info)"}}); err != nil {
		t.Fatal(err)
	}
}
