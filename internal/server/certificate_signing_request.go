package server

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/csr"
	"github.com/skyhook-io/radar/pkg/topology"
)

// resourceReadResponse is a single-resource read plus what Radar decodes for
// kinds whose payload the UI cannot read itself: a CertificateSigningRequest's
// request and certificate are base64 DER inside PEM.
type resourceReadResponse struct {
	topology.ResourceWithRelationships
	CertificateSigningRequest *csr.Details `json:"certificateSigningRequest,omitempty"`
}

func newResourceReadResponse(read topology.ResourceWithRelationships) resourceReadResponse {
	out := resourceReadResponse{ResourceWithRelationships: read}
	if u, ok := read.Resource.(*unstructured.Unstructured); ok && csr.IsObject(u.Object) {
		details := csr.Describe(u.Object, time.Now())
		out.CertificateSigningRequest = &details
	}
	return out
}
