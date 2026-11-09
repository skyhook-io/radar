package search

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestCollectorUnauthorizedIsCredentialsRejected(t *testing.T) {
	for _, err := range []error{apierrors.NewUnauthorized("token expired"), fmt.Errorf("list: %w", apierrors.NewUnauthorized("token expired"))} {
		for _, query := range []string{"robusta", "kind:Pod robusta"} {
			res, searchErr := Search(t.Context(), &fakeProvider{listErrors: map[string]error{"pods": err}}, Parse(query), Options{})
			want := []resourceapi.Gap{{Code: readreason.RadarCredentialsRejected, Kinds: resourceapi.Kinds([]resourceapi.KindRef{{Kind: "Pod"}}, 1), Namespaces: resourceapi.AllNames()}}
			if searchErr != nil || !partial(res) || !reflect.DeepEqual(res.Coverage.Gaps, want) {
				t.Fatalf("%s: %+v %v", query, res.Coverage, searchErr)
			}
		}
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
		for _, query := range []string{"kind:Widget", "kind:Widget ns:a ns:b"} {
			p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a"}}}, warmError: err}
			q := Parse(query)
			opts := Options{Selection: readscope.Selection{Namespaces: []string{"a", "b"}}}
			if len(q.NSFilter) > 0 {
				opts.Selection = SelectNamespaces(p, q, readscope.NamespaceAccess{Checked: true}, nil)
			}
			res, searchErr := Search(t.Context(), p, q, opts)
			want := []resourceapi.Gap{{Code: readreason.RadarCredentialsRejected, Kinds: resourceapi.Kinds([]resourceapi.KindRef{{Kind: "Widget", Group: gvr.Group}}, 1), Namespaces: resourceapi.UnknownNames()}}
			if searchErr != nil || !partial(res) || !reflect.DeepEqual(res.Coverage.Gaps, want) {
				t.Fatalf("%s: %+v %v", query, res.Coverage, searchErr)
			}
		}
	}
}

func TestWarmDeadlineAndCancellationStayPending(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, fmt.Errorf("warm: %w", context.DeadlineExceeded)} {
		if got := listErrorReason(err); got != readreason.KindSyncPending {
			t.Fatalf("%v: %s", err, got)
		}
	}
}
