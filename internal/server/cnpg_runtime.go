package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// The only requests the runtime endpoints can make: a GET of these fixed
// paths on these fixed ports of a validated Pod. The instance manager's port
// also serves endpoints that change state (some on GET), so the fixed path and
// the refusal to follow redirects are the security boundary. Nothing of the
// caller's request reaches the proxied URL or its headers.
const (
	cnpgStatusPort        = 8000
	cnpgMetricsPort       = 9187
	cnpgPoolerMetricsPort = 9127
	cnpgStatusPath        = "/pg/status"
	cnpgMetricsPath       = "/metrics"

	cnpgRuntimeRequestTimeout = 5 * time.Second
	cnpgRuntimeConcurrency    = 4
	cnpgRuntimeMaxRows        = 200

	cnpgRuntimeStateOK          = "ok"
	cnpgRuntimeStatePartial     = "partial"
	cnpgRuntimeStateDenied      = "denied"
	cnpgRuntimeStateUnreachable = "unreachable"
	cnpgRuntimeStateError       = "error"

	cnpgPoolerNameLabel      = "cnpg.io/poolerName"
	cnpgPgBouncerContainer   = "pgbouncer"
	cnpgStatusPortTLSFlag    = "--status-port-tls"
	cnpgMetricsPortTLSFlag   = "--metrics-port-tls"
	cnpgMetricsExporterApp   = "cnpg_metrics_exporter"
	cnpgPgBouncerAdminDB     = "pgbouncer"
	cnpgPgBouncerAuthUser    = "cnpg_pooler_pgbouncer"
	cnpgFencedErrorExplained = "instance is fenced: PostgreSQL is stopped on purpose"
)

