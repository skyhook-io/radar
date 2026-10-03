package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Diagnosis inside PostgreSQL. Radar runs fixed SQL with psql in the
// instance's postgres container through the caller's own pods/exec — the same
// access that lets them open psql themselves, which is why query text is
// shown to them. Nothing a caller sends is ever part of the SQL text: the only
// inputs (a backend's pid and start time) are validated and passed as psql
// variables, which psql quotes as literals.

const (
	cnpgExecTimeout       = 10 * time.Second
	cnpgExecStdoutCap     = 1 << 20
	cnpgExecStderrCap     = 8 << 10
	cnpgExecConcurrency   = 4
	cnpgSessionsMaxRows   = 200
	cnpgQueryTextChars    = 200
	cnpgDiagnosticsApp    = "radar-diagnostics"
	cnpgExecStateDenied   = cnpgRuntimeStateDenied
	cnpgPsqlDatabase      = "postgres"
	cnpgSignalCancel      = "cancel"
	cnpgSignalTerminate   = "terminate"
	cnpgSQLTimeoutPrelude = "SET statement_timeout = '5s';\n"
)

var cnpgGrantCreateExec = Grant{Verb: "create", Resource: "pods", Subresource: "exec"}

// cnpgExecSlots bounds concurrent execs across all callers: each one holds a
// streaming connection to a kubelet.
var cnpgExecSlots = make(chan struct{}, cnpgExecConcurrency)

// cnpgExecFunc runs argv in a container with stdin and returns stdout. The
// error carries the command's stderr.
type cnpgExecFunc func(ctx context.Context, namespace, pod, container string, argv []string, stdin string) ([]byte, error)

var errCNPGExecOutputTooLarge = errors.New("output exceeded the size limit")

type cnpgCappedBuffer struct {
	buf      []byte
	limit    int
	overflow bool
}

// Write never fails: refusing bytes would break the stream mid-command, so
// overflow is recorded and reported once the command ends.
func (b *cnpgCappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - len(b.buf)
	if room < len(p) {
		b.overflow = true
		if room > 0 {
			b.buf = append(b.buf, p[:room]...)
		}
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// cnpgExecFor returns the caller's exec: the impersonated identity when auth
// is on, nil when no client can be built (never Radar's own identity).
func (s *Server) cnpgExecFor(r *http.Request) cnpgExecFunc {
	client := s.getClientForRequest(r)
	cfg := s.getConfigForRequest(r)
	if client == nil || cfg == nil {
		return nil
	}
	return func(ctx context.Context, namespace, pod, container string, argv []string, stdin string) ([]byte, error) {
		select {
		case cnpgExecSlots <- struct{}{}:
			defer func() { <-cnpgExecSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(ctx, cnpgExecTimeout)
		defer cancel()
		ex, err := k8score.NewPodExecExecutor(client, cfg, namespace, pod, container, argv, false)
		if err != nil {
			return nil, err
		}
		out := &cnpgCappedBuffer{limit: cnpgExecStdoutCap}
		errOut := &cnpgCappedBuffer{limit: cnpgExecStderrCap}
		err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: strings.NewReader(stdin), Stdout: out, Stderr: errOut})
		if err != nil {
			if msg := strings.TrimSpace(string(errOut.buf)); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, truncateCNPGRuntimeError(msg))
			}
			return nil, err
		}
		if out.overflow {
			return nil, errCNPGExecOutputTooLarge
		}
		return out.buf, nil
	}
}

// cnpgExecSourceState classifies an exec failure the way the runtime reads
// classify proxy failures.
func cnpgExecSourceState(err error) CNPGRuntimeSource {
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "forbidden") {
		return CNPGRuntimeSource{State: cnpgExecStateDenied, Error: truncateCNPGRuntimeError(msg)}
	}
	// psql's own "connection refused" from PostgreSQL's socket is not a
	// transport failure; read it before the transport hints.
	if postgres, ok := cnpgPostgresSentence(msg); ok {
		log.Printf("[cnpg] Exec failed: %v", err)
		return CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: postgres}
	}
	text := truncateCNPGRuntimeError(msg)
	plain, transport := cnpgTransportSentence(err, 0, cnpgExecTimeout)
	if transport {
		log.Printf("[cnpg] Exec failed: %v", err)
		text = plain
	}
	switch {
	case transport, errors.Is(err, context.DeadlineExceeded), strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"),
		strings.Contains(lower, "container not found"), strings.Contains(lower, "unable to upgrade connection"):
		return CNPGRuntimeSource{State: cnpgRuntimeStateUnreachable, Error: text}
	default:
		return CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: text}
	}
}

// ---------- sessions (blocking view) ----------

type CNPGExecPermission struct {
	Exec  string `json:"exec"`
	Grant string `json:"grant"`
}

