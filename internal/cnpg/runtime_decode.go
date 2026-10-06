package cnpg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// The operator's own sessions: replication and the metrics exporter. Always
// present and long-lived by design, so counting them would make an idle
// database look busy. CNPG 1.27's exporter connects as postgres, so it is only
// recognizable by its application_name.
var cnpgPlatformUsers = map[string]bool{"streaming_replica": true, cnpgMetricsExporterApp: true}

// The default-monitoring families the runtime view reads. An absent one is
// reported in `missing` — a custom monitoring configuration can replace any
// default query — rather than read as zero. Replication-slot retention is not
// listed: the exporter emits nothing when an instance has no slots.
var cnpgExpectedInstanceFamilies = []string{
	"cnpg_backends_total",
	"cnpg_backends_waiting_total",
	"cnpg_pg_postmaster_start_time",
	"cnpg_backends_max_tx_duration_seconds",
	"cnpg_pg_settings_setting",
	"cnpg_pg_database_size_bytes",
	"cnpg_pg_database_xid_age",
	"cnpg_pg_database_mxid_age",
	"cnpg_pg_extensions_update_available",
	"cnpg_pg_stat_archiver_archived_count",
	"cnpg_pg_stat_archiver_failed_count",
	"cnpg_pg_stat_archiver_seconds_since_last_archival",
	"cnpg_pg_stat_archiver_seconds_since_last_failure",
	"cnpg_collector_pg_wal",
	"cnpg_pg_stat_database_xact_commit",
	"cnpg_pg_stat_database_xact_rollback",
	"cnpg_pg_stat_database_blks_hit",
	"cnpg_pg_stat_database_blks_read",
	"cnpg_pg_stat_database_deadlocks",
}

var cnpgExpectedPoolerFamilies = []string{
	"cnpg_pgbouncer_pools_cl_active",
	"cnpg_pgbouncer_pools_cl_waiting",
	"cnpg_pgbouncer_pools_sv_active",
	"cnpg_pgbouncer_pools_sv_idle",
	"cnpg_pgbouncer_pools_sv_used",
	"cnpg_pgbouncer_pools_maxwait",
}

// cnpgPgStatus is the subset of the instance manager's GET /pg/status answer
// (CloudNativePG's PostgresqlStatus) the runtime view reads.
type cnpgPgStatus struct {
	IsPrimary           *bool  `json:"isPrimary"`
	MightBeUnavailable  bool   `json:"mightBeUnavailable"`
	MaskedError         string `json:"mightBeUnavailableMaskedError"`
	CurrentLsn          string `json:"currentLsn"`
	ReceivedLsn         string `json:"receivedLsn"`
	ReplayLsn           string `json:"replayLsn"`
	TimeLineID          *int   `json:"timeLineID"`
	ReplayPaused        bool   `json:"replayPaused"`
	PendingRestart      bool   `json:"pendingRestart"`
	IsWalReceiverActive bool   `json:"isWalReceiverActive"`

	PendingRestartForDecrease  bool   `json:"pendingRestartForDecrease"`
	IsPgRewindRunning          bool   `json:"isPgRewindRunning"`
	InstanceManagerVersion     string `json:"instanceManagerVersion"`
	IsInstanceManagerUpgrading bool   `json:"isInstanceManagerUpgrading"`
	LastArchivedWAL            string `json:"lastArchivedWAL"`
	LastArchivedWALTime        string `json:"lastArchivedWALTime"`
	LastFailedWAL              string `json:"lastFailedWAL"`
	LastFailedWALTime          string `json:"lastFailedWALTime"`
	ReadyWalFiles              *int   `json:"readyWalFiles"`
	ReplicationInfo            []struct {
		ApplicationName string `json:"applicationName"`
		State           string `json:"state"`
		// CNPG serializes pg_stat_replication.sent_lsn under "receivedLsn".
		SentLsn      string          `json:"receivedLsn"`
		WriteLsn     string          `json:"writeLsn"`
		FlushLsn     string          `json:"flushLsn"`
		ReplayLsn    string          `json:"replayLsn"`
		WriteLag     string          `json:"writeLag"`
		FlushLag     string          `json:"flushLag"`
		ReplayLag    string          `json:"replayLag"`
		SyncState    string          `json:"syncState"`
		SyncPriority json.RawMessage `json:"syncPriority"`
	} `json:"replicationInfo"`
	ReplicationSlotsInfo []struct {
		SlotName    string `json:"slotName"`
		Plugin      string `json:"plugin"`
		SlotType    string `json:"slotType"`
		Database    string `json:"database"`
		Active      bool   `json:"active"`
		RestartLsn  string `json:"restartLsn"`
		WalStatus   string `json:"walStatus"`
		SafeWalSize *int64 `json:"safeWalSize"`
	} `json:"replicationSlotsInfo"`
	PgStatBasebackupsInfo []struct {
		ApplicationName     string `json:"application_name"`
		BackendStart        string `json:"backend_start"`
		Phase               string `json:"phase"`
		BackupTotal         int64  `json:"backup_total"`
		BackupStreamed      int64  `json:"backup_streamed"`
		TablespacesTotal    int64  `json:"tablespaces_total"`
		TablespacesStreamed int64  `json:"tablespaces_streamed"`
	} `json:"pgStatBasebackupsInfo"`
}

