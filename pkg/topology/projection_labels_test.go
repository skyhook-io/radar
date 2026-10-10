package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestProjectionConfigurationDirection(t *testing.T) {
	dynamic := &genericIdentityDynamic{kinds: map[schema.GroupVersionResource]string{}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{}, listCalls: map[schema.GroupVersionResource]int{}}
	specs := []struct {
		group, kind, plural, name string
		spec                      map[string]any
	}{
		{"traefik.io", "IngressRoute", "ingressroutes", "web", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"name": "web", "serversTransport": "transport"}}, "middlewares": []any{map[string]any{"name": "chain"}}}}, "tls": map[string]any{"secretName": "tls", "options": map[string]any{"name": "option"}, "store": map[string]any{"name": "store"}}}},
		{"traefik.io", "Middleware", "middlewares", "chain", map[string]any{"chain": map[string]any{"middlewares": []any{map[string]any{"name": "child"}}}}},
		{"traefik.io", "Middleware", "middlewares", "child", map[string]any{"headers": map[string]any{}}},
		{"traefik.io", "ServersTransport", "serverstransports", "transport", map[string]any{"rootCAsSecrets": []any{"ca"}}},
		{"traefik.io", "TLSOption", "tlsoptions", "option", map[string]any{"clientAuth": map[string]any{"secretNames": []any{"ca"}}}},
		{"traefik.io", "TLSStore", "tlsstores", "store", map[string]any{"defaultCertificate": map[string]any{"secretName": "tls"}}},
		{"cert-manager.io", "Certificate", "certificates", "cert", map[string]any{"secretName": "tls"}},
		{"projectcontour.io", "HTTPProxy", "httpproxies", "proxy", map[string]any{"virtualhost": map[string]any{"fqdn": "example.com", "tls": map[string]any{"secretName": "tls"}}, "routes": []any{map[string]any{"services": []any{map[string]any{"name": "web"}}}}}},
	}
	for _, s := range specs {
		gvr := schema.GroupVersionResource{Group: s.group, Version: "v1alpha1", Resource: s.plural}
		if _, ok := dynamic.kinds[gvr]; !ok {
			dynamic.watched = append(dynamic.watched, gvr)
		}
		dynamic.kinds[gvr] = s.kind
		obj := genericIdentityObject(gvr, s.kind, "demo", s.name)
		obj.Object["spec"] = s.spec
		dynamic.resources[gvr] = append(dynamic.resources[gvr], obj)
	}
	provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo"}}}}
	for _, mode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(mode), func(t *testing.T) {
			opts := DefaultBuildOptions()
			opts.ViewMode = mode
			topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			pairs := [][2]string{{"middleware/demo/chain", "ingressroute/demo/web"}}
			if mode == ViewModeResources {
				pairs = append(pairs, [][2]string{
					{"middleware/demo/child", "middleware/demo/chain"}, {"serverstransport/demo/transport", "ingressroute/demo/web"},
					{"tlsoption/demo/option", "ingressroute/demo/web"}, {"tlsstore/demo/store", "ingressroute/demo/web"},
					{"certificate/demo/cert", "ingressroute/demo/web"}, {"secret/demo/ca", "serverstransport/demo/transport"},
					{"secret/demo/ca", "tlsoption/demo/option"}, {"secret/demo/tls", "tlsstore/demo/store"},
					{"secret/demo/tls", "httpproxy/demo/proxy"}, {"certificate/demo/cert", "httpproxy/demo/proxy"},
				}...)
			}
			for _, pair := range pairs {
				if !hasKarpenterTopologyEdge(topo, pair[0], pair[1], EdgeConfigures) || hasKarpenterTopologyEdge(topo, pair[1], pair[0], EdgeConfigures) {
					t.Errorf("configuration must point %s -> %s", pair[0], pair[1])
				}
			}
		})
	}
}
