package server

import (
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type openCostWorkloadLookup struct {
	Resource        any
	DesiredReplicas int
	Status          int
	Message         string
	Reason          string
}

func (s *Server) handleOpenCostWorkload(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind, namespace, name, ok := s.parseOpenCostWorkloadRequest(w, r)
	if !ok {
		return
	}
	_, desiredReplicas, ok := s.loadOpenCostWorkloadResource(w, kind, namespace, name)
	if !ok {
		return
	}

	resp := &pkgopencost.WorkloadCostDetailResponse{
		Namespace: namespace,
		Kind:      kind,
		Name:      name,
	}
	connection, err := internalopencost.Selected(r.Context())
	if err != nil {
		resp.Reason = internalopencost.ConnectionFailureReason(err)
		resp.Currency = s.resolvedOpenCostCurrency()
		s.writeJSON(w, resp)
		return
	}
	if connection.Source == internalopencost.SourceKubecost {
		workloads, err := pkgopencost.ComputeKubecostWorkloads(r.Context(), connection.Client, namespace, pkgopencost.KubecostCurrentOptions{
			Currency: s.resolvedOpenCostCurrency(), ClusterID: connection.ClusterID, Owners: internalopencost.BuildPodOwnerLookup(namespace),
		})
		if err != nil {
			log.Printf("[opencost] Kubecost workload cost failed for %s/%s: %s", sanitizeForLog(namespace), sanitizeForLog(name), sanitizeForLog(err.Error()))
			resp.Reason = internalopencost.ConnectionFailureReason(err)
			resp.Currency = s.resolvedOpenCostCurrency()
			resp.Source = "kubecost"
			s.writeJSON(w, resp)
			return
		}
		result := focusOpenCostWorkload(workloads, kind, namespace, name, desiredReplicas)
		result.Currency = s.resolvedOpenCostCurrency()
		result.Source = "kubecost"
		result.DataThrough = workloads.DataThrough
		s.writeJSON(w, result)
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		resp.Available = false
		resp.Reason = pkgopencost.ReasonNoPrometheus
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Print("[opencost] EnsureConnected failed for workload cost")
		resp.Available = false
		resp.Reason = internalopencost.ConnectionFailureReason(err)
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}

	workloads := pkgopencost.ComputeWorkloadsFromProm(r.Context(), client.Prom(), namespace, internalopencost.BuildPodOwnerLookup(namespace))
	result := focusOpenCostWorkload(workloads, kind, namespace, name, desiredReplicas)
	result.Currency = s.resolvedOpenCostCurrency()
	result.Source = "prometheus"
	s.writeJSON(w, result)
}

func (s *Server) handleOpenCostWorkloadTrend(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind, namespace, name, ok := s.parseOpenCostWorkloadRequest(w, r)
	if !ok {
		return
	}
	if _, _, ok := s.loadOpenCostWorkloadResource(w, kind, namespace, name); !ok {
		return
	}

	resp := &pkgopencost.WorkloadCostTrendResponse{
		Namespace: namespace,
		Kind:      kind,
		Name:      name,
		Range:     r.URL.Query().Get("range"),
	}
	connection, err := internalopencost.Selected(r.Context())
	if err != nil {
		resp.Reason = internalopencost.ConnectionFailureReason(err)
		resp.Currency = s.resolvedOpenCostCurrency()
		s.writeJSON(w, resp)
		return
	}
	if connection.Source == internalopencost.SourceKubecost {
		resp.Reason = pkgopencost.ReasonHistoryUnsupported
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "kubecost"
		s.writeJSON(w, resp)
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		resp.Available = false
		resp.Reason = pkgopencost.ReasonNoPrometheus
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Print("[opencost] EnsureConnected failed for workload trend")
		resp.Available = false
		resp.Reason = internalopencost.ConnectionFailureReason(err)
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}

	result := pkgopencost.ComputeWorkloadCostTrendFromProm(r.Context(), client.Prom(), pkgopencost.WorkloadTrendOptions{
		Range:     r.URL.Query().Get("range"),
		Namespace: namespace,
		Kind:      kind,
		Name:      name,
	})
	result.Currency = s.resolvedOpenCostCurrency()
	result.Source = "prometheus"
	s.writeJSON(w, result)
}

func (s *Server) resolvedOpenCostCurrency() string {
	if s.openCostCurrency == nil {
		return pkgopencost.DefaultCurrency
	}
	return s.openCostCurrency.Resolve()
}

func (s *Server) parseOpenCostWorkloadRequest(w http.ResponseWriter, r *http.Request) (kind, namespace, name string, ok bool) {
	kind, supported := pkgopencost.CanonicalWorkloadKind(chi.URLParam(r, "kind"))
	if !supported {
		s.writeError(w, http.StatusBadRequest, "only deployments, statefulsets, and daemonsets are supported")
		return "", "", "", false
	}
	namespace = chi.URLParam(r, "namespace")
	name = chi.URLParam(r, "name")
	if namespace == "" || namespace == "_" || name == "" {
		s.writeError(w, http.StatusBadRequest, "namespace and name are required")
		return "", "", "", false
	}

	if status, msg, ok := s.preflightResourceGet(r, normalizeKind(kind), namespace, name, "apps"); !ok {
		s.writeError(w, status, msg)
		return "", "", "", false
	}
	return kind, namespace, name, true
}

func (s *Server) loadOpenCostWorkloadResource(w http.ResponseWriter, kind, namespace, name string) (any, int, bool) {
	lookup := s.lookupOpenCostWorkloadResource(kind, namespace, name)
	if lookup.Status != 0 {
		s.writeError(w, lookup.Status, lookup.Message)
		return nil, 0, false
	}
	return lookup.Resource, lookup.DesiredReplicas, true
}

func (s *Server) lookupOpenCostWorkloadResource(kind, namespace, name string) openCostWorkloadLookup {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return openCostWorkloadLookup{
			Status:  http.StatusServiceUnavailable,
			Message: "Resource cache not available",
			Reason:  pkgopencost.ReasonQueryError,
		}
	}

	switch kind {
	case "Deployment":
		if cache.Deployments() == nil {
			return openCostWorkloadLookup{
				Status:  http.StatusForbidden,
				Message: "insufficient permissions to access deployments",
				Reason:  pkgopencost.ReasonAccessDenied,
			}
		}
		deploy, err := cache.Deployments().Deployments(namespace).Get(name)
		if err != nil {
			return openCostWorkloadGetError(kind, namespace, name, err)
		}
		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}
		return openCostWorkloadLookup{Resource: deploy, DesiredReplicas: int(replicas)}
	case "StatefulSet":
		if cache.StatefulSets() == nil {
			return openCostWorkloadLookup{
				Status:  http.StatusForbidden,
				Message: "insufficient permissions to access statefulsets",
				Reason:  pkgopencost.ReasonAccessDenied,
			}
		}
		sts, err := cache.StatefulSets().StatefulSets(namespace).Get(name)
		if err != nil {
			return openCostWorkloadGetError(kind, namespace, name, err)
		}
		replicas := int32(1)
		if sts.Spec.Replicas != nil {
			replicas = *sts.Spec.Replicas
		}
		return openCostWorkloadLookup{Resource: sts, DesiredReplicas: int(replicas)}
	case "DaemonSet":
		if cache.DaemonSets() == nil {
			return openCostWorkloadLookup{
				Status:  http.StatusForbidden,
				Message: "insufficient permissions to access daemonsets",
				Reason:  pkgopencost.ReasonAccessDenied,
			}
		}
		ds, err := cache.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return openCostWorkloadGetError(kind, namespace, name, err)
		}
		return openCostWorkloadLookup{Resource: ds, DesiredReplicas: int(ds.Status.DesiredNumberScheduled)}
	default:
		return openCostWorkloadLookup{
			Status:  http.StatusBadRequest,
			Message: "only deployments, statefulsets, and daemonsets are supported",
			Reason:  pkgopencost.ReasonQueryError,
		}
	}
}