func cnpgInstanceStatusFrom(out cnpgProxyOutcome) CNPGInstanceStatus {
	src := out.source()
	if out.state != runtimeStateOK {
		return CNPGInstanceStatus{CNPGRuntimeSource: src}
	}
	if out.truncated {
		src.State, src.Error = runtimeStateError, fmt.Sprintf("status answer larger than the %s this view reads", formatCNPGByteCap(cnpgRuntimeStatusCap))
		return CNPGInstanceStatus{CNPGRuntimeSource: src}
	}
	facts, partial, err := parseCNPGPgStatus(out.body)
	if err != nil {
		src.State, src.Error = runtimeStateError, err.Error()
		return CNPGInstanceStatus{CNPGRuntimeSource: src}
	}
	if partial != "" {
		src.State, src.Reason = cnpgRuntimeStatePartial, partial
	}
	return CNPGInstanceStatus{CNPGRuntimeSource: src, CNPGInstanceStatusFacts: facts}
}

// parseCNPGPgStatus returns the facts and, when rows were capped, why the
// answer is partial.
func parseCNPGPgStatus(body []byte) (*CNPGInstanceStatusFacts, string, error) {
	var st cnpgPgStatus
	if err := json.Unmarshal(body, &st); err != nil || st.IsPrimary == nil {
		return nil, "", errors.New("the instance manager's answer was not a status report")
	}
	facts := &CNPGInstanceStatusFacts{
		IsPrimary:           *st.IsPrimary,
		MightBeUnavailable:  st.MightBeUnavailable,
		CurrentLsn:          st.CurrentLsn,
		ReceivedLsn:         st.ReceivedLsn,
		ReplayLsn:           st.ReplayLsn,
		Timeline:            st.TimeLineID,
		ReplayPaused:        st.ReplayPaused,
		PendingRestart:      st.PendingRestart,
		IsWalReceiverActive: st.IsWalReceiverActive,

		PendingRestartForDecrease:  st.PendingRestartForDecrease,
		IsPgRewindRunning:          st.IsPgRewindRunning,
		InstanceManagerVersion:     st.InstanceManagerVersion,
		IsInstanceManagerUpgrading: st.IsInstanceManagerUpgrading,
		Archiving: &CNPGArchivingStatus{
			LastArchivedWal: st.LastArchivedWAL,
			LastArchivedAt:  cnpgStatusTime(st.LastArchivedWALTime),
			LastFailedWal:   st.LastFailedWAL,
			LastFailedAt:    cnpgStatusTime(st.LastFailedWALTime),
			ReadyWalFiles:   st.ReadyWalFiles,
		},
		Replication:    []CNPGReplicationStatus{},
		Slots:          []CNPGSlotStatus{},
		SlotsTruncated: len(st.ReplicationSlotsInfo) > cnpgRuntimeMaxRows,
		BaseBackups:    []CNPGBaseBackupStatus{},
	}
	facts.RoleDetail = cnpgRoleDetail(facts)
	var partial []string
	for i, ri := range st.ReplicationInfo {
		if i >= cnpgRuntimeMaxRows {
			partial = append(partial, fmt.Sprintf("%d replication connections; the first %d are shown", len(st.ReplicationInfo), cnpgRuntimeMaxRows))
			break
		}
		facts.Replication = append(facts.Replication, CNPGReplicationStatus{
			ApplicationName: ri.ApplicationName,
			State:           ri.State,
			SyncState:       ri.SyncState,
			SyncPriority:    parseCNPGSyncPriority(ri.SyncPriority),
			WriteLag:        parseCNPGPgInterval(ri.WriteLag),
			WriteLagRaw:     ri.WriteLag,
			FlushLag:        parseCNPGPgInterval(ri.FlushLag),
			FlushLagRaw:     ri.FlushLag,
			ReplayLag:       parseCNPGPgInterval(ri.ReplayLag),
			ReplayLagRaw:    ri.ReplayLag,
			SentLsn:         ri.SentLsn,
			WriteLsn:        ri.WriteLsn,
			FlushLsn:        ri.FlushLsn,
			ReplayLsn:       ri.ReplayLsn,
		})
	}
	for i, si := range st.ReplicationSlotsInfo {
		if i >= cnpgRuntimeMaxRows {
			partial = append(partial, fmt.Sprintf("%d replication slots; the first %d are shown", len(st.ReplicationSlotsInfo), cnpgRuntimeMaxRows))
			break
		}
		facts.Slots = append(facts.Slots, CNPGSlotStatus{
			Name: si.SlotName, Type: si.SlotType, Plugin: si.Plugin, Active: si.Active, Database: si.Database,
			RestartLsn: si.RestartLsn, WalStatus: si.WalStatus, SafeWalSize: si.SafeWalSize,
		})
	}
	for i, bb := range st.PgStatBasebackupsInfo {
		if i >= cnpgRuntimeMaxRows {
			partial = append(partial, fmt.Sprintf("%d base backups; the first %d are shown", len(st.PgStatBasebackupsInfo), cnpgRuntimeMaxRows))
			break
		}
		row := CNPGBaseBackupStatus{
			ApplicationName:     bb.ApplicationName,
			Instance:            strings.TrimSuffix(bb.ApplicationName, "-join"),
			Phase:               bb.Phase,
			StartedAt:           cnpgStatusTime(bb.BackendStart),
			StreamedBytes:       bb.BackupStreamed,
			TablespacesTotal:    bb.TablespacesTotal,
			TablespacesStreamed: bb.TablespacesStreamed,
		}
		if row.Instance == row.ApplicationName {
			row.Instance = ""
		}
		if bb.BackupTotal > 0 {
			total := bb.BackupTotal
			row.TotalBytes = &total
		}
		facts.BaseBackups = append(facts.BaseBackups, row)
	}
	if why := cnpgIncompleteReport(&st); why != "" {
		markCNPGStatusIncomplete(facts, &st)
		partial = append(partial, why)
	}
	return facts, strings.Join(partial, "; "), nil
}

