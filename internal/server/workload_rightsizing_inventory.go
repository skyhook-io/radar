package server

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	Gaps       []readreason.Code `json:"gaps,omitempty"`
	read       bool
	listDenied bool
}

func (s *Server) handleWorkloadRightsizingInventory(w http.ResponseWriter, r *http.Request) {
	if !s.sizingCaller(w, r) {
		return
	}
	requested := parseNamespaces(r.URL.Query())
	for _, ns := range requested {
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := s.sizingInventoryForRequest(r.WithContext(ctx), requested, client, caller)
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
	if !out.read && len(out.Gaps) > 0 {
		if slices.Contains(out.Gaps, readreason.ReadError) || slices.Contains(out.Gaps, readreason.RadarNamespaceNotCollected) {
			code := readreason.ReadError
			if slices.Contains(out.Gaps, readreason.RadarNamespaceNotCollected) {
				code = readreason.RadarNamespaceNotCollected
			}
			s.writeErrorCode(w, 503, string(code), "Kubernetes workload inventory is unavailable. Try selecting a namespace")
		} else {
			s.writeErrorCode(w, 403, string(readreason.Primary(out.Gaps)), "Kubernetes denied the rightsizing inventory read")
		}
		return
	}
	s.writeJSON(w, out)
}

func (s *Server) sizingInventoryForRequest(r *http.Request, requested []string, client dynamic.Interface, caller kubernetes.Interface) sizingInventory {
	ctx := r.Context()
	// Explicit targets use fresh, impersonated reads. A cached permission ceiling
	// may predate a new namespace grant or revocation.
	if len(requested) > 0 {
		return readSizingInventory(ctx, client, caller, requested)
	}
	if k8s.ForceNamespaceScope {
		if pin := k8s.GetNamespaceScopeTarget(); pin != "" {
			return readSizingInventory(ctx, client, caller, []string{pin})
		}
		return sizingInventory{Gaps: []readreason.Code{readreason.RadarNamespaceNotCollected}}
	}
	allowed, failed := s.getUserNamespacesWithStatus(r, nil)
	if failed {
		return sizingInventory{Gaps: []readreason.Code{readreason.ReadError}}
	}
	if allowed != nil {
		if len(allowed) == 0 {
			return sizingInventory{Gaps: []readreason.Code{readreason.UserNoNamespaceAccess}}
		}
		slices.Sort(allowed)
		return readSizingInventory(ctx, client, caller, allowed)
	}
	namespaces, source := k8s.GetAccessibleNamespacesWithSource(ctx)
	if source == k8s.NamespaceListCluster || source == k8s.NamespaceListCache {
		// Cluster-wide Pod access does not imply cluster-wide workload or
		// revision access. Fall back to namespaced reads for mixed grants.
		inv := readSizingInventory(ctx, client, caller, []string{metav1.NamespaceAll})
		if !inv.listDenied || ctx.Err() != nil {
			return inv
		}
	}
	inv := readSizingInventory(ctx, client, caller, namespaces)
	if !source.Complete() {
		inv.addGap(readreason.RadarNamespaceNotCollected)
	}
	return inv
}

func (i *sizingInventory) addGap(code readreason.Code) {
	if !slices.Contains(i.Gaps, code) {
		i.Gaps = append(i.Gaps, code)
	}
}

func (i *sizingInventory) recordListError(err error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		i.listDenied = true
		i.addGap(readreason.UserForbidden)
	} else {
		i.addGap(readreason.ReadError)
	}
}

func readSizingInventory(ctx context.Context, client dynamic.Interface, caller kubernetes.Interface, namespaces []string) sizingInventory {
	const itemLimit = 2000
	out := sizingInventory{Items: []sizingCandidate{}}
	for _, ns := range namespaces {
		if ctx.Err() != nil {
			out.addGap(readreason.ReadError)
			break
		}
		if _, err := client.Resource(corev1.SchemeGroupVersion.WithResource("pods")).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			out.recordListError(err)
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
					out.recordListError(err)
					continue
				}
			}
			res := client.Resource(appsv1.SchemeGroupVersion.WithResource(wk.Resource))
			// A fresh self-review runs through the caller's exact token or impersonation.
			broadGet, _ := k8score.CanI(ctx, caller, ns, "apps", wk.Resource, "get")
			var token string
			for {
				page, err := res.Namespace(ns).List(ctx, metav1.ListOptions{Limit: 100, Continue: token})
				if err != nil {
					out.recordListError(err)
					break
				}
				out.read = true
				for i := range page.Items {
					obj := &page.Items[i]
					objNS := obj.GetNamespace()
					if (ns != metav1.NamespaceAll && objNS != ns) || objNS == "" || obj.GetName() == "" {
						out.addGap(readreason.ReadError)
						continue
					}
					if sizingScaledToZero(wk.Kind, obj) {
						continue
					}
					if !broadGet {
						if _, err := res.Namespace(objNS).Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
							if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
								out.addGap(readreason.UserForbidden)
							} else if !apierrors.IsNotFound(err) {
								out.addGap(readreason.ReadError)
							}
							continue
						}
					}
					if len(out.Items) >= itemLimit {
						out.addGap(readreason.ListTooLarge)
						return out
					}
					out.Items = append(out.Items, sizingCandidate{Kind: wk.Kind, Namespace: objNS, Name: obj.GetName()})
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
