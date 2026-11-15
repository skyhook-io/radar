package topology

import "github.com/skyhook-io/radar/pkg/configrefs"

// istioServiceDomain is configrefs.IstioServiceDomain over the cached
// Deployments; unknown when they can't be read.
func (b *Builder) istioServiceDomain() string {
	deployments, err := b.provider.Deployments()
	if err != nil {
		return ""
	}
	return configrefs.IstioServiceDomain(deployments)
}

func parseIstioHost(host, defaultNamespace, domain string) (string, string) {
	return configrefs.IstioHostService(host, defaultNamespace, domain)
}