// CNPGSessionsResponse is GET /api/cnpg/clusters/{ns}/{name}/sessions.
// Facts are present only when the read succeeded.
type CNPGSessionsResponse struct {
	Cluster    CNPGRuntimeObjectRef `json:"cluster"`
	Pod        string               `json:"pod"`
	PodUID     types.UID            `json:"podUID"`
	Role       string               `json:"role"`
	SampledAt  string               `json:"sampledAt"`
	Permission CNPGExecPermission   `json:"permission"`
	CNPGRuntimeSource
	*CNPGSessionFacts
	Instances []CNPGSessionInstance `json:"instances"`
}

// CNPGSessionFacts: Sessions are only the backends in a blocking relation
// (blocking another, or waiting on one), oldest transaction first, capped at
// cnpgSessionsMaxRows; InvolvedTotal counts all of them.
type CNPGSessionFacts struct {
	ServerTime                   string        `json:"serverTime"`
	MaxConnections               int           `json:"maxConnections"`
	SuperuserReservedConnections int           `json:"superuserReservedConnections"`
	ClientBackends               int           `json:"clientBackends"`
	InvolvedTotal                int           `json:"involvedTotal"`
	Truncated                    bool          `json:"truncated"`
	Sessions                     []CNPGBackend `json:"sessions"`
}

// CNPGBackend is one pg_stat_activity row. BackendStart is PostgreSQL's own
// rendering with microseconds; a cancel or terminate must echo it verbatim.
type CNPGBackend struct {
	PID               int      `json:"pid"`
	BlockedBy         []int    `json:"blockedBy"`
	BackendStart      string   `json:"backendStart"`
	State             string   `json:"state,omitempty"`
	WaitEventType     string   `json:"waitEventType,omitempty"`
	WaitEvent         string   `json:"waitEvent,omitempty"`
	User              string   `json:"user,omitempty"`
	Database          string   `json:"database,omitempty"`
	Application       string   `json:"application,omitempty"`
	ClientAddr        string   `json:"clientAddr,omitempty"`
	BackendType       string   `json:"backendType,omitempty"`
	BackendAgeSeconds *float64 `json:"backendAgeSeconds,omitempty"`
	XactAgeSeconds    *float64 `json:"xactAgeSeconds,omitempty"`
	QueryAgeSeconds   *float64 `json:"queryAgeSeconds,omitempty"`
	StateAgeSeconds   *float64 `json:"stateAgeSeconds,omitempty"`
	Query             string   `json:"query,omitempty"`
	QueryTruncated    bool     `json:"queryTruncated,omitempty"`
}

// CNPGSessionInstance carries the postgres container's declared resources, so
// usage (read separately from metrics-server) can be read against them.
type CNPGSessionInstance struct {
	Pod           string `json:"pod"`
	Role          string `json:"role"`
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

var cnpgPsqlArgv = []string{"psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-d", cnpgPsqlDatabase, "-f", "-"}

var cnpgBlockingSQL = cnpgSQLTimeoutPrelude + `SET lock_timeout = '1s';
SET application_name = '` + cnpgDiagnosticsApp + `';
WITH a AS (
  SELECT pid, pg_blocking_pids(pid) AS blocked_by, backend_start, xact_start, query_start, state_change,
         state, wait_event_type, wait_event, usename, datname, application_name,
         host(client_addr) AS client_addr, backend_type, query
  FROM pg_stat_activity
  WHERE pid <> pg_backend_pid()
),
involved AS (
  SELECT * FROM a
  WHERE cardinality(blocked_by) > 0
     OR pid IN (SELECT unnest(blocked_by) FROM a)
)
SELECT json_build_object(
  'serverTime', now(),
  'maxConnections', current_setting('max_connections')::int,
  'superuserReservedConnections', current_setting('superuser_reserved_connections')::int,
  'clientBackends', (SELECT count(*) FROM a WHERE backend_type = 'client backend'),
  'involvedTotal', (SELECT count(*) FROM involved),
  'sessions', coalesce((
    SELECT json_agg(row_to_json(s)) FROM (
      SELECT pid, blocked_by AS "blockedBy", backend_start AS "backendStart",
             state, wait_event_type AS "waitEventType", wait_event AS "waitEvent",
             usename AS "user", datname AS "database", application_name AS "application",
             client_addr AS "clientAddr", backend_type AS "backendType",
             extract(epoch FROM now() - backend_start) AS "backendAgeSeconds",
             extract(epoch FROM now() - xact_start) AS "xactAgeSeconds",
             extract(epoch FROM now() - query_start) AS "queryAgeSeconds",
             extract(epoch FROM now() - state_change) AS "stateAgeSeconds",
             left(query, ` + strconv.Itoa(cnpgQueryTextChars) + `) AS query,
             length(query) > ` + strconv.Itoa(cnpgQueryTextChars) + ` AS "queryTruncated"
      FROM involved
      ORDER BY xact_start NULLS LAST, pid
      LIMIT ` + strconv.Itoa(cnpgSessionsMaxRows) + `
    ) s
  ), '[]'::json)
);
`

func parseCNPGSessions(out []byte) (*CNPGSessionFacts, error) {
	var f CNPGSessionFacts
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &f); err != nil {
		return nil, fmt.Errorf("unexpected psql output: %w", err)
	}
	if f.Sessions == nil {
		f.Sessions = []CNPGBackend{}
	}
	for i := range f.Sessions {
		if f.Sessions[i].BlockedBy == nil {
			f.Sessions[i].BlockedBy = []int{}
		}
	}
	f.Truncated = f.InvolvedTotal > len(f.Sessions)
	return &f, nil
}