func openCostWorkloadGetError(kind, namespace, name string, err error) openCostWorkloadLookup {
	if apierrors.IsNotFound(err) {
		return openCostWorkloadLookup{
			Status:  http.StatusNotFound,
			Message: fmt.Sprintf("%s %s/%s not found", kind, namespace, name),
			Reason:  pkgopencost.ReasonNotFound,
		}
	}
	log.Print("[opencost] Failed to get workload for cost")
	return openCostWorkloadLookup{
		Status:  http.StatusInternalServerError,
		Message: "failed to get workload",
		Reason:  pkgopencost.ReasonQueryError,
	}
}

func focusOpenCostWorkload(resp *pkgopencost.WorkloadCostResponse, kind, namespace, name string, desiredReplicas int) *pkgopencost.WorkloadCostDetailResponse {
	out := &pkgopencost.WorkloadCostDetailResponse{
		Namespace: namespace,
		Kind:      kind,
		Name:      name,
	}
	if resp == nil {
		out.Available = false
		out.Reason = pkgopencost.ReasonQueryError
		return out
	}
	out.Source = resp.Source
	if resp.Available {
		out.Window = resp.Window
	}
	out.DataThrough = resp.DataThrough
	if resp.Available {
		for i := range resp.Workloads {
			wl := resp.Workloads[i]
			if wl.Kind == kind && wl.Name == name {
				out.Available = true
				out.Current = &wl
				return out
			}
		}
		if desiredReplicas == 0 {
			out.Available = true
			out.Current = zeroWorkloadCost(kind, name)
			return out
		}
		out.Available = false
		out.Reason = pkgopencost.ReasonNoMetrics
		return out
	}
	if resp.Reason == pkgopencost.ReasonNoMetrics && desiredReplicas == 0 {
		out.Available = true
		out.Current = zeroWorkloadCost(kind, name)
		return out
	}
	out.Available = false
	out.Reason = resp.Reason
	return out
}

func zeroWorkloadCost(kind, name string) *pkgopencost.WorkloadCost {
	return &pkgopencost.WorkloadCost{
		Name:       name,
		Kind:       kind,
		HourlyCost: 0,
		CPUCost:    0,
		MemoryCost: 0,
		Replicas:   0,
		Efficiency: 0,
		IdleCost:   0,
	}
}