// Raw-size caps, enforced before parsing, and memo lifetimes sized to the
// frontend's polling (status ~5s, metrics ~30s). Variables so tests can shrink
// them.
var (
	cnpgRuntimeStatusCap  int64 = 1 << 20
	cnpgRuntimeMetricsCap int64 = 4 << 20
	cnpgStatusMemoTTL           = 5 * time.Second
	cnpgMetricsMemoTTL          = 25 * time.Second
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
	"cnpg_backends_max_tx_duration_seconds",
	"cnpg_pg_settings_setting",
	"cnpg_pg_database_size_bytes",
	"cnpg_pg_database_xid_age",
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

type CNPGRuntimeObjectRef struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

type CNPGRuntimePermission struct {
	Proxy string `json:"proxy"`
	Grant string `json:"grant"`
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
	IsPrimary           bool                    `json:"isPrimary"`
	MightBeUnavailable  bool                    `json:"mightBeUnavailable,omitempty"`
	CurrentLsn          string                  `json:"currentLsn,omitempty"`
	ReceivedLsn         string                  `json:"receivedLsn,omitempty"`
	ReplayLsn           string                  `json:"replayLsn,omitempty"`
	Timeline            *int                    `json:"timeline,omitempty"`
	ReplayPaused        bool                    `json:"replayPaused"`
	PendingRestart      bool                    `json:"pendingRestart"`
	IsWalReceiverActive bool                    `json:"isWalReceiverActive"`
	Archiving           CNPGArchivingStatus     `json:"archiving"`
	Replication         []CNPGReplicationStatus `json:"replication"`
	Slots               []CNPGSlotStatus        `json:"slots"`
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
	Missing                       []string              `json:"missing,omitempty"`
	MaxConnections                *float64              `json:"maxConnections,omitempty"`
	Sessions                      []CNPGSessionGroup    `json:"sessions,omitempty"`
	SessionsTotal                 *float64              `json:"sessionsTotal,omitempty"`
	WaitingBackends               *float64              `json:"waitingBackends,omitempty"`
	OldestXactSeconds             *float64              `json:"oldestXactSeconds,omitempty"`
	XidAge                        []CNPGDatabaseValue   `json:"xidAge,omitempty"`
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
	Pod string `json:"pod"`
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

// authorizeCNPGRuntime gates like the logs endpoint — namespace, the owning
// CNPG object, listing Pods — before the object is looked up. pods/proxy is
// deliberately not part of the gate: without it the Kubernetes facts still
// render and each source reports itself denied.
func (s *Server) authorizeCNPGRuntime(w http.ResponseWriter, r *http.Request, namespace, resource string) bool {
	if !s.requireConnected(w) {
		return false
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return false
	}
	if !s.canRead(r, cnpgGroup, resource, namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to "+resource+".postgresql.cnpg.io in namespace "+namespace)
		return false
	}
	if !s.canRead(r, "", "pods", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to pods in namespace "+namespace)
		return false
	}
	return true
}

func cnpgRuntimePermission(namespace string, allowed bool) CNPGRuntimePermission {
	p := CNPGRuntimePermission{Proxy: "allowed", Grant: "get pods/proxy in " + namespace}
	if !allowed {
		p.Proxy = "denied"
	}
	return p
}

func cnpgProxyDenied(namespace string) CNPGRuntimeSource {
	return CNPGRuntimeSource{State: cnpgRuntimeStateDenied, Error: "reading live data needs get pods/proxy in " + namespace}
}

// cnpgRuntimeClient builds the caller's client for the proxy reads: the
// impersonated identity when auth is on (nil when impersonation fails — never
// Radar's own identity), with redirects refused so a proxied answer cannot
// steer the request to another path on the Pod.
func cnpgRuntimeClient(r *http.Request) kubernetes.Interface {
	cfg := k8s.ConfigFromContext(r.Context())
	if cfg == nil {
		return nil
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		log.Printf("[cnpg] Failed to build runtime HTTP client: %v", err)
		return nil
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := kubernetes.NewForConfigAndClient(cfg, &noRedirect)
	if err != nil {
		log.Printf("[cnpg] Failed to build runtime client: %v", err)
		return nil
	}
	return client
}

// handleCNPGClusterRuntime serves GET /api/cnpg/clusters/{namespace}/{name}/runtime:
// each instance's /pg/status and exporter metrics, read through the caller's
// own pods/proxy.
func (s *Server) handleCNPGClusterRuntime(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "instance Pods unavailable: "+err.Error())
		return
	}

	proxyAllowed := s.canReadSubresource(r, "", "pods", "proxy", namespace, "get")
	fenced := parseCNPGFenced(cluster.GetAnnotations()[cnpgFencedAnnotation])
	resp := CNPGClusterRuntimeResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: cnpgRuntimePermission(namespace, proxyAllowed),
		Instances:  make([]CNPGInstanceRuntime, len(pods)),
	}
	for i, p := range pods {
		resp.Instances[i] = CNPGInstanceRuntime{Pod: p.Name, Role: cnpgRuntimeRole(p), Fenced: fenced.fences(p.Name)}
	}
	if len(pods) == 0 {
		s.writeJSON(w, resp)
		return
	}
	if !proxyAllowed {
		for i := range resp.Instances {
			resp.Instances[i].Status.CNPGRuntimeSource = cnpgProxyDenied(namespace)
			resp.Instances[i].Metrics.CNPGRuntimeSource = cnpgProxyDenied(namespace)
		}
		s.writeJSON(w, resp)
		return
	}
	client := cnpgRuntimeClient(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client unavailable")
		return
	}

	clusterMetricsTLS, _, _ := unstructured.NestedBool(cluster.Object, "spec", "monitoring", "tls", "enabled")
	identity := cnpgRuntimeIdentity(r)
	run := newCNPGRuntimeRunner(r.Context())
	for i, p := range pods {
		inst := &resp.Instances[i]
		statusTarget := cnpgProxyTarget{
			namespace: namespace, pod: p.Name, podUID: p.UID, port: cnpgStatusPort, path: cnpgStatusPath,
			scheme: cnpgSchemeFor(cnpgContainerHasFlag(p, cnpgDefaultLogContainer, cnpgStatusPortTLSFlag)), limit: cnpgRuntimeStatusCap,
		}
		metricsTarget := cnpgProxyTarget{
			namespace: namespace, pod: p.Name, podUID: p.UID, port: cnpgMetricsPort, path: cnpgMetricsPath,
			scheme: cnpgSchemeFor(cnpgContainerHasFlag(p, cnpgDefaultLogContainer, cnpgMetricsPortTLSFlag) || clusterMetricsTLS), limit: cnpgRuntimeMetricsCap,
		}
		run.do(func(ctx context.Context) {
			inst.Status = cnpgMemoized(ctx, identity, statusTarget, cnpgStatusMemoTTL, func(ctx context.Context) CNPGInstanceStatus {
				return cnpgInstanceStatusFrom(cnpgProxyGetWithFallback(ctx, client, statusTarget))
			})
		})
		run.do(func(ctx context.Context) {
			inst.Metrics = cnpgMemoized(ctx, identity, metricsTarget, cnpgMetricsMemoTTL, func(ctx context.Context) CNPGInstanceMetrics {
				return cnpgInstanceMetricsFrom(cnpgProxyGetWithFallback(ctx, client, metricsTarget))
			})
		})
	}
	run.wait()

	for i := range resp.Instances {
		inst := &resp.Instances[i]
		if inst.Status.State == cnpgRuntimeStateDenied || inst.Metrics.State == cnpgRuntimeStateDenied {
			resp.Permission.Proxy = "denied"
		}
		if inst.Fenced {
			explainCNPGFenced(&inst.Status.CNPGRuntimeSource)
			explainCNPGFenced(&inst.Metrics.CNPGRuntimeSource)
		}
		if inst.Status.CNPGInstanceStatusFacts != nil && inst.Metrics.CNPGInstanceMetricFacts != nil {
			inst.Status.CNPGInstanceStatusFacts = withCNPGSlotRetention(inst.Status.CNPGInstanceStatusFacts, inst.Metrics.ReplicationSlotsRetainedBytes)
		}
	}
	s.writeJSON(w, resp)
}

