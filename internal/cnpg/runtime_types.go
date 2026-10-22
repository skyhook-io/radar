package cnpg

import (
	"k8s.io/apimachinery/pkg/types"

	auth "github.com/skyhook-io/radar/internal/auth"
)

type CNPGRuntimeObjectRef struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

type CNPGRuntimePermission struct {
	Proxy string      `json:"proxy"`
	Grant *auth.Grant `json:"grant,omitempty"`
}

// CNPGRuntimeSource describes one read. State is ok | partial | denied |
// unreachable | error; Reason explains partial, Error explains the failures.
// CapturedAt is when the answer was read, which can precede the response's
// sampledAt by up to the memo lifetime.
type CNPGRuntimeSource struct {
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Scheme     string `json:"scheme,omitempty"`
	CapturedAt string `json:"capturedAt,omitempty"`
}

// CNPGClusterRuntimeResponse is GET /api/cnpg/clusters/{namespace}/{name}/runtime.
type CNPGClusterRuntimeResponse struct {
	Cluster    CNPGRuntimeObjectRef  `json:"cluster"`
	SampledAt  string                `json:"sampledAt"`
	Permission CNPGRuntimePermission `json:"permission"`
	Instances  []CNPGInstanceRuntime `json:"instances"`
}

type CNPGInstanceRuntime struct {
	Pod     string              `json:"pod"`
	PodUID  types.UID           `json:"podUID,omitempty"`
	Role    string              `json:"role"`
	Fenced  bool                `json:"fenced,omitempty"`
	Status  CNPGInstanceStatus  `json:"status"`
	Metrics CNPGInstanceMetrics `json:"metrics"`
}

// CNPGInstanceStatus carries facts only when the read succeeded; the embedded
// pointer keeps every fact out of the JSON otherwise, so an unavailable source
// can never read as zeros.
type CNPGInstanceStatus struct {
	CNPGRuntimeSource
	*CNPGInstanceStatusFacts
}

type CNPGInstanceStatusFacts struct {
	IsPrimary          bool   `json:"isPrimary"`
	MightBeUnavailable bool   `json:"mightBeUnavailable,omitempty"`
	CurrentLsn         string `json:"currentLsn,omitempty"`
	ReceivedLsn        string `json:"receivedLsn,omitempty"`
	ReplayLsn          string `json:"replayLsn,omitempty"`
	Timeline           *int   `json:"timeline,omitempty"`
	ReplayPaused       bool   `json:"replayPaused"`
	PendingRestart     bool   `json:"pendingRestart"`
	// PendingRestartForDecrease: the pending change lowers a setting a standby
	// must match, so the primary restarts before its standbys.
	PendingRestartForDecrease  bool   `json:"pendingRestartForDecrease"`
	IsWalReceiverActive        bool   `json:"isWalReceiverActive"`
	IsPgRewindRunning          bool   `json:"isPgRewindRunning"`
	InstanceManagerVersion     string `json:"instanceManagerVersion,omitempty"`
	IsInstanceManagerUpgrading bool   `json:"isInstanceManagerUpgrading,omitempty"`
	// RoleDetail is derived only from the instance's own report: primary |
	// pgRewind | replayPaused | streaming | fileBased.
	RoleDetail string `json:"roleDetail"`
	// Archiving is absent when the report did not reach pg_stat_archiver.
	Archiving      *CNPGArchivingStatus    `json:"archiving,omitempty"`
	Replication    []CNPGReplicationStatus `json:"replication"`
	Slots          []CNPGSlotStatus        `json:"slots"`
	SlotsTruncated bool                    `json:"slotsTruncated,omitempty"`
	// BaseBackups are pg_basebackup streams from this instance to a joining
	// instance: CloudNativePG's probe reads pg_stat_progress_basebackup only
	// for application names ending in "-join" (its join Job), so these are new
	// replicas being cloned, never Backup objects. Empty = none running.
	BaseBackups []CNPGBaseBackupStatus `json:"baseBackups"`
	// Incomplete: the instance manager answered without finishing its reads
	// (it masks errors while PostgreSQL may be unavailable, and reads nothing
	// while pg_rewind runs). Lists it did not fill are then null, not empty,
	// and pendingRestart and a standby's roleDetail are not established.
	Incomplete  bool   `json:"incomplete,omitempty"`
	MaskedError string `json:"maskedError,omitempty"`
}

// CNPGBaseBackupStatus is one pg_stat_progress_basebackup row. TotalBytes is
// absent when PostgreSQL has no estimate yet (waiting for a checkpoint, or
// estimation disabled), so progress is then unknown rather than 0 %.
type CNPGBaseBackupStatus struct {
	ApplicationName     string `json:"applicationName"`
	Instance            string `json:"instance,omitempty"`
	Phase               string `json:"phase"`
	StartedAt           string `json:"startedAt,omitempty"`
	TotalBytes          *int64 `json:"totalBytes,omitempty"`
	StreamedBytes       int64  `json:"streamedBytes"`
	TablespacesTotal    int64  `json:"tablespacesTotal"`
	TablespacesStreamed int64  `json:"tablespacesStreamed"`
}

