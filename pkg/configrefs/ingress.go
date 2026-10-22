package configrefs

import networkingv1 "k8s.io/api/networking/v1"

// IngressBackendServices returns the names of the Services an Ingress routes
// to, in first-seen order and each once: spec.defaultBackend, then every rule
// path. A Service backend is always in the Ingress's own namespace; resource
// backends name no Service.
func IngressBackendServices(ing *networkingv1.Ingress) []string {
	var names []string
	seen := map[string]bool{}
	add := func(backend *networkingv1.IngressBackend) {
		if backend == nil || backend.Service == nil || backend.Service.Name == "" || seen[backend.Service.Name] {
			return
		}
		seen[backend.Service.Name] = true
		names = append(names, backend.Service.Name)
	}
	add(ing.Spec.DefaultBackend)
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for i := range rule.HTTP.Paths {
			add(&rule.HTTP.Paths[i].Backend)
		}
	}
	return names
}

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