func cnpgSessionInstanceOf(p *corev1.Pod) CNPGSessionInstance {
	out := CNPGSessionInstance{Pod: p.Name, Role: cnpgRuntimeRole(p)}
	for _, c := range p.Spec.Containers {
		if c.Name != cnpgDefaultLogContainer {
			continue
		}
		q := func(l corev1.ResourceList, n corev1.ResourceName) string {
			if v, ok := l[n]; ok {
				return v.String()
			}
			return ""
		}
		out.CPURequest, out.CPULimit = q(c.Resources.Requests, corev1.ResourceCPU), q(c.Resources.Limits, corev1.ResourceCPU)
		out.MemoryRequest, out.MemoryLimit = q(c.Resources.Requests, corev1.ResourceMemory), q(c.Resources.Limits, corev1.ResourceMemory)
	}
	return out
}

// handleCNPGClusterSessions serves GET /api/cnpg/clusters/{ns}/{name}/sessions:
// the blocker → victim relations on one instance (the primary unless ?pod=
// names another instance), read with fixed SQL over the caller's pods/exec.
// Without exec the answer is a 200 whose state is denied, never empty.
func (s *Server) handleCNPGClusterSessions(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "clusters") {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	cluster, ok := s.loadCNPGCluster(w, r, cache, namespace, name)
	if !ok {
		return
	}
	pods, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}
	want := r.URL.Query().Get("pod")
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if want == "" {
		want = primary
	}
	resp := CNPGSessionsResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		Pod:        want,
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: permissionAllowed, Grant: cnpgGrantCreateExec.In(namespace).String()},
		Instances:  make([]CNPGSessionInstance, 0, len(pods)),
	}
	var target *corev1.Pod
	for _, p := range pods {
		resp.Instances = append(resp.Instances, cnpgSessionInstanceOf(p))
		if p.Name == want {
			target = p
		}
	}
	if target == nil {
		if r.URL.Query().Get("pod") != "" {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not an instance Pod of Cluster %s/%s", want, namespace, name))
			return
		}
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: "no primary instance Pod is reported"}
		s.writeJSON(w, resp)
		return
	}
	resp.PodUID = target.UID
	resp.Role = cnpgRuntimeRole(target)

	resp.Permission.Exec = s.grantPermission(r, cnpgGrantCreateExec.In(namespace))
	if resp.Permission.Exec == permissionDenied {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgExecStateDenied, Error: "reading sessions needs " + resp.Permission.Grant}
		s.writeJSON(w, resp)
		return
	}
	exec := s.cnpgExecFor(r)
	if exec == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp.CNPGRuntimeSource, resp.CNPGSessionFacts = readCNPGSessions(r.Context(), exec, namespace, target.Name)
	if resp.State == cnpgExecStateDenied {
		resp.Permission.Exec = permissionDenied
	}
	s.writeJSON(w, resp)
}

func readCNPGSessions(ctx context.Context, exec cnpgExecFunc, namespace, pod string) (CNPGRuntimeSource, *CNPGSessionFacts) {
	out, err := exec(ctx, namespace, pod, cnpgDefaultLogContainer, cnpgPsqlArgv, cnpgBlockingSQL)
	captured := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		src := cnpgExecSourceState(err)
		src.CapturedAt = captured
		return src, nil
	}
	facts, err := parseCNPGSessions(out)
	if err != nil {
		return CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: err.Error(), CapturedAt: captured}, nil
	}
	return CNPGRuntimeSource{State: cnpgRuntimeStateOK, CapturedAt: captured}, facts
}

// ---------- cancel / terminate a backend ----------

type cnpgSignalParams struct {
	Pod          string `json:"pod"`
	PodUID       string `json:"podUID"`
	PID          int    `json:"pid"`
	BackendStart string `json:"backendStart"`
}

