package server

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
)

var argoApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

type gitOpsDestinationResponse struct {
	// Server is the destination's API server as scheme://host[:port], for
	// display. Empty when Radar can't tell which cluster it is.
	Server string `json:"server"`
	// Contexts are the kubeconfig contexts, other than the current one, whose
	// cluster is served at the destination's full API server URL.
	Contexts []string `json:"contexts"`
}

// handleGitOpsDestination resolves the cluster a remote Argo Application or
// Flux object (spec.kubeConfig) deploys to and the kubeconfig contexts that
// reach it, so the UI can offer to open a destination resource there. The
// object, and a Flux kubeconfig Secret or ConfigMap, are read as the
// requesting user. Full server URLs are compared here and never returned:
// their path identifies a cluster behind a shared gateway but can also carry
// credentials.
func (s *Server) handleGitOpsDestination(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	dyn := s.getDynamicClientForRequest(r)
	client := s.getClientForRequest(r)
	if dyn == nil || client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kubernetes client not available")
		return
	}

	var server string
	if strings.EqualFold(kind, "applications") || strings.EqualFold(kind, "application") {
		app, err := dyn.Resource(argoApplicationGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeDestinationError(w, err, namespace, name)
			return
		}
		server = argoDestinationServer(app, argoClusterServers)
	} else {
		entry, err := gitops.ResolveFluxKind(kind)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		obj, err := dyn.Resource(entry.GVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeDestinationError(w, err, namespace, name)
			return
		}
		server, err = fluxTargetServer(r.Context(), obj,
			func(ctx context.Context, ns, n string) (*corev1.Secret, error) {
				return client.CoreV1().Secrets(ns).Get(ctx, n, metav1.GetOptions{})
			},
			func(ctx context.Context, ns, n string) (*corev1.ConfigMap, error) {
				return client.CoreV1().ConfigMaps(ns).Get(ctx, n, metav1.GetOptions{})
			},
		)
		if err != nil {
			s.writeDestinationError(w, err, namespace, name)
			return
		}
	}

	resp := gitOpsDestinationResponse{Contexts: []string{}}
	if server != "" {
		resp.Server, _ = sanitizeDestinationServer(server)
		if contexts := k8s.ContextsForServer(server); contexts != nil {
			resp.Contexts = contexts
		}
	}
	s.writeJSON(w, resp)
}

func (s *Server) writeDestinationError(w http.ResponseWriter, err error, namespace, name string) {
	switch {
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, err.Error())
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, err.Error())
	default:
		log.Printf("[gitops] Failed to resolve destination for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// argoDestinationServer returns the API server a remote Application deploys
// to: spec.destination.server, or the server of the Argo cluster registered
// under spec.destination.name. Local, missing and ambiguous destinations
// resolve to "".
func argoDestinationServer(app *unstructured.Unstructured, clusterServers func(appNamespace, name string) []string) string {
	if gitops.IsInClusterDestination(app) {
		return ""
	}
	server, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "server")
	if server = strings.TrimSpace(server); server != "" {
		return server
	}
	name, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "name")
	if name = strings.TrimSpace(name); name == "" {
		return ""
	}
	if servers := clusterServers(app.GetNamespace(), name); len(servers) == 1 {
		return servers[0]
	}
	return ""
}

// argoClusterServers returns the servers registered under a destination
// name, from the cached Argo cluster Secrets.
func argoClusterServers(appNamespace, name string) []string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	lister := cache.Secrets()
	if lister == nil {
		return nil
	}
	secrets, err := lister.List(labels.SelectorFromSet(labels.Set{argoClusterSecretLabel: "cluster"}))
	if err != nil {
		return nil
	}
	var scopedTo string
	if perm := k8s.GetCachedPermissionResult(); perm != nil {
		if scope, ok := perm.Scopes[k8score.Secrets]; ok && scope.Enabled {
			scopedTo = scope.Namespace
		}
	}
	return registeredClusterServers(secrets, scopedTo, appNamespace, name)
}

// registeredClusterServers resolves a destination name only within the Argo
// install that owns the Application, since two installs can register
// different clusters under one name. Cluster Secrets in the Application's
// own namespace win; otherwise a lone install namespace is used. With several
// installs, or a Secrets cache limited to another namespace, the owner can't
// be told and nothing resolves.
func registeredClusterServers(secrets []*corev1.Secret, scopedTo, appNamespace, name string) []string {
	if scopedTo != "" && scopedTo != appNamespace {
		return nil
	}
	installs := map[string]bool{}
	for _, sec := range secrets {
		installs[sec.Namespace] = true
	}
	var registry string
	switch {
	case installs[appNamespace]:
		registry = appNamespace
	case len(installs) == 1:
		for ns := range installs {
			registry = ns
		}
	default:
		return nil
	}
	var out []string
	for _, sec := range secrets {
		if sec.Namespace == registry && string(sec.Data["name"]) == name {
			out = append(out, string(sec.Data["server"]))
		}
	}
	return out
}

// fluxTargetServer reads the API server out of a Flux object's
// spec.kubeConfig: the kubeconfig in the referenced Secret (Flux reads key
// "value", then "value.yaml", unless the ref names a key), or the "address"
// of a workload-identity ConfigMap. A missing Secret or ConfigMap, or a
// kubeconfig without a usable server, resolves to "" rather than an error:
// the object still targets another cluster, just not one Radar can name.
func fluxTargetServer(
	ctx context.Context,
	obj *unstructured.Unstructured,
	getSecret func(ctx context.Context, namespace, name string) (*corev1.Secret, error),
	getConfigMap func(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error),
) (string, error) {
	namespace := obj.GetNamespace()
	if name, _, _ := unstructured.NestedString(obj.Object, "spec", "kubeConfig", "secretRef", "name"); name != "" {
		secret, err := getSecret(ctx, namespace, name)
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		key, _, _ := unstructured.NestedString(obj.Object, "spec", "kubeConfig", "secretRef", "key")
		var raw []byte
		if key != "" {
			raw = secret.Data[key]
		} else if v, ok := secret.Data["value"]; ok {
			raw = v
		} else {
			raw = secret.Data["value.yaml"]
		}
		return kubeconfigServer(raw), nil
	}
	if name, _, _ := unstructured.NestedString(obj.Object, "spec", "kubeConfig", "configMapRef", "name"); name != "" {
		cm, err := getConfigMap(ctx, namespace, name)
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(cm.Data["address"]), nil
	}
	return "", nil
}

func kubeconfigServer(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return ""
	}
	if kctx := cfg.Contexts[cfg.CurrentContext]; kctx != nil {
		if cl := cfg.Clusters[kctx.Cluster]; cl != nil {
			return cl.Server
		}
		return ""
	}
	if len(cfg.Clusters) == 1 {
		for _, cl := range cfg.Clusters {
			return cl.Server
		}
	}
	return ""
}
