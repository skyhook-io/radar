package resourcecontext

import (
	"context"
	"errors"
	"fmt"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"reflect"
	"slices"
	"testing"
)

type webhookContextProvider struct {
	topology.DynamicProvider
	objects map[string][]*unstructured.Unstructured
	lists   map[string]int
	fail    bool
	cold    bool
	state   k8score.DynamicObservationState
	err     error
}

func (p *webhookContextProvider) GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool) {
	if group != "admissionregistration.k8s.io" {
		panic("wrong group")
	}
	if _, ok := p.objects[kind]; !ok {
		return schema.GroupVersionResource{}, false
	}
	return schema.GroupVersionResource{Group: group, Version: "v1", Resource: kind}, true
}
func (p *webhookContextProvider) ListWatchedReadOnly(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	p.lists[gvr.Resource]++
	if p.err != nil {
		return nil, p.err
	}
	if p.fail {
		return nil, errors.New("informer not ready")
	}
	return p.objects[gvr.Resource], nil
}

func (p *webhookContextProvider) Observation(schema.GroupVersionResource) k8score.DynamicResourceObservation {
	state := p.state
	if state == "" {
		state = k8score.DynamicObservationSynced
		if p.cold {
			state = k8score.DynamicObservationSyncing
		}
	}
	return k8score.DynamicResourceObservation{State: state}
}

func webhookContextObject(kind, name, ns, service string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind, "metadata": map[string]any{"name": name}, "webhooks": []any{
		map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": ns, "name": service}}},
		map[string]any{"clientConfig": map[string]any{"service": map[string]any{"namespace": ns, "name": service}}},
	}}}
}
func TestWebhookContextForwardDeclaredAndPermissionFiltered(t *testing.T) {
	obj := webhookContextObject("MutatingWebhookConfiguration", "mutation", "backend", "missing-service")
	rc := Build(context.Background(), obj, Options{Tier: TierBasic})
	if len(rc.Dependencies) != 1 || rc.Dependencies[0] != (ContextRef{Kind: "Service", Namespace: "backend", Name: "missing-service"}) {
		t.Fatalf("declared backend without observed target: %+v", rc)
	}
	rc = Build(context.Background(), obj, Options{Tier: TierBasic, AccessChecker: denyChecker{kind: "Service", namespace: "backend"}})
	if len(rc.Dependencies) != 0 || !slices.Contains(rc.Omitted, OmittedField{Field: "dependencies", Reason: OmittedRBACDenied}) {
		t.Fatalf("denied backend: %+v", rc)
	}
}
func TestWebhookContextReverseExactBoundedAndCached(t *testing.T) {
	p := &webhookContextProvider{objects: map[string][]*unstructured.Unstructured{}, lists: map[string]int{}}
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		for i := 0; i < 25; i++ {
			p.objects[kind] = append(p.objects[kind], webhookContextObject(kind, fmt.Sprintf("hook-%02d", i), "backend", "admission"))
		}
		p.objects[kind] = append(p.objects[kind], webhookContextObject(kind, "other-namespace", "other", "admission"), webhookContextObject(kind, "other-service", "backend", "other"))
	}
	custom := webhookContextObject("ValidatingWebhookConfiguration", "custom", "backend", "admission")
	custom.SetAPIVersion("custom.example/v1")
	p.objects["ValidatingWebhookConfiguration"] = append(p.objects["ValidatingWebhookConfiguration"], custom)
	before := p.objects["MutatingWebhookConfiguration"][0].DeepCopy()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	rc := Build(context.Background(), svc, Options{DynamicProv: p})
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[0].Kind == rc.Dependents[1].Kind || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("bounded fair reverse refs: %+v omitted %+v", rc, rc.Omitted)
	}
	if !reflect.DeepEqual(before, p.objects["MutatingWebhookConfiguration"][0]) {
		t.Fatal("reverse scan mutated cached webhook")
	}
	for _, ref := range rc.Dependents {
		if ref.Group != "admissionregistration.k8s.io" || ref.Namespace != "" || ref.Name == "custom" || ref.Name == "other-namespace" || ref.Name == "other-service" {
			t.Fatalf("wrong target: %+v", ref)
		}
	}
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		if p.lists[kind] != 1 {
			t.Fatalf("lists: %+v", p.lists)
		}
	}
	p.lists = map[string]int{}
	rc = Build(context.Background(), svc, Options{DynamicProv: p, AccessChecker: denyChecker{group: "admissionregistration.k8s.io", kind: "MutatingWebhookConfiguration"}})
	if p.lists["MutatingWebhookConfiguration"] != 0 || p.lists["ValidatingWebhookConfiguration"] != 1 || len(rc.Dependents) != maxReferencedByItems || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedRBACDenied}) {
		t.Fatalf("permission gate before lists: %+v %+v %+v", p.lists, rc, rc.Omitted)
	}
	p.fail = true
	rc = Build(context.Background(), svc, Options{DynamicProv: p})
	if len(rc.Dependents) != 0 || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedUnavailable}) {
		t.Fatalf("failed cache lookup: %+v %+v", rc, rc.Omitted)
	}
}

