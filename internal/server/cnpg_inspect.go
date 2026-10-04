package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Read-only inspection inside PostgreSQL, with the same fixed-SQL-over-the-
// caller's-pods/exec model as the Sessions view: what a restored cluster
// holds, and which values declared PostgreSQL parameters have on each
// instance. Nothing here writes, and nothing a caller sends is SQL text.

const (
	cnpgRestoreListCap       = 50
	cnpgHistoryReadBytes     = 16 << 10
	cnpgParametersCap        = 200
	cnpgDefaultAppDatabase   = "app"
	cnpgSessionSourceSession = "session"
	cnpgSessionSourceClient  = "client"
)

// A database name goes to psql's -d, which reads a string containing '=' or a
// URI prefix as a connection string; only plain names are used.
var cnpgPlainDatabaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$-]{0,62}$`)

// PostgreSQL parameter names, including extension ones ("pg_stat_statements.max").
var cnpgParameterName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,62}$`)

// ---------- restore checks ----------

// CNPGRestoreChecksResponse is GET /api/cnpg/clusters/{ns}/{name}/restore-checks:
// what Radar read in the primary of a Cluster bootstrapped from a backup.
// These are observations, not a verdict on the restore: where recovery
// stopped is shown beside the declared target, never matched against it, and
// a database or role existing does not prove what it holds (CloudNativePG
// creates the bootstrap database and owner when they are missing).
type CNPGRestoreChecksResponse struct {
	Cluster    CNPGRuntimeObjectRef `json:"cluster"`
	Pod        string               `json:"pod"`
	SampledAt  string               `json:"sampledAt"`
	Permission CNPGExecPermission   `json:"permission"`
	// Target is spec.bootstrap.recovery.recoveryTarget as declared.
	Target   map[string]any `json:"target,omitempty"`
	Database string         `json:"database"`
	CNPGRuntimeSource
	*CNPGRestoreFacts
	// Contents describes Database; absent when it could not be read, with
	// ContentsSource saying why.
	Contents       *CNPGDatabaseContents `json:"contents,omitempty"`
	ContentsSource *CNPGRuntimeSource    `json:"contentsSource,omitempty"`
}

type CNPGRestoreFacts struct {
	InRecovery bool  `json:"inRecovery"`
	Timeline   int64 `json:"timeline"`
	// History is the current timeline's history file, oldest switch first.
	// HistoryMissing is set when there is none (timeline 1 never switched).
	History        []CNPGTimelineSwitch `json:"history"`
	HistoryMissing bool                 `json:"historyMissing,omitempty"`
	DatabaseCount  int                  `json:"databaseCount"`
	Databases      []CNPGDatabaseSize   `json:"databases"`
	RoleCount      int                  `json:"roleCount"`
	Roles          []CNPGRoleFact       `json:"roles"`
}

// CNPGTimelineSwitch is one history line: timeline From ended at SwitchLSN and
// To began. Reason is PostgreSQL's own text — for a recovery that stopped at a
// target, "before|after <commit time of the deciding transaction>", "at
// restore point …" or "before|after transaction …"; a promotion without a
// target (end of WAL, failover, switchover) reads "no recovery target specified".
type CNPGTimelineSwitch struct {
	From      int64  `json:"from"`
	To        int64  `json:"to"`
	SwitchLSN string `json:"switchLsn"`
	Reason    string `json:"reason"`
}

type CNPGDatabaseSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type CNPGRoleFact struct {
	Name     string `json:"name"`
	CanLogin bool   `json:"canLogin"`
}

// CNPGDatabaseContents: tables outside the system schemas. Row counts are the
// planner's estimates (pg_class.reltuples), which can be stale. A table
// without one is counted in NoEstimate, not as zero rows: from PostgreSQL 14
// that is -1 (never vacuumed or analyzed); before 14 it is 0, which an empty
// analyzed table also reads, so those count as unknown too.
type CNPGDatabaseContents struct {
	Tables        int                `json:"tables"`
	EstimatedRows int64              `json:"estimatedRows"`
	NoEstimate    int                `json:"noEstimate"`
	Largest       []CNPGTableSummary `json:"largest"`
}

