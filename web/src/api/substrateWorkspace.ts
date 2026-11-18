import { useMutation, useQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { getApiBase } from './config'
import { ApiError, apiFetch, fetchJSON, useClusterInfo, useRadarFeature } from './client'
import { httpStatusMessage, readErrorBody } from './httpErrors'

// Agent Substrate workspace: /api/substrate/*. Wire shapes mirror
// pkg/substrate (read model, derivations) and
// internal/server/substrate_workspace_handlers.go.
//
// What an absent field means depends on the field, and each says which:
// - an object or a measurement (a count, a duration, a reading): unknown,
//   not reported or not read, never zero;
// - a list or map on something Substrate returned: empty (snapshots: none;
//   a workerSelector: matches every pool), and a template's cpu/memory: 0;
// - a boolean: false;
// - a list a response builds from reads (rows, assigned actors, atespaces,
//   freeSlots): [] when read and empty, absent when not read.
//
// The API tier (actors, templates, workers) is served only by a local Radar on
// a private listener; everywhere else apiTier is disabled with code
// sharedRadar, and the API-tier endpoints answer 403 with that code.

export type SubstrateActorState =
  | 'running'
  | 'suspended'
  | 'resuming'
  | 'suspending'
  | 'reverting'
  | 'crashed'
  | 'deleting'
  | 'unknown'

/**
 * connected: a snapshot was read (it may be stale: see refreshing/lastError).
 * connecting: settings are saved and the first read is still running; status
 * never waits for it. notConfigured: no identity saved for this context.
 * disabled: this Radar does not read Substrate's API for this request (code
 * sharedRadar, or ownersOnly in Radar Hub below org owner). failed: settings
 * exist, or a Hub owner's token could not be minted (mintDenied), but no
 * usable snapshot was read.
 */
export type SubstrateTierState = 'connected' | 'connecting' | 'notConfigured' | 'disabled' | 'failed'

/** Shared by apiTier.code, coverage codes and error_code on error responses. */
export type SubstrateCode =
  | 'sharedRadar'
  /** Radar Hub: only org owners read Substrate's API for now (403). */
  | 'ownersOnly'
  /** Radar Hub: the owner's own Kubernetes access cannot create a token for ate-system/ate-client (403). */
  | 'mintDenied'
  | 'notConfigured'
  | 'targetChanged'
  | 'notInstalled'
  | 'tls'
  | 'unreachable'
  | 'unauthenticated'
  | 'denied'
  | 'notFound'
  | 'capExceeded'
  /** Substrate would not list atespaces; Radar read only the settings' atespaces, others are unknown. */
  | 'scoped'
  /** The cluster connection or the settings changed during the read; safe to retry. */
  | 'contextChanged'
  | 'failed'
  /** The submitted connection settings are invalid (PUT /connection, 400). */
  | 'invalidSettings'
  /** Radar could not read or write ~/.radar/substrate.json. */
  | 'settingsFile'
  /** The worker Pod's log could not be read (the logs endpoint only, 502). */
  | 'logsUnavailable'
  /** Radar read too little to say whether the WorkerPool exists (pool detail, 503). */
  | 'incomplete'

export interface SubstrateIdentity {
  /**
   * shared: ate-system/ate-client, minted with Radar's Kubernetes access. sharedAsCaller: the same
   * identity, minted with the requesting Hub owner's own access; implicit, nothing to save or edit.
   */
  mode: 'shared' | 'tokenFile' | 'sharedAsCaller'
  /** Who Substrate sees. Absent for tokenFile: Substrate does not tell a client who its token is. */
  principal?: string
}

export interface SubstrateAPITier {
  state: SubstrateTierState
  /** Why, in a sentence; absent when connected. */
  reason?: string
  /**
   * With notConfigured (notConfigured), disabled (sharedRadar) and failed
   * (targetChanged, contextChanged, notInstalled, tls, unreachable,
   * unauthenticated, settingsFile, failed); absent when connected or connecting.
   */
  code?: SubstrateCode
  /** Set once settings are saved. */
  identity?: SubstrateIdentity
  endpoint?: string
  /**
   * RFC 3339. The read that produced the snapshot served ran from
   * readStartedAt to readAt (its completion); its counts were observed over
   * that interval, not at one instant. Absent when no read has completed.
   */
  readStartedAt?: string
  readAt?: string
  /** The snapshot served is older than ~5s and a newer read is running, or the newest one failed (lastError). Served for up to 2 min. */
  refreshing?: boolean
  /** The newest read's failure while an older snapshot is served. */
  lastError?: { code: SubstrateCode; message: string; at: string }
}

export interface SubstrateCoverage {
  /**
   * full: every page of every scope (the same word as KindCoverage). partial:
   * some atespaces denied, unread or not named, or the budget reached.
   */
  state: 'full' | 'partial' | 'denied' | 'failed'
  code?: SubstrateCode
  message?: string
  /**
   * The atespaces read one by one: in a snapshot when the list across
   * atespaces was denied; on /actors?atespace= the one read directly. Listed
   * is not complete: with code capExceeded the budget may have cut any short.
   */
  readAtespaces?: string[]
  deniedAtespaces?: string[]
  /** Atespaces not read because the read's budget ran out first; what they hold is unknown. */
  unreadAtespaces?: string[]
  /** Items read; absent when none could be. */
  count?: number
  /** The limit reached, with code capExceeded. */
  cap?: number
}

export interface SubstrateSnapshotCoverage {
  actors: SubstrateCoverage
  templates: SubstrateCoverage
  workers: SubstrateCoverage
  atespaces: SubstrateCoverage
}

/**
 * The Kubernetes side: WorkerPools as the caller may list them, in the shape
 * every workspace serves per kind (integration.KindCoverage). Only full and
 * notInstalled are complete enough to say a pool does not exist; syncing,
 * uncached, denied and error mean the pools are unknown, not none.
 */
export interface SubstratePoolsCoverage {
  state: 'full' | 'partial' | 'denied' | 'uncached' | 'notInstalled' | 'syncing' | 'error'
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  /** Set on partial: the namespaces read. */
  allowedNamespaces?: string[]
}

export interface SubstrateRef {
  atespace: string
  name: string
}

export interface SubstratePoolRef {
  namespace: string
  name: string
}

export interface SubstrateSnapshot {
  generation: number
  fidelity?: 'memory' | 'rootfs' | 'volumes'
  owner?: 'actor' | 'tag'
  /** The node holding the snapshot's files; absent once detached. */
  locality?: string
  /** none: no durable copy recorded; the files exist only on `locality`, if any. */
  durable: 'pending' | 'completed' | 'none'
  sandboxClass?: string
}

export interface SubstrateAssignment {
  /** Substrate's opaque Worker name. */
  worker: string
  namespace: string
  pool: string
  pod: string
  node: string
  epoch?: number
  sandboxClass?: string
}

export interface SubstrateActor {
  atespace: string
  name: string
  uid?: string
  template: SubstrateRef
  /** Absent when the actor started from its template. */
  sourceTag?: SubstrateRef
  state: SubstrateActorState
  /** Absent whenever the actor has no worker (suspended, crashed). */
  assignment?: SubstrateAssignment
  assignedNode?: string
  crash?: { message?: string; time?: string }
  snapshots?: SubstrateSnapshot[]
  workerSelector?: Record<string, string>
  createTime?: string
  /** Substrate's last change; the current state has held at least since then. */
  updateTime?: string
}

export type SubstrateResumesWith = 'memory' | 'filesystem' | 'volumes' | 'cold' | 'unknown'

export interface SubstrateWorkerPodRef {
  worker: string
  namespace: string
  pool: string
  pod: string
}

export interface SubstrateActorRow {
  atespace: string
  name: string
  state: SubstrateActorState
  /** Crash message, "Resuming for 6m", "Upload pending for 12m"; absent when nothing to add. */
  reason?: string
  /** A crash's cause, where Substrate's message names it; absent otherwise, and from a Radar that predates it. */
  cause?: 'memoryLimit'
  template: SubstrateRef
  /** Absent when the actor has no worker. */
  worker?: SubstrateWorkerPodRef
  /** The node it is on or was last on. */
  node?: string
  /** The highest generation, finished or not. */
  newestSnapshot?: SubstrateSnapshot
  /**
   * The generation a resume restores from, as Substrate picks it: the newest
   * with local files or a completed durable copy. Absent when it boots cold.
   */
  resumeGeneration?: number
  /**
   * What that resume restores. volumes when the snapshot was captured under a
   * template the actor no longer runs; unknown when that cannot be checked.
   * memory holds only on a worker whose runtime matches the snapshot's;
   * elsewhere Substrate restores the filesystem.
   */
  resumesWith: SubstrateResumesWith
  /**
   * When resumesWith holds only on a condition. compatibleRuntime: memory if
   * the worker it lands on runs a compatible runtime, else the filesystem.
   * runtimeMismatch: a known downgrade, the runtime that would host it cannot
   * restore the memory. Absent when unconditional.
   */
  resumeCondition?: 'compatibleRuntime' | 'runtimeMismatch'
  /** Where a resuming, suspending, reverting or deleting actor waits; absent in other states. */
  waitingOn?: SubstrateWaiting
  /** What its worker's state means for it; absent when the worker means nothing for it or was not read. */
  workerImpact?: SubstrateWorkerImpact
  createTime?: string
  updateTime?: string
}

export interface SubstrateStateCounts {
  total: number
  /** Every state is present, at zero when it has no actors. */
  byState: Record<SubstrateActorState, number>
}

export interface SubstrateResources {
  cpuMillicores: number
  memoryBytes: number
  /** Substrate counts an unreported dimension as zero: capacity 0 actors is 0 slots. */
  actors: number
}

export interface SubstrateWorkerCounts {
  total: number
  active: number
  draining: number
  unavailable: number
}

export interface SubstrateUsage {
  used: number
  total: number
}

/** Totals over active workers, the only ones Substrate places on. */
export interface SubstratePlacement {
  workers: SubstrateWorkerCounts
  slots: SubstrateUsage
  cpuMillicores: SubstrateUsage
  memoryBytes: SubstrateUsage
  /**
   * Every Substrate worker was read. When false (a capped or partly denied read) the totals are
   * lower bounds: they show what was observed, never that a pool lacks workers or free slots.
   */
  complete: boolean
}

/** A pool's worker Pods, those labelled with its name. */
export interface SubstratePoolPods {
  /** Not being deleted. */
  created: number
  /** Of those, Ready. */
  ready: number
  /** Being deleted; a Deployment's ready count leaves them out even while Ready. */
  terminating: number
}

export interface SubstrateK8sPool {
  namespace: string
  name: string
  /** spec.replicas; absent when unset. */
  desired?: number
  /** status.readyReplicas: Kubernetes' own claim, which can lag the Pods. Prefer pods.ready when present. */
  ready: number
  /** The pool's worker Pods in Radar's cache; absent when the caller can't list Pods there or the cache doesn't hold them. */
  pods?: SubstratePoolPods
  sandboxClasses?: string[]
  /** Radar's WorkerPool verdict (pkg/health). */
  health: string
  healthReason: string
  createTime?: string
}

export interface SubstratePoolRow {
  namespace: string
  name: string
  /** Absent when only Substrate's workers name the pool. */
  kubernetes?: SubstrateK8sPool
  /** Absent when Substrate's workers were not read. */
  placement?: SubstratePlacement
  /** Actors assigned to its workers; absent when actors were not read. */
  actors?: number
  sandboxClasses?: string[]
  /** Absent when neither side was read well enough to say. */
  capacity?: 'short' | 'full' | 'free'
  /** Substrate's own measurements from Prometheus; absent when not read for this pool. */
  service?: SubstratePoolService
}

export type SubstrateServiceVerdict =
  | 'workerShortfall'
  | 'noFreeActorSlots'
  | 'freeActorSlots'
  | 'noSchedulableWorkers'
  | 'uploadsOverdue'
  | 'noUploadsOverdue'
  | 'unknown'

/** One pool's Prometheus verdict and facts. An absent fact is unknown; reason says why when the verdict is unknown. */
export interface SubstratePoolService {
  verdict: SubstrateServiceVerdict
  reason?: string
  /** Over resumes placed on the pool; a resume that fails before placement names no pool. */
  resumeP95Seconds?: number
  failedResumesPercent?: number
  crashes?: number
  /** When the pool is short: schedulable out of desired. */
  schedulableWorkers?: number
  desiredWorkers?: number
}

/** The workspace's one-line reading of the Prometheus measurements. */
export interface SubstrateServiceSummary {
  read: boolean
  /** Why they were not read: Prometheus not connected, or the first collection still running. */
  why?: string
  /** When the measurements were collected. */
  readAt?: string
  /** The range the resume and crash facts cover, such as 1h. */
  window?: string
  /** Summed over pools that reported crashes; absent when none did. */
  crashes?: number
  /** value in seconds (resume p95). */
  slowestResume?: { pool: SubstratePoolRef; value: number }
  /** value in percent of resumes. */
  mostFailedResumes?: { pool: SubstratePoolRef; value: number }
  poolsUnknown: number
  nodesOverdue: number
  nodesUnknown: number
}

/** One needs-attention rule and whether it ran on this read. */
export interface SubstrateCheck {
  /** An attention rule, or poolService (the pool verdicts from Prometheus). */
  rule: SubstrateAttentionRule | 'poolService'
  label: string
  ran: boolean
  /** What was missing when it did not run. */
  why?: string
}

export interface SubstrateGolden {
  state: 'ready' | 'failed' | 'building' | 'disabled'
  tag?: SubstrateRef
  /** When warm-up ended (ready) or the template last changed (failed). */
  time?: string
  error?: string
  /**
   * Why a building snapshot cannot progress. noMatchingPool (the same word as
   * the rule): the complete pool list holds no pool the selector matches;
   * Substrate runs the golden actor like any actor, so the build retries
   * without end and never fails.
   */
  blocked?: 'noMatchingPool'
}

export interface SubstrateTemplateRow {
  atespace: string
  name: string
  golden: SubstrateGolden
  /** Absent when actors were not read. */
  actors?: SubstrateStateCounts
  /**
   * Pools the selector matches. null when Radar cannot say (no pool list, or
   * none of a partial list matches); [] only when the list is complete and
   * nothing matches.
   */
  matchingPools: SubstratePoolRef[] | null
  /** The pool list matched against is complete. */
  matchingPoolsComplete: boolean
  sandboxClasses?: string[]
  workerSelector?: Record<string, string>
  cpuMillicores?: number
  memoryBytes?: number
  preferredFidelity: 'memory' | 'rootfs' | 'volumes'
  /** Absent: the API server's default location. */
  storageLocation?: string
  containers?: string[]
  updateTime?: string
}

export type SubstrateSubjectKind = 'Actor' | 'ActorTemplate' | 'Worker' | 'WorkerPool' | 'Node' | 'Pod' | 'Actors'

export interface SubstrateSubject {
  kind: SubstrateSubjectKind
  atespace?: string
  namespace?: string
  name?: string
  /** For kind Actors: the /actors query parameters. */
  filter?: Record<string, string>
}

export interface SubstrateTally {
  subject?: SubstrateSubject
  text?: string
  count: number
  /** What every crash in a template group shares, when Substrate's messages name it: memoryLimit, a kill for exceeding the actor's memory limit. */
  cause?: 'memoryLimit'
  /** The group's remedy when cause names one; command may hold `<name>`, `<atespace>` and `<new-template>` placeholders. */
  nextStep?: string
  command?: string
}

export type SubstrateAttentionRule =
  | 'crashedActors'
  | 'stuckTransition'
  | 'goldenSnapshotFailed'
  | 'noMatchingPool'
  /** Actors naming a template Substrate does not have. */
  | 'missingTemplate'
  | 'workerUnavailable'
  | 'workerDraining'
  | 'workerRestarting'
  | 'poolShort'
  | 'poolFull'
  | 'uploadOverdue'
  /** Nodes whose atelet counts snapshot uploads past their deadline (Prometheus). */
  | 'nodeUploadsOverdue'

export interface SubstrateAttention {
  /** Stable across reads for the same rule and subject. */
  id: string
  rule: SubstrateAttentionRule
  /**
   * critical: something is already lost or down. alert: at risk, nothing lost
   * yet. info: expected and still within its budget (a drain before its Pod's
   * deadline).
   */
  severity: 'critical' | 'alert' | 'warning' | 'info'
  title: string
  reading: string
  /** One action in a short sentence. */
  nextStep: string
  /**
   * The command that runs the next step, when one does. A row covering several actors writes
   * `<name>` and `<atespace>` for them; an actor page's rows have them filled in.
   */
  command?: string
  subject: SubstrateSubject
  related?: SubstrateSubject[]
  count: number
  /** Newest change among them; a lower bound on how long the condition has held. */
  since?: string
  /** Crashes by template, largest first. */
  groups?: SubstrateTally[]
  /** Most common Substrate messages, largest first. */
  messages?: SubstrateTally[]
  /** uploadOverdue only: the nodes holding the unstored snapshots, most actors first. */
  nodes?: SubstrateUploadNode[]
  /** Up to 20 of the actors covered, for display; not which actors the row covers. */
  sample?: SubstrateRef[]
}

export interface SubstrateUploadNode {
  /** Empty when Substrate names no node. */
  node: string
  /** gone: Radar's Node list does not hold it. unknown: Nodes not read, or no node named. */
  state: 'ready' | 'notReady' | 'gone' | 'unknown'
  count: number
  /** The earliest last update among them; the wait is at least since then. */
  oldest?: string
}

export interface SubstrateStatus {
  /** The cluster serves Substrate's WorkerPool kind. */
  installed: boolean
  apiService: {
    namespace: string
    name: string
    port: number
    /** Absent when the caller cannot read Services there. */
    found?: boolean
  }
  apiTier: SubstrateAPITier
  /** The caller's WorkerPool read, under the same name as on every Substrate response. */
  coverage: { pools: SubstratePoolsCoverage }
  /** This Radar may read Substrate's API, so the connection can be set here. */
  canConfigure: boolean
}

export interface SubstrateSettings {
  identity: 'shared' | 'tokenFile'
  tokenFile?: string
  /**
   * Atespaces to read when the identity may not list atespaces (a token with
   * viewer on some atespaces only). Others stay unknown. Ignored when
   * Substrate lists atespaces.
   */
  atespaces?: string[]
  /** host:port; dials directly instead of port-forwarding to ate-system/api. */
  endpoint?: string
  caFile?: string
  serverName?: string
}

/**
 * The context and cluster a connection view was read for (k8s.ProfileTarget).
 * Opaque to the UI: send it back unchanged with a save or disconnect.
 */
export interface SubstrateConnectionTarget {
  binding: string
  context: string
  fingerprint: string
  clientGeneration: number
  operationGeneration: number
  [field: string]: unknown
}

export interface SubstrateConnection {
  /** The context (target.context) and cluster this view was read for. Send back with PUT; a save for a context that has since moved is refused with 409 targetChanged. */
  target: SubstrateConnectionTarget
  saved: boolean
  /** Saved for another cluster than this context points at now, so not used. */
  targetChanged?: boolean
  settings?: SubstrateSettings
  /** Who the shared identity authenticates as. */
  sharedPrincipal: string
}

/**
 * PUT /connection's body: the target the connection view returned, and
 * settings shaped exactly as GET returns them. settings {identity: 'none'},
 * with no other field, removes this context's settings. The save applies only
 * if the context is still the target.
 * A save first dials the settings and reads once; if that fails it answers
 * 502 with the tier code (unreachable, unauthenticated, tls, notInstalled…)
 * and saves nothing. An identity Substrate authenticates but denies the
 * atespace list is accepted: it is scoped.
 */
export interface SubstrateConnectionUpdate {
  target: SubstrateConnectionTarget
  settings: SubstrateSettings | { identity: 'none' }
}

export interface SubstrateWorkspace {
  apiTier: SubstrateAPITier
  /** pools is always present; the API-tier kinds are absent unless the API tier is connected. */
  coverage: Partial<SubstrateSnapshotCoverage> & { pools: SubstratePoolsCoverage }
  /** Each part is absent when what it counts was not read. */
  summary: {
    /** Absent unless WorkerPools or workers were read; a partial pool list counts what was read. */
    pools?: { total: number; short: number; full: number }
    workers?: SubstrateWorkerCounts
    slots?: SubstrateUsage
    /** Free actor slots per pool, largest first; [] when workers were read and none is free. */
    freeSlots?: { pool: SubstratePoolRef; free: number }[]
    actors?: { total: number; crashed: number }
    /** noMatchingPool counts templates whose selector matches no pool; absent unless the pool list is complete. */
    templates?: { total: number; goldenFailed: number; noMatchingPool?: number }
  }
  actorStates?: SubstrateStateCounts
  needsAttention: SubstrateAttention[]
  /** Every rule and whether it ran, so an empty needsAttention says what was checked. */
  checks: SubstrateCheck[]
  /** Health surfaces no check reads: an empty needsAttention never means they are healthy. */
  notAssessed: string[]
  service: SubstrateServiceSummary
  pools: SubstratePoolRow[]
  /** Absent unless atespaces were listed or read through the settings' scope; [] when none. */
  atespaces?: {
    name: string
    /**
     * How much of each kind was read for this atespace. partial: the read's
     * budget ran out, so the count is a lower bound. unread: not reached, or
     * not named by the settings' scope. The count is present only for full
     * and partial: never 0 for an atespace that was not read.
     */
    actorsRead: SubstrateAtespaceRead
    actors?: SubstrateStateCounts
    templatesRead: SubstrateAtespaceRead
    templates?: number
  }[]
}

export type SubstrateAtespaceRead = 'full' | 'partial' | 'denied' | 'unread'

export interface SubstrateActorsQuery {
  atespace?: string
  /** name, or atespace/name */
  template?: string
  /** name, or namespace/name */
  pool?: string
  /** worker Pod name or Substrate worker name */
  worker?: string
  /**
   * updated (default): newest change first. attention: crashed, then stuck in
   * a transition over 5 min, then the rest, each newest first. name: by
   * atespace and name. Paging applies after the sort.
   */
  sort?: 'updated' | 'attention' | 'name'
  /** With nodeHas: running = the actor's worker is on node; localSnapshot = its newest snapshot is not durably stored and its files are on node. */
  node?: string
  nodeHas?: 'running' | 'localSnapshot'
  /** Only actors whose worker's state means this for them (SubstrateWorkerImpact.kind); with worker, a worker row's "Show the N actors". */
  impact?: SubstrateWorkerImpact['kind']
  /** One state, or several (sent comma-separated); rows match any of them. */
  state?: SubstrateActorState | SubstrateActorState[]
  q?: string
  limit?: number
  offset?: number
}

export interface SubstrateActors {
  apiTier: SubstrateAPITier
  /** Coverage of the source that answered. */
  coverage: SubstrateCoverage
  /**
   * What answered: snapshot (the shared read); atespace (a direct read of the
   * atespace filter, when the snapshot does not hold all of it); actor (a
   * direct read of the exact name searched, when that atespace read is itself
   * capped).
   */
  sources: ('snapshot' | 'atespace' | 'actor')[]
  /** Absent when actors could not be read. */
  total?: number
  /** Per state, after every filter but state, before paging. */
  counts?: SubstrateStateCounts
  rows: SubstrateActorRow[]
  limit: number
  offset: number
}

export interface SubstrateEgress {
  /** set: a policy exists. none: no policy, so Substrate's egress gateway denies all outbound traffic. */
  state: 'set' | 'none' | 'denied' | 'failed'
  rules: number
  /** Hostnames the rules name, deduplicated, at most 20. */
  destinations?: string[]
  more?: number
  message?: string
}

export interface SubstrateWorkerRow {
  name: string
  namespace: string
  pool: string
  pod: string
  node: string
  state: 'active' | 'draining' | 'unavailable' | 'unknown'
  capacity?: SubstrateResources
  allocated?: SubstrateResources
  /** Above observedEpoch: the worker restarted and Substrate has not yet crashed its earlier actors. */
  registeredEpoch: number
  observedEpoch: number
  sandboxClasses?: string[]
  updateTime?: string
  /** An actor slot is left. Slots only: CPU or memory can still be short. */
  hasFreeSlot: boolean
  /** How long the state has held, at least: since Substrate's last update to the worker. Absent when it reported no update time. */
  heldForSeconds?: number
  /** The worker Pod's Ready condition; absent when the Pod is not cached. */
  podReady?: boolean
  /** The cached Pod is being deleted. A Deployment's ready count (the pool's ready workers) leaves it out even while podReady holds. */
  podTerminating?: boolean
  /** What its state means for its actors; absent for an active worker whose registration Substrate has observed. */
  workerState?: SubstrateWorkerReading
  /** Absent when actors were not read; [] when none is assigned. */
  actors?: SubstrateAssignedActor[]
}

/**
 * draining or unavailable: Substrate's state. reconciling: active, but its process restarted and Substrate
 * hasn't reconciled it; with lost > 0 those actors' sandboxes are gone (show it amber, "Active · reconciling").
 */
export interface SubstrateWorkerReading {
  kind: 'draining' | 'unavailable' | 'reconciling'
  /** Actors still assigned; absent when actors were not read. */
  assigned?: number
  /** Assigned actors that ran under an earlier process and lost their sandboxes; absent when actors were not read. */
  lost?: number
  reading: string
}

export interface SubstrateAssignedActor {
  atespace: string
  name: string
  state: SubstrateActorState
  template: SubstrateRef
  /** The actor's last change: tells a stuck transition from one within its budget. */
  updateTime?: string
  /** What this worker's state means for the actor; absent when it means nothing. */
  workerImpact?: SubstrateWorkerImpact
}

export interface SubstrateActorDetail {
  apiTier: SubstrateAPITier
  /** Read fresh from Substrate. */
  actor: SubstrateActor
  row: SubstrateActorRow
  template?: SubstrateTemplateRow
  /** The assigned worker; absent when the actor has none or workers were not read. */
  worker?: SubstrateWorkerRow
  /** What that worker's state means for this actor; absent when the worker is active and has not restarted under it. */
  workerImpact?: SubstrateWorkerImpact
  egress: SubstrateEgress
  related: SubstrateSubject[]
  /** Needs-attention rows that cover this actor, judged on the fresh read. */
  attention: SubstrateAttention[]
  /** What a revert would leave; absent when Substrate would refuse to revert this actor. */
  recovery?: SubstrateRecovery
  /**
   * Pools matching both the template's selector and the actor's own. null
   * when the template or pool list was not read, or a partial list matches
   * none; [] only when the list is complete and nothing matches.
   */
  matchingPools: SubstratePoolRef[] | null
  matchingPoolsComplete: boolean
  /** Coverage of the reads the template, worker, pool and attention readings come from. */
  coverage: { actors: SubstrateCoverage; templates: SubstrateCoverage; workers: SubstrateCoverage; pools: SubstratePoolsCoverage }
}

export interface SubstrateRecovery {
  /** The recovery source; absent when nothing is stored, so the actor boots cold after a revert. */
  generation?: number
  fidelity?: 'memory' | 'rootfs' | 'volumes'
  resumesWith: SubstrateResumesWith
  resumeCondition?: 'compatibleRuntime' | 'runtimeMismatch'
  /** Newer generations a revert drops, newest first. */
  discarded: { generation: number; fidelity?: 'memory' | 'rootfs' | 'volumes'; why: 'localOnly' | 'uploadPending' | 'uploadAbandoned' | 'incomplete' }[]
}

/**
 * containers and lifecycle are kubectl ate logs actor's --source. worker is
 * the worker process's own records about this actor ("Killing a sandbox over
 * its memory limit", "Sandbox died"), matched by the actor's UID; kubectl ate
 * does not show them. all is every one of them.
 */
export type SubstrateLogSource = 'all' | 'containers' | 'lifecycle' | 'worker'

export interface SubstrateLogLine {
  /** Which source the line belongs to. */
  kind: 'container' | 'lifecycle' | 'worker'
  time?: string
  /** Absent for a lifecycle event or a worker record. */
  container?: string
  severity?: string
  message?: string
  /** The record as kubectl ate prints it (a worker record without its identity keys). */
  text: string
  /** On a worker's memory-limit kill: the limit, and the usage that crossed it. */
  memoryLimitBytes?: number
  memoryUsageBytes?: number
}

/**
 * Where a transitional actor waits, as Substrate's workflows run it. A
 * resuming actor is always placed (Substrate sets RESUMING and the worker in
 * one write; one no worker can take stays suspended). An unfinished resume or
 * suspend continues only on the next resume or suspend call.
 * restore: placed, restoring on worker. checkpoint: checkpointing on worker
 * (a synchronous suspend also waits for the upload). revert, delete: in the
 * API server. unrecorded: no worker recorded, which Substrate never leaves.
 */
export interface SubstrateWaiting {
  on: 'restore' | 'checkpoint' | 'revert' | 'delete' | 'unrecorded'
  worker?: SubstrateWorkerPodRef
  node?: string
  reading: string
}

/**
 * unavailable: Substrate marks the worker unavailable, which its controller does while the
 * worker Pod is not Ready or has no IP; nothing new is placed there and the actor may not be
 * served, though Substrate still records it running. podState says which side holds it back.
 * draining: the Pod is terminating; when it is gone Substrate crashes the actor. restarted: the
 * worker process restarted under the actor, so its sandbox is gone; Substrate crashes it when it
 * reconciles.
 */
export interface SubstrateWorkerImpact {
  kind: 'unavailable' | 'draining' | 'restarted'
  reading: string
  /**
   * An unavailable worker's Pod in Radar's cache. notReady, noIP: Kubernetes is the cause.
   * ready: Ready with an IP, so Kubernetes is not the cause; Substrate's controller has not marked
   * the worker available again. unread: the cache does not hold the Pod. Absent for other kinds.
   */
  podState?: 'notReady' | 'noIP' | 'ready' | 'unread'
  /** When a draining worker's Pod is due to be gone; absent when unknown. */
  deadline?: string
  /**
   * One action for this actor, and the command that runs it with the actor's and worker Pod's names filled in.
   * unavailable: the worker row's step for its podState; draining: suspend it; restarted: revert it.
   */
  nextStep: string
  command: string
}

/** A worker Pod whose log was searched. */
export interface SubstrateSearchedPod {
  namespace: string
  pod: string
  /** gone: the Pod no longer exists. denied: the caller may not get pods/log there. */
  outcome: 'read' | 'denied' | 'gone' | 'failed'
  message?: string
  scanned: number
  /** The read reached its last-20k-lines window: older lines may exist. */
  windowFull: boolean
  matched: number
}

export interface SubstrateActorLogs {
  actor: SubstrateRef
  state: SubstrateActorState
  /**
   * worker: read from the worker Pod Substrate assigns it now. lastNode: it has
   * no worker, so the worker Pods on the node it was last on (assignedNode, or
   * its newest snapshot's locality) were searched for its lines; Substrate does
   * not record which Pod it ran in, so these are candidates, each line naming
   * its Pod. none: nothing to read.
   */
  origin: 'worker' | 'lastNode' | 'none'
  /** The assigned worker, for origin worker. */
  worker?: SubstrateWorkerPodRef
  /** The worker's node, or the node searched. */
  node?: string
  /**
   * noWorker: no worker and no node recorded. nodeGone. noWorkerPods: no worker
   * Pod on that node now (the one it ran in is gone). workerPodGone: its
   * assigned worker's Pod no longer exists. denied: no readable worker Pod.
   * noMatch: Pods searched, none holds its lines. candidates: lines found on
   * the last node.
   */
  reasonCode?: 'noWorker' | 'nodeGone' | 'noWorkerPods' | 'workerPodGone' | 'denied' | 'noMatch' | 'candidates'
  reason?: string
  source: SubstrateLogSource
  /** Newest lines, oldest first, each with the Pod it came from. */
  lines: (SubstrateLogLine & { pod: string })[]
  /** The Pods read; up to 6 on a last-node search. */
  searched: SubstrateSearchedPod[]
  /** Worker Pods on the node left unsearched. */
  morePods?: number
  scanned: number
  /** Some read reached its window: older lines of this actor may exist beyond it. */
  windowFull: boolean
  /**
   * The newest worker record among the lines of the worker killing this
   * actor's sandbox over its memory limit; absent when no line read says so.
   * With a crash, it is the crash's cause: "killed over its memory limit of 2 GiB".
   */
  memoryKill?: {
    time?: string
    pod: string
    limitBytes: number
    usageBytes?: number
    /**
     * The kill is tied to the actor's present crash: at most 10 minutes before it, with no start
     * or restore of the actor between them in the lines read. When false it is an earlier
     * execution's: say "a memory-limit kill was recorded at …", not that it caused this crash.
     */
    current: boolean
    /** The remedy, the crash step; absent unless current. */
    nextStep?: string
    command?: string
  }
}

export interface SubstrateWorkers {
  apiTier: SubstrateAPITier
  coverage: { workers: SubstrateCoverage; actors: SubstrateCoverage }
  counts?: SubstrateWorkerCounts
  workers: SubstrateWorkerRow[]
}

export interface SubstrateTemplates {
  apiTier: SubstrateAPITier
  coverage: { templates: SubstrateCoverage; actors: SubstrateCoverage; pools: SubstratePoolsCoverage }
  templates: SubstrateTemplateRow[]
}

/**
 * A template's definition, read fresh, for display only. Secret-shaped values
 * are redacted as Radar's AI context redacts them: an env value under a
 * sensitive name entirely (redacted true, value [REDACTED]); any other value,
 * command or argument by its secret patterns.
 */
export interface SubstrateTemplateSpec {
  containers: {
    name: string
    image: string
    command?: string[]
    args?: string[]
    env?: { name: string; value: string; redacted?: boolean }[]
    volumeMounts?: { name: string; mountPath: string }[]
    /** HTTP path Substrate probes after a resume. */
    wakeupProbe?: string
  }[]
  volumes: { name: string; kind: 'durableDir' | 'external' | 'systemInfo' | 'image'; image?: string }[]
  workerSelector?: Record<string, string>
  cpuMillicores?: number
  memoryBytes?: number
  snapshot: { preferredFidelity: 'memory' | 'rootfs' | 'volumes'; storageLocation?: string; golden: 'enabled' | 'disabled' }
  /** Rules of the egress policy the golden actor warms up under; absent when none. */
  goldenEgressRules?: number
  createTime?: string
}

export interface SubstrateTemplate {
  apiTier: SubstrateAPITier
  template: SubstrateTemplateRow
  /** Absent when the fresh read failed; specUnavailable says why. */
  spec?: SubstrateTemplateSpec
  specUnavailable?: string
  /** Coverage of the reads its actor counts and eligible pools come from. */
  coverage: { templates: SubstrateCoverage; actors: SubstrateCoverage; pools: SubstratePoolsCoverage }
}

export interface SubstratePool {
  apiTier: SubstrateAPITier
  pool: SubstratePoolRow
  workers: SubstrateWorkerRow[]
  /** Templates whose selector matches this pool; absent when templates were not read, [] when none. */
  templates?: SubstrateRef[]
  coverage: { workers: SubstrateCoverage; actors: SubstrateCoverage; templates: SubstrateCoverage; pools: SubstratePoolsCoverage }
}

/** The error_code of a failed substrate request, when it carries one. */
export function substrateErrorCode(error: unknown): SubstrateCode | undefined {
  if (error instanceof ApiError && typeof error.data?.error_code === 'string') {
    return error.data.error_code as SubstrateCode
  }
  return undefined
}

const SNAPSHOT_STALE_MS = 5_000
const POLL_MS = 15_000

// A denial, a missing actor or an unconfigured tier will not change on retry;
// an unreachable API might, once.
function retrySubstrate(failureCount: number, error: unknown) {
  const code = substrateErrorCode(error)
  return failureCount < 1 && (code === 'unreachable' || code === 'contextChanged' || (error instanceof ApiError && error.status === 409 && !code))
}

const enc = encodeURIComponent

/** pollMs: refetch interval; false stops polling. Defaults to 15s. */
export interface SubstratePollOptions {
  pollMs?: number | false
}

/**
 * One Substrate read. The key is ['substrate', apiBase, context, ...key, ...gatedKey]. There is no
 * placeholder unless `keepWhile` says the previous key is the same logical list: a detail shown
 * under another object's name, cluster or API base would be another object's facts.
 */
function useSubstrateQuery<T>(
  key: unknown[],
  path: string,
  enabled = true,
  poll: number | false = POLL_MS,
  keepWhile?: (previous: QueryKey, next: QueryKey) => boolean,
) {
  const { data: cluster } = useClusterInfo()
  const { guard, gatedKey, upgrade, blockedReason, support } = useRadarFeature('substrateWorkspace')
  const queryKey: QueryKey = ['substrate', getApiBase(), cluster?.context, ...key, ...gatedKey]
  const query = useQuery<T>({
    queryKey,
    // guard refuses a Radar known not to serve the route and still tries one whose support is unknown.
    queryFn: ({ signal }) => guard(() => fetchJSON<T>(path, signal)),
    enabled,
    staleTime: SNAPSHOT_STALE_MS,
    refetchInterval: poll,
    retry: retrySubstrate,
    placeholderData: keepWhile
      ? (previous, previousQuery) => (previousQuery && keepWhile(previousQuery.queryKey, queryKey) ? previous : undefined)
      : undefined,
  })
  return { ...query, upgrade, blockedReason, support }
}

/**
 * Two actor-list keys are the same list, paged differently: same API base, cluster context and
 * filters; only offset or limit differ. Keys are ['substrate', base, context, 'actors', filters, page, ...].
 */
export function sameSubstrateActorList(previous: QueryKey, next: QueryKey): boolean {
  return (
    previous[0] === 'substrate' &&
    previous[1] === next[1] &&
    previous[2] === next[2] &&
    previous[3] === 'actors' &&
    next[3] === 'actors' &&
    previous[4] === next[4]
  )
}

export function useSubstrateStatus(enabled = true, options: SubstratePollOptions = {}) {
  return useSubstrateQuery<SubstrateStatus>(['status'], '/substrate/status', enabled, options.pollMs ?? POLL_MS)
}

/** Local private Radar only; elsewhere it answers 403 sharedRadar. Enable when status.canConfigure. */
export function useSubstrateConnection(enabled = true) {
  return useSubstrateQuery<SubstrateConnection>(['connection'], '/substrate/connection', enabled, false)
}

/** The sidebar passes a slower pollMs while no Substrate screen is open. */
export function useSubstrateWorkspace(enabled = true, options: SubstratePollOptions = {}) {
  return useSubstrateQuery<SubstrateWorkspace>(['workspace'], '/substrate/workspace', enabled, options.pollMs ?? POLL_MS)
}

export function useSubstrateActors(query: SubstrateActorsQuery, enabled = true) {
  const params = new URLSearchParams()
  const filters = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    const value = Array.isArray(v) ? v.join(',') : v
    if (value === undefined || value === '') continue
    params.set(k, String(value))
    if (k !== 'offset' && k !== 'limit') filters.set(k, String(value))
  }
  const qs = params.toString()
  return useSubstrateQuery<SubstrateActors>(
    ['actors', filters.toString(), qs],
    `/substrate/actors${qs ? `?${qs}` : ''}`,
    enabled,
    POLL_MS,
    sameSubstrateActorList,
  )
}

