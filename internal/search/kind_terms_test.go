package search

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestSearch_NewTypedKindsAndKubectlNames(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"resourcequotas":  {&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "memory-limit-quota"}}},
		"serviceaccounts": {&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}}},
		"ingresses":       {&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}},
		"networkpolicies": {&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deny-all"}}},
		"events":          {&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "quota-exceeded"}}},
	}}
	for query, want := range map[string]string{
		"kind:ResourceQuota":  "memory-limit-quota",
		"kind:resourcequotas": "memory-limit-quota",
		"kind:sa":             "checkout",
		"kind:ingresses":      "web",
		"kind:ing":            "web",
		"kind:quota":          "memory-limit-quota",
		"kind:ev":             "quota-exceeded",
		"kind:netpol":         "deny-all",
	} {
		res, err := Search(context.Background(), p, Parse(query), Options{Include: IncludeNone})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Name != want || partial(res) {
			t.Errorf("%s: hits=%+v coverage=%+v, want %s", query, res.Hits, res.Coverage, want)
		}
	}
}

func TestSearch_BuiltinAliasDoesNotMatchCRDWithSameKind(t *testing.T) {
	knative := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	u := &unstructured.Unstructured{}
	u.SetNamespace("shop")
	u.SetName("hello")
	p := &fakeProvider{
		typed:      map[string][]runtime.Object{"services": {&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}}},
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{knative: {u}},
		kinds:      map[schema.GroupVersionResource]string{knative: "Service"},
		namespaced: map[schema.GroupVersionResource]bool{knative: true},
	}
	res, _ := Search(context.Background(), p, Parse("kind:svc"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 || res.Hits[0].Name != "web" || res.Hits[0].Group != "" {
		t.Fatalf("kind:svc must match only core Services: %+v", res.Hits)
	}
}

func TestSearch_IrregularCRDPluralMatchesThroughWholeSearch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	u := &unstructured.Unstructured{}
	u.SetNamespace("shop")
	u.SetName("allow-dns")
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "CiliumNetworkPolicy"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}
	res, _ := Search(context.Background(), p, Parse("kind:ciliumnetworkpolicies"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 || res.Hits[0].Name != "allow-dns" || partial(res) {
		t.Fatalf("irregular plural: hits=%+v coverage=%+v", res.Hits, res.Coverage)
	}
}

func TestTypedKindsScopeComesFromTheBuiltinCatalogue(t *testing.T) {
	clusterScoped := map[string]bool{"Node": true, "Namespace": true, "PersistentVolume": true, "StorageClass": true, "IngressClass": true, "ClusterRole": true, "ClusterRoleBinding": true}
	for _, tk := range typedKinds {
		if got := isClusterScopedKind(tk.Kind); got != clusterScoped[tk.Kind] {
			t.Errorf("isClusterScopedKind(%s) = %v, want %v", tk.Kind, got, clusterScoped[tk.Kind])
		}
	}
}

// A cluster-scoped kind is gated on cluster-scoped read access, and a kind:
// term naming one ignores ns:, as kubectl ignores -n for it.
func TestSearch_IngressClassIsGatedAsClusterScoped(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"ingressclasses": {&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}}},
	}}
	deny := func(kind, group, resource string) (bool, bool) {
		return !(kind == "IngressClass" && group == "networking.k8s.io" && resource == "ingressclasses"), true
	}
	res, _ := Search(context.Background(), p, Parse("kind:IngressClass"), Options{Include: IncludeNone, CanReadClusterScoped: deny})
	if want := []UnsearchedKind{{Kind: "IngressClass", Group: "networking.k8s.io", Reason: readreason.UserForbidden}}; len(res.Hits) != 0 || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("a named IngressClass the caller can't read must be their access gap: %+v", res)
	}
	allow := func(string, string, string) (bool, bool) { return true, true }
	for _, query := range []string{"kind:IngressClass", "kind:IngressClass ns:shop"} {
		res, _ := Search(context.Background(), p, Parse(query), Options{Include: IncludeNone, CanReadClusterScoped: allow})
		if len(res.Hits) != 1 || res.Hits[0].Name != "nginx" || partial(res) {
			t.Fatalf("%s: %+v", query, res)
		}
	}
}

