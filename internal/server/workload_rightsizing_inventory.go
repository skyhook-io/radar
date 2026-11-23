package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type sizingCandidate struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type sizingInventory struct {
	ClusterUID string            `json:"clusterUID"`
	Items      []sizingCandidate `json:"items"`
	Partial    bool              `json:"partial"`
}

func (s *Server) handleWorkloadRightsizingInventory(w http.ResponseWriter, r *http.Request) {
	if !s.sizingCaller(w, r) {
		return
	}
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		if len(validation.IsDNS1123Label(ns)) > 0 {
			s.writeError(w, 400, "Invalid Kubernetes namespace")
			return
		}
		if outsideNamespacePin(ns) != nil {
			s.writeError(w, 403, "Namespace is outside the rightsizing scope")
			return
		}
	}
	if !s.requireConnected(w) {
		return
	}
	gen := k8s.Current().ClientGeneration()
	client, caller := s.getDynamicClientForRequest(r), s.getClientForRequest(r)
	if client == nil || caller == nil {
		s.writeError(w, 503, "Kubernetes is unavailable")
		return
	}
	namespaces, failed := s.parseNamespacesForUserWithStatus(r)
	if failed {
		s.writeError(w, 503, "Unable to determine authorized namespaces")
		return
	}
	if namespaces == nil {
		cache := k8s.GetResourceCache()
		if cache == nil || cache.Namespaces() == nil {
			s.writeError(w, 503, "Namespace inventory is unavailable")
			return
		}
		if synced, _ := cache.InformerSynced("namespaces"); !synced {
			s.writeError(w, 503, "Namespace inventory is unavailable")
			return
		}
		ns, err := cache.Namespaces().List(labels.Everything())
		if err != nil {
			s.writeError(w, 503, "Namespace inventory is unavailable")
			return
		}
		for _, n := range ns {
			if outsideNamespacePin(n.Name) == nil {
				namespaces = append(namespaces, n.Name)
			}
		}
	}
	sort.Strings(namespaces)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := readSizingInventory(ctx, client, caller, namespaces)
	var err error
	out.ClusterUID, err = cloud.DiscoverClusterUID(r.Context(), k8s.GetClientInterface())
	if k8s.Current().ClientGeneration() != gen {
		s.writeError(w, 409, "Kubernetes connection changed. Retry the request")
		return
	}
	if err != nil || out.ClusterUID == "" {
		s.writeError(w, 503, "Cluster identity is unavailable")
		return
	}
	s.writeJSON(w, out)
}

func readSizingInventory(ctx context.Context, client dynamic.Interface, caller kubernetes.Interface, namespaces []string) sizingInventory {
	const itemLimit = 2000
	out := sizingInventory{Items: []sizingCandidate{}}
	for _, ns := range namespaces {
		if ctx.Err() != nil {
			out.Partial = true
			break
		}
		if _, err := client.Resource(corev1.SchemeGroupVersion.WithResource("pods")).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			out.Partial = true
			continue
		}
		for _, wk := range prometheus.RightsizingScanKinds {
			var required string
			switch wk.Kind {
			case "Deployment":
				required = "replicasets"
			case "DaemonSet":
				required = "controllerrevisions"
			}
			if required != "" {
				if _, err := client.Resource(appsv1.SchemeGroupVersion.WithResource(required)).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
					out.Partial = true
					continue
				}
			}
			res := client.Resource(appsv1.SchemeGroupVersion.WithResource(wk.Resource)).Namespace(ns)
			// A fresh self-review runs through the caller's exact token or impersonation.
			broadGet, _ := k8score.CanI(ctx, caller, ns, "apps", wk.Resource, "get")
			var token string
			for {
				page, err := res.List(ctx, metav1.ListOptions{Limit: 100, Continue: token})
				if err != nil {
					out.Partial = out.Partial || !apierrors.IsForbidden(err)
					break
				}
				for i := range page.Items {
					obj := &page.Items[i]
					if obj.GetNamespace() != ns || obj.GetName() == "" {
						out.Partial = true
						continue
					}
					if sizingScaledToZero(wk.Kind, obj) {
						continue
					}
					if !broadGet {
						if _, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
							out.Partial = out.Partial || !(apierrors.IsForbidden(err) || apierrors.IsNotFound(err))
							continue
						}
					}
					if len(out.Items) >= itemLimit {
						out.Partial = true
						return out
					}
					out.Items = append(out.Items, sizingCandidate{Kind: wk.Kind, Namespace: ns, Name: obj.GetName()})
				}
				token = page.GetContinue()
				if token == "" {
					break
				}
			}
		}
	}
	return out
}

func sizingScaledToZero(kind string, obj *unstructured.Unstructured) bool {
	if kind == "DaemonSet" {
		observed, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
		desired, found, err := unstructured.NestedInt64(obj.Object, "status", "desiredNumberScheduled")
		return obj.GetGeneration() > 0 && observed == obj.GetGeneration() && found && err == nil && desired == 0
	}
	n, found, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	return found && err == nil && n == 0
}
