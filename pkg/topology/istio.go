package topology

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Istio's service registry builds FQDNs using pilot-discovery's --domain, not
// an arbitrary suffix inferred from the destination. Standard Istiod manifests
// expose it in the discovery container. Conflicting revisions or unresolved
// argument values leave the domain unknown. All reads are from the cache.
func (b *Builder) istioServiceDomain() string {
	deployments, err := b.provider.Deployments()
	if err != nil {
		return ""
	}
	domain, observed := "", false
	for _, deployment := range deployments {
		if deployment.Labels["app"] != "istiod" && deployment.Labels["istio"] != "pilot" {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "discovery" {
				continue
			}
			configured := "cluster.local" // pilot-discovery's documented default.
			args := append(append([]string(nil), container.Command...), container.Args...)
			for i := 0; i < len(args); i++ {
				arg := args[i]
				if arg == "--domain" {
					if i+1 == len(args) {
						return ""
					}
					i++
					configured = args[i]
				} else if strings.HasPrefix(arg, "--domain=") {
					configured = strings.TrimPrefix(arg, "--domain=")
				}
			}
			if len(validation.IsDNS1123Subdomain(configured)) != 0 || configured == "" {
				return ""
			}
			if observed && domain != configured {
				return ""
			}
			domain, observed = configured, true
		}
	}
	if !observed {
		return "cluster.local"
	}
	return domain
}

func parseIstioHost(host, defaultNamespace, domain string) (string, string) {
	parts := strings.Split(host, ".")
	if len(parts) == 1 && len(validation.IsDNS1123Label(host)) == 0 {
		return host, defaultNamespace
	}
	if domain == "" || !strings.HasSuffix(host, ".svc."+domain) {
		return "", ""
	}
	service := strings.TrimSuffix(host, ".svc."+domain)
	parts = strings.Split(service, ".")
	if len(parts) == 2 && len(validation.IsDNS1123Label(parts[0])) == 0 && len(validation.IsDNS1123Label(parts[1])) == 0 {
		return parts[0], parts[1]
	}
	return "", ""
}