// CNPGArchivingStatus times are RFC3339; the instance manager's "-infinity"
// (never) is omitted.
type CNPGArchivingStatus struct {
	LastArchivedWal string `json:"lastArchivedWal,omitempty"`
	LastArchivedAt  string `json:"lastArchivedAt,omitempty"`
	LastFailedWal   string `json:"lastFailedWal,omitempty"`
	LastFailedAt    string `json:"lastFailedAt,omitempty"`
	ReadyWalFiles   *int   `json:"readyWalFiles,omitempty"`
}

// CNPGReplicationStatus lags are seconds when the PostgreSQL interval parses;
// the Raw fields always carry what the instance manager said.
type CNPGReplicationStatus struct {
	ApplicationName string   `json:"applicationName"`
	State           string   `json:"state,omitempty"`
	SyncState       string   `json:"syncState,omitempty"`
	SyncPriority    *int     `json:"syncPriority,omitempty"`
	WriteLag        *float64 `json:"writeLag,omitempty"`
	WriteLagRaw     string   `json:"writeLagRaw,omitempty"`
	FlushLag        *float64 `json:"flushLag,omitempty"`
	FlushLagRaw     string   `json:"flushLagRaw,omitempty"`
	ReplayLag       *float64 `json:"replayLag,omitempty"`
	ReplayLagRaw    string   `json:"replayLagRaw,omitempty"`
	SentLsn         string   `json:"sentLsn,omitempty"`
	WriteLsn        string   `json:"writeLsn,omitempty"`
	FlushLsn        string   `json:"flushLsn,omitempty"`
	ReplayLsn       string   `json:"replayLsn,omitempty"`
}

type CNPGSlotStatus struct {
	Name          string   `json:"name"`
	Type          string   `json:"type,omitempty"`
	Plugin        string   `json:"plugin,omitempty"`
	Active        bool     `json:"active"`
	Database      string   `json:"database,omitempty"`
	RestartLsn    string   `json:"restartLsn,omitempty"`
	WalStatus     string   `json:"walStatus,omitempty"`
	SafeWalSize   *int64   `json:"safeWalSize,omitempty"`
	RetainedBytes *float64 `json:"retainedBytes,omitempty"`
}

type CNPGInstanceMetrics struct {
	CNPGRuntimeSource
	*CNPGInstanceMetricFacts
}

// CNPGInstanceMetricFacts are this instance's own figures — never summed
// across instances. A measurement whose family the exporter did not report is
// absent and its family is listed in Missing. SessionsTotal counts every
// non-platform session even when Sessions is capped.
type CNPGInstanceMetricFacts struct {
	Missing        []string `json:"missing,omitempty"`
	MaxConnections *float64 `json:"maxConnections,omitempty"`
	// PostmasterStartTime is epoch seconds: an in-place PostgreSQL restart
	// moves it while the container keeps running.
	PostmasterStartTime *float64            `json:"postmasterStartTime,omitempty"`
	Sessions            []CNPGSessionGroup  `json:"sessions,omitempty"`
	SessionsTotal       *float64            `json:"sessionsTotal,omitempty"`
	WaitingBackends     *float64            `json:"waitingBackends,omitempty"`
	OldestXactSeconds   *float64            `json:"oldestXactSeconds,omitempty"`
	XidAge              []CNPGDatabaseValue `json:"xidAge,omitempty"`
	MxidAge             []CNPGDatabaseValue `json:"mxidAge,omitempty"`
	// ExtensionUpdates lists installed extensions whose installed_version is
	// not the default_version; nil when the family was not exported (see
	// Missing), empty when every installed extension is current.
	ExtensionUpdates              []CNPGExtensionUpdate `json:"extensionUpdates"`
	DatabaseSizes                 []CNPGDatabaseBytes   `json:"databaseSizes,omitempty"`
	Archiver                      *CNPGArchiverCounters `json:"archiver,omitempty"`
	WalBytes                      *float64              `json:"walBytes,omitempty"`
	WalSegments                   *float64              `json:"walSegments,omitempty"`
	ReplicationSlotsRetainedBytes []CNPGSlotBytes       `json:"replicationSlotsRetainedBytes,omitempty"`
	XactCommitTotal               *float64              `json:"xactCommitTotal,omitempty"`
	XactRollbackTotal             *float64              `json:"xactRollbackTotal,omitempty"`
	BlksHit                       *float64              `json:"blksHit,omitempty"`
	BlksRead                      *float64              `json:"blksRead,omitempty"`
	DeadlocksTotal                *float64              `json:"deadlocksTotal,omitempty"`
	TempBytesTotal                *float64              `json:"tempBytesTotal,omitempty"`
	// LastUpdateTimestamp is when the exporter last ran its queries (epoch
	// seconds, cnpg_last_update_timestamp). From 1.30 query results are cached
	// for monitoring.metricsQueriesTTL, so counters change only between
	// generations; absent on operators that do not publish it.
	LastUpdateTimestamp *float64 `json:"lastUpdateTimestamp,omitempty"`
	// SessionsByState sums client sessions (platform users excluded) per
	// pg_stat_activity state over every group, before Sessions is capped.
	SessionsByState map[string]float64 `json:"sessionsByState,omitempty"`
	// Databases are pg_stat_database counters per datname (cumulative since
	// the statistics were last reset), for per-database ratios.
	Databases   []CNPGDatabaseStats     `json:"databases,omitempty"`
	Checkpoints *CNPGCheckpointCounters `json:"checkpoints,omitempty"`
}