type CNPGTableSummary struct {
	Name          string `json:"name"`
	EstimatedRows *int64 `json:"estimatedRows,omitempty"`
	Bytes         int64  `json:"bytes"`
}

var cnpgRestoreFactsSQL = cnpgSQLPrelude + cnpgSQLReadOnly + `SET application_name = '` + cnpgDiagnosticsApp + `';
WITH tl AS (SELECT timeline_id AS id FROM pg_control_checkpoint())
SELECT json_build_object(
  'inRecovery', pg_is_in_recovery(),
  'timeline', (SELECT id FROM tl),
  'history', (SELECT pg_read_file('pg_wal/' || lpad(upper(to_hex(id)), 8, '0') || '.history', 0, ` + strconv.Itoa(cnpgHistoryReadBytes) + `, true) FROM tl),
  'databaseCount', (SELECT count(*) FROM pg_database WHERE datallowconn AND NOT datistemplate),
  'databases', coalesce((
    SELECT json_agg(d) FROM (
      SELECT datname AS name, pg_database_size(oid) AS bytes
      FROM pg_database WHERE datallowconn AND NOT datistemplate
      ORDER BY datname LIMIT ` + strconv.Itoa(cnpgRestoreListCap) + `
    ) d
  ), '[]'::json),
  'roleCount', (SELECT count(*) FROM pg_roles WHERE rolname !~ '^pg_'),
  'roles', coalesce((
    SELECT json_agg(r) FROM (
      SELECT rolname AS name, rolcanlogin AS "canLogin"
      FROM pg_roles WHERE rolname !~ '^pg_'
      ORDER BY rolname LIMIT ` + strconv.Itoa(cnpgRestoreListCap) + `
    ) r
  ), '[]'::json)
);
`

var cnpgDatabaseContentsSQL = cnpgSQLPrelude + cnpgSQLReadOnly + `SET application_name = '` + cnpgDiagnosticsApp + `';
WITH t AS (
  SELECT c.oid, n.nspname, c.relname, c.relkind, c.relispartition, c.reltuples,
         CASE WHEN current_setting('server_version_num')::int >= 140000 THEN c.reltuples < 0 ELSE c.reltuples <= 0 END AS unknown
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r', 'p')
    AND n.nspname NOT IN ('pg_catalog', 'information_schema')
    AND n.nspname !~ '^pg_toast'
)
SELECT json_build_object(
  'tables', (SELECT count(*) FROM t WHERE NOT relispartition),
  'estimatedRows', (SELECT coalesce(sum(reltuples), 0)::bigint FROM t WHERE relkind = 'r' AND NOT unknown),
  'noEstimate', (SELECT count(*) FROM t WHERE relkind = 'r' AND unknown),
  'largest', coalesce((
    SELECT json_agg(x) FROM (
      SELECT nspname || '.' || relname AS name,
             CASE WHEN NOT unknown THEN reltuples::bigint END AS "estimatedRows",
             pg_total_relation_size(oid) AS bytes
      FROM t WHERE relkind = 'r'
      ORDER BY pg_total_relation_size(oid) DESC, nspname, relname
      LIMIT 5
    ) x
  ), '[]'::json)
);
`

func cnpgPsqlArgvFor(database string) []string {
	return []string{"psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-d", database, "-f", "-"}
}

type cnpgRestoreFactsRaw struct {
	InRecovery    bool               `json:"inRecovery"`
	Timeline      int64              `json:"timeline"`
	History       *string            `json:"history"`
	DatabaseCount int                `json:"databaseCount"`
	Databases     []CNPGDatabaseSize `json:"databases"`
	RoleCount     int                `json:"roleCount"`
	Roles         []CNPGRoleFact     `json:"roles"`
}

func parseCNPGRestoreFacts(out []byte) (*CNPGRestoreFacts, error) {
	var raw cnpgRestoreFactsRaw
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &raw); err != nil {
		return nil, fmt.Errorf("unexpected psql output: %w", err)
	}
	f := &CNPGRestoreFacts{
		InRecovery:    raw.InRecovery,
		Timeline:      raw.Timeline,
		DatabaseCount: raw.DatabaseCount,
		Databases:     raw.Databases,
		RoleCount:     raw.RoleCount,
		Roles:         raw.Roles,
		History:       []CNPGTimelineSwitch{},
	}
	if raw.History == nil {
		f.HistoryMissing = true
	} else {
		f.History = parseCNPGTimelineHistory(*raw.History, raw.Timeline)
	}
	if f.Databases == nil {
		f.Databases = []CNPGDatabaseSize{}
	}
	if f.Roles == nil {
		f.Roles = []CNPGRoleFact{}
	}
	return f, nil
}