func cnpgIncompleteReport(st *cnpgPgStatus) string {
	switch {
	case st.MaskedError != "":
		masked := truncateCNPGRuntimeError(st.MaskedError)
		if plain, ok := cnpgPostgresSentence(st.MaskedError); ok {
			masked = plain
		}
		return "the instance manager answered while PostgreSQL may be unavailable and masked an error (" + masked + "); what it did not read is unknown"
	case st.IsPgRewindRunning:
		return "pg_rewind is running, so the instance manager read nothing from PostgreSQL"
	}
	return ""
}

// markCNPGStatusIncomplete keeps what the report did carry: each list is
// filled in one step, so a non-empty one is whole, but an empty one may never
// have been read.
func markCNPGStatusIncomplete(f *CNPGInstanceStatusFacts, st *cnpgPgStatus) {
	f.Incomplete, f.MaskedError = true, st.MaskedError
	if len(f.Replication) == 0 {
		f.Replication = nil
	}
	if len(f.Slots) == 0 {
		f.Slots = nil
	}
	if len(f.BaseBackups) == 0 {
		f.BaseBackups = nil
	}
	if st.LastArchivedWALTime == "" {
		f.Archiving = nil
	}
	if !f.IsPrimary && !f.IsPgRewindRunning {
		f.RoleDetail = ""
	}
}

// cnpgRoleDetail names what an instance is doing from its own report, never
// from the Cluster's phase. A standby without an active WAL receiver is
// replaying from the archive or waiting to reconnect; `kubectl cnpg status`
// calls both "file based".
func cnpgRoleDetail(f *CNPGInstanceStatusFacts) string {
	switch {
	case f.IsPrimary:
		return "primary"
	case f.IsPgRewindRunning:
		return "pgRewind"
	case f.ReplayPaused:
		return "replayPaused"
	case f.IsWalReceiverActive:
		return "streaming"
	default:
		return "fileBased"
	}
}

// cnpgStatusTime normalizes an instance manager timestamp; "-infinity" and
// anything unparseable mean "never" and are omitted.
func cnpgStatusTime(v string) string {
	if v == "" || strings.Contains(v, "infinity") {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// The instance manager has sent syncPriority as both a string and a number.
func parseCNPGSyncPriority(raw json.RawMessage) *int {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return &n
		}
	}
	return nil
}