func TestWebhookContextSuccessfulUnsyncedCacheIsPartial(t *testing.T) {
	p := &webhookContextProvider{cold: true, objects: map[string][]*unstructured.Unstructured{"MutatingWebhookConfiguration": {webhookContextObject("MutatingWebhookConfiguration", "partial", "backend", "admission")}, "ValidatingWebhookConfiguration": nil}, lists: map[string]int{}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	rc := Build(context.Background(), svc, Options{DynamicProv: p})
	if len(rc.Dependents) != 1 || !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
		t.Fatalf("unsynced cache claimed complete: %+v %+v", rc, rc.Omitted)
	}
	rc = Build(context.Background(), svc, Options{DynamicProv: struct{ topology.DynamicProvider }{p}})
	if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedUnavailable}) {
		t.Fatalf("unsupported capability claimed complete: %+v", rc.Omitted)
	}
}

func TestWebhookContextDistinguishesUnavailableSources(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	for _, scenario := range []struct {
		name   string
		err    error
		reason OmittedReason
	}{
		{"denied", apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("denied")), OmittedRBACDenied},
		{"unsupported", apierrors.NewMethodNotSupported(gvr.GroupResource(), "watch"), OmittedUnavailable},
		{"read failure", errors.New("informer read failed"), OmittedUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := &webhookContextProvider{err: scenario.err, objects: map[string][]*unstructured.Unstructured{"ValidatingWebhookConfiguration": nil}, lists: map[string]int{}}
			rc := Build(context.Background(), svc, Options{DynamicProv: p})
			if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: scenario.reason}) {
				t.Fatalf("source state was lost: %+v", rc.Omitted)
			}
		})
	}
	p := &webhookContextProvider{objects: map[string][]*unstructured.Unstructured{}, lists: map[string]int{}}
	rc := Build(context.Background(), svc, Options{DynamicProv: p})
	if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedCacheCold}) || len(p.lists) != 0 {
		t.Fatalf("unresolved discovery was treated as complete: %+v lists=%+v", rc.Omitted, p.lists)
	}
}

func TestWebhookContextAbsentProviderMarksIncompleteCoverage(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	for _, relationships := range []*topology.Relationships{
		nil,
		{Dependents: []topology.ResourceRef{{Kind: "Application", Group: "argoproj.io", Namespace: "gitops", Name: "known"}}},
	} {
		rc := Build(context.Background(), svc, Options{Relationships: relationships})
		if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
			t.Fatalf("absent provider claimed complete reverse coverage: %+v", rc)
		}
		if relationships != nil && (len(rc.Dependents) != 1 || rc.Dependents[0].Name != "known") {
			t.Fatalf("absent provider discarded existing evidence: %+v", rc.Dependents)
		}
	}
	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": "foreign", "namespace": "backend"}}}
	rc := Build(context.Background(), foreign, Options{})
	if slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
		t.Fatalf("foreign Service inherited core webhook coverage: %+v", rc.Omitted)
	}
}

