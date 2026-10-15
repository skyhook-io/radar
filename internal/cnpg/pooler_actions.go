package cnpg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

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
	Pause  integration.ActionCapability `json:"pause"`
	Resume integration.ActionCapability `json:"resume"`
	// ObserveState is reading each PgBouncer's paused state (pods/exec).
	ObserveState integration.ActionCapability `json:"observeState"`
}

// CNPGPoolerCapabilitiesResponse is GET /api/cnpg/poolers/{ns}/{name}/capabilities.
type CNPGPoolerCapabilitiesResponse struct {
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Context         string            `json:"context"`
	Facts           CNPGPoolerFacts   `json:"facts"`
	Actions         CNPGPoolerActions `json:"actions"`
}

func cnpgPoolerFactsOf(ctx context.Context, c ActionClients, pooler *unstructured.Unstructured) CNPGPoolerFacts {
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
	if c.Typed == nil {
		f.Deployment.State, f.Service.State = "unreadable", "unreadable"
		return f
	}
	ns, name, uid := pooler.GetNamespace(), pooler.GetName(), pooler.GetUID()
	switch d, err := c.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); {
	case apierrors.IsNotFound(err):
		f.Deployment.State = "missing"
	case err != nil:
		f.Deployment.State = "unreadable"
	case !controlledBy(d.OwnerReferences, Group, "Pooler", name, uid):
		f.Deployment.State = "foreign"
	default:
		f.Deployment.State = "ok"
		f.Deployment.Replicas = d.Spec.Replicas
		ready, updated, available := d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas
		f.Deployment.ReadyReplicas, f.Deployment.UpdatedReplicas, f.Deployment.AvailableReplicas = &ready, &updated, &available
	}
	switch svc, err := c.Typed.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); {
	case apierrors.IsNotFound(err):
		f.Service.State = "missing"
	case err != nil:
		f.Service.State = "unreadable"
	case !controlledBy(svc.OwnerReferences, Group, "Pooler", name, uid):
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

func (s *Reader) PoolerCapabilities(ctx context.Context, c ActionClients, contextName, namespace, name string) (*CNPGPoolerCapabilitiesResponse, error) {
	pooler, err := c.Dynamic.Resource(cnpgPoolerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	f := cnpgPoolerFactsOf(ctx, c, pooler)
	one := func(guard string, g auth.Grant) integration.ActionCapability {
		g = g.In(namespace)
		return integration.CapabilityVerdict(guard, []string{s.Access.Permission(ctx, g)}, []auth.Grant{g})
	}
	return &CNPGPoolerCapabilitiesResponse{
		UID:             string(pooler.GetUID()),
		ResourceVersion: pooler.GetResourceVersion(),
		Context:         contextName,
		Facts:           f,
		Actions: CNPGPoolerActions{
			Pause:        one(cnpgGuardPooler(f, true), cnpgGrantPatchPool),
			Resume:       one(cnpgGuardPooler(f, false), cnpgGrantPatchPool),
			ObserveState: one("", grantCreateExec),
		},
	}, nil
}

type cnpgPoolerReviewed struct {
	Paused *bool `json:"paused"`
}

func RunCNPGPoolerAction(ctx context.Context, c ActionClients, namespace, name, action string, req integration.ActionRequest) (*CNPGActionResult, error) {
	var reviewed cnpgPoolerReviewed
	if len(req.Facts) > 0 {
		if err := json.Unmarshal(req.Facts, &reviewed); err != nil {
			return nil, integration.RefuseAction(http.StatusBadRequest, "", "facts: %v", err)
		}
	}
	if reviewed.Paused == nil {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "facts.paused is required for %s: the confirmation must bind what the dialog showed", action)
	}
	if err := integration.DecodeActionParams(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	pooler, err := c.Dynamic.Resource(cnpgPoolerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts := cnpgPoolerFactsOf(ctx, ActionClients{Dynamic: c.Dynamic}, pooler)
	if string(pooler.GetUID()) != req.UID {
		return nil, integration.ChangedAction(facts, "Pooler %s/%s was deleted and recreated since you reviewed it", namespace, name)
	}
	if *reviewed.Paused != facts.Paused {
		return nil, integration.ChangedAction(facts, "Pooler %s/%s changed since you confirmed (paused); review the action again", namespace, name)
	}
	pause := action == "pause"
	if r := cnpgGuardPooler(facts, pause); r != "" {
		return nil, integration.BlockedAction(r)
	}
	err = integration.MergePatchAtVersion(ctx, c.Dynamic, cnpgPoolerGVR, pooler, map[string]any{"spec": map[string]any{"pgbouncer": map[string]any{"paused": pause}}})
	if apierrors.IsConflict(err) {
		if fresh, gerr := c.Dynamic.Resource(cnpgPoolerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
			facts = cnpgPoolerFactsOf(ctx, ActionClients{Dynamic: c.Dynamic}, fresh)
		}
		return nil, integration.ChangedAction(facts, "Pooler %s/%s changed while the request was being sent; review the action again", namespace, name)
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

func readCNPGPgBouncerState(ctx context.Context, exec ExecFunc, namespace string, pod *corev1.Pod) CNPGPgBouncerState {
	captured := time.Now().UTC().Format(time.RFC3339)
	out, err := exec(ctx, namespace, pod.Name, pgBouncerContainer, cnpgShowStateArgv, "")
	if err != nil {
		src := cnpgExecSourceState(err)
		src.CapturedAt = captured
		if reason := poolerNotStarted(pod); reason != "" && src.State != execStateDenied {
			src.State, src.Error = runtimeStateUnreachable, reason
		}
		return CNPGPgBouncerState{Pod: pod.Name, CNPGRuntimeSource: src}
	}
	st, err := parseCNPGShowState(out)
	st.Pod = pod.Name
	if err != nil {
		st.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateError, Error: err.Error(), CapturedAt: captured}
		return st
	}
	st.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateOK, CapturedAt: captured}
	return st
}

func (s *Reader) PgBouncerState(ctx context.Context, cache *k8s.ResourceCache, pooler *unstructured.Unstructured) (*CNPGPgBouncerStateResponse, error) {
	namespace, name := pooler.GetNamespace(), pooler.GetName()
	pods, err := poolerPods(cache, pooler)
	if err != nil {
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "pooler Pods unavailable: " + err.Error()}
	}
	resp := CNPGPgBouncerStateResponse{
		Pooler:     CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: pooler.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: s.Access.Permission(ctx, grantCreateExec.In(namespace)), Grant: grantCreateExec.In(namespace).Ref()},
		Pods:       make([]CNPGPgBouncerState, len(pods)),
	}
	for i, p := range pods {
		resp.Pods[i].Pod = p.Name
	}
	if resp.Permission.Exec == integration.PermissionDenied {
		for i := range resp.Pods {
			resp.Pods[i].CNPGRuntimeSource = CNPGRuntimeSource{State: execStateDenied, Error: "reading PgBouncer state needs " + integration.GrantText(resp.Permission.Grant)}
		}
		return &resp, nil
	}
	exec := s.Clients.Exec
	if exec == nil {
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "cluster client not available — check cluster connection"}
	}
	run := newCNPGRuntimeRunner(ctx)
	for i, p := range pods {
		out := &resp.Pods[i]
		run.do(func(ctx context.Context) {
			*out = readCNPGPgBouncerState(ctx, exec, namespace, p)
		})
	}
	run.wait()
	for _, p := range resp.Pods {
		if p.State == execStateDenied {
			resp.Permission.Exec = integration.PermissionDenied
		}
	}
	return &resp, nil
}
