package resourcecontext

import (
	"context"
	"errors"
	"fmt"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"slices"
	"testing"
)

type webhookContextProvider struct {
	topology.DynamicProvider
	objects map[string][]*unstructured.Unstructured
	lists   map[string]int
	fail    bool
	cold    bool
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
func (p *webhookContextProvider) AdmissionWebhookConsumers(gvr schema.GroupVersionResource, namespace, name string) ([]*unstructured.Unstructured, bool, error) {
	p.lists[gvr.Resource]++
	if p.fail {
		return nil, false, errors.New("informer not ready")
	}
	return p.objects[gvr.Resource], !p.cold, nil
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
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	rc := &ResourceContext{}
	omitted := newOmittedTracker()
	addWebhookContext(context.Background(), svc, Options{DynamicProv: p}, rc, omitted)
	if len(rc.Dependents) != maxReferencedByItems || rc.Dependents[0].Kind == rc.Dependents[1].Kind || !slices.Contains(omitted.collect(), OmittedField{Field: "dependents", Reason: OmittedBudgetExceeded}) {
		t.Fatalf("bounded fair reverse refs: %+v omitted %+v", rc, omitted.collect())
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
	rc = &ResourceContext{}
	omitted = newOmittedTracker()
	addWebhookContext(context.Background(), svc, Options{DynamicProv: p, AccessChecker: denyChecker{group: "admissionregistration.k8s.io", kind: "MutatingWebhookConfiguration"}}, rc, omitted)
	if p.lists["MutatingWebhookConfiguration"] != 0 || p.lists["ValidatingWebhookConfiguration"] != 1 || len(rc.Dependents) != maxReferencedByItems || !slices.Contains(omitted.collect(), OmittedField{Field: "dependents", Reason: OmittedRBACDenied}) {
		t.Fatalf("permission gate before lists: %+v %+v %+v", p.lists, rc, omitted.collect())
	}
	p.fail = true
	rc = &ResourceContext{}
	omitted = newOmittedTracker()
	addWebhookContext(context.Background(), svc, Options{DynamicProv: p}, rc, omitted)
	if len(rc.Dependents) != 0 || !slices.Contains(omitted.collect(), OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
		t.Fatalf("cold cache: %+v %+v", rc, omitted.collect())
	}
}

func TestWebhookContextSuccessfulUnsyncedIndexIsPartial(t *testing.T) {
	p := &webhookContextProvider{cold: true, objects: map[string][]*unstructured.Unstructured{"MutatingWebhookConfiguration": {webhookContextObject("MutatingWebhookConfiguration", "partial", "backend", "admission")}, "ValidatingWebhookConfiguration": nil}, lists: map[string]int{}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "backend"}}
	rc := &ResourceContext{}
	omitted := newOmittedTracker()
	addWebhookContext(context.Background(), svc, Options{DynamicProv: p}, rc, omitted)
	if len(rc.Dependents) != 1 || !slices.Contains(omitted.collect(), OmittedField{Field: "dependents", Reason: OmittedCacheCold}) {
		t.Fatalf("unsynced index claimed complete: %+v %+v", rc, omitted.collect())
	}
	rc = &ResourceContext{}
	omitted = newOmittedTracker()
	addWebhookContext(context.Background(), svc, Options{DynamicProv: struct{ topology.DynamicProvider }{p}}, rc, omitted)
	if !slices.Contains(omitted.collect(), OmittedField{Field: "dependents", Reason: OmittedSourceUnavailable}) {
		t.Fatalf("unsupported capability claimed complete: %+v", omitted.collect())
	}
}