// parseCNPGTimelineHistory reads a timeline history file: one line per switch,
// "<parent TLI>\t<switch LSN>\t<reason>", oldest first; '#' lines are comments.
// Each line's child is the next line's parent, and the last line's is current.
func parseCNPGTimelineHistory(text string, current int64) []CNPGTimelineSwitch {
	out := []CNPGTimelineSwitch{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		from, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			continue
		}
		sw := CNPGTimelineSwitch{From: from, SwitchLSN: strings.TrimSpace(parts[1])}
		if len(parts) == 3 {
			sw.Reason = strings.TrimSpace(parts[2])
		}
		out = append(out, sw)
	}
	for i := range out {
		if i+1 < len(out) {
			out[i].To = out[i+1].From
		} else {
			out[i].To = current
		}
	}
	return out
}

func parseCNPGDatabaseContents(out []byte) (*CNPGDatabaseContents, error) {
	var c CNPGDatabaseContents
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &c); err != nil {
		return nil, fmt.Errorf("unexpected psql output: %w", err)
	}
	if c.Largest == nil {
		c.Largest = []CNPGTableSummary{}
	}
	return &c, nil
}

// cnpgRestoreDatabase is the database applications use after the restore:
// spec.bootstrap.recovery.database, which CloudNativePG defaults to "app".
func cnpgRestoreDatabase(cluster *unstructured.Unstructured) string {
	if db, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "recovery", "database"); db != "" {
		return db
	}
	return cnpgDefaultAppDatabase
}

// handleCNPGRestoreChecks serves GET /api/cnpg/clusters/{ns}/{name}/restore-checks
// for a Cluster bootstrapped from a backup (400 otherwise): read-only facts
// from its primary over the caller's pods/exec. Without exec the answer is a
// 200 whose state is denied.
func (s *Server) handleCNPGRestoreChecks(w http.ResponseWriter, r *http.Request) {
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
	recovery, found, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap", "recovery")
	if !found {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Cluster %s/%s was not bootstrapped from a backup", namespace, name))
		return
	}
	resp := CNPGRestoreChecksResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: permissionAllowed, Grant: cnpgGrantCreateExec.In(namespace).Ref()},
		Database:   cnpgRestoreDatabase(cluster),
	}
	if target, ok := recovery["recoveryTarget"].(map[string]any); ok && len(target) > 0 {
		resp.Target = target
	}
	pods, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	var target *corev1.Pod
	for _, p := range pods {
		if p.Name == primary {
			target = p
		}
	}
	if target == nil {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: "no primary instance Pod is reported"}
		s.writeJSON(w, resp)
		return
	}
	resp.Pod = target.Name
	resp.Permission.Exec = s.grantPermission(r, cnpgGrantCreateExec.In(namespace))
	if resp.Permission.Exec == permissionDenied {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgExecStateDenied, Error: "reading the restored databases needs " + grantText(resp.Permission.Grant)}
		s.writeJSON(w, resp)
		return
	}
	exec := s.cnpgExecFor(r)
	if exec == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	readCNPGRestoreChecks(r.Context(), exec, namespace, target.Name, &resp)
	if resp.State == cnpgExecStateDenied {
		resp.Permission.Exec = permissionDenied
	}
	s.writeJSON(w, resp)
}

