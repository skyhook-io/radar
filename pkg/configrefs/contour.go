package configrefs

import "strings"

// ContourSecretRef resolves a Secret reference in an HTTPProxy, such as
// spec.virtualhost.tls.secretName, the way Contour does. "namespace/name"
// names a Secret another namespace delegates with a TLSCertificateDelegation;
// a bare name, or an empty namespace, is in the proxy's own namespace.
func ContourSecretRef(proxyNamespace, ref string) (namespace, name string) {
	namespace, name, found := strings.Cut(ref, "/")
	if !found {
		return proxyNamespace, ref
	}
	if namespace == "" {
		namespace = proxyNamespace
	}
	return namespace, name
}
