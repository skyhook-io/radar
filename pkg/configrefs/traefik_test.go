package configrefs

import (
	"fmt"
	"testing"
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
