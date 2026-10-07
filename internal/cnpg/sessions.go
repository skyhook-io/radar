package cnpg

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

const (
	execTimeout         = 10 * time.Second
	execStdoutCap       = 1 << 20
	execStderrCap       = 8 << 10
	cnpgExecConcurrency = 4
	cnpgSessionsMaxRows = 200
	cnpgQueryTextChars  = 200
	cnpgDiagnosticsApp  = "radar-diagnostics"
	execStateDenied     = runtimeStateDenied
	cnpgPsqlDatabase    = "postgres"
	cnpgSignalCancel    = "cancel"
	cnpgSignalTerminate = "terminate"
	// cnpgSQLPrelude starts every diagnostic SQL. psql runs as the postgres
	// superuser, so search_path is pinned to pg_catalog: otherwise a function
	// someone created in an application schema with an exact-match signature
	// (pg_total_relation_size(oid), cardinality(int[])) would be chosen over
	// the built-in and run as superuser.
	cnpgSQLPrelude = "SET search_path = pg_catalog;\nSET statement_timeout = '5s';\n"
	// cnpgSQLReadOnly is added to the diagnostics that only read.
	cnpgSQLReadOnly = "SET default_transaction_read_only = on;\n"
)

var grantCreateExec = auth.Grant{Verb: "create", Resource: "pods", Subresource: "exec"}

// cnpgExecSlots bounds concurrent execs across all callers: each one holds a
// streaming connection to a kubelet.
var execSlots = make(chan struct{}, cnpgExecConcurrency)

// cnpgExecFunc runs argv in a container with stdin and returns stdout. The
// error carries the command's stderr.
type ExecFunc func(ctx context.Context, namespace, pod, container string, argv []string, stdin string) ([]byte, error)

var errCNPGExecOutputTooLarge = errors.New("output exceeded the size limit")

type cappedBuffer struct {
	buf      []byte
	limit    int
	overflow bool
}

// Write never fails: refusing bytes would break the stream mid-command, so
// overflow is recorded and reported once the command ends.
func (b *cappedBuffer) Write(p []byte) (int, error) {
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

// cnpgExecSourceState classifies an exec failure the way the runtime reads
// classify proxy failures.
func cnpgExecSourceState(err error) CNPGRuntimeSource {
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "forbidden") {
		return CNPGRuntimeSource{State: execStateDenied, Error: truncateCNPGRuntimeError(msg)}
	}
	// psql's own "connection refused" from PostgreSQL's socket is not a
	// transport failure; read it before the transport hints.
	if postgres, ok := cnpgPostgresSentence(msg); ok {
		log.Printf("[cnpg] Exec failed: %v", err)
		return CNPGRuntimeSource{State: runtimeStateError, Error: postgres}
	}
	text := truncateCNPGRuntimeError(msg)
	plain, transport := cnpgTransportSentence(err, 0, execTimeout)
	if transport {
		log.Printf("[cnpg] Exec failed: %v", err)
		text = plain
	}
	switch {
	case transport, errors.Is(err, context.DeadlineExceeded), strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"),
		strings.Contains(lower, "container not found"), strings.Contains(lower, "unable to upgrade connection"):
		return CNPGRuntimeSource{State: runtimeStateUnreachable, Error: text}
	default:
		return CNPGRuntimeSource{State: runtimeStateError, Error: text}
	}
}