export function useSubstrateActor(ref: SubstrateRef | null) {
  return useSubstrateQuery<SubstrateActorDetail>(
    ['actor', ref?.atespace, ref?.name],
    `/substrate/actors/${enc(ref?.atespace ?? '')}/${enc(ref?.name ?? '')}`,
    !!ref,
  )
}

export function useSubstrateActorLogs(
  ref: SubstrateRef | null,
  options: { source?: SubstrateLogSource; container?: string; tailLines?: number } = {},
) {
  const params = new URLSearchParams()
  if (options.source) params.set('source', options.source)
  if (options.container) params.set('container', options.container)
  if (options.tailLines) params.set('tailLines', String(options.tailLines))
  const qs = params.toString()
  return useSubstrateQuery<SubstrateActorLogs>(
    ['actor-logs', ref?.atespace, ref?.name, qs],
    `/substrate/actors/${enc(ref?.atespace ?? '')}/${enc(ref?.name ?? '')}/logs${qs ? `?${qs}` : ''}`,
    !!ref,
  )
}

/** pool: name, or namespace/name. */
export function useSubstrateWorkers(pool?: string, enabled = true) {
  const qs = pool ? `?pool=${enc(pool)}` : ''
  return useSubstrateQuery<SubstrateWorkers>(['workers', pool ?? ''], `/substrate/workers${qs}`, enabled)
}

