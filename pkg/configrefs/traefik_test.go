package configrefs

import (
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTraefikTransportRefs(t *testing.T) {
	transport := map[string]any{"spec": map[string]any{
		"rootCAs": []any{
			map[string]any{"configMap": "ca-bundle"},
			map[string]any{"secret": "ca-secret"},
			map[string]any{"secret": "both-set", "configMap": "both-set"},
		},
		"rootCAsSecrets":      []any{"legacy-ca", "ca-secret"},
		"certificatesSecrets": []any{"client-cert"},
		"tls":                 map[string]any{"certificatesSecrets": []any{"tcp-only"}},
	}}
	secrets, configMaps := TraefikTransportRefs(transport, false)
	if fmt.Sprint(secrets) != "[ca-secret legacy-ca client-cert]" || fmt.Sprint(configMaps) != "[ca-bundle]" {
		t.Errorf("HTTP transport = %v %v", secrets, configMaps)
	}
	secrets, configMaps = TraefikTransportRefs(transport, true)
	if fmt.Sprint(secrets) != "[tcp-only]" || len(configMaps) != 0 {
		t.Errorf("TCP transport = %v %v, want only spec.tls fields", secrets, configMaps)
	}
}

func TestTraefikTLSStoreSecrets(t *testing.T) {
	store := map[string]any{"spec": map[string]any{
		"defaultCertificate": map[string]any{"secretName": "default-cert"},
		"certificates":       []any{map[string]any{"secretName": "a"}, map[string]any{"secretName": "default-cert"}},
	}}
	if got := TraefikTLSStoreSecrets(store); fmt.Sprint(got) != "[default-cert a]" {
		t.Errorf("TraefikTLSStoreSecrets = %v", got)
	}
}

func TestTraefikBackendReferenceDefaults(t *testing.T) {
	for _, kind := range []string{"", "Service", "TraefikService", "Other"} {
		for _, namespace := range []string{"", "backends"} {
			t.Run(kind+"/"+namespace, func(t *testing.T) {
				obj := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "traefik.io/v1alpha1", "kind": "IngressRoute", "metadata": map[string]any{"namespace": "routes"},
					"spec": map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"kind": kind, "name": "web", "namespace": namespace}}}}},
				}}
				refs := TraefikBackendReferences(obj)
				if kind == "Other" {
					if len(refs) != 0 {
						t.Fatalf("other kind: %+v", refs)
					}
					return
				}
				wantKind, wantGroup, wantNS := "Service", "", namespace
				if wantNS == "" {
					wantNS = "routes"
				}
				if kind == "TraefikService" {
					wantKind, wantGroup = kind, TraefikGroup
				}
				want := PathRef{Ref: resourceid.NewRef(wantGroup, wantKind, wantNS, "web"), Path: "spec.routes[].services[]"}
				if len(refs) != 1 || refs[0].Ref != want.Ref || refs[0].Path != want.Path {
					t.Fatalf("got %+v, want %+v", refs, want)
				}
			})
		}
	}
}

func TestTraefikTransportsRequireANamedServiceBackend(t *testing.T) {
	for _, kind := range []string{"IngressRoute", "IngressRouteTCP"} {
		for _, backend := range []map[string]any{
			{"kind": "TraefikService", "name": "web", "serversTransport": "transport"},
			{"kind": "Other", "name": "web", "serversTransport": "transport"},
			{"serversTransport": "transport"},
		} {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "traefik.io/v1alpha1", "kind": kind, "metadata": map[string]any{"namespace": "routes"},
				"spec": map[string]any{"routes": []any{map[string]any{"services": []any{backend}}}},
			}}
			if refs := TraefikRouteConfigReferences(obj); len(refs) != 0 {
				t.Fatalf("%s/%v: %+v", kind, backend, refs)
			}
		}
	}
}

// A provider-qualified name is a Traefik identifier, never an object name.
func TestTraefikProviderQualifiedNamesNameNoObject(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1", "kind": "IngressRoute", "metadata": map[string]any{"namespace": "routes"},
		"spec": map[string]any{"routes": []any{map[string]any{
			"services":    []any{map[string]any{"kind": "TraefikService", "name": "weighted@file"}, map[string]any{"name": "web", "serversTransport": "transport@file"}},
			"middlewares": []any{map[string]any{"name": "auth@file"}, map[string]any{"name": "shared-strip@kubernetescrd"}, map[string]any{"name": "strip"}},
		}}},
	}}
	var got []string
	for _, r := range append(TraefikBackendReferences(obj), TraefikRouteConfigReferences(obj)...) {
		got = append(got, r.Ref.Kind+"/"+r.Ref.Name)
	}
	if fmt.Sprint(got) != "[Service/web Middleware/strip]" {
		t.Errorf("references = %v, want only the plain names", got)
	}
}