func TestWebhookContextObservationStates(t *testing.T) {
	for _, scenario := range []struct {
		state  k8score.DynamicObservationState
		reason OmittedReason
		read   bool
	}{
		{k8score.DynamicObservationUnwatched, OmittedCacheCold, true},
		{k8score.DynamicObservationDeferred, OmittedCacheCold, true},
		{k8score.DynamicObservationSyncing, OmittedCacheCold, true},
		{k8score.DynamicObservationSynced, "", true},
		{k8score.DynamicObservationDenied, OmittedRBACDenied, false},
		{k8score.DynamicObservationUnsupported, OmittedUnavailable, false},
	} {
		t.Run(string(scenario.state), func(t *testing.T) {
			p := &webhookContextProvider{state: scenario.state, objects: map[string][]*unstructured.Unstructured{}, lists: map[string]int{}}
			for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
				p.objects[kind] = []*unstructured.Unstructured{webhookContextObject(kind, "known", "backend", "admission")}
			}
			svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
			rc := Build(context.Background(), svc, Options{DynamicProv: p})
			if scenario.reason == "" {
				if len(rc.Omitted) != 0 {
					t.Fatalf("synced scan incomplete: %+v", rc.Omitted)
				}
			} else if !slices.Contains(rc.Omitted, OmittedField{Field: "dependents", Reason: scenario.reason}) {
				t.Fatalf("state lost: %+v", rc.Omitted)
			}
			if scenario.read {
				if len(rc.Dependents) != 2 || len(p.lists) != 2 {
					t.Fatalf("known matches lost: %+v lists=%+v", rc, p.lists)
				}
			} else if len(rc.Dependents) != 0 || len(p.lists) != 0 {
				t.Fatalf("unreadable source scanned: %+v lists=%+v", rc, p.lists)
			}
		})
	}
}

func TestServiceDependentsShareOnePreviewWithWebhooks(t *testing.T) {
	const kind = "MutatingWebhookConfiguration"
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	p := &webhookContextProvider{objects: map[string][]*unstructured.Unstructured{
		kind:                             {webhookContextObject(kind, "hook", "backend", "admission")},
		"ValidatingWebhookConfiguration": nil,
	}, lists: map[string]int{}, cold: true}
	var refs []topology.ResourceRef
	for i := range 25 {
		refs = append(refs, topology.ResourceRef{Group: "example.io", Kind: "Consumer", Namespace: "backend", Name: fmt.Sprintf("item-%02d", i)})
	}
	checker := &countingRefChecker{}
	rc := Build(t.Context(), svc, Options{Relationships: &topology.Relationships{Dependents: refs}, DynamicProv: p, AccessChecker: checker})
	if len(rc.Dependents) != 20 || !slices.Contains(rc.Dependents, ContextRef{Group: "admissionregistration.k8s.io", Kind: kind, Name: "hook"}) || checker.calls != 4 {
		t.Fatalf("checks=%d, dependents=%v", checker.calls, rc.Dependents)
	}
	for _, reason := range []OmittedReason{OmittedBudgetExceeded, OmittedCacheCold} {
		if !slices.Contains(rc.Omitted, OmittedField{"dependents", reason}) {
			t.Fatalf("omissions %v", rc.Omitted)
		}
	}
}

func TestWebhookContextFailedPermissionCheckSkipsInventory(t *testing.T) {
	const uncheckedKind = "MutatingWebhookConfiguration"
	const readableKind = "ValidatingWebhookConfiguration"
	p := &webhookContextProvider{
		objects: map[string][]*unstructured.Unstructured{
			uncheckedKind: {webhookContextObject(uncheckedKind, "unchecked", "backend", "admission")},
			readableKind:  nil,
		},
		lists: map[string]int{},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	rc := Build(t.Context(), svc, Options{DynamicProv: p, AccessChecker: kindDecider{kind: uncheckedKind, decision: ownership.Undecided}})
	if len(rc.Dependents) != 0 || p.lists[uncheckedKind] != 0 || p.lists[readableKind] != 1 || !slices.Equal(rc.Omitted, []OmittedField{{"dependents", OmittedUnavailable}}) {
		t.Fatalf("failed access check: omissions %v, lists %v", rc.Omitted, p.lists)
	}
}
