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
