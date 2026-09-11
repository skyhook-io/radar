package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

const maxOpenCostApplicationWorkloads = 100

type openCostApplicationRequest struct {
	Range     string                               `json:"range,omitempty"`
	Workloads []pkgopencost.ApplicationWorkloadRef `json:"workloads"`
}

func (s *Server) handleOpenCostApplication(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	_, inputs, unavailable, unsupported, ok := s.parseOpenCostApplicationRequest(w, r)
	if !ok {
		return
	}
	connection, err := internalopencost.Selected(r.Context())
	if err != nil {
		resp := pkgopencost.UnavailableApplicationCostResponse(inputs, unavailable, unsupported, internalopencost.ConnectionFailureReason(err))
		resp.Currency = s.resolvedOpenCostCurrency()
		s.writeJSON(w, resp)
		return
	}
	if connection.Source == internalopencost.SourceKubecost {
		namespaces := applicationInputNamespaces(inputs)
		currency := s.resolvedOpenCostCurrency()
		namespaceCosts := make(map[string]*pkgopencost.WorkloadCostResponse, len(namespaces))
		if len(namespaces) > 0 {
			owners := make(map[string]pkgopencost.PodOwnerLookup, len(namespaces))
			for _, namespace := range namespaces {
				owners[namespace] = internalopencost.BuildPodOwnerLookup(namespace)
			}
			var queryErr error
			var namespaceErrors map[string]error
			namespaceCosts, namespaceErrors, queryErr = pkgopencost.ComputeKubecostWorkloadsForNamespaces(r.Context(), connection.Client, owners, pkgopencost.KubecostCurrentOptions{
				Currency: currency, ClusterID: connection.ClusterID,
			})
			if queryErr != nil {
				log.Printf("[opencost] Kubecost application cost failed: %s", sanitizeForLog(queryErr.Error()))
				namespaceCosts = make(map[string]*pkgopencost.WorkloadCostResponse, len(namespaces))
				for _, namespace := range namespaces {
					namespaceCosts[namespace] = &pkgopencost.WorkloadCostResponse{Namespace: namespace, Reason: internalopencost.ConnectionFailureReason(queryErr), Source: "kubecost"}
				}
			} else {
				for namespace, namespaceErr := range namespaceErrors {
					log.Printf("[opencost] Kubecost application cost failed for namespace %q: %s", sanitizeForLog(namespace), sanitizeForLog(namespaceErr.Error()))
					namespaceCosts[namespace] = &pkgopencost.WorkloadCostResponse{Namespace: namespace, Reason: internalopencost.ConnectionFailureReason(namespaceErr), Source: "kubecost"}
				}
			}
		}
		resp := pkgopencost.BuildApplicationCostResponse(inputs, unavailable, unsupported, namespaceCosts)
		resp.Currency = currency
		resp.Source = "kubecost"
		s.writeJSON(w, resp)
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		resp := pkgopencost.UnavailableApplicationCostResponse(inputs, unavailable, unsupported, pkgopencost.ReasonNoPrometheus)
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Print("[opencost] EnsureConnected failed for application cost")
		resp := pkgopencost.UnavailableApplicationCostResponse(inputs, unavailable, unsupported, internalopencost.ConnectionFailureReason(err))
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}

	namespaceCosts := make(map[string]*pkgopencost.WorkloadCostResponse)
	for _, namespace := range applicationInputNamespaces(inputs) {
		namespaceCosts[namespace] = pkgopencost.ComputeWorkloadsFromProm(
			r.Context(), client.Prom(), namespace, internalopencost.BuildPodOwnerLookup(namespace))
	}

	resp := pkgopencost.BuildApplicationCostResponse(inputs, unavailable, unsupported, namespaceCosts)
	resp.Currency = s.resolvedOpenCostCurrency()
	resp.Source = "prometheus"
	s.writeJSON(w, resp)
}

func (s *Server) handleOpenCostApplicationTrend(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	req, inputs, unavailable, unsupported, ok := s.parseOpenCostApplicationRequest(w, r)
	if !ok {
		return
	}
	refs := make([]pkgopencost.ApplicationWorkloadRef, 0, len(inputs)+len(unsupported))
	for _, input := range inputs {
		refs = append(refs, input.ApplicationWorkloadRef)
	}
	refs = append(refs, unsupported...)
	connection, err := internalopencost.Selected(r.Context())
	if err != nil {
		resp := pkgopencost.ComputeApplicationCostTrendFromProm(r.Context(), nil, pkgopencost.ApplicationTrendOptions{
			Range: req.Range, Workloads: refs, Unavailable: unavailable, UnavailableReason: internalopencost.ConnectionFailureReason(err),
		})
		resp.Currency = s.resolvedOpenCostCurrency()
		s.writeJSON(w, resp)
		return
	}
	if connection.Source == internalopencost.SourceKubecost {
		resp := pkgopencost.ComputeApplicationCostTrendFromProm(r.Context(), nil, pkgopencost.ApplicationTrendOptions{
			Range: req.Range, Workloads: refs, Unavailable: unavailable, UnavailableReason: pkgopencost.ReasonHistoryUnsupported,
		})
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "kubecost"
		s.writeJSON(w, resp)
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		resp := pkgopencost.ComputeApplicationCostTrendFromProm(r.Context(), nil, pkgopencost.ApplicationTrendOptions{
			Range:       req.Range,
			Workloads:   refs,
			Unavailable: unavailable,
		})
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Print("[opencost] EnsureConnected failed for application trend")
		resp := pkgopencost.ComputeApplicationCostTrendFromProm(r.Context(), nil, pkgopencost.ApplicationTrendOptions{
			Range:             req.Range,
			Workloads:         refs,
			Unavailable:       unavailable,
			UnavailableReason: internalopencost.ConnectionFailureReason(err),
		})
		resp.Currency = s.resolvedOpenCostCurrency()
		resp.Source = "prometheus"
		s.writeJSON(w, resp)
		return
	}

	resp := pkgopencost.ComputeApplicationCostTrendFromProm(r.Context(), client.Prom(), pkgopencost.ApplicationTrendOptions{
		Range:       req.Range,
		Workloads:   refs,
		Unavailable: unavailable,
	})
	resp.Currency = s.resolvedOpenCostCurrency()
	resp.Source = "prometheus"
	s.writeJSON(w, resp)
}

