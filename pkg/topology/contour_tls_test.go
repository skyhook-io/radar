package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestHTTPProxyTLSSecretResolvesDelegatedNamespace(t *testing.T) {
	hpGVR := schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}
	proxy := func(name, secretName string) *unstructured.Unstructured {
		obj := genericIdentityObject(hpGVR, "HTTPProxy", "shop", name)
		obj.Object["spec"] = map[string]any{"virtualhost": map[string]any{
			"fqdn": name + ".example.com",
			"tls":  map[string]any{"secretName": secretName},
		}}
		return obj
	}
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert := genericIdentityObject(certGVR, "Certificate", "certs", "wildcard-cert")
	cert.Object["spec"] = map[string]any{"secretName": "wildcard"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{hpGVR, certGVR},
		kinds:   map[schema.GroupVersionResource]string{hpGVR: "HTTPProxy", certGVR: "Certificate"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			hpGVR:   {proxy("own", "own-cert"), proxy("delegated", "certs/wildcard")},
			certGVR: {cert},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	secretNs := map[string]any{}
	for _, node := range topo.Nodes {
		if node.Kind == KindSecret {
			secretNs[node.ID] = node.Data["namespace"]
		}
	}
	want := map[string]string{
		"httpproxy/shop/own":       "secret/shop/own-cert",
		"httpproxy/shop/delegated": "secret/certs/wildcard",
	}
	hasEdge := func(source, target string) bool {
		for _, edge := range topo.Edges {
			if edge.Source == source && edge.Target == target && edge.Type == EdgeConfigures {
				return true
			}
		}
		return false
	}
	// Configuration points at what it configures: Secret → HTTPProxy.
	for proxy, secret := range want {
		if !hasEdge(secret, proxy) {
			t.Errorf("missing %s -> %s; secrets: %v", secret, proxy, secretNs)
		}
	}
	if !hasEdge("certificate/certs/wildcard-cert", "httpproxy/shop/delegated") {
		t.Error("delegated Secret lost its cert-manager Certificate edge")
	}
	if ns := secretNs["secret/certs/wildcard"]; ns != "certs" {
		t.Errorf("delegated Secret node namespace = %v, want certs", ns)
	}
	if _, ok := secretNs["secret/shop/certs/wildcard"]; ok {
		t.Error("delegated reference produced a Secret node in the proxy's namespace")
	}
}

func TestHTTPProxyDelegatedSecretRespectsNamespaceFilter(t *testing.T) {
	hpGVR := schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}
	obj := genericIdentityObject(hpGVR, "HTTPProxy", "shop", "delegated")
	obj.Object["spec"] = map[string]any{"virtualhost": map[string]any{
		"fqdn": "delegated.example.com",
		"tls":  map[string]any{"secretName": "certs/wildcard"},
	}}
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{hpGVR},
		kinds:     map[schema.GroupVersionResource]string{hpGVR: "HTTPProxy"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hpGVR: {obj}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	for _, tt := range []struct {
		namespaces []string
		want       bool
	}{
		{namespaces: []string{"shop"}, want: false},
		{namespaces: []string{"shop", "certs"}, want: true},
	} {
		opts := DefaultBuildOptions()
		opts.Namespaces = tt.namespaces
		topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		node, edge := false, false
		for _, n := range topo.Nodes {
			if n.ID == "secret/certs/wildcard" {
				node = true
			}
		}
		for _, e := range topo.Edges {
			if e.Source == "secret/certs/wildcard" {
				edge = true
			}
		}
		if node != tt.want || edge != tt.want {
			t.Errorf("namespaces %v: delegated Secret node = %v, edge = %v, want %v", tt.namespaces, node, edge, tt.want)
		}
	}
}