export function useSubstrateTemplates(enabled = true) {
  return useSubstrateQuery<SubstrateTemplates>(['templates'], '/substrate/templates', enabled)
}

export function useSubstrateTemplate(ref: SubstrateRef | null) {
  return useSubstrateQuery<SubstrateTemplate>(
    ['template', ref?.atespace, ref?.name],
    `/substrate/templates/${enc(ref?.atespace ?? '')}/${enc(ref?.name ?? '')}`,
    !!ref,
  )
}

export function useSubstratePool(ref: SubstratePoolRef | null) {
  return useSubstrateQuery<SubstratePool>(
    ['pool', ref?.namespace, ref?.name],
    `/substrate/workerpools/${enc(ref?.namespace ?? '')}/${enc(ref?.name ?? '')}`,
    !!ref,
  )
}

/**
 * Saves this context's connection; every substrate query then reads through
 * it. A save whose target no longer matches the context answers 409
 * targetChanged and saves nothing; queries refetch either way, so the form
 * shows the current context's connection and target to review again.
 */
export function useUpdateSubstrateConnection() {
  const queryClient = useQueryClient()
  const feature = useRadarFeature('substrateWorkspace')
  return useMutation({
    mutationFn: (update: SubstrateConnectionUpdate) =>
      feature.guard(async () => {
        const resp = await apiFetch(`${getApiBase()}/substrate/connection`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(update),
        })
        if (!resp.ok) {
          const body = await readErrorBody(resp)
          throw new ApiError(body.error || httpStatusMessage(resp.status, resp.statusText), resp.status, body as Record<string, unknown>)
        }
        return (await resp.json()) as SubstrateConnection
      }),
    meta: {
      errorMessage: 'Failed to save the Substrate connection',
      successMessage: 'Substrate connection saved',
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['substrate', getApiBase()] })
    },
  })
}
