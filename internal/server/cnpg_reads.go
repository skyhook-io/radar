package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var errCNPGDisconnected = errors.New("not connected to cluster")

type cnpgReadFailure struct {
	status  int
	message string
}

func (e *cnpgReadFailure) Error() string { return e.message }

func (s *Server) authorizeCNPGCachedRead(r *http.Request, namespace, resource string, extra ...Grant) error {
	if !k8s.IsConnected() {
		return errCNPGDisconnected
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		return &cnpgReadFailure{http.StatusForbidden, "no access to namespace " + namespace}
	}
	if !s.canRead(r, cnpgGroup, resource, namespace, "get") {
		return &cnpgReadFailure{http.StatusForbidden, "no access to " + resource + ".postgresql.cnpg.io in namespace " + namespace}
	}
	for _, g := range extra {
		var allowed bool
		if g.Subresource == "" {
			allowed = s.canRead(r, g.Group, g.Resource, namespace, g.Verb)
		} else {
			allowed = s.canReadSubresource(r, g.Group, g.Resource, g.Subresource, namespace, g.Verb)
		}
		if !allowed {
			return &cnpgReadFailure{http.StatusForbidden, "no access to " + g.Resource + " in namespace " + namespace}
		}
	}
	return nil
}

func (s *Server) cnpgClusterRead(r *http.Request, namespace, name string, extra ...Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error) {
	if err := s.authorizeCNPGCachedRead(r, namespace, "clusters", extra...); err != nil {
		return nil, nil, err
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, &cnpgReadFailure{http.StatusServiceUnavailable, "resource cache not available"}
	}
	cluster, err := readCNPGCachedCluster(r, cache, namespace, name)
	return cache, cluster, err
}

func readCNPGCachedCluster(r *http.Request, cache *k8s.ResourceCache, namespace, name string) (*unstructured.Unstructured, error) {
	cluster, err := findCNPGCluster(r.Context(), cache, namespace, name)
	switch {
	case err == nil && cluster != nil:
		return cluster, nil
	case err == nil, errors.Is(err, k8s.ErrUnknownDynamicKind):
		return nil, &cnpgReadFailure{http.StatusNotFound, "CloudNativePG Cluster " + namespace + "/" + name + " not found"}
	case errors.Is(err, errDynamicNotSynced):
		return nil, &cnpgReadFailure{http.StatusServiceUnavailable, "CloudNativePG Clusters are still syncing"}
	default:
		log.Printf("[cnpg] Failed to read Cluster %s/%s: %v", namespace, name, err)
		return nil, &cnpgReadFailure{http.StatusInternalServerError, "failed to read CloudNativePG Cluster"}
	}
}

func (s *Server) writeCNPGCachedReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, errCNPGDisconnected) {
		s.writeNotConnected(w)
		return
	}
	var failure *cnpgReadFailure
	if errors.As(err, &failure) {
		s.writeError(w, failure.status, failure.message)
		return
	}
	log.Printf("[cnpg] Failed to read: %v", err)
	s.writeError(w, http.StatusInternalServerError, "failed to read CloudNativePG data")
}