var cnpgPgIntervalUnitSeconds = map[string]float64{"year": 365.25 * 86400, "mon": 30 * 86400, "day": 86400}

// parsePgIntervalSeconds reads a PostgreSQL interval in the default output
// style ("00:00:00.012345", "1 day 02:03:04", "2 mons 3 days"). Unparseable
// is nil, never zero: a lag that cannot be read must not look like no lag.
func parseCNPGPgInterval(v string) *float64 {
	tokens := strings.Fields(v)
	if len(tokens) == 0 {
		return nil
	}
	total := 0.0
	for i := 0; i < len(tokens); {
		if secs, ok := parseCNPGPgClock(tokens[i]); ok {
			total += secs
			i++
			continue
		}
		if i+1 >= len(tokens) {
			return nil
		}
		n, err := strconv.ParseFloat(tokens[i], 64)
		if err != nil {
			return nil
		}
		mult, ok := cnpgPgIntervalUnitSeconds[strings.TrimSuffix(tokens[i+1], "s")]
		if !ok {
			return nil
		}
		total += n * mult
		i += 2
	}
	return &total
}

func parseCNPGPgClock(tok string) (float64, bool) {
	neg := strings.HasPrefix(tok, "-")
	parts := strings.Split(strings.TrimPrefix(tok, "-"), ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	s, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	secs := h*3600 + m*60 + s
	if neg {
		secs = -secs
	}
	return secs, true
}

// cnpgPromText parses exporter text. An answer cut at the byte cap is read up
// to its last complete line, and the family on that line is dropped since
// its samples may continue past the cut; the returned reason says so.
func cnpgPromText(out cnpgProxyOutcome) (map[string][]cnpgSample, string, error) {
	body, reason := out.body, ""
	if out.truncated {
		cut := bytes.LastIndexByte(body, '\n')
		if cut < 0 {
			return nil, "", fmt.Errorf("exporter answer larger than the %s this view reads", formatCNPGByteCap(int64(len(body))))
		}
		body = body[:cut+1]
		reason = fmt.Sprintf("the exporter's answer exceeded %s; measurements past that point are missing", formatCNPGByteCap(int64(len(out.body))))
	}
	samples, err := parseCNPGPromSamples(body)
	if err != nil {
		return nil, "", err
	}
	if out.truncated {
		delete(samples, cnpgLastPromFamily(body))
	}
	return samples, reason, nil
}

func cnpgLastPromFamily(body []byte) string {
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(string(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				return fields[2]
			}
			continue
		}
		if i := strings.IndexAny(line, "{ "); i > 0 {
			return line[:i]
		}
		return line
	}
	return ""
}

func cnpgInstanceMetricsFrom(out cnpgProxyOutcome) CNPGInstanceMetrics {
	src := out.source()
	if out.state != runtimeStateOK {
		return CNPGInstanceMetrics{CNPGRuntimeSource: src}
	}
	samples, reason, err := cnpgPromText(out)
	if err != nil {
		src.State, src.Error = runtimeStateError, "exporter answer was not Prometheus text: "+truncateCNPGRuntimeError(err.Error())
		return CNPGInstanceMetrics{CNPGRuntimeSource: src}
	}
	facts, capped := cnpgInstanceMetricFacts(samples)
	if reason != "" || capped != "" {
		src.State, src.Reason = cnpgRuntimeStatePartial, cnpgJoinReasons(reason, capped)
	}
	return CNPGInstanceMetrics{CNPGRuntimeSource: src, CNPGInstanceMetricFacts: facts}
}

func poolerPodFrom(out cnpgProxyOutcome) CNPGPoolerPodRuntime {
	src := out.source()
	if out.state != runtimeStateOK {
		return CNPGPoolerPodRuntime{CNPGRuntimeSource: src}
	}
	samples, reason, err := cnpgPromText(out)
	if err != nil {
		src.State, src.Error = runtimeStateError, "exporter answer was not Prometheus text: "+truncateCNPGRuntimeError(err.Error())
		return CNPGPoolerPodRuntime{CNPGRuntimeSource: src}
	}
	facts, capped := cnpgPoolerFacts(samples)
	if reason != "" || capped != "" {
		src.State, src.Reason = cnpgRuntimeStatePartial, cnpgJoinReasons(reason, capped)
	}
	return CNPGPoolerPodRuntime{CNPGRuntimeSource: src, CNPGPoolerPodFacts: facts}
}

