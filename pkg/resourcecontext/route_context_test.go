package resourcecontext

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// namespaceReader reads its namespace by namespace access: it gets the kinds
// it is granted there, and nothing in other namespaces.
type namespaceReader struct {
	namespace string
	granted   []string
}

func (a namespaceReader) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == a.namespace && slices.Contains(a.granted, gk.Kind) {
		return ownership.Allowed
	}
	return ownership.Denied
}
func (a namespaceReader) Namespaces() ([]string, bool) { return []string{a.namespace}, false }

func (a namespaceReader) check(_ context.Context, group, kind, namespace string) bool {
	return a.CanRead(resourceid.GroupKind{Group: group, Kind: kind}, namespace) == ownership.Allowed
}

type refChecker func(context.Context, string, string, string) bool

func (f refChecker) CanRead(ctx context.Context, group, kind, namespace string) bool {
	return f(ctx, group, kind, namespace)
}

// A route the caller reads by namespace access, without a get grant on its
// kind, lists the backend Service it names, and its parent Gateway in a
// namespace the caller can't see only narrows what exposes it.
func TestRouteContextKeepsItsBackendWhenItsParentIsHidden(t *testing.T) {
	ctx := context.Background()
	backend := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "parity-web", Namespace: "own-share"}}
	cr := func(apiVersion, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": spec}}
	}
	gatewayAPI := map[string]any{
		"parentRefs": []any{map[string]any{"name": "edge", "namespace": "radar-rel-fixtures"}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "parity-web", "port": int64(80)}}}},
	}
	for _, tc := range []struct {
		route   *unstructured.Unstructured
		gateway *unstructured.Unstructured
	}{
		{cr("gateway.networking.k8s.io/v1", "HTTPRoute", "own-share", "parity-route", gatewayAPI), cr("gateway.networking.k8s.io/v1", "Gateway", "radar-rel-fixtures", "edge", map[string]any{})},
		{cr("gateway.networking.k8s.io/v1", "GRPCRoute", "own-share", "parity-route", gatewayAPI), cr("gateway.networking.k8s.io/v1", "Gateway", "radar-rel-fixtures", "edge", map[string]any{})},
		{cr("networking.istio.io/v1", "VirtualService", "own-share", "parity-route", map[string]any{
			"gateways": []any{"radar-rel-fixtures/edge"},
			"http":     []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "parity-web"}}}}},
		}), cr("networking.istio.io/v1", "Gateway", "radar-rel-fixtures", "edge", map[string]any{})},
	} {
		kind := tc.route.GetKind()
		t.Run(kind, func(t *testing.T) {
			gvk := tc.route.GroupVersionKind()
			subject := resourceid.NewRef(gvk.Group, kind, "own-share", "parity-route")
			session := newContextFixtureSession(subject, tc.route, nil)
			session.objects = []runtime.Object{backend, tc.gateway}
			caller := namespaceReader{namespace: "own-share", granted: []string{"Service"}}
			rel, extras := topology.RelationsContextAnswer(session, topology.ReadSubject(caller, subject), ownership.Unrestricted(), ownership.NewView(session, caller), subject, tc.route, nil, nil)
			rc := Build(ctx, tc.route, Options{Relationships: rel, RelationshipExtras: extras, AccessChecker: refChecker(caller.check)})
			want := []ContextRef{{Kind: "Service", Namespace: "own-share", Name: "parity-web"}}
			if !slices.Equal(rc.Backends, want) || !slices.Equal(rc.Exposes, want) {
				t.Fatalf("backends %+v, exposes %+v; want the readable backend", rc.Backends, rc.Exposes)
			}
			for _, field := range []string{"exposedBy", "exposes"} {
				if !slices.Contains(rc.Omitted, OmittedField{Field: field, Reason: OmittedRBACDenied}) {
					t.Errorf("the hidden Gateway doesn't narrow %s: %+v", field, rc.Omitted)
				}
			}
			if slices.ContainsFunc(rc.Omitted, func(o OmittedField) bool { return o.Field == "backends" }) {
				t.Errorf("a complete backend list was marked: %+v", rc.Omitted)
			}
		})
	}
}
