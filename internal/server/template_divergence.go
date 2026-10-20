package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/runtime"
)

// resourceDetailResponse is the resource API's answer: the resource with its
// relationships, plus the Pod/template comparison for the kinds that have one.
// An older Radar sends no templateDivergence, and readers render nothing.
type resourceDetailResponse struct {
	topology.ResourceWithRelationships
	TemplateDivergence *resourcecontext.TemplateDivergence `json:"templateDivergence,omitempty"`
}

// templateDivergenceFor compares obj's live Pods with their owner templates as
// the caller, or returns nil for kinds without a comparison.
func (s *Server) templateDivergenceFor(r *http.Request, obj any) *resourcecontext.TemplateDivergence {
	o, ok := obj.(runtime.Object)
	if !ok {
		return nil
	}
	return issues.NewCacheProvider().TemplateDivergence(o, s.issueClusterScopedAccess(r), s.issueRelatedResourceAccess(r), func(verb, group, resource, namespace string) bool {
		return s.canRead(r, group, resource, namespace, verb)
	})
}