func cnpgJoinReasons(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

type cnpgSample struct {
	labels map[string]string
	value  float64
}

func parseCNPGPromSamples(body []byte) (map[string][]cnpgSample, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out := make(map[string][]cnpgSample, len(families))
	for name, fam := range families {
		list := []cnpgSample{}
		for _, m := range fam.Metric {
			v, ok := cnpgMetricValue(fam.GetType(), m)
			if !ok {
				continue
			}
			lbls := make(map[string]string, len(m.Label))
			for _, l := range m.Label {
				lbls[l.GetName()] = l.GetValue()
			}
			list = append(list, cnpgSample{labels: lbls, value: v})
		}
		out[name] = list
	}
	return out, nil
}

// cnpgMetricValue drops NaN and infinities: the exporter reports NaN for a
// value it could not read, which is unavailable, not a number.
func cnpgMetricValue(t dto.MetricType, m *dto.Metric) (float64, bool) {
	var v float64
	switch {
	case t == dto.MetricType_COUNTER && m.Counter != nil:
		v = m.Counter.GetValue()
	case t == dto.MetricType_GAUGE && m.Gauge != nil:
		v = m.Gauge.GetValue()
	case m.Untyped != nil:
		v = m.Untyped.GetValue()
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func cnpgSingle(samples map[string][]cnpgSample, name string, match map[string]string) *float64 {
	for _, s := range samples[name] {
		ok := true
		for k, v := range match {
			if s.labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			v := s.value
			return &v
		}
	}
	return nil
}

// cnpgSum totals one instance's per-database counter; absent when the family
// reported nothing.
func cnpgSum(samples map[string][]cnpgSample, name string) *float64 {
	list := samples[name]
	if len(list) == 0 {
		return nil
	}
	total := 0.0
	for _, s := range list {
		total += s.value
	}
	return &total
}

func cnpgIsPlatformSession(labels map[string]string) bool {
	return cnpgPlatformUsers[labels["usename"]] || labels["application_name"] == cnpgMetricsExporterApp
}

func cnpgMissingFamilies(samples map[string][]cnpgSample, expected []string) []string {
	var missing []string
	for _, name := range expected {
		if _, ok := samples[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// cnpgInstanceMetricFacts returns the facts and, when a row list was capped,
// why the answer is partial.
func cnpgInstanceMetricFacts(samples map[string][]cnpgSample) (*CNPGInstanceMetricFacts, string) {
	facts := &CNPGInstanceMetricFacts{
		Missing:             cnpgMissingFamilies(samples, cnpgExpectedInstanceFamilies),
		MaxConnections:      cnpgSingle(samples, "cnpg_pg_settings_setting", map[string]string{"name": "max_connections"}),
		WaitingBackends:     cnpgSingle(samples, "cnpg_backends_waiting_total", nil),
		PostmasterStartTime: cnpgSingle(samples, "cnpg_pg_postmaster_start_time", nil),
		WalBytes:            cnpgSingle(samples, "cnpg_collector_pg_wal", map[string]string{"value": "size"}),
		WalSegments:         cnpgSingle(samples, "cnpg_collector_pg_wal", map[string]string{"value": "count"}),
		XactCommitTotal:     cnpgSum(samples, "cnpg_pg_stat_database_xact_commit"),
		XactRollbackTotal:   cnpgSum(samples, "cnpg_pg_stat_database_xact_rollback"),
		BlksHit:             cnpgSum(samples, "cnpg_pg_stat_database_blks_hit"),
		BlksRead:            cnpgSum(samples, "cnpg_pg_stat_database_blks_read"),
		DeadlocksTotal:      cnpgSum(samples, "cnpg_pg_stat_database_deadlocks"),
		TempBytesTotal:      cnpgSum(samples, "cnpg_pg_stat_database_temp_bytes"),
		LastUpdateTimestamp: cnpgSingle(samples, "cnpg_last_update_timestamp", nil),
		Checkpoints:         cnpgCheckpointFacts(samples),
	}
	var capped []string

	if backends, ok := samples["cnpg_backends_total"]; ok {
		total := 0.0
		for _, s := range backends {
			if cnpgIsPlatformSession(s.labels) || s.labels["state"] == "" || s.value <= 0 {
				continue
			}
			total += s.value
			if facts.SessionsByState == nil {
				facts.SessionsByState = map[string]float64{}
			}
			facts.SessionsByState[s.labels["state"]] += s.value
			facts.Sessions = append(facts.Sessions, CNPGSessionGroup{
				State: s.labels["state"], Database: s.labels["datname"], User: s.labels["usename"],
				Application: s.labels["application_name"], Count: s.value,
			})
		}
		sort.Slice(facts.Sessions, func(i, j int) bool {
			a, b := facts.Sessions[i], facts.Sessions[j]
			if a.Count != b.Count {
				return a.Count > b.Count
			}
			return a.State+"\x00"+a.Database+"\x00"+a.User+"\x00"+a.Application < b.State+"\x00"+b.Database+"\x00"+b.User+"\x00"+b.Application
		})
		if len(facts.Sessions) > cnpgRuntimeMaxRows {
			capped = append(capped, fmt.Sprintf("%d session groups; the largest %d are listed and sessionsTotal counts all", len(facts.Sessions), cnpgRuntimeMaxRows))
			facts.Sessions = facts.Sessions[:cnpgRuntimeMaxRows]
		}
		facts.SessionsTotal = &total
	}

	if durations, ok := samples["cnpg_backends_max_tx_duration_seconds"]; ok {
		oldest := 0.0
		for _, s := range durations {
			if !cnpgIsPlatformSession(s.labels) && s.value > oldest {
				oldest = s.value
			}
		}
		facts.OldestXactSeconds = &oldest
	}

	for _, s := range samples["cnpg_pg_database_xid_age"] {
		facts.XidAge = append(facts.XidAge, CNPGDatabaseValue{Database: s.labels["datname"], Age: s.value})
	}
	sort.Slice(facts.XidAge, func(i, j int) bool { return facts.XidAge[i].Age > facts.XidAge[j].Age })
	if len(facts.XidAge) > cnpgRuntimeMaxRows {
		capped = append(capped, fmt.Sprintf("%d databases; the %d oldest by xid age are listed", len(facts.XidAge), cnpgRuntimeMaxRows))
		facts.XidAge = facts.XidAge[:cnpgRuntimeMaxRows]
	}
	for _, s := range samples["cnpg_pg_database_mxid_age"] {
		facts.MxidAge = append(facts.MxidAge, CNPGDatabaseValue{Database: s.labels["datname"], Age: s.value})
	}
	sort.Slice(facts.MxidAge, func(i, j int) bool { return facts.MxidAge[i].Age > facts.MxidAge[j].Age })
	if len(facts.MxidAge) > cnpgRuntimeMaxRows {
		capped = append(capped, fmt.Sprintf("%d databases; the %d oldest by multixact age are listed", len(facts.MxidAge), cnpgRuntimeMaxRows))
		facts.MxidAge = facts.MxidAge[:cnpgRuntimeMaxRows]
	}
	if exts, ok := samples["cnpg_pg_extensions_update_available"]; ok {
		facts.ExtensionUpdates = []CNPGExtensionUpdate{}
		for _, s := range exts {
			if s.value <= 0 {
				continue
			}
			facts.ExtensionUpdates = append(facts.ExtensionUpdates, CNPGExtensionUpdate{
				Database: s.labels["datname"], Extension: s.labels["extname"],
				InstalledVersion: s.labels["installed_version"], DefaultVersion: s.labels["default_version"],
			})
		}
		sort.Slice(facts.ExtensionUpdates, func(i, j int) bool {
			a, b := facts.ExtensionUpdates[i], facts.ExtensionUpdates[j]
			return a.Database+"\x00"+a.Extension < b.Database+"\x00"+b.Extension
		})
		if len(facts.ExtensionUpdates) > cnpgRuntimeMaxRows {
			capped = append(capped, fmt.Sprintf("%d extensions with updates; the first %d are listed", len(facts.ExtensionUpdates), cnpgRuntimeMaxRows))
			facts.ExtensionUpdates = facts.ExtensionUpdates[:cnpgRuntimeMaxRows]
		}
	}
	for _, s := range samples["cnpg_pg_database_size_bytes"] {
		facts.DatabaseSizes = append(facts.DatabaseSizes, CNPGDatabaseBytes{Database: s.labels["datname"], Bytes: s.value})
	}
	sort.Slice(facts.DatabaseSizes, func(i, j int) bool { return facts.DatabaseSizes[i].Bytes > facts.DatabaseSizes[j].Bytes })
	if len(facts.DatabaseSizes) > cnpgRuntimeMaxRows {
		capped = append(capped, fmt.Sprintf("%d databases; the %d largest are listed", len(facts.DatabaseSizes), cnpgRuntimeMaxRows))
		facts.DatabaseSizes = facts.DatabaseSizes[:cnpgRuntimeMaxRows]
	}
	for _, s := range samples["cnpg_pg_replication_slots_pg_wal_lsn_diff"] {
		facts.ReplicationSlotsRetainedBytes = append(facts.ReplicationSlotsRetainedBytes, CNPGSlotBytes{Slot: s.labels["slot_name"], Bytes: s.value})
	}
	sort.Slice(facts.ReplicationSlotsRetainedBytes, func(i, j int) bool {
		return facts.ReplicationSlotsRetainedBytes[i].Bytes > facts.ReplicationSlotsRetainedBytes[j].Bytes
	})
	if len(facts.ReplicationSlotsRetainedBytes) > cnpgRuntimeMaxRows {
		capped = append(capped, fmt.Sprintf("%d replication slots; the %d retaining the most WAL are listed", len(facts.ReplicationSlotsRetainedBytes), cnpgRuntimeMaxRows))
		facts.ReplicationSlotsRetainedBytes = facts.ReplicationSlotsRetainedBytes[:cnpgRuntimeMaxRows]
	}

	if dbs, more := cnpgDatabaseStats(samples); len(dbs) > 0 {
		facts.Databases = dbs
		if more > 0 {
			capped = append(capped, fmt.Sprintf("%d databases; the first %d by name are listed", len(dbs)+more, cnpgRuntimeMaxRows))
		}
	}

	archiver := CNPGArchiverCounters{
		ArchivedCount:            cnpgSingle(samples, "cnpg_pg_stat_archiver_archived_count", nil),
		FailedCount:              cnpgSingle(samples, "cnpg_pg_stat_archiver_failed_count", nil),
		SecondsSinceLastArchival: cnpgNonNegative(cnpgSingle(samples, "cnpg_pg_stat_archiver_seconds_since_last_archival", nil)),
		SecondsSinceLastFailure:  cnpgNonNegative(cnpgSingle(samples, "cnpg_pg_stat_archiver_seconds_since_last_failure", nil)),
	}
	if archiver != (CNPGArchiverCounters{}) {
		facts.Archiver = &archiver
	}
	return facts, strings.Join(capped, "; ")
}

// The row pg_stat_database keeps for shared objects has no datname; it is not
// a database.
func cnpgDatabaseStats(samples map[string][]cnpgSample) ([]CNPGDatabaseStats, int) {
	byName := map[string]*CNPGDatabaseStats{}
	set := func(family string, field func(*CNPGDatabaseStats) **float64) {
		for _, s := range samples[family] {
			name := s.labels["datname"]
			if name == "" {
				continue
			}
			d := byName[name]
			if d == nil {
				d = &CNPGDatabaseStats{Database: name}
				byName[name] = d
			}
			v := s.value
			*field(d) = &v
		}
	}
	set("cnpg_pg_stat_database_xact_commit", func(d *CNPGDatabaseStats) **float64 { return &d.XactCommit })
	set("cnpg_pg_stat_database_xact_rollback", func(d *CNPGDatabaseStats) **float64 { return &d.XactRollback })
	set("cnpg_pg_stat_database_temp_files", func(d *CNPGDatabaseStats) **float64 { return &d.TempFiles })
	set("cnpg_pg_stat_database_temp_bytes", func(d *CNPGDatabaseStats) **float64 { return &d.TempBytes })
	set("cnpg_pg_stat_database_deadlocks", func(d *CNPGDatabaseStats) **float64 { return &d.Deadlocks })
	set("cnpg_pg_stat_database_blks_hit", func(d *CNPGDatabaseStats) **float64 { return &d.BlksHit })
	set("cnpg_pg_stat_database_blks_read", func(d *CNPGDatabaseStats) **float64 { return &d.BlksRead })
	out := make([]CNPGDatabaseStats, 0, len(byName))
	for _, d := range byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Database < out[j].Database })
	if len(out) > cnpgRuntimeMaxRows {
		return out[:cnpgRuntimeMaxRows], len(out) - cnpgRuntimeMaxRows
	}
	return out, 0
}

func cnpgCheckpointFacts(samples map[string][]cnpgSample) *CNPGCheckpointCounters {
	get := func(name string) *float64 { return cnpgSingle(samples, name, nil) }
	if _, ok := samples["cnpg_pg_stat_checkpointer_checkpoints_timed"]; ok {
		return &CNPGCheckpointCounters{
			Source:                 "pg_stat_checkpointer",
			Timed:                  get("cnpg_pg_stat_checkpointer_checkpoints_timed"),
			Requested:              get("cnpg_pg_stat_checkpointer_checkpoints_req"),
			RestartpointsTimed:     get("cnpg_pg_stat_checkpointer_restartpoints_timed"),
			RestartpointsRequested: get("cnpg_pg_stat_checkpointer_restartpoints_req"),
			RestartpointsDone:      get("cnpg_pg_stat_checkpointer_restartpoints_done"),
			BuffersWritten:         get("cnpg_pg_stat_checkpointer_buffers_written"),
		}
	}
	if _, ok := samples["cnpg_pg_stat_bgwriter_checkpoints_timed"]; ok {
		return &CNPGCheckpointCounters{
			Source:         "pg_stat_bgwriter",
			Timed:          get("cnpg_pg_stat_bgwriter_checkpoints_timed"),
			Requested:      get("cnpg_pg_stat_bgwriter_checkpoints_req"),
			BuffersWritten: get("cnpg_pg_stat_bgwriter_buffers_checkpoint"),
		}
	}
	return nil
}

// The exporter reports -1 for "never happened".
func cnpgNonNegative(v *float64) *float64 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}

// withCNPGSlotRetention returns a copy with each slot's retained WAL from the
// same instance's exporter. A copy, because the facts may be shared through
// the memo.
func withCNPGSlotRetention(facts *CNPGInstanceStatusFacts, retained []CNPGSlotBytes) *CNPGInstanceStatusFacts {
	if len(retained) == 0 || len(facts.Slots) == 0 {
		return facts
	}
	byName := make(map[string]float64, len(retained))
	for _, r := range retained {
		byName[r.Slot] = r.Bytes
	}
	out := *facts
	out.Slots = append([]CNPGSlotStatus(nil), facts.Slots...)
	for i := range out.Slots {
		if b, ok := byName[out.Slots[i].Name]; ok {
			out.Slots[i].RetainedBytes = &b
		}
	}
	return &out
}

// The exporter encodes pool_mode as 1 session, 2 transaction, 3 statement.
var cnpgPgBouncerPoolModes = map[int]string{1: "session", 2: "transaction", 3: "statement"}

func cnpgPoolerFacts(samples map[string][]cnpgSample) (*CNPGPoolerPodFacts, string) {
	type key struct{ db, user string }
	pools := map[key]*CNPGPoolerPool{}
	pool := func(labels map[string]string) *CNPGPoolerPool {
		k := key{labels["database"], labels["user"]}
		if k.db == cnpgPgBouncerAdminDB || k.user == cnpgPgBouncerAuthUser {
			return nil
		}
		p := pools[k]
		if p == nil {
			p = &CNPGPoolerPool{Database: k.db, User: k.user}
			pools[k] = p
		}
		return p
	}
	fields := map[string]func(*CNPGPoolerPool) **float64{
		"cnpg_pgbouncer_pools_cl_active":  func(p *CNPGPoolerPool) **float64 { return &p.ClActive },
		"cnpg_pgbouncer_pools_cl_waiting": func(p *CNPGPoolerPool) **float64 { return &p.ClWaiting },
		"cnpg_pgbouncer_pools_sv_active":  func(p *CNPGPoolerPool) **float64 { return &p.SvActive },
		"cnpg_pgbouncer_pools_sv_idle":    func(p *CNPGPoolerPool) **float64 { return &p.SvIdle },
		"cnpg_pgbouncer_pools_sv_used":    func(p *CNPGPoolerPool) **float64 { return &p.SvUsed },
		"cnpg_pgbouncer_pools_maxwait":    func(p *CNPGPoolerPool) **float64 { return &p.MaxwaitSeconds },
	}
	for name, field := range fields {
		for _, s := range samples[name] {
			if p := pool(s.labels); p != nil {
				v := s.value
				*field(p) = &v
			}
		}
	}
	// PgBouncer splits maxwait into whole seconds and a microsecond part.
	for _, s := range samples["cnpg_pgbouncer_pools_maxwait_us"] {
		if p := pool(s.labels); p != nil && p.MaxwaitSeconds != nil {
			secs := *p.MaxwaitSeconds + s.value/1e6
			p.MaxwaitSeconds = &secs
		}
	}
	for _, s := range samples["cnpg_pgbouncer_pools_pool_mode"] {
		if p := pool(s.labels); p != nil {
			p.PoolMode = cnpgPgBouncerPoolModes[int(s.value)]
		}
	}
	out := make([]CNPGPoolerPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Database != out[j].Database {
			return out[i].Database < out[j].Database
		}
		return out[i].User < out[j].User
	})
	capped := ""
	if len(out) > cnpgRuntimeMaxRows {
		capped = fmt.Sprintf("%d pools; the first %d by database and user are listed", len(out), cnpgRuntimeMaxRows)
		out = out[:cnpgRuntimeMaxRows]
	}
	return &CNPGPoolerPodFacts{Missing: cnpgMissingFamilies(samples, cnpgExpectedPoolerFamilies), Pools: out}, capped
}
