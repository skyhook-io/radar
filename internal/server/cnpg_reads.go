package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func (s *Server) authorizeCNPGCachedRead(r *http.Request, namespace, resource string, extra ...Grant) error {
	if !k8s.IsConnected() {
		return cnpgsvc.ErrCNPGDisconnected
	}
	if integration.NoNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		return &cnpgsvc.ReadFailure{http.StatusForbidden, "no access to namespace " + namespace}
	}
	if !s.canRead(r, cnpgsvc.Group, resource, namespace, "get") {
		return &cnpgsvc.ReadFailure{http.StatusForbidden, "no access to " + resource + ".postgresql.cnpg.io in namespace " + namespace}
	}
	for _, g := range extra {
		var allowed bool
		if g.Subresource == "" {
			allowed = s.canRead(r, g.Group, g.Resource, namespace, g.Verb)
		} else {
			allowed = s.canReadSubresource(r, g.Group, g.Resource, g.Subresource, namespace, g.Verb)
		}
		if !allowed {
			return &cnpgsvc.ReadFailure{http.StatusForbidden, "no access to " + g.Resource + " in namespace " + namespace}
		}
	}
	return nil
}

func cnpgCachedClusterResult(cluster *unstructured.Unstructured, err error, namespace, name string) (*unstructured.Unstructured, error) {
	switch {
	case err == nil && cluster != nil:
		return cluster, nil
	case err == nil, errors.Is(err, k8s.ErrUnknownDynamicKind):
		return nil, &cnpgsvc.ReadFailure{http.StatusNotFound, "CloudNativePG Cluster " + namespace + "/" + name + " not found"}
	case errors.Is(err, integration.ErrDynamicNotSynced):
		return nil, &cnpgsvc.ReadFailure{http.StatusServiceUnavailable, "CloudNativePG Clusters are still syncing"}
	default:
		return nil, fmt.Errorf("%w: %w", &cnpgsvc.ReadFailure{http.StatusInternalServerError, "failed to read CloudNativePG Cluster"}, err)
	}
}

func (s *Server) writeCNPGCachedReadError(w http.ResponseWriter, err error, namespace, name string) {
	if errors.Is(err, cnpgsvc.ErrCNPGDisconnected) {
		s.writeNotConnected(w)
		return
	}
	var failure *cnpgsvc.ReadFailure
	if errors.As(err, &failure) {
		if failure.Status == http.StatusInternalServerError {
			log.Printf("[cnpg] Failed to read %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		}
		s.writeError(w, failure.Status, failure.Message)
		return
	}
	log.Printf("[cnpg] Failed to read %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
	s.writeError(w, http.StatusInternalServerError, "failed to read CloudNativePG data")
}
