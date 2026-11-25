package openobserve

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// OpenObserve drops a matcher whose value is its dashboard placeholder
// (_o2_all_ by default, configurable), so such a value must never scope a
// query.
func TestRewriteRefusesPlaceholderValues(t *testing.T) {
	for _, opts := range []RewriteOptions{
		{Matchers: []Matcher{{Name: "cluster", Value: "_o2_all_"}}},
		{Matchers: []Matcher{{Name: "cluster", Value: "_O2_ALL_"}}},
		{Matchers: []Matcher{{Name: "cluster", Value: "_o2_custom"}}},
		{Matchers: []Matcher{{Name: IDLabel, Value: "x"}, {Name: "cluster", Value: "_o2_all_"}}},
		{Matchers: []Matcher{{Name: "k8s_cluster", Value: "_o2_all_"}}, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true},
	} {
		if got, err := Rewrite(`up`, opts); !errors.Is(err, ErrReservedName) {
			t.Errorf("Rewrite with %+v = %q, %v; want ErrReservedName", opts.Matchers, got, err)
		}
		if _, err := RewriteSeriesSelector(`up`, opts); !errors.Is(err, ErrReservedName) {
			t.Errorf("RewriteSeriesSelector with %+v: %v", opts.Matchers, err)
		}
	}
	if _, err := Rewrite(`up`, RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "prod_o2_all_"}}}); err != nil {
		t.Fatalf("a name only containing the placeholder was refused: %v", err)
	}
}

// A collector sending the placeholder as its cluster name is matched by its
// UID alone, and nothing that carries only the name is read.
func TestDiscoverIDsCollectorPlaceholderName(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"k8s_pod_cpu_time{k8s_cluster_uid=~"}, rows: []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "_o2_all_"}}},
		{match: []string{"k8s_pod_cpu_time[7d]"}, rows: []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "_o2_all_"}}},
	}}
	got, err := DiscoverIDs(context.Background(), store, []string{uidA})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got[uidA]
	if !ok {
		t.Fatal("A not matched by its UID")
	}
	for _, m := range a.Options.Matchers {
		if IsReservedValue(m.Value) {
			t.Fatalf("placeholder used as a matcher: %+v", a.Options)
		}
	}
	if a.Options.GatewayMetricsAllowed || a.Name != "" || !strings.Contains(a.Warning, "dashboard placeholder") {
		t.Fatalf("A = %+v", a)
	}
	q, err := Rewrite(`sum(rate(container_cpu_usage_seconds_total[5m]))`, a.Options)
	if err != nil || !strings.Contains(q, `k8s_cluster_uid="`+uidA+`"`) {
		t.Fatalf("A's CPU query = %q, %v", q, err)
	}
	if _, err := Rewrite(`kube_pod_container_status_restarts_total`, a.Options); !errors.Is(err, ErrNotCollected) {
		t.Fatalf("name-only metric err = %v", err)
	}
}

func TestDiscoverIDsSkipsPlaceholderDisplayName(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"kube_pod_info{radar_cluster_uid=~"}, rows: []map[string]string{{"radar_cluster_uid": uidB, "cluster": "_o2_all_", "cluster_name": "oo-b"}}},
	}}
	got, err := DiscoverIDs(context.Background(), store, []string{uidB})
	if err != nil {
		t.Fatal(err)
	}
	if got[uidB].Name != "oo-b" {
		t.Fatalf("B = %+v", got[uidB])
	}
}

func TestSuggestNameNeverSuggestsPlaceholder(t *testing.T) {
	only := &routeStore{routes: []route{
		{match: []string{`uid=~`}, rows: []map[string]string{{"uid": cDB, "cluster": "_o2_all_"}}},
	}}
	got, err := SuggestName(context.Background(), only, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "" || !strings.Contains(got.Reason, "dashboard placeholder") {
		t.Fatalf("suggestion = %+v", got)
	}

	other := &routeStore{routes: []route{
		{match: []string{`uid=~`}, rows: []map[string]string{{"uid": cDB, "cluster": "_o2_all_", "cluster_name": "shop"}}},
	}}
	got, err = SuggestName(context.Background(), other, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "cluster_name" || got.Value != "shop" {
		t.Fatalf("suggestion = %+v", got)
	}
}

func TestConfirmedNameMatchRefusesPlaceholder(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"kube_pod_info"}, rows: []map[string]string{{"cluster": "_o2_all_"}}},
	}}
	for _, label := range []string{"cluster", "k8s_cluster"} {
		if _, err := ConfirmedNameMatch(context.Background(), store, label, "_o2_all_", uidC); !errors.Is(err, ErrReservedName) {
			t.Fatalf("%s err = %v", label, err)
		}
	}
	if len(store.sent()) != 0 {
		t.Fatalf("queried OpenObserve for a placeholder name: %v", store.sent())
	}
}

func TestListClusterNamesSkipsPlaceholder(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"kube_pod_info"}, rows: []map[string]string{{"cluster": "_o2_all_"}, {"cluster": "prod"}}},
		{match: []string{"k8s_pod_cpu_time"}, rows: []map[string]string{{"k8s_cluster": "_o2_all_"}}},
	}}
	got, err := ListClusterNames(context.Background(), store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "prod" {
		t.Fatalf("names = %+v", got)
	}
}
