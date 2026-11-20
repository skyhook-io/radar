package topology

import (
	"encoding/json"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
	"testing"
)

func TestContourDelegatedTLSUsesSecretNamespace(t *testing.T) {
	hpGVR := schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	for _, secretName := range []string{"certs/wildcard", "wildcard", "/wildcard"} {
		t.Run(secretName, func(t *testing.T) {
			ns := "app"
			if secretName == "certs/wildcard" {
				ns = "certs"
			}
			for _, present := range []bool{true, false} {
				t.Run(map[bool]string{true: "present", false: "absent"}[present], func(t *testing.T) {
					hp := genericIdentityObject(hpGVR, "HTTPProxy", "app", "web")
					hp.Object["spec"] = map[string]any{"virtualhost": map[string]any{"fqdn": "example.com", "tls": map[string]any{"secretName": secretName}}}
					cert := genericIdentityObject(certGVR, "Certificate", ns, "certificate")
					cert.Object["spec"] = map[string]any{"secretName": "wildcard"}
					d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{hpGVR, certGVR}, kinds: map[schema.GroupVersionResource]string{hpGVR: "HTTPProxy", certGVR: "Certificate"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hpGVR: {hp}, certGVR: {cert}}, listCalls: map[schema.GroupVersionResource]int{}}
					provider := &mockProvider{}
					if present {
						provider.secrets = []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "wildcard", Labels: map[string]string{"real": "true"}}}}
					}
					opts := DefaultBuildOptions()
					opts.IncludeSecrets = true
					topo, err := NewBuilder(provider).WithDynamic(d).Build(opts)
					if err != nil {
						t.Fatal(err)
					}
					for _, target := range []string{"secret/" + ns + "/wildcard", "certificate/" + ns + "/certificate"} {
						found := false
						for _, e := range topo.Edges {
							found = found || e.Source == target && e.Target == "httpproxy/app/web" && e.Type == EdgeConfigures
						}
						if !found {
							t.Errorf("missing TLS edge to %s", target)
						}
					}
					if nodeByID(topo.Nodes, "secret/app/certs/wildcard") != nil {
						t.Error("created phantom slash-named Secret")
					}
					n := nodeByID(topo.Nodes, "secret/"+ns+"/wildcard")
					if n == nil || n.Name != "wildcard" || n.Data["namespace"] != ns {
						t.Fatalf("wrong Secret identity: %+v", n)
					}
					if present && n.Data["labels"].(map[string]string)["real"] != "true" {
						t.Error("replaced real Secret with stub")
					}
				})
			}
		})
	}
}

func TestContourMalformedTLSNameKeepsTheProxyRoutes(t *testing.T) {
	hpGVR := schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}
	hp := genericIdentityObject(hpGVR, "HTTPProxy", "app", "tcp")
	hp.Object["spec"] = map[string]any{
		"virtualhost": map[string]any{"fqdn": "db.example.com", "tls": map[string]any{"secretName": "a/b/c"}},
		"tcpproxy":    map[string]any{"services": []any{map[string]any{"name": "db", "port": int64(5432)}}},
	}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{hpGVR}, kinds: map[schema.GroupVersionResource]string{hpGVR: "HTTPProxy"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hpGVR: {hp}}, listCalls: map[schema.GroupVersionResource]int{}}
	provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}}}}
	topo, err := NewBuilder(provider).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range topo.Edges {
		found = found || e.Source == "httpproxy/app/tcp" && e.Target == "service/app/db"
	}
	if !found {
		t.Error("an unusable TLS secret name dropped the proxy's tcpproxy backend")
	}
}

func TestRelationshipGraphRetainsUnusedCertificateSecrets(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert := genericIdentityObject(gvr, "Certificate", "shop", "tls")
	cert.Object["spec"] = map[string]any{"secretName": "tls"}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Certificate"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cert}}, listCalls: map[schema.GroupVersionResource]int{}}
	provider := &mockProvider{secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls"}, Data: map[string][]byte{"tls.key": []byte("must-not-appear")}}}}
	for i, secretType := range []corev1.SecretType{corev1.SecretType("helm.sh/release.v1"), corev1.SecretTypeServiceAccountToken, corev1.SecretTypeOpaque} {
		provider.secrets = append(provider.secrets, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("unrelated-%d", i)}, Type: secretType})
	}
	for _, full := range []bool{false, true} {
		for _, ns := range []string{"shop", "other"} {
			opts := DefaultBuildOptions()
			opts.IncludeSecrets, opts.ForRelationshipCache = true, full
			opts.Namespaces = []string{ns}
			graph, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range graph.Nodes {
				if node.Kind == KindSecret && node.Name != "tls" {
					t.Fatalf("unrelated Secret entered the graph: %s", node.Name)
				}
			}
			found := false
			for _, edge := range graph.Edges {
				found = found || edge.Source == "certificate/shop/tls" && edge.Target == "secret/shop/tls" && edge.Type == EdgeManages
			}
			if found != (full && ns == "shop") {
				t.Fatalf("relationship=%v namespace=%s edge=%v", full, ns, found)
			}
			wire, _ := json.Marshal(graph)
			if strings.Contains(string(wire), "must-not-appear") || strings.Contains(string(wire), "tls.key") {
				t.Fatal("Secret payload leaked into graph")
			}
		}
	}
}

func BenchmarkRelationshipGraphUnrelatedSecrets(b *testing.B) {
	for _, count := range []int{0, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
			cert := genericIdentityObject(gvr, "Certificate", "shop", "tls")
			cert.Object["spec"] = map[string]any{"secretName": "tls"}
			dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Certificate"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cert}}, listCalls: map[schema.GroupVersionResource]int{}}
			provider := &mockProvider{secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls"}}}}
			for i := range count {
				provider.secrets = append(provider.secrets, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("release-%d", i)}, Type: corev1.SecretType("helm.sh/release.v1")})
			}
			opts := RelationshipBuildOptions()
			opts.IncludeSecrets = true
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				graph, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
				if err != nil {
					b.Fatal(err)
				}
				secrets := 0
				for _, node := range graph.Nodes {
					if node.Kind == KindSecret {
						secrets++
					}
				}
				if secrets != 1 {
					b.Fatalf("retained %d Secrets, want only the targeted one", secrets)
				}
			}
		})
	}
}