// handleCNPGPoolerRuntime serves GET /api/cnpg/poolers/{namespace}/{name}/runtime:
// each pooler Pod's PgBouncer exporter, read through the caller's pods/proxy.
func (s *Server) handleCNPGPoolerRuntime(w http.ResponseWriter, r *http.Request) {
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
	switch {
	case err == nil && pooler != nil:
	case err == nil, errors.Is(err, k8s.ErrUnknownDynamicKind):
		s.writeError(w, http.StatusNotFound, "CloudNativePG Pooler "+namespace+"/"+name+" not found")
		return
	case errors.Is(err, errDynamicNotSynced):
		s.writeError(w, http.StatusServiceUnavailable, "CloudNativePG Poolers are still syncing")
		return
	default:
		log.Printf("[cnpg] Failed to read Pooler %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to read CloudNativePG Pooler")
		return
	}
	pods, err := cnpgPoolerPods(cache, pooler)
	if err != nil {
		log.Printf("[cnpg] Failed to list pooler Pods for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "pooler Pods unavailable: "+err.Error())
		return
	}

	proxyAllowed := s.canReadSubresource(r, "", "pods", "proxy", namespace, "get")
	resp := CNPGPoolerRuntimeResponse{
		Pooler:     CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: pooler.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: cnpgRuntimePermission(namespace, proxyAllowed),
		Pods:       make([]CNPGPoolerPodRuntime, len(pods)),
	}
	for i, p := range pods {
		resp.Pods[i] = CNPGPoolerPodRuntime{Pod: p.Name}
	}
	if len(pods) == 0 {
		s.writeJSON(w, resp)
		return
	}
	if !proxyAllowed {
		for i := range resp.Pods {
			resp.Pods[i].CNPGRuntimeSource = cnpgProxyDenied(namespace)
		}
		s.writeJSON(w, resp)
		return
	}
	client := cnpgRuntimeClient(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client unavailable")
		return
	}

	poolerTLS, _, _ := unstructured.NestedBool(pooler.Object, "spec", "monitoring", "tls", "enabled")
	identity := cnpgRuntimeIdentity(r)
	run := newCNPGRuntimeRunner(r.Context())
	for i, p := range pods {
		out := &resp.Pods[i]
		target := cnpgProxyTarget{
			namespace: namespace, pod: p.Name, podUID: p.UID, port: cnpgPoolerMetricsPort, path: cnpgMetricsPath,
			scheme: cnpgSchemeFor(cnpgContainerHasFlag(p, cnpgPgBouncerContainer, cnpgMetricsPortTLSFlag) || poolerTLS), limit: cnpgRuntimeMetricsCap,
		}
		run.do(func(ctx context.Context) {
			got := cnpgMemoized(ctx, identity, target, cnpgMetricsMemoTTL, func(ctx context.Context) CNPGPoolerPodRuntime {
				return cnpgPoolerPodFrom(cnpgProxyGetWithFallback(ctx, client, target))
			})
			got.Pod = p.Name
			*out = got
		})
	}
	run.wait()
	for _, p := range resp.Pods {
		if p.State == cnpgRuntimeStateDenied {
			resp.Permission.Proxy = "denied"
		}
	}
	s.writeJSON(w, resp)
}

func findCNPGPooler(ctx context.Context, cache *k8s.ResourceCache, namespace, name string) (*unstructured.Unstructured, error) {
	poolers, err := filterCNPGGroup(listDynamicSynced(ctx, cache, "Pooler", cnpgGroup, namespace))
	if err != nil {
		return nil, err
	}
	for _, p := range poolers {
		if p.GetNamespace() == namespace && p.GetName() == name && p.GroupVersionKind().Group == cnpgGroup {
			return p, nil
		}
	}
	return nil, nil
}

// cnpgPoolerPods returns the Pods of the Pooler's Deployment, validated along
// the controller chain Pooler → Deployment (named after the Pooler) →
// ReplicaSet → Pod by UID. The poolerName label alone is something any Pod
// can carry.
func cnpgPoolerPods(cache *k8s.ResourceCache, pooler *unstructured.Unstructured) ([]*corev1.Pod, error) {
	podLister, rsLister, deployLister := cache.Pods(), cache.ReplicaSets(), cache.Deployments()
	if podLister == nil || rsLister == nil || deployLister == nil {
		return nil, errors.New("pod, ReplicaSet or Deployment cache unavailable")
	}
	namespace, name := pooler.GetNamespace(), pooler.GetName()
	deploy, err := deployLister.Deployments(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return []*corev1.Pod{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !cnpgControlledBy(deploy.OwnerReferences, cnpgGroup, "Pooler", name, pooler.GetUID()) {
		return []*corev1.Pod{}, nil
	}
	candidates, err := podLister.Pods(namespace).List(labels.SelectorFromSet(labels.Set{cnpgPoolerNameLabel: name}))
	if err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, 0, len(candidates))
	for _, p := range candidates {
		ref := cnpgControllerRef(p.OwnerReferences)
		if ref == nil || ref.Kind != "ReplicaSet" {
			continue
		}
		rs, err := rsLister.ReplicaSets(namespace).Get(ref.Name)
		if err != nil || rs.UID != ref.UID {
			continue
		}
		if cnpgControlledBy(rs.OwnerReferences, "apps", "Deployment", deploy.Name, deploy.UID) {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func cnpgControllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func cnpgControlledBy(refs []metav1.OwnerReference, group, kind, name string, uid types.UID) bool {
	ref := cnpgControllerRef(refs)
	if ref == nil || ref.Kind != kind || ref.Name != name || ref.UID != uid {
		return false
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	return err == nil && gv.Group == group
}

func cnpgRuntimeRole(p *corev1.Pod) string {
	switch role := cnpgInstanceRole(p); role {
	case "primary", "replica":
		return role
	default:
		return "unknown"
	}
}

func explainCNPGFenced(src *CNPGRuntimeSource) {
	if src.State == cnpgRuntimeStateUnreachable || src.State == cnpgRuntimeStateError {
		src.Error = cnpgFencedErrorExplained + " (" + src.Error + ")"
	}
}

// cnpgContainerHasFlag reports whether the running Pod's container was
// started with a flag — how the operator turns TLS on for a port.
func cnpgContainerHasFlag(p *corev1.Pod, container, flag string) bool {
	for _, c := range p.Spec.Containers {
		if c.Name != container {
			continue
		}
		for _, words := range [][]string{c.Command, c.Args} {
			for _, w := range words {
				if w == flag {
					return true
				}
			}
		}
	}
	return false
}

func cnpgSchemeFor(tls bool) string {
	if tls {
		return "https"
	}
	return "http"
}

// cnpgRuntimeRunner runs proxy reads with bounded concurrency under the
// request's context.
type cnpgRuntimeRunner struct {
	ctx context.Context
	sem chan struct{}
	wg  sync.WaitGroup
}

func newCNPGRuntimeRunner(ctx context.Context) *cnpgRuntimeRunner {
	return &cnpgRuntimeRunner{ctx: ctx, sem: make(chan struct{}, cnpgRuntimeConcurrency)}
}

func (r *cnpgRuntimeRunner) do(fn func(ctx context.Context)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-r.ctx.Done():
		}
		// With the request gone, fn's reads fail fast on the cancelled context.
		fn(r.ctx)
	}()
}

func (r *cnpgRuntimeRunner) wait() { r.wg.Wait() }

// The memo collapses repeated reads of one endpoint by one identity — several
// viewers, a fast refresh — into one scrape per lifetime. The key includes the
// kube context and the Pod UID, so a context switch or a recreated Pod never
// serves a stale answer, and the identity, so one caller's answer never
// reaches another.
var (
	cnpgRuntimeMemoMu      sync.Mutex
	cnpgRuntimeMemoEntries = map[string]cnpgRuntimeMemoEntry{}
	cnpgRuntimeMemoGroup   singleflight.Group
)

const cnpgRuntimeMemoMaxEntries = 4096

type cnpgRuntimeMemoEntry struct {
	value   any
	expires time.Time
}

func cnpgRuntimeIdentity(r *http.Request) string {
	id := k8s.GetContextName()
	if user := auth.UserFromContext(r.Context()); user != nil {
		groups := append([]string(nil), user.Groups...)
		sort.Strings(groups)
		id += "\x00" + user.Username + "\x00" + strings.Join(groups, ",")
	}
	return id
}

func cnpgMemoized[T any](ctx context.Context, identity string, target cnpgProxyTarget, ttl time.Duration, fetch func(context.Context) T) T {
	key := fmt.Sprintf("%s\x00%s/%s\x00%s\x00%s:%d%s", identity, target.namespace, target.pod, target.podUID, target.scheme, target.port, target.path)
	now := time.Now()
	cnpgRuntimeMemoMu.Lock()
	if e, ok := cnpgRuntimeMemoEntries[key]; ok && now.Before(e.expires) {
		cnpgRuntimeMemoMu.Unlock()
		return e.value.(T)
	}
	cnpgRuntimeMemoMu.Unlock()

	v, _, _ := cnpgRuntimeMemoGroup.Do(key, func() (any, error) {
		got := fetch(ctx)
		// A read cut short by the caller going away says nothing about the Pod.
		if ctx.Err() == nil {
			cnpgRuntimeMemoMu.Lock()
			if len(cnpgRuntimeMemoEntries) >= cnpgRuntimeMemoMaxEntries {
				pruneCNPGRuntimeMemoLocked(time.Now())
			}
			if len(cnpgRuntimeMemoEntries) < cnpgRuntimeMemoMaxEntries {
				cnpgRuntimeMemoEntries[key] = cnpgRuntimeMemoEntry{value: got, expires: time.Now().Add(ttl)}
			}
			cnpgRuntimeMemoMu.Unlock()
		}
		return got, nil
	})
	return v.(T)
}

func pruneCNPGRuntimeMemoLocked(now time.Time) {
	for k, e := range cnpgRuntimeMemoEntries {
		if !now.Before(e.expires) {
			delete(cnpgRuntimeMemoEntries, k)
		}
	}
}

type cnpgProxyTarget struct {
	namespace, pod string
	podUID         types.UID
	port           int
	path           string
	scheme         string
	limit          int64
}

type cnpgProxyOutcome struct {
	state      string
	err        string
	scheme     string
	capturedAt string
	body       []byte
	// truncated: the answer exceeded the cap and body holds only its first
	// limit bytes.
	truncated bool
	// schemeMismatch: the failure was the wrong protocol on the port, the only
	// failure that justifies trying the other scheme.
	schemeMismatch bool
}

func (o cnpgProxyOutcome) source() CNPGRuntimeSource {
	return CNPGRuntimeSource{State: o.state, Error: o.err, Scheme: o.scheme, CapturedAt: o.capturedAt}
}

// cnpgProxyGetWithFallback tries the declared scheme and, only when that
// failed as a protocol mismatch, the other one once: a Pod created by an older
// operator may disagree with what its object declares today.
func cnpgProxyGetWithFallback(ctx context.Context, client kubernetes.Interface, t cnpgProxyTarget) cnpgProxyOutcome {
	first := cnpgProxyGet(ctx, client, t, t.scheme)
	if !first.schemeMismatch {
		return first
	}
	other := "https"
	if t.scheme == "https" {
		other = "http"
	}
	second := cnpgProxyGet(ctx, client, t, other)
	if second.schemeMismatch {
		second.state = cnpgRuntimeStateUnreachable
		second.err = fmt.Sprintf("neither http nor https worked on port %d: %s", t.port, first.err)
		second.scheme = ""
	}
	return second
}

// cnpgProxyGet issues one GET through the apiserver's pods/proxy, built only
// from the validated Pod and the fixed port and path.
func cnpgProxyGet(ctx context.Context, client kubernetes.Interface, t cnpgProxyTarget, scheme string) cnpgProxyOutcome {
	ctx, cancel := context.WithTimeout(ctx, cnpgRuntimeRequestTimeout)
	defer cancel()
	out := cnpgProxyOutcome{scheme: scheme, capturedAt: time.Now().UTC().Format(time.RFC3339)}
	stream, err := client.CoreV1().RESTClient().Get().
		Namespace(t.namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%s:%d", scheme, t.pod, t.port)).
		SubResource("proxy").
		Suffix(t.path).
		Stream(ctx)
	if err != nil {
		return classifyCNPGProxyError(ctx, err, out)
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, t.limit+1))
	if err != nil {
		return classifyCNPGProxyError(ctx, err, out)
	}
	if int64(len(body)) > t.limit {
		out.body, out.truncated = body[:t.limit], true
	} else {
		out.body = body
	}
	out.state = cnpgRuntimeStateOK
	return out
}

// What the apiserver relays when the scheme is wrong, in either direction.
// Certificate-verification failures are deliberately absent: those are a
// verdict on the TLS setup, not a sign the port speaks plain HTTP.
var cnpgSchemeMismatchHints = []string{
	"http: server gave http response to https client",
	"client sent an http request to an https server",
	"first record does not look like a tls handshake",
	"malformed http response",
}

func classifyCNPGProxyError(ctx context.Context, err error, out cnpgProxyOutcome) cnpgProxyOutcome {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		out.state, out.err = cnpgRuntimeStateUnreachable, fmt.Sprintf("no answer within %s", cnpgRuntimeRequestTimeout)
		return out
	}
	if errors.Is(err, context.Canceled) {
		out.state, out.err = cnpgRuntimeStateError, "request cancelled"
		return out
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	// The apiserver's own refusal names the pods/proxy subresource, which an
	// answer relayed from the Pod never does.
	if apierrors.IsForbidden(err) && strings.Contains(lower, "proxy") {
		out.state, out.err = cnpgRuntimeStateDenied, "the apiserver denied get pods/proxy"
		return out
	}
	code := 0
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code = int(status.Status().Code)
	}
	out.err = truncateCNPGRuntimeError(msg)
	certificate := strings.Contains(lower, "x509") || strings.Contains(lower, "certificate")
	if !certificate {
		// A plain request against a TLS port comes back as a bare 400: nothing
		// else makes a GET of these fixed paths a bad request.
		out.schemeMismatch = code == http.StatusBadRequest
		for _, hint := range cnpgSchemeMismatchHints {
			if strings.Contains(lower, hint) {
				out.schemeMismatch = true
			}
		}
	}
	switch {
	case code >= 300 && code < 400:
		out.state, out.err = cnpgRuntimeStateError, fmt.Sprintf("the Pod answered with a redirect (%d), which is not followed", code)
		out.schemeMismatch = false
	case out.schemeMismatch, code == 0, code >= 500:
		out.state = cnpgRuntimeStateUnreachable
	default:
		out.state = cnpgRuntimeStateError
	}
	return out
}

func truncateCNPGRuntimeError(s string) string {
	const limit = 300
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func formatCNPGByteCap(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}

// cnpgPgStatus is the subset of the instance manager's GET /pg/status answer
// (CloudNativePG's PostgresqlStatus) the runtime view reads.
type cnpgPgStatus struct {
	IsPrimary           *bool  `json:"isPrimary"`
	MightBeUnavailable  bool   `json:"mightBeUnavailable"`
	CurrentLsn          string `json:"currentLsn"`
	ReceivedLsn         string `json:"receivedLsn"`
	ReplayLsn           string `json:"replayLsn"`
	TimeLineID          *int   `json:"timeLineID"`
	ReplayPaused        bool   `json:"replayPaused"`
	PendingRestart      bool   `json:"pendingRestart"`
	IsWalReceiverActive bool   `json:"isWalReceiverActive"`
	LastArchivedWAL     string `json:"lastArchivedWAL"`
	LastArchivedWALTime string `json:"lastArchivedWALTime"`
	LastFailedWAL       string `json:"lastFailedWAL"`
	LastFailedWALTime   string `json:"lastFailedWALTime"`
	ReadyWalFiles       *int   `json:"readyWalFiles"`
	ReplicationInfo     []struct {
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
		SlotType    string `json:"slotType"`
		Database    string `json:"database"`
		Active      bool   `json:"active"`
		RestartLsn  string `json:"restartLsn"`
		WalStatus   string `json:"walStatus"`
		SafeWalSize *int64 `json:"safeWalSize"`
	} `json:"replicationSlotsInfo"`
}

func cnpgInstanceStatusFrom(out cnpgProxyOutcome) CNPGInstanceStatus {
	src := out.source()
	if out.state != cnpgRuntimeStateOK {
		return CNPGInstanceStatus{CNPGRuntimeSource: src}
	}
	if out.truncated {
		src.State, src.Error = cnpgRuntimeStateError, fmt.Sprintf("status answer larger than the %s this view reads", formatCNPGByteCap(cnpgRuntimeStatusCap))
		return CNPGInstanceStatus{CNPGRuntimeSource: src}
	}
	facts, partial, err := parseCNPGPgStatus(out.body)
	if err != nil {
		src.State, src.Error = cnpgRuntimeStateError, err.Error()
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
		Archiving: CNPGArchivingStatus{
			LastArchivedWal: st.LastArchivedWAL,
			LastArchivedAt:  cnpgStatusTime(st.LastArchivedWALTime),
			LastFailedWal:   st.LastFailedWAL,
			LastFailedAt:    cnpgStatusTime(st.LastFailedWALTime),
			ReadyWalFiles:   st.ReadyWalFiles,
		},
		Replication: []CNPGReplicationStatus{},
		Slots:       []CNPGSlotStatus{},
	}
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
			Name: si.SlotName, Type: si.SlotType, Active: si.Active, Database: si.Database,
			RestartLsn: si.RestartLsn, WalStatus: si.WalStatus, SafeWalSize: si.SafeWalSize,
		})
	}
	return facts, strings.Join(partial, "; "), nil
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
	if out.state != cnpgRuntimeStateOK {
		return CNPGInstanceMetrics{CNPGRuntimeSource: src}
	}
	samples, reason, err := cnpgPromText(out)
	if err != nil {
		src.State, src.Error = cnpgRuntimeStateError, "exporter answer was not Prometheus text: "+truncateCNPGRuntimeError(err.Error())
		return CNPGInstanceMetrics{CNPGRuntimeSource: src}
	}
	facts, capped := cnpgInstanceMetricFacts(samples)
	if reason != "" || capped != "" {
		src.State, src.Reason = cnpgRuntimeStatePartial, cnpgJoinReasons(reason, capped)
	}
	return CNPGInstanceMetrics{CNPGRuntimeSource: src, CNPGInstanceMetricFacts: facts}
}

func cnpgPoolerPodFrom(out cnpgProxyOutcome) CNPGPoolerPodRuntime {
	src := out.source()
	if out.state != cnpgRuntimeStateOK {
		return CNPGPoolerPodRuntime{CNPGRuntimeSource: src}
	}
	samples, reason, err := cnpgPromText(out)
	if err != nil {
		src.State, src.Error = cnpgRuntimeStateError, "exporter answer was not Prometheus text: "+truncateCNPGRuntimeError(err.Error())
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
		Missing:           cnpgMissingFamilies(samples, cnpgExpectedInstanceFamilies),
		MaxConnections:    cnpgSingle(samples, "cnpg_pg_settings_setting", map[string]string{"name": "max_connections"}),
		WaitingBackends:   cnpgSingle(samples, "cnpg_backends_waiting_total", nil),
		WalBytes:          cnpgSingle(samples, "cnpg_collector_pg_wal", map[string]string{"value": "size"}),
		WalSegments:       cnpgSingle(samples, "cnpg_collector_pg_wal", map[string]string{"value": "count"}),
		XactCommitTotal:   cnpgSum(samples, "cnpg_pg_stat_database_xact_commit"),
		XactRollbackTotal: cnpgSum(samples, "cnpg_pg_stat_database_xact_rollback"),
		BlksHit:           cnpgSum(samples, "cnpg_pg_stat_database_blks_hit"),
		BlksRead:          cnpgSum(samples, "cnpg_pg_stat_database_blks_read"),
		DeadlocksTotal:    cnpgSum(samples, "cnpg_pg_stat_database_deadlocks"),
	}
	var capped []string

	if backends, ok := samples["cnpg_backends_total"]; ok {
		total := 0.0
		for _, s := range backends {
			if cnpgIsPlatformSession(s.labels) || s.labels["state"] == "" || s.value <= 0 {
				continue
			}
			total += s.value
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
