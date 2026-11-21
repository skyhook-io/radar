package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

// Hub uses this tunnel-only read to discover the installation identity of agents
// connected with a cluster token, which have no enrollment identity on file.
func (s *Server) handleMetricsClusterIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) || auth.UserFromContext(r.Context()) == nil {
		http.NotFound(w, r)
		return
	}
	uid, err := cloud.DiscoverClusterUID(r.Context(), k8s.GetClientInterface())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cluster identity is unavailable")
		return
	}
	s.writeJSON(w, map[string]string{"uid": uid})
}

// handleWorkloadMetricsScope authorizes a Hub-hosted workload metrics read. The
// Hub has no Kubernetes credentials of its own and must query with this caller's
// Kubernetes privileges. Bind the authorized workload to this physical cluster.
func (s *Server) handleWorkloadMetricsScope(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if auth.UserFromContext(r.Context()) == nil {
		s.writeErrorCode(w, http.StatusUnauthorized, "workload_metrics_identity_required", "Workload metrics require an authenticated Hub identity")
		return
	}
	kind := k8s.CanonicalWorkloadKind(chi.URLParam(r, "kind"))
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if (kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet") ||
		len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		s.writeError(w, http.StatusBadRequest, "Expected a Deployment, StatefulSet, or DaemonSet with a namespace and name")
		return
	}
	if failure := outsideNamespacePin(namespace); failure != nil {
		s.writeError(w, http.StatusForbidden, "Workload is outside Radar's namespace scope")
		return
	}
	_, generation := k8s.GetClientWithGeneration()
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes is unavailable")
		return
	}
	_, resource, _ := k8s.NamespacedBuiltinGVR(kind)
	createdAt, err := workloadMetricsCreationTime(r.Context(), client, resource, namespace, name)
	var uid string
	var identityErr error
	if err == nil {
		uid, identityErr = cloud.DiscoverClusterUID(r.Context(), k8s.GetClientInterface())
	}
	if _, current := k8s.GetClientWithGeneration(); generation != current {
		s.writeError(w, http.StatusConflict, "Kubernetes connection changed. Retry the request")
		return
	}
	switch {
	case apierrors.IsUnauthorized(err), apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "Metrics require permission to read this workload and list Pods in its namespace")
	case apierrors.IsNotFound(err):
		s.writeErrorCode(w, http.StatusNotFound, "workload_not_found", "Workload not found")
	case identityErr != nil:
		s.writeError(w, http.StatusServiceUnavailable, "Radar could not read the Kubernetes cluster UID")
	case err != nil || createdAt.IsZero():
		s.writeError(w, http.StatusServiceUnavailable, "Could not establish workload scope")
	default:
		s.writeJSON(w, map[string]any{"createdAt": createdAt, "clusterUID": uid})
	}
}

func workloadMetricsCreationTime(ctx context.Context, client dynamic.Interface, resource, namespace, name string) (time.Time, error) {
	workload, err := client.Resource(appsv1.SchemeGroupVersion.WithResource(resource)).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return time.Time{}, err
	}
	_, err = client.Resource(corev1.SchemeGroupVersion.WithResource("pods")).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return time.Time{}, err
	}
	return workload.GetCreationTimestamp().Time, nil
}
