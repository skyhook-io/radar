package topology

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestContourDelegatedTLSUsesSecretNamespace(t *testing.T) {
	hpGVR := schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	for _, secretName := range []string{"certs/wildcard", "wildcard"} {
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
							found = found || e.Source == "httpproxy/app/web" && e.Target == target && e.Type == EdgeConfigures
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