// cnpgSignalSQL signals only the client backend whose pid AND start time still
// match what the user reviewed: a reused pid is a different session.
func cnpgSignalSQL(fn string) string {
	return cnpgSQLTimeoutPrelude + `SET application_name = '` + cnpgDiagnosticsApp + `';
SELECT json_build_object('found', count(*), 'signalled', coalesce(bool_or(` + fn + `(pid)), false))
FROM pg_stat_activity
WHERE pid = :'pid'::int
  AND backend_start = :'backend_start'::timestamptz
  AND backend_type = 'client backend'
  AND pid <> pg_backend_pid();
`
}

func cnpgSignalArgv(pid int, backendStart string) []string {
	return []string{"psql", "-XAtq", "-v", "ON_ERROR_STOP=1",
		"-v", "pid=" + strconv.Itoa(pid),
		"-v", "backend_start=" + backendStart,
		"-d", cnpgPsqlDatabase, "-f", "-"}
}

func cnpgRunSignalBackend(signal string) func(context.Context, *cnpgClusterRun) (*CNPGActionResult, error) {
	return func(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
		var p cnpgSignalParams
		if err := decodeActionParams(x.params, &p); err != nil {
			return nil, err
		}
		if p.Pod == "" || p.PodUID == "" || p.PID <= 0 || p.BackendStart == "" {
			return nil, refuseAction(http.StatusBadRequest, "", "params.pod, params.podUID, params.pid and params.backendStart are required")
		}
		if _, err := time.Parse(time.RFC3339Nano, p.BackendStart); err != nil {
			return nil, refuseAction(http.StatusBadRequest, "", "params.backendStart must be the backend_start the sessions view returned")
		}
		if r := cnpgGuardCommon(x.facts); r != "" {
			return nil, blockedAction(r)
		}
		inst, ok := x.facts.instance(p.Pod)
		if !ok {
			return nil, changedAction(x.facts, "%s is not an instance of this cluster", p.Pod)
		}
		if !inst.PodReadable {
			return nil, refuseAction(http.StatusForbidden, "", "Pod %s cannot be read, so it cannot be verified as this cluster's instance", p.Pod)
		}
		if !inst.PodExists || inst.PodUID != p.PodUID {
			return nil, changedAction(x.facts, "Pod %s was recreated since you reviewed it; the backend is gone", p.Pod)
		}
		if x.c.exec == nil {
			return nil, refuseAction(http.StatusServiceUnavailable, "", "cluster client not available — check cluster connection")
		}
		fn, verb := "pg_cancel_backend", "Cancel of the running query"
		if signal == cnpgSignalTerminate {
			fn, verb = "pg_terminate_backend", "Termination"
		}
		out, err := x.c.exec(ctx, x.cluster.GetNamespace(), p.Pod, cnpgDefaultLogContainer, cnpgSignalArgv(p.PID, p.BackendStart), cnpgSignalSQL(fn))
		if err != nil {
			if src := cnpgExecSourceState(err); src.State == cnpgExecStateDenied {
				return nil, refuseAction(http.StatusForbidden, "", "This needs %s: %s", cnpgGrantCreateExec.In(x.cluster.GetNamespace()).String(), src.Error)
			}
			return nil, fmt.Errorf("psql on %s: %w", p.Pod, err)
		}
		var res struct {
			Found     int  `json:"found"`
			Signalled bool `json:"signalled"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &res); err != nil {
			return nil, fmt.Errorf("unexpected psql output: %w", err)
		}
		if res.Found == 0 || !res.Signalled {
			return nil, changedAction(nil, "Backend %d on %s ended or was replaced since you reviewed it; nothing was signalled", p.PID, p.Pod)
		}
		return &CNPGActionResult{
			Message: fmt.Sprintf("%s for backend %d on %s sent", verb, p.PID, p.Pod),
			Target:  &CNPGActionTarget{Pod: p.Pod, PodUID: p.PodUID, PID: p.PID, BackendStart: p.BackendStart},
		}, nil
	}
}

// CNPGActionTarget is what an action acted on. Only the fields that apply are
// set.
type CNPGActionTarget struct {
	Pod          string            `json:"pod,omitempty"`
	PodUID       string            `json:"podUID,omitempty"`
	PID          int               `json:"pid,omitempty"`
	BackendStart string            `json:"backendStart,omitempty"`
	KeepPVC      *bool             `json:"keepPVC,omitempty"`
	PVCs         []cnpgReviewedPVC `json:"pvcs,omitempty"`
	Jobs         []string          `json:"jobs,omitempty"`
	Paused       *bool             `json:"paused,omitempty"`
	Generation   int64             `json:"generation,omitempty"`
}

func cnpgGuardPsql(f CNPGClusterFacts, i CNPGInstanceFact) string {
	switch {
	case !i.PodReadable:
		return "The Pod cannot be read"
	case !i.PodExists:
		return "The instance has no running Pod"
	case i.Fenced:
		return "It is fenced: the operator stops PostgreSQL on a fenced instance"
	case f.Hibernated:
		return "The cluster is hibernated"
	}
	return ""
}
