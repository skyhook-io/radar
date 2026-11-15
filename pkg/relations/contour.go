package relations

import (
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var kindHTTPProxy = resourceid.GroupKind{Group: configrefs.ContourGroup, Kind: "HTTPProxy"}

// ContourBackends is the family of the Services a Contour HTTPProxy sends
// traffic to and the HTTPProxies it includes, which may be in another
// namespace.
var ContourBackends = &Family{
	ScanAccepted:   "Representative include: 1763 ns and 928 B per Indexer.Update exceed the 512 B bar.",
	Name:           "contourBackends",
	Sources:        []Source{{Kind: kindHTTPProxy}},
	Targets:        []resourceid.GroupKind{kindService, kindHTTPProxy},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindHTTPProxy); ok {
			for _, r := range configrefs.ContourBackendReferences(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// ContourSecrets is the family of the Secrets a Contour HTTPProxy reads
// for TLS, client validation and upstream or JWKS validation, which a
// delegated name places in another namespace.
var ContourSecrets = &Family{
	ScanAccepted:   "Representative TLS reference: 2302 ns and 808 B per Indexer.Update exceed the 2 us / 512 B bar.",
	Name:           "contourSecrets",
	Sources:        []Source{{Kind: kindHTTPProxy}},
	Targets:        []resourceid.GroupKind{kindSecret},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindHTTPProxy); ok {
			for _, r := range configrefs.ContourSecretPathReferences(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}
