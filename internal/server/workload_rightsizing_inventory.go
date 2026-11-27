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
	retryKinds []prometheus.RightsizingScanKind
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
	if installed := k8s.RBACNamespaces(); len(installed) > 0 {
		return readSizingInventory(ctx, client, caller, installed)
	}
	inv := readSizingInventory(ctx, client, caller, []string{metav1.NamespaceAll})
	if len(inv.retryKinds) == 0 || slices.Contains(inv.Gaps, readreason.ListTooLarge) {
		return inv
	}
	if ctx.Err() != nil {
		inv.addGap(readreason.ReadError)
		return inv
	}

	// A caller can list a kind across namespaces even when Radar cannot
	// enumerate Namespace objects. Only denied kinds need named reads.
	namespaces, source := k8s.GetAccessibleNamespacesWithSource(ctx)
	allowed, failed := s.getUserNamespacesWithStatus(r, nil)
	if failed {
		inv.addGap(readreason.ReadError)
	}
	if allowed != nil && !failed {
		namespaces = slices.Clone(allowed)
	}
	if !source.Complete() {
		inv.addGap(readreason.RadarNamespaceNotCollected)
		for _, item := range inv.Items {
			namespaces = append(namespaces, item.Namespace)
		}
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	if len(namespaces) == 0 {
		inv.addGap(readreason.UserForbidden)
	}
	inv.collect(ctx, client, caller, namespaces, inv.retryKinds)
	return inv
}

func (i *sizingInventory) addGap(code readreason.Code) {
	if !slices.Contains(i.Gaps, code) {
		i.Gaps = append(i.Gaps, code)
	}
}

func (i *sizingInventory) recordListError(ns string, kinds []prometheus.RightsizingScanKind, err error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		if ns == metav1.NamespaceAll {
			i.retryKinds = append(i.retryKinds, kinds...)
			return
		}
		i.addGap(readreason.UserForbidden)
	} else {
		i.addGap(readreason.ReadError)
	}
}

func readSizingInventory(ctx context.Context, client dynamic.Interface, caller kubernetes.Interface, namespaces []string) sizingInventory {
	out := sizingInventory{Items: []sizingCandidate{}}
	out.collect(ctx, client, caller, namespaces, prometheus.RightsizingScanKinds)
	return out
}

func (out *sizingInventory) collect(ctx context.Context, client dynamic.Interface, caller kubernetes.Interface, namespaces []string, kinds []prometheus.RightsizingScanKind) {
	const itemLimit = 2000
	// A later page can be denied after earlier pages succeeded. A named
	// retry of the same kind must not emit those candidates twice.
	existing := make(map[sizingCandidate]struct{}, len(out.Items))
	for _, item := range out.Items {
		existing[item] = struct{}{}
	}
	for _, ns := range namespaces {
		if ctx.Err() != nil {
			out.addGap(readreason.ReadError)
			break
		}
		if _, err := client.Resource(corev1.SchemeGroupVersion.WithResource("pods")).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			out.recordListError(ns, kinds, err)
			continue
		}
		for _, wk := range kinds {
			var required string
			switch wk.Kind {
			case "Deployment":
				required = "replicasets"
			case "DaemonSet":
				required = "controllerrevisions"
			}
			if required != "" {
				if _, err := client.Resource(appsv1.SchemeGroupVersion.WithResource(required)).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
					out.recordListError(ns, []prometheus.RightsizingScanKind{wk}, err)
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
					out.recordListError(ns, []prometheus.RightsizingScanKind{wk}, err)
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
					item := sizingCandidate{Kind: wk.Kind, Namespace: objNS, Name: obj.GetName()}
					if _, seen := existing[item]; seen {
						continue
					}
					if len(out.Items) >= itemLimit {
						out.addGap(readreason.ListTooLarge)
						return
					}
					out.Items = append(out.Items, item)
				}
				token = page.GetContinue()
				if token == "" {
					break
				}
			}
		}
	}
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