func readCNPGRestoreChecks(ctx context.Context, exec cnpgExecFunc, namespace, pod string, resp *CNPGRestoreChecksResponse) {
	out, err := exec(ctx, namespace, pod, cnpgDefaultLogContainer, cnpgPsqlArgv, cnpgRestoreFactsSQL)
	captured := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		resp.CNPGRuntimeSource = cnpgExecSourceState(err)
		resp.CapturedAt = captured
		return
	}
	facts, err := parseCNPGRestoreFacts(out)
	if err != nil {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: err.Error(), CapturedAt: captured}
		return
	}
	resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateOK, CapturedAt: captured}
	resp.CNPGRestoreFacts = facts

	contents := func(src CNPGRuntimeSource) { resp.ContentsSource = &src }
	if !cnpgPlainDatabaseName.MatchString(resp.Database) {
		contents(CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: fmt.Sprintf("%q is not a plain database name, so Radar does not connect to it", resp.Database)})
		return
	}
	if !cnpgHasDatabase(facts, resp.Database) {
		contents(CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: fmt.Sprintf("database %q is not in the list read from the primary", resp.Database)})
		return
	}
	out, err = exec(ctx, namespace, pod, cnpgDefaultLogContainer, cnpgPsqlArgvFor(resp.Database), cnpgDatabaseContentsSQL)
	captured = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		src := cnpgExecSourceState(err)
		src.CapturedAt = captured
		contents(src)
		return
	}
	c, err := parseCNPGDatabaseContents(out)
	if err != nil {
		contents(CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: err.Error(), CapturedAt: captured})
		return
	}
	resp.Contents = c
	contents(CNPGRuntimeSource{State: cnpgRuntimeStateOK, CapturedAt: captured})
}

// cnpgHasDatabase is false only when the list is complete and lacks it; a
// capped list may simply not show it.
func cnpgHasDatabase(f *CNPGRestoreFacts, db string) bool {
	for _, d := range f.Databases {
		if d.Name == db {
			return true
		}
	}
	return f.DatabaseCount > len(f.Databases)
}

// ---------- parameters in effect ----------

// CNPGParametersResponse is GET /api/cnpg/clusters/{ns}/{name}/parameters: for
// each name in spec.postgresql.parameters, what each instance's PostgreSQL
// reports in a fresh session of Radar's — the server's value, which a
// client's own session, role or database settings can still override.
type CNPGParametersResponse struct {
	Cluster    CNPGRuntimeObjectRef   `json:"cluster"`
	SampledAt  string                 `json:"sampledAt"`
	Permission CNPGExecPermission     `json:"permission"`
	Declared   []CNPGParameterValue   `json:"declared"`
	Omitted    int                    `json:"omitted,omitempty"`
	Skipped    []string               `json:"skipped,omitempty"`
	Instances  []CNPGInstanceSettings `json:"instances"`
	CNPGRuntimeSource
}

type CNPGParameterValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CNPGInstanceSettings struct {
	Pod  string `json:"pod"`
	Role string `json:"role"`
	CNPGRuntimeSource
	// Settings is null when the read failed; [] is a read that matched nothing.
	Settings []CNPGParameterSetting `json:"settings"`
}

// CNPGParameterSetting is one pg_settings row. Value is PostgreSQL's display
// (current_setting, units normalized, so "1024MB" reads "1GB"). A parameter
// the connection itself sets (psql sends application_name) has no Value:
// the server's own value is not visible from inside that session.
type CNPGParameterSetting struct {
	Name           string  `json:"name"`
	Value          *string `json:"value"`
	SetByClient    bool    `json:"setByClient,omitempty"`
	Source         string  `json:"source"`
	Context        string  `json:"context"`
	PendingRestart bool    `json:"pendingRestart"`
}

// Only search_path is set (see cnpgSQLPrelude): any other SET would hide the
// server's value of the very parameter it sets, so a declared search_path is
// the one parameter this read reports as set by its own connection. The exec
// deadline bounds the read.
var cnpgParametersSQL = `SET search_path = pg_catalog;
SELECT coalesce(json_agg(json_build_object(
  'name', s.name,
  'value', CASE WHEN s.source IN ('` + cnpgSessionSourceSession + `', '` + cnpgSessionSourceClient + `') THEN NULL ELSE current_setting(s.name) END,
  'setByClient', s.source IN ('` + cnpgSessionSourceSession + `', '` + cnpgSessionSourceClient + `'),
  'source', s.source,
  'context', s.context,
  'pendingRestart', s.pending_restart
) ORDER BY s.name), '[]'::json)
FROM pg_settings s
WHERE s.name = ANY (string_to_array(:'names', ','));
`