func (s *Server) parseOpenCostApplicationRequest(w http.ResponseWriter, r *http.Request) (openCostApplicationRequest, []pkgopencost.ApplicationWorkloadCostInput, []pkgopencost.ApplicationWorkloadStatus, []pkgopencost.ApplicationWorkloadRef, bool) {
	var req openCostApplicationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid application cost request")
		return req, nil, nil, nil, false
	}
	if len(req.Workloads) == 0 {
		s.writeError(w, http.StatusBadRequest, "at least one workload is required")
		return req, nil, nil, nil, false
	}
	if len(req.Workloads) > maxOpenCostApplicationWorkloads {
		s.writeError(w, http.StatusBadRequest, "too many workloads requested")
		return req, nil, nil, nil, false
	}

	inputs := make([]pkgopencost.ApplicationWorkloadCostInput, 0, len(req.Workloads))
	unavailable := make([]pkgopencost.ApplicationWorkloadStatus, 0)
	unsupported := make([]pkgopencost.ApplicationWorkloadRef, 0)
	seen := make(map[string]bool, len(req.Workloads))
	for _, ref := range req.Workloads {
		ref.Kind = strings.TrimSpace(ref.Kind)
		ref.Namespace = strings.TrimSpace(ref.Namespace)
		ref.Name = strings.TrimSpace(ref.Name)
		if ref.Kind == "" || ref.Namespace == "" || ref.Name == "" || ref.Namespace == "_" {
			s.writeError(w, http.StatusBadRequest, "workload kind, namespace, and name are required")
			return req, nil, nil, nil, false
		}

		kind, supported := pkgopencost.CanonicalWorkloadKind(ref.Kind)
		if supported {
			ref.Kind = kind
		}
		key := ref.Namespace + "/" + ref.Kind + "/" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true

		if !supported {
			unsupported = append(unsupported, ref)
			continue
		}
		if status, _, ok := s.preflightResourceGet(r, normalizeKind(ref.Kind), ref.Namespace, ref.Name, "apps"); !ok {
			unavailable = append(unavailable, pkgopencost.ApplicationWorkloadStatus{
				ApplicationWorkloadRef: ref,
				Reason:                 openCostUnavailableReasonForHTTPStatus(status),
			})
			log.Printf("[opencost] Skipping application workload during cost preflight (status=%d)", status)
			continue
		}
		lookup := s.lookupOpenCostWorkloadResource(ref.Kind, ref.Namespace, ref.Name)
		if lookup.Status != 0 {
			unavailable = append(unavailable, pkgopencost.ApplicationWorkloadStatus{
				ApplicationWorkloadRef: ref,
				Reason:                 lookup.Reason,
			})
			continue
		}
		inputs = append(inputs, pkgopencost.ApplicationWorkloadCostInput{
			ApplicationWorkloadRef: ref,
			DesiredReplicas:        lookup.DesiredReplicas,
		})
	}
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].Namespace+"/"+inputs[i].Kind+"/"+inputs[i].Name < inputs[j].Namespace+"/"+inputs[j].Kind+"/"+inputs[j].Name
	})
	sort.Slice(unsupported, func(i, j int) bool {
		return unsupported[i].Namespace+"/"+unsupported[i].Kind+"/"+unsupported[i].Name < unsupported[j].Namespace+"/"+unsupported[j].Kind+"/"+unsupported[j].Name
	})
	sort.Slice(unavailable, func(i, j int) bool {
		return unavailable[i].Namespace+"/"+unavailable[i].Kind+"/"+unavailable[i].Name < unavailable[j].Namespace+"/"+unavailable[j].Kind+"/"+unavailable[j].Name
	})
	return req, inputs, unavailable, unsupported, true
}

func openCostUnavailableReasonForHTTPStatus(status int) string {
	if status == http.StatusForbidden {
		return pkgopencost.ReasonAccessDenied
	}
	if status == http.StatusNotFound {
		return pkgopencost.ReasonNotFound
	}
	return pkgopencost.ReasonQueryError
}

func applicationInputNamespaces(inputs []pkgopencost.ApplicationWorkloadCostInput) []string {
	seen := make(map[string]bool)
	for _, input := range inputs {
		seen[input.Namespace] = true
	}
	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}
