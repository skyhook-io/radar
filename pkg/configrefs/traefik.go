package configrefs

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// TraefikTransportRefs lists the Secrets and ConfigMaps, in the transport's own
// namespace, that a Traefik ServersTransport or ServersTransportTCP reads TLS
// material from: rootCAs (a Secret or a ConfigMap each), the deprecated
// rootCAsSecrets, and certificatesSecrets. ServersTransport keeps these under
// spec; ServersTransportTCP nests them under spec.tls. Only the traefik.io
// group has rootCAs; the legacy traefik.containo.us group has the Secret lists.
func TraefikTransportRefs(obj map[string]any, tcp bool) (secrets, configMaps []string) {
	base := []string{"spec"}
	if tcp {
		base = []string{"spec", "tls"}
	}
	field := func(name string) []string { return append(append([]string{}, base...), name) }

	seenSecret, seenConfigMap := map[string]bool{}, map[string]bool{}
	addSecret := func(name string) {
		if name != "" && !seenSecret[name] {
			seenSecret[name] = true
			secrets = append(secrets, name)
		}
	}
	rootCAs, _, _ := unstructured.NestedSlice(obj, field("rootCAs")...)
	for _, item := range rootCAs {
		ca, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Traefik skips an entry that names both, whatever its type comment says.
		secret, _ := ca["secret"].(string)
		cm, _ := ca["configMap"].(string)
		switch {
		case secret != "" && cm != "":
		case secret != "":
			addSecret(secret)
		case cm != "" && !seenConfigMap[cm]:
			seenConfigMap[cm] = true
			configMaps = append(configMaps, cm)
		}
	}
	for _, name := range []string{"rootCAsSecrets", "certificatesSecrets"} {
		values, _, _ := unstructured.NestedStringSlice(obj, field(name)...)
		for _, v := range values {
			addSecret(v)
		}
	}
	return secrets, configMaps
}

// TraefikTLSStoreSecrets lists the Secrets a Traefik TLSStore loads
// certificates from: spec.defaultCertificate and every spec.certificates entry.
func TraefikTLSStoreSecrets(obj map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	name, _, _ := unstructured.NestedString(obj, "spec", "defaultCertificate", "secretName")
	add(name)
	certs, _, _ := unstructured.NestedSlice(obj, "spec", "certificates")
	for _, item := range certs {
		if cert, ok := item.(map[string]any); ok {
			secret, _ := cert["secretName"].(string)
			add(secret)
		}
	}
	return out
}
