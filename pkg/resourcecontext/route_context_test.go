package resourcecontext

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
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

// The subject the read gate admitted counts as readable for its own
// relationship searches, which so admit other objects of its kind there; the
// context still checks each reference it emits, so a sibling the caller can't
// get never reaches it and its field says something was withheld.
func TestReadSubjectSiblingIsFilteredFromTheContext(t *testing.T) {
	ctx := context.Background()
	front := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1", "kind": "TraefikService",
		"metadata": map[string]any{"name": "front", "namespace": "shop"},
		"spec": map[string]any{"weighted": map[string]any{"services": []any{
			map[string]any{"name": "canary", "kind": "TraefikService"},
			map[string]any{"name": "web", "kind": "Service", "port": int64(80)},
		}}},
	}}
	canary := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "TraefikService", "metadata": map[string]any{"name": "canary", "namespace": "shop"}, "spec": map[string]any{}}}
	web := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}
	subject := resourceid.NewRef("traefik.io", "TraefikService", "shop", "front")
	session := newContextFixtureSession(subject, front, nil)
	session.objects = []runtime.Object{canary, web}
	caller := namespaceReader{namespace: "shop", granted: []string{"Service"}}
	rel, extras := topology.RelationsContextAnswer(session, topology.ReadSubject(caller, subject), ownership.Unrestricted(), ownership.NewView(session, caller), subject, front, nil, nil)
	if !slices.ContainsFunc(rel.Backends, func(r topology.ResourceRef) bool { return r.Name == "canary" }) {
		t.Fatalf("the fixture no longer admits the sibling, so it guards nothing: %+v", rel.Backends)
	}
	rc := Build(ctx, front, Options{Relationships: rel, RelationshipExtras: extras, AccessChecker: refChecker(caller.check)})
	raw, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "canary") {
		t.Fatalf("a sibling the caller can't get reached the context: %s", raw)
	}
	if want := []ContextRef{{Kind: "Service", Namespace: "shop", Name: "web"}}; !slices.Equal(rc.Backends, want) {
		t.Fatalf("backends = %+v, want %+v", rc.Backends, want)
	}
	for _, field := range []string{"backends", "exposes"} {
		if !slices.Contains(rc.Omitted, OmittedField{Field: field, Reason: OmittedRBACDenied}) {
			t.Errorf("%s doesn't say the sibling was withheld: %+v", field, rc.Omitted)
		}
	}
}

// undecidedReader reads its namespace by namespace access, can't check the
// kinds it names, and gets the rest.
type undecidedReader struct {
	namespace string
	undecided []string
}

func (a undecidedReader) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	switch {
	case namespace != a.namespace:
		return ownership.Denied
	case slices.Contains(a.undecided, gk.Kind):
		return ownership.Undecided
	}
	return ownership.Allowed
}
func (a undecidedReader) Namespaces() ([]string, bool) { return []string{a.namespace}, false }

// ReadSubject answers only for the subject's own kind: a check on another
// kind that couldn't be made still leaves a declared name unverified and a
// searched reference unread, not allowed.
func TestReadSubjectKeepsAFailedCheckOnAnotherKind(t *testing.T) {
	ctx := context.Background()
	caller := undecidedReader{namespace: "shop", undecided: []string{"Secret", "Service", "Deployment"}}
	dep := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}}}}}
	subject := resourceid.NewRef("apps", "Deployment", "shop", "web")
	if got := topology.ReadSubject(caller, subject).CanRead(resourceid.GroupKind{Kind: "Secret"}, "shop"); got != ownership.Undecided {
		t.Fatalf("ReadSubject decided Secrets %v", got)
	}
	session := newContextFixtureSession(subject, dep, nil)
	rel, extras := topology.RelationsContextAnswer(session, topology.ReadSubject(caller, subject), ownership.Unrestricted(), ownership.NewView(session, caller), subject, dep, nil, nil)
	rc := Build(ctx, dep, Options{Relationships: rel, RelationshipExtras: extras})
	if rc.Uses == nil || !slices.Equal(rc.Uses.Secrets, []ContextRef{{Kind: "Secret", Namespace: "shop", Name: "registry", State: topology.ResourceStateUnverified}}) {
		t.Fatalf("uses = %+v", rc.Uses)
	}

	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "parity-route", "namespace": "shop"},
		"spec":     map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}}}},
	}}
	web := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}
	routeRef := resourceid.NewRef("gateway.networking.k8s.io", "HTTPRoute", "shop", "parity-route")
	session = newContextFixtureSession(routeRef, route, nil)
	session.objects = []runtime.Object{web}
	routeCaller := undecidedReader{namespace: "shop", undecided: []string{"Service", "HTTPRoute"}}
	rel, extras = topology.RelationsContextAnswer(session, topology.ReadSubject(routeCaller, routeRef), ownership.Unrestricted(), ownership.NewView(session, routeCaller), routeRef, route, nil, nil)
	if len(rel.Backends) != 0 || !slices.ContainsFunc(extras.Fields["Backends"].NotChecked, func(g ownership.Gap) bool { return g.Reason == readreason.UserPermissionCheckFailed }) {
		t.Fatalf("an unchecked backend was admitted: backends %+v, fields %+v", rel.Backends, extras.Fields)
	}
	rc = Build(ctx, route, Options{Relationships: rel, RelationshipExtras: extras})
	if len(rc.Backends) != 0 || !slices.Contains(rc.Omitted, OmittedField{Field: "backends", Reason: OmittedUnavailable}) {
		t.Fatalf("backends %+v, omitted %+v", rc.Backends, rc.Omitted)
	}
}
