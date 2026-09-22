package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
)

type applicationEvidenceAPI struct {
	operationContext func() context.Context
	snapshot         func() (*rest.Config, string)
	currentContext   func() string
	resolve          func(context.Context, trace.Deps, collector.Subject) collector.CandidateSet
	collect          func(context.Context, kubernetes.Interface, *rest.Config, evidence.Adapter, collector.Target) collector.Result
}

func newApplicationEvidenceAPI() *applicationEvidenceAPI {
	return &applicationEvidenceAPI{
		operationContext: k8s.OperationContext,
		snapshot:         k8s.GetConfigSnapshot,
		currentContext:   k8s.ActiveClusterContext,
		resolve:          collector.ResolveCandidates,
		collect:          collector.SharedCollector().CollectTarget,
	}
}

type applicationEvidenceCandidatesResponse struct {
	Enabled                 bool   `json:"enabled"`
	Context                 string `json:"context,omitempty"`
	PermissionCheckTimedOut bool   `json:"permissionCheckTimedOut,omitempty"`
	collector.CandidateSet
}

type applicationEvidenceRequest struct {
	Application          evidence.Adapter `json:"application"`
	Namespace            string           `json:"namespace"`
	Pod                  string           `json:"pod"`
	UID                  string           `json:"uid"`
	Context              string           `json:"context"`
	ConfirmNetworkAccess bool             `json:"confirmNetworkAccess"`
}

func (s *Server) applicationEvidenceEnabled(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() &&
		deploymentMode() == k8s.DeploymentModeLocal && !s.authConfig.Enabled() &&
		!s.cloudConnectCfg.CloudTunnelConfigured && !cloud.IsAuthenticatedTunnelRequest(r.Context()) &&
		auth.UserFromContext(r.Context()) == nil
}

func (s *Server) handleApplicationEvidenceCandidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response := applicationEvidenceCandidatesResponse{CandidateSet: collector.CandidateSet{Candidates: []collector.Candidate{}}}
	if !s.applicationEvidenceEnabled(r) {
		s.writeJSON(w, response)
		return
	}
	if !s.requireConnected(w) {
		return
	}
	q := r.URL.Query()
	if len(validation.IsDNS1123Label(q.Get("namespace"))) != 0 || len(validation.IsDNS1123Subdomain(q.Get("name"))) != 0 || q.Get("kind") == "" {
		s.writeError(w, http.StatusBadRequest, "a resource kind, namespace and name are required")
		return
	}
	namespaces := s.traceNamespaceCeiling(r)
	if noNamespaceAccess(namespaces) || !namespaceAllowed(namespaces, q.Get("namespace")) {
		s.writeError(w, http.StatusForbidden, "no access to the selected namespace")
		return
	}
	api := s.applicationEvidence
	operation := api.operationContext()
	if k8s.ContextOperationInProgress() {
		s.writeError(w, http.StatusConflict, "cluster context is changing; retry when the connection is ready")
		return
	}
	collectionCtx, cancelCollection := context.WithCancel(r.Context())
	defer cancelCollection()
	stop := context.AfterFunc(operation, cancelCollection)
	defer stop()
	config, capturedContext := api.snapshot()
	if config == nil || capturedContext == "" {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes connection unavailable")
		return
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes connection unavailable")
		return
	}
	kind, group := strings.TrimSpace(q.Get("kind")), strings.TrimSpace(q.Get("group"))
	if gvr, ok := k8s.BuiltinGVR(kind, group); ok {
		kind, _ = k8s.BuiltinKindForResource(gvr.Resource)
		group = gvr.Group
	} else if group == "" {
		if gvr, ok := k8s.BuiltinGVRAnyGroup(kind); ok {
			kind, _ = k8s.BuiltinKindForResource(gvr.Resource)
			group = gvr.Group
		}
	} else if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		if resource, ok := discovery.GetResourceWithGroup(kind, group); ok {
			kind = resource.Kind
		}
	}
	response.CandidateSet = api.resolve(collectionCtx, trace.Deps{
		Cache: k8s.GetResourceCache(), Dynamic: k8s.GetDynamicResourceCache(), Discovery: k8s.GetResourceDiscovery(), AllowedNamespaces: namespaces,
	}, collector.Subject{Group: group, Kind: kind, Namespace: q.Get("namespace"), Name: q.Get("name")})
	permissionBudget := 500 * time.Millisecond
	if q.Get("retryPermissions") == "true" {
		permissionBudget = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(collectionCtx, permissionBudget)
	defer cancel()
	allowedTargets := make([]bool, len(response.Candidates))
	var checks sync.WaitGroup
	for i, candidate := range response.Candidates {
		checks.Add(1)
		go func() {
			defer checks.Done()
			allowedTargets[i] = collector.CheckAccess(ctx, client, candidate.Target)
		}()
	}
	checks.Wait()
	response.PermissionCheckTimedOut = ctx.Err() == context.DeadlineExceeded
	allowed := make([]collector.Candidate, 0, len(response.Candidates))
	for i, candidate := range response.Candidates {
		if allowedTargets[i] {
			allowed = append(allowed, candidate)
		} else {
			response.CoverageLimited = true
		}
	}
	response.Candidates = allowed
	if operation.Err() != nil || api.currentContext() != capturedContext {
		s.writeError(w, http.StatusConflict, "cluster context changed; reopen the resource")
		return
	}
	response.Enabled = true
	response.Context = capturedContext
	s.writeJSON(w, response)
}

func (s *Server) handleCollectApplicationEvidence(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.applicationEvidenceEnabled(r) {
		s.writeError(w, http.StatusNotFound, "application evidence collection is not available on this deployment")
		return
	}
	if !s.sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	var input applicationEvidenceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid application evidence request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "expected one application evidence request")
		return
	}
	if !input.ConfirmNetworkAccess || input.UID == "" || input.Context == "" ||
		len(validation.IsDNS1123Label(input.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(input.Pod)) != 0 {
		s.writeError(w, http.StatusBadRequest, "explicit collection authorization, current context and selected Pod identity are required")
		return
	}
	switch input.Application {
	case evidence.RabbitMQ, evidence.NATS, evidence.Vault:
	default:
		s.writeError(w, http.StatusBadRequest, "unsupported application")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.traceNamespaceCeiling(r)
	if noNamespaceAccess(namespaces) || !namespaceAllowed(namespaces, input.Namespace) {
		s.writeError(w, http.StatusForbidden, "no access to the selected namespace")
		return
	}
	api := s.applicationEvidence
	operation := api.operationContext()
	if k8s.ContextOperationInProgress() {
		s.writeError(w, http.StatusConflict, "cluster context is changing; retry when the connection is ready")
		return
	}
	collectionCtx, cancelCollection := context.WithCancel(r.Context())
	defer cancelCollection()
	stop := context.AfterFunc(operation, cancelCollection)
	defer stop()
	config, capturedContext := api.snapshot()
	if operation.Err() != nil || capturedContext != input.Context || api.currentContext() != input.Context {
		s.writeError(w, http.StatusConflict, "cluster context changed; reopen the resource before collecting evidence")
		return
	}
	if config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes connection unavailable")
		return
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes connection unavailable")
		return
	}
	result := api.collect(collectionCtx, client, config, input.Application, collector.Target{Namespace: input.Namespace, Pod: input.Pod, UID: input.UID})
	if operation.Err() != nil || api.currentContext() != capturedContext {
		s.writeError(w, http.StatusConflict, "cluster context changed; collected evidence was discarded")
		return
	}
	s.writeJSON(w, result)
}
