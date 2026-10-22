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
		if len(res.Hits) != 1 || res.Hits[0].Name != want || len(res.UnsearchedKinds) != 0 {
			t.Errorf("%s: hits=%+v unsearched=%+v, want %s", query, res.Hits, res.UnsearchedKinds, want)
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
	if len(res.Hits) != 1 || res.Hits[0].Name != "allow-dns" || len(res.UnsearchedKinds) != 0 {
		t.Fatalf("irregular plural: hits=%+v unsearched=%+v", res.Hits, res.UnsearchedKinds)
	}
}

func TestTypedKindsScopeComesFromTheBuiltinCatalogue(t *testing.T) {
	clusterScoped := map[string]bool{"Node": true, "Namespace": true, "PersistentVolume": true, "StorageClass": true, "IngressClass": true}
	for _, tk := range typedKinds {
		if got := isClusterScopedKind(tk.Kind); got != clusterScoped[tk.Kind] {
			t.Errorf("isClusterScopedKind(%s) = %v, want %v", tk.Kind, got, clusterScoped[tk.Kind])
		}
	}
}

func TestSearch_IngressClassIsGatedAsClusterScoped(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"ingressclasses": {&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}}},
	}}
	deny := func(kind, group, resource string) bool {
		return !(kind == "IngressClass" && group == "networking.k8s.io" && resource == "ingressclasses")
	}
	if res, _ := Search(context.Background(), p, Parse("kind:IngressClass"), Options{Include: IncludeNone, CanReadClusterScoped: deny}); len(res.Hits) != 0 || len(res.UnsearchedKinds) != 0 {
		t.Fatalf("denied IngressClass must be absent and silent: %+v", res)
	}
	allow := func(string, string, string) bool { return true }
	if res, _ := Search(context.Background(), p, Parse("kind:IngressClass"), Options{Include: IncludeNone, CanReadClusterScoped: allow}); len(res.Hits) != 1 || res.Hits[0].Name != "nginx" {
		t.Fatalf("allowed IngressClass without a namespace filter must match: %+v", res)
	}
	res, _ := Search(context.Background(), p, Parse("kind:IngressClass ns:shop"), Options{Include: IncludeNone, CanReadClusterScoped: allow})
	if len(res.Hits) != 0 || len(res.UnsearchedKinds) != 1 || !reflect.DeepEqual(res.UnsearchedKinds[0], UnsearchedKind{Kind: "IngressClass", Group: "networking.k8s.io", Reason: UnsearchedClusterScoped}) {
		t.Fatalf("namespace-limited IngressClass search must say it is cluster-scoped: %+v", res)
	}
}

func TestSearch_ReportsKindsItCouldNotAnswerFor(t *testing.T) {
	t.Run("kind search does not scan", func(t *testing.T) {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:Role ns:shop"), Options{Include: IncludeNone})
		if len(res.UnsearchedKinds) != 1 || !reflect.DeepEqual(res.UnsearchedKinds[0], UnsearchedKind{Kind: "role", Reason: UnsearchedNotIndexed}) {
			t.Fatalf("unsearched = %+v", res.UnsearchedKinds)
		}
	})
	t.Run("list error", func(t *testing.T) {
		p := &fakeProvider{typedErr: map[string]error{"resourcequotas": errors.New("forbidden")}}
		res, _ := Search(context.Background(), p, Parse("kind:ResourceQuota"), Options{Include: IncludeNone})
		if len(res.UnsearchedKinds) != 1 || !reflect.DeepEqual(res.UnsearchedKinds[0], UnsearchedKind{Kind: "ResourceQuota", Reason: UnsearchedUnreadable}) {
			t.Fatalf("unsearched = %+v", res.UnsearchedKinds)
		}
	})
	t.Run("dynamic kind fails in one namespace", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
		p := &fakeProvider{
			dynamic:              map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: nil},
			kinds:                map[schema.GroupVersionResource]string{gvr: "Certificate"},
			namespaced:           map[schema.GroupVersionResource]bool{gvr: true},
			dynamicErrNamespaces: map[string]bool{"b": true},
		}
		res, _ := Search(context.Background(), p, Parse("kind:Certificate"), Options{Include: IncludeNone, Namespaces: []string{"a", "b"}})
		want := UnsearchedKind{Kind: "Certificate", Group: "cert-manager.io", Reason: UnsearchedUnreadable, Namespaces: []string{"b"}}
		if len(res.UnsearchedKinds) != 1 || !reflect.DeepEqual(res.UnsearchedKinds[0], want) {
			t.Fatalf("unsearched = %+v, want %+v", res.UnsearchedKinds, want)
		}
	})
	t.Run("kinds the caller may not list stay silent", func(t *testing.T) {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:Secret"), Options{Include: IncludeNone, SkipKinds: map[string]bool{"Secret": true}})
		if len(res.UnsearchedKinds) != 0 {
			t.Fatalf("a skipped kind must not be reported: %+v", res.UnsearchedKinds)
		}
	})
	t.Run("answered kinds and unfiltered queries report nothing", func(t *testing.T) {
		p := &fakeProvider{typed: map[string][]runtime.Object{"pods": {newPod("ns", "redis", "redis:7", nil)}}}
		for _, query := range []string{"kind:Pod", "kind:Pod nothing-matches", "redis"} {
			if res, _ := Search(context.Background(), p, Parse(query), Options{Include: IncludeNone}); len(res.UnsearchedKinds) != 0 {
				t.Fatalf("%q: unsearched = %+v", query, res.UnsearchedKinds)
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