// cnpgDeclaredParameters returns spec.postgresql.parameters sorted, capped,
// and split into names Radar can ask about and names it skips.
func cnpgDeclaredParameters(cluster *unstructured.Unstructured) (declared []CNPGParameterValue, query []string, skipped []string, omitted int) {
	params, _, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "parameters")
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > cnpgParametersCap {
		omitted = len(names) - cnpgParametersCap
		names = names[:cnpgParametersCap]
	}
	declared = make([]CNPGParameterValue, 0, len(names))
	for _, n := range names {
		declared = append(declared, CNPGParameterValue{Name: n, Value: fmt.Sprint(params[n])})
		if cnpgParameterName.MatchString(n) {
			// pg_settings names are lower case; PostgreSQL matches names case-insensitively.
			query = append(query, strings.ToLower(n))
		} else {
			skipped = append(skipped, n)
		}
	}
	return declared, query, skipped, omitted
}

func cnpgParametersArgv(names []string) []string {
	return []string{"psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-v", "names=" + strings.Join(names, ","), "-d", cnpgPsqlDatabase, "-f", "-"}
}

func parseCNPGParameterSettings(out []byte) ([]CNPGParameterSetting, error) {
	var rows []CNPGParameterSetting
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &rows); err != nil {
		return nil, fmt.Errorf("unexpected psql output: %w", err)
	}
	if rows == nil {
		rows = []CNPGParameterSetting{}
	}
	return rows, nil
}

// handleCNPGClusterParameters serves GET /api/cnpg/clusters/{ns}/{name}/parameters:
// observation only — it reads, on every instance, the parameters the Cluster
// declares. Without exec the answer is a 200 whose state is denied.
func (s *Server) handleCNPGClusterParameters(w http.ResponseWriter, r *http.Request) {
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
	declared, query, skipped, omitted := cnpgDeclaredParameters(cluster)
	resp := CNPGParametersResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: CNPGExecPermission{Exec: permissionAllowed, Grant: cnpgGrantCreateExec.In(namespace).Ref()},
		Declared:   declared,
		Skipped:    skipped,
		Omitted:    omitted,
		Instances:  []CNPGInstanceSettings{},
	}
	if len(query) == 0 {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateOK}
		s.writeJSON(w, resp)
		return
	}
	pods, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}
	resp.Permission.Exec = s.grantPermission(r, cnpgGrantCreateExec.In(namespace))
	if resp.Permission.Exec == permissionDenied {
		resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgExecStateDenied, Error: "reading parameters on each instance needs " + grantText(resp.Permission.Grant)}
		s.writeJSON(w, resp)
		return
	}
	exec := s.cnpgExecFor(r)
	if exec == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp.Instances = readCNPGParameters(r.Context(), exec, namespace, pods, query)
	resp.CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateOK}
	for _, inst := range resp.Instances {
		if inst.State == cnpgExecStateDenied {
			resp.Permission.Exec = permissionDenied
		}
	}
	s.writeJSON(w, resp)
}

func readCNPGParameters(ctx context.Context, exec cnpgExecFunc, namespace string, pods []*corev1.Pod, names []string) []CNPGInstanceSettings {
	out := make([]CNPGInstanceSettings, len(pods))
	var wg sync.WaitGroup
	for i, p := range pods {
		out[i] = CNPGInstanceSettings{Pod: p.Name, Role: cnpgRuntimeRole(p)}
		wg.Add(1)
		go func(i int, pod string) {
			defer wg.Done()
			stdout, err := exec(ctx, namespace, pod, cnpgDefaultLogContainer, cnpgParametersArgv(names), cnpgParametersSQL)
			captured := time.Now().UTC().Format(time.RFC3339)
			if err != nil {
				src := cnpgExecSourceState(err)
				src.CapturedAt = captured
				out[i].CNPGRuntimeSource = src
				return
			}
			rows, err := parseCNPGParameterSettings(stdout)
			if err != nil {
				out[i].CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateError, Error: err.Error(), CapturedAt: captured}
				return
			}
			out[i].CNPGRuntimeSource = CNPGRuntimeSource{State: cnpgRuntimeStateOK, CapturedAt: captured}
			out[i].Settings = rows
		}(i, p.Name)
	}
	wg.Wait()
	return out
}
