package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Pooler detail and pause/resume. spec.pgbouncer.paused is desired state: the
// pooler's instance manager applies it to each PgBouncer with PAUSE/RESUME.
// Whether a PgBouncer is paused is observed separately (SHOW STATE over the
// caller's pods/exec); the exporter does not publish it.

// CNPGPoolerDeploymentFact is the Deployment the Pooler runs, which is where
// readiness lives; the Pooler's own status only counts scheduled Pods.
// State: ok | missing | unreadable | foreign (a same-named Deployment the
// Pooler does not control).
type CNPGPoolerDeploymentFact struct {
	Name              string `json:"name"`
	State             string `json:"state"`
	Replicas          *int32 `json:"replicas,omitempty"`
	ReadyReplicas     *int32 `json:"readyReplicas,omitempty"`
	UpdatedReplicas   *int32 `json:"updatedReplicas,omitempty"`
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`
}

// CNPGPoolerServiceFact is the Service clients connect to.
type CNPGPoolerServiceFact struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Type  string `json:"type,omitempty"`
	Port  *int32 `json:"port,omitempty"`
}

type CNPGPoolerFacts struct {
	Generation  int64                    `json:"generation"`
	Cluster     string                   `json:"cluster"`
	Type        string                   `json:"type"`
	Instances   *int64                   `json:"instances,omitempty"`
	Paused      bool                     `json:"paused"`
	PoolMode    string                   `json:"poolMode,omitempty"`
	Parameters  map[string]string        `json:"parameters"`
	Terminating bool                     `json:"terminating"`
	Deployment  CNPGPoolerDeploymentFact `json:"deployment"`
	Service     CNPGPoolerServiceFact    `json:"service"`
}

type CNPGPoolerActions struct {
	Pause  ActionCapability `json:"pause"`
	Resume ActionCapability `json:"resume"`
	// ObserveState is reading each PgBouncer's paused state (pods/exec).
	ObserveState ActionCapability `json:"observeState"`
}

// CNPGPoolerCapabilitiesResponse is GET /api/cnpg/poolers/{ns}/{name}/capabilities.
type CNPGPoolerCapabilitiesResponse struct {
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Context         string            `json:"context"`
	Facts           CNPGPoolerFacts   `json:"facts"`
	Actions         CNPGPoolerActions `json:"actions"`
}

func cnpgPoolerFactsOf(ctx context.Context, c cnpgActionClients, pooler *unstructured.Unstructured) CNPGPoolerFacts {
	str := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(pooler.Object, fields...)
		return v
	}
	paused, _, _ := unstructured.NestedBool(pooler.Object, "spec", "pgbouncer", "paused")
	params, _, _ := unstructured.NestedStringMap(pooler.Object, "spec", "pgbouncer", "parameters")
	if params == nil {
		params = map[string]string{}
	}
	f := CNPGPoolerFacts{
		Generation:  pooler.GetGeneration(),
		Cluster:     str("spec", "cluster", "name"),
		Type:        str("spec", "type"),
		Paused:      paused,
		PoolMode:    str("spec", "pgbouncer", "poolMode"),
		Parameters:  params,
		Terminating: !pooler.GetDeletionTimestamp().IsZero(),
		Deployment:  CNPGPoolerDeploymentFact{Name: pooler.GetName()},
		Service:     CNPGPoolerServiceFact{Name: pooler.GetName()},
	}
	if n, ok, _ := unstructured.NestedInt64(pooler.Object, "spec", "instances"); ok {
		f.Instances = &n
	}
	if c.typed == nil {
		f.Deployment.State, f.Service.State = "unreadable", "unreadable"
		return f
	}
	ns, name, uid := pooler.GetNamespace(), pooler.GetName(), pooler.GetUID()
	switch d, err := c.typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); {
	case apierrors.IsNotFound(err):
		f.Deployment.State = "missing"
	case err != nil:
		f.Deployment.State = "unreadable"
	case !cnpgControlledBy(d.OwnerReferences, cnpgGroup, "Pooler", name, uid):
		f.Deployment.State = "foreign"
	default:
		f.Deployment.State = "ok"
		f.Deployment.Replicas = d.Spec.Replicas
		ready, updated, available := d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas
		f.Deployment.ReadyReplicas, f.Deployment.UpdatedReplicas, f.Deployment.AvailableReplicas = &ready, &updated, &available
	}
	switch svc, err := c.typed.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); {
	case apierrors.IsNotFound(err):
		f.Service.State = "missing"
	case err != nil:
		f.Service.State = "unreadable"
	case !cnpgControlledBy(svc.OwnerReferences, cnpgGroup, "Pooler", name, uid):
		f.Service.State = "foreign"
	default:
		f.Service.State = "ok"
		f.Service.Type = string(svc.Spec.Type)
		if len(svc.Spec.Ports) > 0 {
			port := svc.Spec.Ports[0].Port
			f.Service.Port = &port
		}
	}
	return f
}

func cnpgGuardPooler(f CNPGPoolerFacts, pause bool) string {
	switch {
	case f.Terminating:
		return "The Pooler is being deleted"
	case pause && f.Paused:
		return "The Pooler is paused already"
	case !pause && !f.Paused:
		return "The Pooler is not paused"
	}
	return ""
}

func (s *Server) handleCNPGPoolerCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	typed := s.getClientForRequest(r)
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := s.cnpgPoolerCapabilities(r, cnpgActionClients{dyn: dyn, typed: typed}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) cnpgPoolerCapabilities(r *http.Request, c cnpgActionClients, contextName, namespace, name string) (*CNPGPoolerCapabilitiesResponse, error) {
	pooler, err := c.dyn.Resource(cnpgPoolerGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	f := cnpgPoolerFactsOf(r.Context(), c, pooler)
	one := func(guard string, g Grant) ActionCapability {
		g = g.In(namespace)
		return capabilityVerdict(guard, []string{s.grantPermission(r, g)}, []Grant{g})
	}
	return &CNPGPoolerCapabilitiesResponse{
		UID:             string(pooler.GetUID()),
		ResourceVersion: pooler.GetResourceVersion(),
		Context:         contextName,
		Facts:           f,
		Actions: CNPGPoolerActions{
			Pause:        one(cnpgGuardPooler(f, true), cnpgGrantPatchPool),
			Resume:       one(cnpgGuardPooler(f, false), cnpgGrantPatchPool),
			ObserveState: one("", cnpgGrantCreateExec),
		},
	}, nil
}

type cnpgPoolerReviewed struct {
	Paused *bool `json:"paused"`
}

func (s *Server) handleCNPGPoolerAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name, action := chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "action")
	if action != "pause" && action != "resume" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown Pooler action %q: must be pause or resume", action))
		return
	}
	req, dyn, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	auth.AuditLog(r, namespace, name)
	errAction := "pooler" + strings.ToUpper(action[:1]) + action[1:]
	res, err := runCNPGPoolerAction(r.Context(), cnpgActionClients{dyn: dyn, typed: s.getClientForRequest(r)}, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, errAction, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on Pooler %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

func runCNPGPoolerAction(ctx context.Context, c cnpgActionClients, namespace, name, action string, req ActionRequest) (*CNPGActionResult, error) {
	var reviewed cnpgPoolerReviewed
	if len(req.Facts) > 0 {
		if err := json.Unmarshal(req.Facts, &reviewed); err != nil {
			return nil, refuseAction(http.StatusBadRequest, "", "facts: %v", err)
		}
	}
	if reviewed.Paused == nil {
		return nil, refuseAction(http.StatusBadRequest, "", "facts.paused is required for %s: the confirmation must bind what the dialog showed", action)
	}
	if err := decodeActionParams(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	pooler, err := c.dyn.Resource(cnpgPoolerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts := cnpgPoolerFactsOf(ctx, cnpgActionClients{dyn: c.dyn}, pooler)
	if string(pooler.GetUID()) != req.UID {
		return nil, changedAction(facts, "Pooler %s/%s was deleted and recreated since you reviewed it", namespace, name)
	}
	if *reviewed.Paused != facts.Paused {
		return nil, changedAction(facts, "Pooler %s/%s changed since you confirmed (paused); review the action again", namespace, name)
	}
	pause := action == "pause"
	if r := cnpgGuardPooler(facts, pause); r != "" {
		return nil, blockedAction(r)
	}
	err = mergePatchAtVersion(ctx, c.dyn, cnpgPoolerGVR, pooler, map[string]any{"spec": map[string]any{"pgbouncer": map[string]any{"paused": pause}}})
	if apierrors.IsConflict(err) {
		if fresh, gerr := c.dyn.Resource(cnpgPoolerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
			facts = cnpgPoolerFactsOf(ctx, cnpgActionClients{dyn: c.dyn}, fresh)
		}
		return nil, changedAction(facts, "Pooler %s/%s changed while the request was being sent; review the action again", namespace, name)
	}
	if err != nil {
		return nil, err
	}
	msg := "Pause requested: each PgBouncer finishes its running transactions, then holds new queries"
	if !pause {
		msg = "Resume requested: each PgBouncer serves queued clients again"
	}
	return &CNPGActionResult{
		Action:  action,
		Message: msg,
		Target:  &CNPGActionTarget{Paused: &pause, Generation: pooler.GetGeneration() + 1},
	}, nil
}

// ---------- observed PgBouncer state ----------

// CNPGPgBouncerState is one pooler Pod's SHOW STATE. Facts only when read.
type CNPGPgBouncerState struct {
	Pod string `json:"pod"`
	CNPGRuntimeSource
	Paused    *bool `json:"paused,omitempty"`
	Suspended *bool `json:"suspended,omitempty"`
	Active    *bool `json:"active,omitempty"`
}

// CNPGPgBouncerStateResponse is GET /api/cnpg/poolers/{ns}/{name}/pgbouncer-state.
type CNPGPgBouncerStateResponse struct {
	Pooler     CNPGRuntimeObjectRef `json:"pooler"`
	SampledAt  string               `json:"sampledAt"`
	Permission CNPGExecPermission   `json:"permission"`
	Pods       []CNPGPgBouncerState `json:"pods"`
}

// PgBouncer's admin console on its unix socket; the container's PGHOST,
// PGUSER and PGDATABASE already point there.
var cnpgShowStateArgv = []string{"psql", "-XAtq", "-c", "SHOW STATE"}

func parseCNPGShowState(out []byte) (CNPGPgBouncerState, error) {
	var st CNPGPgBouncerState
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		b := v == "yes"
		switch k {
		case "paused":
			st.Paused = &b
		case "suspended":
			st.Suspended = &b
		case "active":
			st.Active = &b
		}
	}
	if st.Paused == nil {
		return st, fmt.Errorf("SHOW STATE did not report paused")
	}
	return st, nil
}

func (s *Server) handleCNPGPgBouncerState(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "poolers") {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	pooler, err := findCNPGPooler(r.Context(), cache, namespace, name)
	if err != nil || pooler == nil {
		s.writeError(w, http.StatusNotFound, "CloudNativePG Pooler "+namespace+"/"+name+" not found")
		return
	}
	pods, err := cnpgPoolerPods(cache, pooler)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "pooler Pods unavailable: "+err.Error())
		return
	}
	resp := CNPGPgBouncerStateResponse{
		Pooler:     CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: pooler.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: s.grantPermission(r, cnpgGrantCreateExec.In(namespace)), Grant: cnpgGrantCreateExec.In(namespace).Ref()},
		Pods:       make([]CNPGPgBouncerState, len(pods)),
	}
	for i, p := range pods {
		resp.Pods[i].Pod = p.Name
	}
	if resp.Permission.Exec == permissionDenied {
		for i := range resp.Pods {
			resp.Pods[i].CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgExecStateDenied, Error: "reading PgBouncer state needs " + grantText(resp.Permission.Grant)}
		}
		s.writeJSON(w, resp)
		return
	}
	exec := s.cnpgExecFor(r)
	if exec == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	run := newCNPGRuntimeRunner(r.Context())
	for i, p := range pods {
		out := &resp.Pods[i]
		pod := p.Name
		run.do(func(ctx context.Context) {
			*out = readCNPGPgBouncerState(ctx, exec, namespace, pod)
		})
	}
	run.wait()
	for _, p := range resp.Pods {
		if p.State == cnpgExecStateDenied {
			resp.Permission.Exec = permissionDenied
		}
	}
	s.writeJSON(w, resp)
}

func readCNPGPgBouncerState(ctx context.Context, exec cnpgExecFunc, namespace, pod string) CNPGPgBouncerState {
	captured := time.Now().UTC().Format(time.RFC3339)
	out, err := exec(ctx, namespace, pod, cnpgPgBouncerContainer, cnpgShowStateArgv, "")
	if err != nil {
		src := cnpgExecSourceState(err)
		src.CapturedAt = captured
		return CNPGPgBouncerState{Pod: pod, CNPGRuntimeSource: src}
	}
	st, err := parseCNPGShowState(out)
	st.Pod = pod
	if err != nil {
		st.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: err.Error(), CapturedAt: captured}
		return st
	}
	st.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateOK, CapturedAt: captured}
	return st
}
