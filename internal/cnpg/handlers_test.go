package cnpg

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

// A ClusterImageCatalog is cluster-scoped and referenceable from any namespace,
// which is why this lookup is an endpoint rather than a client-side list: asking
// the generic resources endpoint without a namespace inherits the caller's
// namespace view filter, and "no cluster uses this catalog" is exactly the
// sentence someone reads before editing it.

// CloudNativePG defaults an omitted `kind` to the namespaced ImageCatalog. A
// namespaced and a cluster-scoped catalog may share a name, so a reference that
// omits the kind must not be counted against the cluster-scoped one.
func TestCatalogRefMatches_DefaultsToTheNamespacedKind(t *testing.T) {
	for _, c := range []struct {
		name     string
		ref      map[string]interface{}
		catalog  string
		wantKind string
		want     bool
	}{
		{"omitted kind counts as ImageCatalog",
			map[string]interface{}{"name": "pg17"}, "pg17", "ImageCatalog", true},
		{"omitted kind is NOT a ClusterImageCatalog",
			map[string]interface{}{"name": "pg17"}, "pg17", "ClusterImageCatalog", false},
		{"explicit cluster-scoped matches its own kind",
			map[string]interface{}{"name": "pg17", "kind": "ClusterImageCatalog"}, "pg17", "ClusterImageCatalog", true},
		{"explicit cluster-scoped does not match the namespaced kind",
			map[string]interface{}{"name": "pg17", "kind": "ClusterImageCatalog"}, "pg17", "ImageCatalog", false},
		{"another catalog entirely",
			map[string]interface{}{"name": "pg16"}, "pg17", "ImageCatalog", false},
		{"no name at all",
			map[string]interface{}{}, "pg17", "ImageCatalog", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := catalogRefMatches(c.ref, c.catalog, c.wantKind); got != c.want {
				t.Errorf("catalogRefMatches(%v, %q, %q) = %v, want %v",
					c.ref, c.catalog, c.wantKind, got, c.want)
			}
		})
	}
}

// The dynamic cache hands the same field back as int64 or float64 depending on
// how the object entered it. Missing the float64 shape would drop a real major
// to zero — which the screen reads as "the reference carries no major", a
// different and wrong statement.
func TestCatalogRefMajor_ReadsEitherNumberShape(t *testing.T) {
	for _, c := range []struct {
		name string
		ref  map[string]interface{}
		want int
	}{
		{"int64 from a typed decode", map[string]interface{}{"major": int64(17)}, 17},
		{"float64 from a JSON decode", map[string]interface{}{"major": float64(17)}, 17},
		{"plain int", map[string]interface{}{"major": 17}, 17},
		{"absent", map[string]interface{}{}, 0},
		{"a string is not a major", map[string]interface{}{"major": "17"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := catalogRefMajor(c.ref); got != c.want {
				t.Errorf("catalogRefMajor(%v) = %d, want %d", c.ref, got, c.want)
			}
		})
	}
}

func TestCNPGCatalogUsersSeparatesAbsentCRDFromFailedRead(t *testing.T) {
	type marker struct{}
	ctx := context.WithValue(context.Background(), marker{}, "caller")
	for _, tc := range []struct {
		name      string
		readError error
		status    int
		message   string
	}{
		{name: "absent CRD", readError: k8s.ErrUnknownDynamicKind},
		{name: "sync pending", readError: integration.ErrDynamicNotSynced, status: http.StatusServiceUnavailable, message: "clusters are still loading"},
		{name: "read failed", readError: errors.New("read unavailable"), status: http.StatusServiceUnavailable, message: "could not read CloudNativePG clusters"},
		{name: "successful empty inventory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := newTestReader(nil)
			calls := 0
			reader.Observations.DynamicList = func(got context.Context, cache *k8s.ResourceCache, kind, group, namespace string) ([]*unstructured.Unstructured, error) {
				calls++
				if got.Value(marker{}) != "caller" || kind != "Cluster" || group != Group || namespace != "db" {
					t.Fatal("catalog read lost caller or scope")
				}
				return nil, tc.readError
			}
			got, err := reader.CatalogUsers(ctx, nil, "db", "pg17", "ImageCatalog")
			if calls != 1 {
				t.Fatal("catalog did not read its bound inventory")
			}
			if tc.status == 0 {
				if err != nil || got == nil || got.Clusters == nil || len(got.Clusters) != 0 {
					t.Fatalf("absence=%+v %v", got, err)
				}
				return
			}
			var failure *ReadFailure
			if got != nil || !errors.As(err, &failure) || failure.Status != tc.status || failure.Message != tc.message {
				t.Fatalf("failure=%+v %v", got, err)
			}
		})
	}
}
