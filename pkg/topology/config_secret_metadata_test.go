package topology

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type countingSecretProvider struct {
	*mockProvider
	calls int
	err   error
}

func (p *countingSecretProvider) Secrets() ([]*corev1.Secret, error) {
	p.calls++
	return p.secrets, p.err
}

func TestConfigSecretMetadataWithoutWorkloadSecretNodes(t *testing.T) {
	for _, includeSecrets := range []bool{false, true} {
		for _, tc := range []struct {
			available  bool
			namespaces []string
		}{
			{false, []string{"app"}},
			{true, []string{"app"}},
			{false, []string{"app", "certs"}},
			{true, []string{"app", "certs"}},
		} {
			available, namespaces := tc.available, tc.namespaces
			t.Run(fmt.Sprintf("include=%v/available=%v/namespaces=%v", includeSecrets, available, namespaces), func(t *testing.T) {
				kinds := map[schema.GroupVersionResource]string{}
				resources := map[schema.GroupVersionResource][]*unstructured.Unstructured{}
				var watched []schema.GroupVersionResource
				references := []struct{ consumer, namespace, name string }{
					{"serverstransport/app/transport", "app", "st-ca"},
					{"serverstransport/app/transport", "app", "st-client"},
					{"tlsoption/app/options", "app", "opt-ca"},
					{"tlsstore/app/store", "app", "store-cert"},
					{"tlsstore/app/store", "app", "extra-cert"},
					{"httpproxy/app/proxy", "certs", "observed"},
					{"httpproxy/app/local", "app", "observed"},
				}
				for _, item := range []struct {
					group, plural, kind, name string
					spec                      map[string]any
				}{
					{"traefik.io", "serverstransports", "ServersTransport", "transport", map[string]any{"rootCAsSecrets": []any{"st-ca", "missing"}, "certificatesSecrets": []any{"st-client"}}},
					{"traefik.io", "tlsoptions", "TLSOption", "options", map[string]any{"clientAuth": map[string]any{"secretNames": []any{"opt-ca"}}}},
					{"traefik.io", "tlsstores", "TLSStore", "store", map[string]any{"defaultCertificate": map[string]any{"secretName": "store-cert"}, "certificates": []any{map[string]any{"secretName": "extra-cert"}, map[string]any{"secretName": "store-cert"}}}},
					{"projectcontour.io", "httpproxies", "HTTPProxy", "proxy", map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "certs/observed"}}}},
					{"projectcontour.io", "httpproxies", "HTTPProxy", "local", map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "/observed"}}}},
				} {
					gvr := schema.GroupVersionResource{Group: item.group, Version: "v1alpha1", Resource: item.plural}
					if item.group == "projectcontour.io" {
						gvr.Version = "v1"
					}
					x := genericIdentityObject(gvr, item.kind, "app", item.name)
					x.Object["spec"] = item.spec
					if _, exists := kinds[gvr]; !exists {
						watched = append(watched, gvr)
					}
					kinds[gvr] = item.kind
					resources[gvr] = append(resources[gvr], x)
				}
				provider := &countingSecretProvider{mockProvider: &mockProvider{}}
				if available {
					for _, ref := range references {
						provider.secrets = append(provider.secrets, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ref.namespace, Name: ref.name, ResourceVersion: "42", Labels: map[string]string{"observed": "yes"}}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.key": []byte("PRIVATE_TEST_KEY")}})
					}
				} else {
					provider.err = fmt.Errorf("secret cache unavailable")
				}
				dynamic := &genericIdentityDynamic{watched: watched, kinds: kinds, resources: resources, listCalls: map[schema.GroupVersionResource]int{}}
				opts := DefaultBuildOptions()
				opts.IncludeSecrets = includeSecrets
				opts.Namespaces = namespaces
				topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
				if err != nil {
					t.Fatal(err)
				}
				wantCalls := 1
				if includeSecrets {
					// The existing reflection pass separately reads workload Secrets.
					wantCalls++
				}
				if provider.calls != wantCalls {
					t.Errorf("cached Secret lists = %d, want %d (one shared configuration read)", provider.calls, wantCalls)
				}
				for _, ref := range references {
					id := "secret/" + ref.namespace + "/" + ref.name
					node := nodeByID(topo.Nodes, id)
					if !opts.MatchesNamespaceFilter(ref.namespace) {
						if node != nil {
							t.Errorf("out-of-filter Secret %s was emitted", id)
						}
						for _, edge := range topo.Edges {
							if edge.Target == id || edge.Source == id {
								t.Errorf("out-of-filter Secret edge: %+v", edge)
							}
						}
						continue
					}
					if node == nil {
						t.Fatalf("missing referenced Secret %s", id)
					}
					if available {
						if node.Status != StatusHealthy || node.Data["resourceVersion"] != "42" || node.Data["type"] != "kubernetes.io/tls" || node.Data["labels"].(map[string]string)["observed"] != "yes" {
							t.Errorf("observed Secret replaced with a stub: %+v", node)
						}
					} else if node.Status != StatusUnknown {
						t.Errorf("unreadable Secret must remain unknown: %+v", node)
					}
					found := false
					for _, edge := range topo.Edges {
						if edge.Source == id && edge.Target == ref.consumer {
							found = true
						}
					}
					if !found {
						t.Errorf("missing configuration edge %s -> %s", id, ref.consumer)
					}
				}
				if node := nodeByID(topo.Nodes, "secret/app/missing"); node == nil || node.Status != StatusUnknown {
					t.Errorf("declared-only Secret must remain unknown: %+v", node)
				}
				encoded, err := json.Marshal(topo)
				if err != nil || strings.Contains(string(encoded), "PRIVATE_TEST_KEY") {
					t.Fatalf("Secret value escaped metadata projection: %v", err)
				}
			})
		}
	}

	provider := &countingSecretProvider{mockProvider: &mockProvider{}}
	if _, err := NewBuilder(provider).Build(DefaultBuildOptions()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Errorf("unused Secret cache listed %d times", provider.calls)
	}
}