func TestSearch_ReportsKindsItCouldNotAnswerFor(t *testing.T) {
	t.Run("a kind Radar reads directly", func(t *testing.T) {
		// Endpoints, EndpointSlices and Leases are read straight from the API,
		// never watched, so search can't cover them; list_resources can.
		gvr := schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
		p := &fakeProvider{
			kinds:        map[schema.GroupVersionResource]string{gvr: "Lease"},
			namespaced:   map[schema.GroupVersionResource]bool{gvr: true},
			observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnsupported, ReasonCode: "direct_read_only"}},
		}
		res, _ := Search(context.Background(), p, Parse("kind:Lease"), Options{Include: IncludeNone})
		if got := unsearched(res); len(got) != 1 || got[0].Kind != "Lease" || got[0].Reason != readreason.KindNotSearchable {
			t.Fatalf("coverage = %+v, want kind_not_searchable for Lease", got)
		}
		if res, _ := Search(context.Background(), p, Parse("lease-holder"), Options{Include: IncludeNone}); partial(res) || len(res.Coverage.NarrowedBy) != 0 {
			t.Fatalf("a broad search must neither report nor be narrowed by it: %+v", res.Coverage)
		}
	})
	t.Run("RBAC kinds and LimitRanges are searched", func(t *testing.T) {
		for _, query := range []string{"kind:Role", "kind:roles", "kind:LimitRange"} {
			if res, _ := Search(context.Background(), &fakeProvider{}, Parse(query), Options{Include: IncludeNone}); partial(res) {
				t.Fatalf("%s: coverage = %+v", query, res.Coverage)
			}
		}
	})
	t.Run("an unknown kind", func(t *testing.T) {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:Widgett"), Options{Include: IncludeNone})
		if got := unsearched(res); len(got) != 1 || got[0].Reason != readreason.KindUnknown {
			t.Fatalf("coverage = %+v, want kind_unknown", got)
		}
	})
	t.Run("list error", func(t *testing.T) {
		p := &fakeProvider{typedErr: map[string]error{"resourcequotas": errors.New("forbidden")}}
		res, _ := Search(context.Background(), p, Parse("kind:ResourceQuota"), Options{Include: IncludeNone})
		if got := unsearched(res); len(got) != 1 || got[0].Kind != "ResourceQuota" || got[0].Reason != readreason.ReadError {
			t.Fatalf("coverage = %+v, want read_error for ResourceQuota", got)
		}
	})
	t.Run("a named kind the caller may not list", func(t *testing.T) {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:Secret"), Options{Include: IncludeNone, SkipKinds: map[string]bool{"Secret": true}})
		if got := unsearched(res); len(got) != 1 || got[0].Reason != readreason.UserForbidden {
			t.Fatalf("coverage = %+v, want the caller's access gap", got)
		}
	})
	t.Run("answered kinds and unfiltered queries report nothing", func(t *testing.T) {
		p := &fakeProvider{typed: map[string][]runtime.Object{"pods": {newPod("ns", "redis", "redis:7", nil)}}}
		for _, query := range []string{"kind:Pod", "kind:Pod nothing-matches", "redis"} {
			if res, _ := Search(context.Background(), p, Parse(query), Options{Include: IncludeNone}); partial(res) {
				t.Fatalf("%q: coverage = %+v", query, res.Coverage)
			}
		}
	})
}

func TestSearch_WebhookContentIsSearchableWithoutCABundle(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"webhooks": []any{map[string]any{
			"name": "limits.platform.example.com",
			"clientConfig": map[string]any{
				"service":  map[string]any{"name": "limit-injector", "namespace": "platform-ops"},
				"caBundle": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUNvekNDQVlzQ0ZGT3Bz",
			},
		}},
	}}
	u.SetName("limit-injector")
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfiguration"},
		namespaced: map[schema.GroupVersionResource]bool{
			gvr: false,
		},
	}
	res, _ := Search(context.Background(), p, Parse("kind:MutatingWebhookConfiguration platform-ops"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 || len(res.Hits[0].Snippets) == 0 || res.Hits[0].Snippets[0].Path != "webhooks[0].clientConfig.service.namespace" {
		t.Fatalf("webhook service namespace must be searchable content: %+v", res.Hits)
	}
	if res, _ := Search(context.Background(), p, Parse("CRUdJTiBDRVJU"), Options{Include: IncludeNone}); len(res.Hits) != 0 {
		t.Fatalf("a token only inside caBundle must not match: %+v", res.Hits)
	}

	apiService := schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}
	a := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"caBundle": "QkVHSU5DRVJUSUZJQ0FURQ==", "service": map[string]any{"name": "metrics-server"}}}}
	a.SetName("v1beta1.metrics.k8s.io")
	p = &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{apiService: {a}},
		kinds:      map[schema.GroupVersionResource]string{apiService: "APIService"},
		namespaced: map[schema.GroupVersionResource]bool{apiService: false},
	}
	if res, _ := Search(context.Background(), p, Parse("QkVHSU5DRVJU"), Options{Include: IncludeNone}); len(res.Hits) != 0 {
		t.Fatalf("spec.caBundle must not be searchable: %+v", res.Hits)
	}
}