type CNPGDatabaseStats struct {
	Database     string   `json:"database"`
	XactCommit   *float64 `json:"xactCommit,omitempty"`
	XactRollback *float64 `json:"xactRollback,omitempty"`
	TempFiles    *float64 `json:"tempFiles,omitempty"`
	TempBytes    *float64 `json:"tempBytes,omitempty"`
	Deadlocks    *float64 `json:"deadlocks,omitempty"`
	BlksHit      *float64 `json:"blksHit,omitempty"`
	BlksRead     *float64 `json:"blksRead,omitempty"`
}

// CNPGCheckpointCounters are cumulative counters from pg_stat_checkpointer
// (PostgreSQL 17+) or pg_stat_bgwriter (before 17), whichever the exporter
// serves. Restartpoints are counted separately only from 17; before that a
// standby counts its restartpoints as checkpoints.
type CNPGCheckpointCounters struct {
	Source                 string   `json:"source"`
	Timed                  *float64 `json:"timed,omitempty"`
	Requested              *float64 `json:"requested,omitempty"`
	RestartpointsTimed     *float64 `json:"restartpointsTimed,omitempty"`
	RestartpointsRequested *float64 `json:"restartpointsRequested,omitempty"`
	RestartpointsDone      *float64 `json:"restartpointsDone,omitempty"`
	BuffersWritten         *float64 `json:"buffersWritten,omitempty"`
}

type CNPGSessionGroup struct {
	State       string  `json:"state"`
	Database    string  `json:"database"`
	User        string  `json:"user"`
	Application string  `json:"application"`
	Count       float64 `json:"count"`
}

type CNPGDatabaseValue struct {
	Database string  `json:"database"`
	Age      float64 `json:"age"`
}

type CNPGExtensionUpdate struct {
	Database         string `json:"database"`
	Extension        string `json:"extension"`
	InstalledVersion string `json:"installedVersion"`
	DefaultVersion   string `json:"defaultVersion"`
}

type CNPGDatabaseBytes struct {
	Database string  `json:"database"`
	Bytes    float64 `json:"bytes"`
}

type CNPGSlotBytes struct {
	Slot  string  `json:"slot"`
	Bytes float64 `json:"bytes"`
}

// CNPGArchiverCounters seconds-since fields are absent when the event never
// happened (the exporter reports -1).
type CNPGArchiverCounters struct {
	ArchivedCount            *float64 `json:"archivedCount,omitempty"`
	FailedCount              *float64 `json:"failedCount,omitempty"`
	SecondsSinceLastArchival *float64 `json:"secondsSinceLastArchival,omitempty"`
	SecondsSinceLastFailure  *float64 `json:"secondsSinceLastFailure,omitempty"`
}

// CNPGPoolerRuntimeResponse is GET /api/cnpg/poolers/{namespace}/{name}/runtime.
type CNPGPoolerRuntimeResponse struct {
	Pooler     CNPGRuntimeObjectRef   `json:"pooler"`
	SampledAt  string                 `json:"sampledAt"`
	Permission CNPGRuntimePermission  `json:"permission"`
	Pods       []CNPGPoolerPodRuntime `json:"pods"`
}

type CNPGPoolerPodRuntime struct {
	Pod              string `json:"pod"`
	SchedulingReason string `json:"schedulingReason,omitempty"`
	CNPGRuntimeSource
	*CNPGPoolerPodFacts
}

// CNPGPoolerPodFacts excludes PgBouncer's admin pool and the operator's
// auth_query pool.
type CNPGPoolerPodFacts struct {
	Missing []string         `json:"missing,omitempty"`
	Pools   []CNPGPoolerPool `json:"pools"`
}

type CNPGPoolerPool struct {
	Database       string   `json:"database"`
	User           string   `json:"user"`
	ClActive       *float64 `json:"clActive,omitempty"`
	ClWaiting      *float64 `json:"clWaiting,omitempty"`
	SvActive       *float64 `json:"svActive,omitempty"`
	SvIdle         *float64 `json:"svIdle,omitempty"`
	SvUsed         *float64 `json:"svUsed,omitempty"`
	MaxwaitSeconds *float64 `json:"maxwaitSeconds,omitempty"`
	// PoolMode is the mode PgBouncer reports it is using for this pool.
	PoolMode string `json:"poolMode,omitempty"`
}
