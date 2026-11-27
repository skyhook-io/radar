package server

import (
	"context"
	"log"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	velerosvc "github.com/skyhook-io/radar/internal/velero"
)

// GET reads current operation objects, never object storage. Refuse the pin
// before any cluster read; require get on the run and list on each operation
// kind separately. Pod visibility is optional and never gates the operations.
func (s *Server) handleVeleroVolumeOperations(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	if _, ok := veleroRunKinds[kind]; !ok || namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "a backup or restore namespace and name are required")
		return
	}
	if !s.canRead(r, veleroGroup, kind, namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to this Velero run")
		return
	}
	operation := k8s.OperationContext()
	reader, ok := s.veleroVolumeReader(r)
	client := s.getDynamicClientForRequest(r)
	if !ok || client == nil || operation == nil || operation.Err() != nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	run, err := client.Resource(schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: kind}).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	switch {
	case err == nil:
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to this Velero run")
		return
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, "Velero run not found")
		return
	default:
		log.Printf("[velero] Failed to read run %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read this Velero run")
		return
	}
	resp := reader.VolumeOperations(r.Context(), run, kind == "restores")
	if slices.ContainsFunc(resp.Operations, func(row velerosvc.VolumeOperation) bool { return row.LongRunning }) {
		cache := k8s.GetResourceCache()
		if cache != nil {
			access, _ := s.typedKindScope(r, cache, []string{namespace}, "", "pods")
			if access.Covers(namespace) && cache.Pods() != nil {
				pods, err := cache.Pods().Pods(namespace).List(labels.SelectorFromSet(labels.Set{"role": "node-agent"}))
				velerosvc.AnnotateNodeAgents(resp.Operations, pods, err == nil)
			}
		}
	}
	if operation.Err() != nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection changed; try again")
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) veleroVolumeReader(r *http.Request) (*velerosvc.Reader, bool) {
	reader, ok := s.veleroReader(r)
	if !ok {
		return nil, false
	}
	discovery := k8s.GetResourceDiscovery()
	read := reader.Observations.WorkspaceRead
	reader.Observations.WorkspaceRead = func(ctx context.Context, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured) {
		if discovery == nil {
			return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
		}
		if _, served := discovery.GetGVRWithGroup(k.Kind, k.Group); !served {
			return integration.KindAccess{State: integration.KindCoverageNotInstalled}, nil
		}
		return read(ctx, k, namespaces)
	}
	return reader, true
}
