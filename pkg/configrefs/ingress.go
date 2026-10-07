package configrefs

import networkingv1 "k8s.io/api/networking/v1"

// IngressTLSSecretReferences returns named TLS Secret declarations in the
// Ingress's namespace. It does not infer controller defaults or TLS readiness.
func IngressTLSSecretReferences(ing *networkingv1.Ingress) []Ref {
	if ing == nil {
		return nil
	}
	var refs []Ref
	seen := map[Ref]bool{}
	for _, tls := range ing.Spec.TLS {
		if tls.SecretName == "" {
			continue
		}
		ref := Ref{Kind: "Secret", Namespace: ing.Namespace, Name: tls.SecretName}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	return refs
}