type CNPGExecPermission struct {
	Exec  string      `json:"exec"`
	Grant *auth.Grant `json:"grant,omitempty"`
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

var cnpgBlockingSQL = cnpgSQLPrelude + cnpgSQLReadOnly + `SET lock_timeout = '1s';
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

func sessionInstanceOf(p *corev1.Pod) CNPGSessionInstance {
	out := CNPGSessionInstance{Pod: p.Name, Role: runtimeRole(p)}
	for _, c := range p.Spec.Containers {
		if c.Name != defaultLogContainer {
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

func readCNPGSessions(ctx context.Context, exec ExecFunc, namespace, pod string) (CNPGRuntimeSource, *CNPGSessionFacts) {
	out, err := exec(ctx, namespace, pod, defaultLogContainer, cnpgPsqlArgv, cnpgBlockingSQL)
	captured := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		src := cnpgExecSourceState(err)
		src.CapturedAt = captured
		return src, nil
	}
	facts, err := parseCNPGSessions(out)
	if err != nil {
		return CNPGRuntimeSource{State: runtimeStateError, Error: err.Error(), CapturedAt: captured}, nil
	}
	return CNPGRuntimeSource{State: runtimeStateOK, CapturedAt: captured}, facts
}

type cnpgSignalParams struct {
	Pod          string `json:"pod"`
	PodUID       string `json:"podUID"`
	PID          int    `json:"pid"`
	BackendStart string `json:"backendStart"`
}

// cnpgSignalSQL signals only the client backend whose pid AND start time still
// match what the user reviewed: a reused pid is a different session.
func cnpgSignalSQL(fn string) string {
	return cnpgSQLPrelude + `SET application_name = '` + cnpgDiagnosticsApp + `';
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
		if err := integration.DecodeActionParams(x.params, &p); err != nil {
			return nil, err
		}
		if p.Pod == "" || p.PodUID == "" || p.PID <= 0 || p.BackendStart == "" {
			return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.pod, params.podUID, params.pid and params.backendStart are required")
		}
		if _, err := time.Parse(time.RFC3339Nano, p.BackendStart); err != nil {
			return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.backendStart must be the backend_start the sessions view returned")
		}
		if r := cnpgGuardCommon(x.facts); r != "" {
			return nil, integration.BlockedAction(r)
		}
		inst, ok := x.facts.instance(p.Pod)
		if !ok {
			return nil, integration.ChangedAction(x.facts, "%s is not an instance of this cluster", p.Pod)
		}
		if !inst.PodReadable {
			return nil, integration.RefuseAction(http.StatusForbidden, "", "Pod %s cannot be read, so it cannot be verified as this cluster's instance", p.Pod)
		}
		if !inst.PodExists || inst.PodUID != p.PodUID {
			return nil, integration.ChangedAction(x.facts, "Pod %s was recreated since you reviewed it; the backend is gone", p.Pod)
		}
		if x.c.Exec == nil {
			return nil, integration.RefuseAction(http.StatusServiceUnavailable, "", "cluster client not available — check cluster connection")
		}
		fn, verb := "pg_cancel_backend", "Cancel of the running query"
		if signal == cnpgSignalTerminate {
			fn, verb = "pg_terminate_backend", "Termination"
		}
		out, err := x.c.Exec(ctx, x.cluster.GetNamespace(), p.Pod, defaultLogContainer, cnpgSignalArgv(p.PID, p.BackendStart), cnpgSignalSQL(fn))
		if err != nil {
			if src := cnpgExecSourceState(err); src.State == execStateDenied {
				return nil, integration.RefuseAction(http.StatusForbidden, "", "This needs %s: %s", grantCreateExec.In(x.cluster.GetNamespace()).String(), src.Error)
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
			return nil, integration.ChangedAction(nil, "Backend %d on %s ended or was replaced since you reviewed it; nothing was signalled", p.PID, p.Pod)
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
	Pod          string               `json:"pod,omitempty"`
	PodUID       string               `json:"podUID,omitempty"`
	PID          int                  `json:"pid,omitempty"`
	BackendStart string               `json:"backendStart,omitempty"`
	KeepPVC      *bool                `json:"keepPVC,omitempty"`
	PVCs         []CNPGReviewedObject `json:"pvcs,omitempty"`
	Jobs         []string             `json:"jobs,omitempty"`
	Paused       *bool                `json:"paused,omitempty"`
	Generation   int64                `json:"generation,omitempty"`
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

func (s *Reader) Sessions(ctx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured, wantedPod string) (*CNPGSessionsResponse, error) {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	pods, err := clusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(name), err)
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "instance Pods unavailable: " + err.Error()}
	}
	want := wantedPod
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if want == "" {
		want = primary
	}
	resp := CNPGSessionsResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		Pod:        want,
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: integration.PermissionAllowed, Grant: grantCreateExec.In(namespace).Ref()},
		Instances:  make([]CNPGSessionInstance, 0, len(pods)),
	}
	var target *corev1.Pod
	for _, p := range pods {
		resp.Instances = append(resp.Instances, sessionInstanceOf(p))
		if p.Name == want {
			target = p
		}
	}
	resp.Permission.Exec = s.Access.Permission(ctx, grantCreateExec.In(namespace))
	if resp.Permission.Exec == integration.PermissionDenied {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: execStateDenied, Error: "reading sessions needs " + integration.GrantText(resp.Permission.Grant)}
		return &resp, nil
	}
	if target == nil {
		if wantedPod != "" {
			return nil, &ReadFailure{Status: http.StatusBadRequest, Message: fmt.Sprintf("%s is not an instance Pod of Cluster %s/%s", want, namespace, name)}
		}
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: "unavailable", Reason: "Available once the primary is running"}
		return &resp, nil
	}
	resp.PodUID = target.UID
	resp.Role = runtimeRole(target)

	exec := s.Clients.Exec
	if exec == nil {
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "cluster client not available — check cluster connection"}
	}
	resp.CNPGRuntimeSource, resp.CNPGSessionFacts = readCNPGSessions(ctx, exec, namespace, target.Name)
	if resp.State == execStateDenied {
		resp.Permission.Exec = integration.PermissionDenied
	}
	return &resp, nil
}

func NewExec(client kubernetes.Interface, cfg *rest.Config) ExecFunc {
	if client == nil || cfg == nil {
		return nil
	}
	return func(ctx context.Context, namespace, pod, container string, argv []string, stdin string) ([]byte, error) {
		select {
		case execSlots <- struct{}{}:
			defer func() { <-execSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(ctx, execTimeout)
		defer cancel()
		ex, err := k8score.NewPodExecExecutor(client, cfg, namespace, pod, container, argv, false)
		if err != nil {
			return nil, err
		}
		out := &cappedBuffer{limit: execStdoutCap}
		errOut := &cappedBuffer{limit: execStderrCap}
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
