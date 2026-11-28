import { isApiGroup } from './resource-utils-cnpg'
import type { HealthLevel, StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

/**
 * Kargo (kargo.akuity.io/v1alpha1, built against v1.12) read model for the UI.
 *
 * Every rule about what a Kargo object means lives here, so the drawers, the
 * table cells and the /kargo workspace read one interpretation. Kargo v2 moves
 * part of this behind its REST API; when it does, this module is the one place
 * that changes.
 *
 * v2-safe reads: what a Stage holds comes from Stage.status.freightHistory[0].
 * Freight.status (currentlyIn / verifiedIn / approvedFor) is v1.12-only and is
 * read only by the functions that say so (getFreightSince, getFreightStageRecords,
 * explainFreightNotInStage).
 */

export const KARGO_GROUP = 'kargo.akuity.io'
export const KARGO_ALIAS_LABEL = 'kargo.akuity.io/alias'
export const KARGO_ARGOCD_CONTEXT_ANNOTATION = 'kargo.akuity.io/argocd-context'

// ============================================================================
// Shapes (only the fields this module reads; every field is optional because
// the objects come from the cluster and may be partial or from another version)
// ============================================================================

export interface KargoMeta {
  name?: string
  namespace?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  creationTimestamp?: string
}

export interface KargoCondition {
  type?: string
  status?: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export interface KargoFreightOrigin {
  kind?: string
  name?: string
}

export interface KargoImage { repoURL?: string; tag?: string; digest?: string }
export interface KargoCommit { repoURL?: string; id?: string; branch?: string; tag?: string; message?: string; author?: string }
export interface KargoChart { repoURL?: string; name?: string; version?: string }
export interface KargoArtifact { artifactType?: string; subscriptionName?: string; version?: string }

/** The artifacts a piece of Freight carries. Shared by Freight and FreightReference. */
export interface KargoFreightContent {
  name?: string
  origin?: KargoFreightOrigin
  images?: KargoImage[]
  commits?: KargoCommit[]
  charts?: KargoChart[]
  artifacts?: KargoArtifact[]
}

export interface KargoVerificationInfo {
  id?: string
  actor?: string
  phase?: string
  message?: string
  analysisRun?: { namespace?: string; name?: string; phase?: string }
  startTime?: string
  finishTime?: string
}

export interface KargoFreightCollection {
  id?: string
  items?: Record<string, KargoFreightContent>
  verificationHistory?: KargoVerificationInfo[]
}

export interface KargoPromotionReference {
  name?: string
  freight?: KargoFreightContent
  status?: { phase?: string; message?: string }
  finishedAt?: string
}

export interface KargoFreightSources {
  direct?: boolean
  stages?: string[]
  requiredSoakTime?: string
  availabilityStrategy?: string
  autoPromotionOptions?: { selectionPolicy?: string }
}

export interface KargoFreightRequest {
  origin?: KargoFreightOrigin
  sources?: KargoFreightSources
}

export interface KargoStage {
  apiVersion?: string
  kind?: string
  metadata?: KargoMeta
  spec?: {
    requestedFreight?: KargoFreightRequest[]
    promotionTemplate?: { spec?: { steps?: unknown[] } }
    verification?: { analysisTemplates?: { name?: string }[] }
  }
  status?: {
    conditions?: KargoCondition[]
    freightHistory?: KargoFreightCollection[]
    health?: { status?: string; issues?: string[]; output?: unknown }
    currentPromotion?: KargoPromotionReference
    lastPromotion?: KargoPromotionReference
    autoPromotionEnabled?: boolean
    effectiveAutoPromotionHolds?: Record<string, unknown>
  }
}

export interface KargoFreight extends KargoFreightContent {
  apiVersion?: string
  kind?: string
  metadata?: KargoMeta
  alias?: string
  discoveredAt?: string
  status?: {
    currentlyIn?: Record<string, { since?: string }>
    verifiedIn?: Record<string, { verifiedAt?: string; longestSoak?: string }>
    approvedFor?: Record<string, { approvedAt?: string }>
  }
}

export interface KargoPromotionStepSpec {
  uses?: string
  as?: string
  task?: { name?: string; kind?: string }
  continueOnError?: boolean
}

export interface KargoStepExecution {
  alias?: string
  status?: string
  message?: string
  errorCount?: number
  startedAt?: string
  finishedAt?: string
  continueOnError?: boolean
}

export interface KargoPromotion {
  apiVersion?: string
  kind?: string
  metadata?: KargoMeta
  spec?: { stage?: string; freight?: string; steps?: KargoPromotionStepSpec[] }
  status?: {
    phase?: string
    message?: string
    freight?: KargoFreightContent
    startedAt?: string
    finishedAt?: string
    currentStep?: number
    stepExecutionMetadata?: KargoStepExecution[]
  }
}

export interface KargoWarehouse {
  apiVersion?: string
  kind?: string
  metadata?: KargoMeta
  spec?: { interval?: string; freightCreationPolicy?: string; subscriptions?: Record<string, unknown>[] }
  status?: {
    conditions?: KargoCondition[]
    lastFreightID?: string
    discoveredArtifacts?: { discoveredAt?: string }
  }
}

// ============================================================================
// Identity
// ============================================================================

export type KargoKind = 'Stage' | 'Freight' | 'Promotion' | 'Warehouse'

/** True only for the named Kargo kind in the exact kargo.akuity.io group. Stage, Promotion and Warehouse are common kind names. */
export function isKargoKind(data: { apiVersion?: unknown; kind?: unknown } | null | undefined, kind: KargoKind): boolean {
  return !!data && data.kind === kind && isApiGroup(data.apiVersion, KARGO_GROUP)
}

function str(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function findCondition(conditions: KargoCondition[] | undefined, type: string): KargoCondition | undefined {
  return Array.isArray(conditions) ? conditions.find(c => c?.type === type) : undefined
}

function badge(text: string, level: HealthLevel): StatusBadge {
  return { text, level, color: healthColors[level] }
}

// ============================================================================
// Freight display: alias + versions, never a bare SHA
// ============================================================================

/** The human name Kargo gives a piece of Freight ("singing-octopus"). Field first, label as fallback, as Kargo's UI does. */
export function getFreightAlias(freight: KargoFreight | null | undefined): string | undefined {
  return str(freight?.alias) ?? str(freight?.metadata?.labels?.[KARGO_ALIAS_LABEL])
}

/** Freight names are SHA-1 hashes; seven characters is enough to tell them apart on screen. */
export function shortFreightName(name: string | undefined): string {
  return (name ?? '').slice(0, 7)
}

export type FreightVersionType = 'image' | 'commit' | 'chart' | 'artifact'

export interface FreightVersion {
  type: FreightVersionType
  /** Short source name: image or chart name, repo name for a commit. */
  source: string
  /** Tag, chart version, commit tag or short commit SHA. */
  version: string
  /** Full repository URL, for a tooltip or a copy. */
  repoURL?: string
}

/** Last path segment of a repo URL, without a trailing ".git". "ghcr.io/acme/web" -> "web". */
export function repoShortName(repoURL: string | undefined): string {
  if (!repoURL) return ''
  let end = repoURL.length
  while (end > 0 && repoURL[end - 1] === '/') end--
  const trimmed = repoURL.slice(0, end).replace(/\.git$/, '')
  const last = trimmed.split('/').pop() ?? trimmed
  return last.split(':')[0] || trimmed
}

function shortDigest(digest: string | undefined): string {
  if (!digest) return ''
  const hex = digest.includes(':') ? digest.split(':')[1] : digest
  return `@${hex.slice(0, 7)}`
}

/**
 * The versions a piece of Freight carries, in Kargo's order: commits, charts,
 * images, other artifacts. Works on a Freight and on a FreightReference.
 */
export function getFreightVersions(freight: KargoFreightContent | null | undefined): FreightVersion[] {
  if (!freight) return []
  const out: FreightVersion[] = []
  for (const c of freight.commits ?? []) {
    const version = str(c?.tag) ?? (str(c?.id) ? str(c?.id)!.slice(0, 7) : '')
    if (version) out.push({ type: 'commit', source: repoShortName(c?.repoURL), version, repoURL: c?.repoURL })
  }
  for (const ch of freight.charts ?? []) {
    const version = str(ch?.version) ?? ''
    if (version) out.push({ type: 'chart', source: str(ch?.name) ?? repoShortName(ch?.repoURL), version, repoURL: ch?.repoURL })
  }
  for (const img of freight.images ?? []) {
    const version = str(img?.tag) ?? shortDigest(img?.digest)
    if (version) out.push({ type: 'image', source: repoShortName(img?.repoURL), version, repoURL: img?.repoURL })
  }
  for (const a of freight.artifacts ?? []) {
    const version = str(a?.version) ?? ''
    if (version) out.push({ type: 'artifact', source: str(a?.subscriptionName) ?? str(a?.artifactType) ?? '', version })
  }
  return out
}

/** "web:1.31.9", "app 0.4.0" (chart), "config@abc1234" (commit). */
export function formatFreightVersion(v: FreightVersion): string {
  if (!v.source) return v.version
  switch (v.type) {
    case 'image': return v.version.startsWith('@') ? `${v.source}${v.version}` : `${v.source}:${v.version}`
    case 'commit': return `${v.source}@${v.version}`
    default: return `${v.source} ${v.version}`
  }
}

/** All versions as one line, for a table cell or a sentence. Empty when the Freight carries none. */
export function formatFreightVersions(freight: KargoFreightContent | null | undefined): string {
  return getFreightVersions(freight).map(formatFreightVersion).join(', ')
}

/**
 * One line naming a piece of Freight: "singing-octopus (web:1.31.9)". Without an
 * alias the versions stand alone; with neither, the short Freight name.
 */
export function formatFreightLabel(freight: KargoFreightContent | null | undefined, alias?: string): string {
  const versions = formatFreightVersions(freight)
  if (alias && versions) return `${alias} (${versions})`
  if (alias) return alias
  if (versions) return versions
  return shortFreightName(freight?.name)
}

/** Newest first, by discoveredAt and then creation time, as Kargo orders its timeline. */
export function sortFreightNewestFirst<T extends KargoFreight>(freights: T[]): T[] {
  const at = (f: KargoFreight) => Date.parse(f.discoveredAt ?? f.metadata?.creationTimestamp ?? '') || 0
  return [...freights].sort((a, b) => at(b) - at(a))
}

// ============================================================================
// Stage: current freight, health, verification, last promotion, sources, apps
// ============================================================================

export interface StageFreight {
  /** Warehouse the Freight came from. A Stage holds one piece of Freight per origin. */
  origin: string
  name: string
  versions: FreightVersion[]
  /** Only known when the caller can read the Freight object. */
  alias?: string
  ref: KargoFreightContent
}

/**
 * What the Stage holds now: one entry per Warehouse origin, from
 * status.freightHistory[0], sorted by origin as Kargo sorts them. Empty when the
 * Stage holds nothing yet. `freightByName` adds aliases when the caller has the
 * Freight objects; without it, alias stays undefined (unknown, not "none").
 */
export function getStageCurrentFreight(
  stage: KargoStage | null | undefined,
  freightByName?: Record<string, KargoFreight | undefined>,
): StageFreight[] {
  const items = stage?.status?.freightHistory?.[0]?.items
  if (!items || typeof items !== 'object') return []
  return Object.keys(items).sort().map((key) => {
    const ref = items[key] ?? {}
    const name = ref.name ?? ''
    return {
      origin: ref.origin?.name ?? key.replace(/^Warehouse\//, ''),
      name,
      versions: getFreightVersions(ref),
      alias: name ? getFreightAlias(freightByName?.[name]) : undefined,
      ref,
    }
  })
}

/**
 * When the Stage started holding this Freight, from Freight.status.currentlyIn.
 * v1.12-only. Undefined means unknown: verification and promotion times are not
 * deployment times and are never used in its place.
 */
export function getFreightSince(freight: KargoFreight | null | undefined, stageName: string): string | undefined {
  return str(freight?.status?.currentlyIn?.[stageName]?.since)
}

export interface StageHealth {
  /** Kargo's word: Healthy, Progressing, Unhealthy, Unknown, NotApplicable. Undefined when not reported. */
  status?: string
  tone: HealthLevel
  issues: string[]
}

export function getStageHealth(stage: KargoStage | null | undefined): StageHealth {
  const health = stage?.status?.health
  const status = str(health?.status)
  const issues = (health?.issues ?? []).filter((i): i is string => typeof i === 'string' && !!i.trim())
  const tone: HealthLevel = status === 'Healthy' ? 'healthy'
    : status === 'Unhealthy' ? 'unhealthy'
      : status === 'Progressing' ? 'degraded'
        : status === 'NotApplicable' ? 'neutral'
          : 'unknown'
  return { status, tone, issues }
}

/** Tone for a verification (AnalysisRun) phase. Aborted is a cancellation, not a failure. */
export function getVerificationPhaseTone(phase: string | undefined): HealthLevel {
  switch (phase) {
    case 'Successful': return 'healthy'
    case 'Failed':
    case 'Error': return 'unhealthy'
    case 'Inconclusive': return 'degraded'
    case 'Pending':
    case 'Running':
    case 'Aborted': return 'neutral'
    default: return 'unknown'
  }
}

export interface StageVerification {
  /** Whether the Stage declares verification at all. */
  configured: boolean
  /** The latest verification of the current Freight, or undefined if none ran yet. */
  latest?: KargoVerificationInfo
  tone: HealthLevel
}

export function getStageVerification(stage: KargoStage | null | undefined): StageVerification {
  const configured = (stage?.spec?.verification?.analysisTemplates?.length ?? 0) > 0
  const latest = stage?.status?.freightHistory?.[0]?.verificationHistory?.[0]
  return { configured, latest, tone: latest ? getVerificationPhaseTone(latest.phase) : configured ? 'unknown' : 'neutral' }
}

/**
 * Tone for a Promotion phase. Failed (the work failed) and Errored (Kargo could
 * not run it) are red. Aborted is a user cancellation: neutral, never red.
 */
export function getPromotionPhaseTone(phase: string | undefined): HealthLevel {
  switch (phase) {
    case 'Succeeded': return 'healthy'
    case 'Failed':
    case 'Errored': return 'unhealthy'
    case 'Pending':
    case 'Running':
    case 'Aborted': return 'neutral'
    default: return 'unknown'
  }
}

export interface StagePromotion {
  name: string
  /** Undefined while Kargo has not recorded a result. */
  phase?: string
  message?: string
  tone: HealthLevel
  finishedAt?: string
  freight?: KargoFreightContent
}

function promotionRef(ref: KargoPromotionReference | undefined): StagePromotion | undefined {
  const name = str(ref?.name)
  if (!name) return undefined
  const phase = str(ref?.status?.phase)
  return { name, phase, message: str(ref?.status?.message), tone: getPromotionPhaseTone(phase), finishedAt: str(ref?.finishedAt), freight: ref?.freight }
}

export function getStageLastPromotion(stage: KargoStage | null | undefined): StagePromotion | undefined {
  return promotionRef(stage?.status?.lastPromotion)
}

export function getStageCurrentPromotion(stage: KargoStage | null | undefined): StagePromotion | undefined {
  const ref = promotionRef(stage?.status?.currentPromotion)
  return ref ? { ...ref, phase: ref.phase ?? 'Running', tone: 'neutral' } : undefined
}

export interface StageSource {
  /** Warehouse the requested Freight comes from. */
  warehouse: string
  /** Takes Freight straight from the Warehouse. */
  direct: boolean
  /** Upstream Stages the Freight must be verified in first. */
  upstreamStages: string[]
  /** "All" needs every upstream Stage; "OneOf" (Kargo's default) needs one. */
  availabilityStrategy: 'All' | 'OneOf'
  requiredSoakTime?: string
  selectionPolicy?: string
}

/** Where the Stage's Freight comes from, one entry per spec.requestedFreight. */
export function getStageSources(stage: KargoStage | null | undefined): StageSource[] {
  return (stage?.spec?.requestedFreight ?? []).map(req => ({
    warehouse: req?.origin?.name ?? '',
    direct: req?.sources?.direct === true,
    upstreamStages: (req?.sources?.stages ?? []).filter((s): s is string => typeof s === 'string' && !!s),
    availabilityStrategy: req?.sources?.availabilityStrategy === 'All' ? 'All' : 'OneOf',
    requiredSoakTime: str(req?.sources?.requiredSoakTime),
    selectionPolicy: str(req?.sources?.autoPromotionOptions?.selectionPolicy),
  }))
}

/** A Stage with no promotion steps passes Freight on without deploying anything. */
export function isControlFlowStage(stage: KargoStage | null | undefined): boolean {
  return (stage?.spec?.promotionTemplate?.spec?.steps?.length ?? 0) === 0
}

export interface StageArgoCDApp {
  name: string
  namespace: string
  /** From the Stage's last health check; undefined when Kargo did not report it. */
  health?: string
  healthMessage?: string
  sync?: string
}

function parseArgoCDContext(raw: string | undefined): { name: string; namespace: string }[] {
  if (!raw) return []
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.flatMap((entry) => {
      if (!entry || typeof entry !== 'object') return []
      const { name, namespace } = entry as { name?: unknown; namespace?: unknown }
      return typeof name === 'string' && name && typeof namespace === 'string' && namespace ? [{ name, namespace }] : []
    })
  } catch {
    return []
  }
}

interface HealthOutputApp {
  name?: string
  namespace?: string
  health?: { status?: string; message?: string }
  sync?: { status?: string }
}

// status.health.output is untyped JSON. The Argo CD checker writes one entry
// {applicationStatuses: [...]} whose items carry Go's default field names
// (Name, Namespace) beside Argo CD's own health and sync blocks.
function readHealthOutputApps(output: unknown): HealthOutputApp[] {
  if (!Array.isArray(output)) return []
  const apps: HealthOutputApp[] = []
  for (const entry of output) {
    const statuses = entry && typeof entry === 'object' ? (entry as { applicationStatuses?: unknown }).applicationStatuses : undefined
    if (!Array.isArray(statuses)) continue
    for (const s of statuses) {
      if (!s || typeof s !== 'object') continue
      const rec = s as Record<string, unknown>
      const name = str(rec.Name) ?? str(rec.name)
      const namespace = str(rec.Namespace) ?? str(rec.namespace)
      const health = rec.health && typeof rec.health === 'object' ? rec.health as { status?: string; message?: string } : undefined
      const sync = rec.sync && typeof rec.sync === 'object' ? rec.sync as { status?: string } : undefined
      if (name) apps.push({ name, namespace, health, sync })
    }
  }
  return apps
}

/**
 * The Argo CD Applications the Stage drives: the apps Kargo recorded on the Stage
 * (annotation kargo.akuity.io/argocd-context, set by its last promotion) and the
 * apps its health check reads, with their health and sync where reported.
 */
export function getStageArgoCDApps(stage: KargoStage | null | undefined): StageArgoCDApp[] {
  const byKey = new Map<string, StageArgoCDApp>()
  for (const app of parseArgoCDContext(stage?.metadata?.annotations?.[KARGO_ARGOCD_CONTEXT_ANNOTATION])) {
    byKey.set(`${app.namespace}/${app.name}`, { ...app })
  }
  for (const app of readHealthOutputApps(stage?.status?.health?.output)) {
    const namespace = app.namespace ?? ''
    const key = `${namespace}/${app.name}`
    const existing = byKey.get(key) ?? (namespace ? undefined : [...byKey.values()].find(a => a.name === app.name))
    const merged: StageArgoCDApp = {
      name: app.name!,
      namespace: existing?.namespace ?? namespace,
      health: str(app.health?.status),
      healthMessage: str(app.health?.message),
      sync: str(app.sync?.status),
    }
    byKey.set(`${merged.namespace}/${merged.name}`, merged)
  }
  return [...byKey.values()].sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name))
}

// Condition reasons Kargo v1.12 writes while work is under way, not because
// something failed: the Stage controller copies them onto Ready from Promoting,
// Healthy and Verified, and the Warehouse controller writes them before and
// during discovery. Mirrors pkg/kargo waitingReasons.
const KARGO_WAITING_REASONS = new Set([
  'ActivePromotion', 'NoFreight', 'WaitingForHealthCheck', 'Progressing',
  'WaitingForVerification', 'PendingVerification', 'VerificationPending', 'VerificationRunning',
  'TargetAwareStage', 'DiscoveryInProgress', 'AwaitingFreightCreation', 'ScheduledDiscovery',
  'FreightCreationInProgress', 'Pending',
])

/** Whether a Kargo condition reason means work is under way rather than a failure. */
export function isKargoWaitingReason(reason: string | undefined): boolean {
  return !!reason && KARGO_WAITING_REASONS.has(reason)
}

// Warehouse conditions that report what the last discovery did, not health:
// FreightCreated=False/AlreadyExists means the newest artifacts already have
// Freight, and FreightCreationCriteriaSatisfied=False means the Warehouse's
// own criteria held new Freight back (pkg/controller/warehouses).
const KARGO_OUTCOME_CONDITIONS = new Set(['FreightCreated', 'FreightCreationCriteriaSatisfied'])

/** Whether a Kargo condition reports an outcome rather than health, so False is not a failure. */
export function isKargoOutcomeCondition(type: string | undefined): boolean {
  return !!type && KARGO_OUTCOME_CONDITIONS.has(type)
}

// Ready=False reasons Kargo's Stage controller writes (pkg/controller/stages,
// summarizeConditions). In-flight work is not a failure.
const STAGE_IN_PROGRESS_REASONS: Record<string, string> = {
  ActivePromotion: 'Promoting',
  WaitingForHealthCheck: 'Checking health',
  WaitingForVerification: 'Verifying',
  PendingVerification: 'Verifying',
  VerificationPending: 'Verifying',
  VerificationRunning: 'Verifying',
  Progressing: 'Progressing',
}

/**
 * One status for a Stage, from its conditions: the same reading for the drawer
 * header, the table and the workspace. Kargo folds promotion, health and
 * verification into Ready; this splits them back out so a Stage that is merely
 * verifying does not read as failed.
 */
export function getStageStatus(stage: KargoStage | null | undefined): StatusBadge {
  const conditions = stage?.status?.conditions
  const ready = findCondition(conditions, 'Ready')
  const promoting = findCondition(conditions, 'Promoting')
  if (!ready && !promoting) return badge('Not reported', 'unknown')
  if (promoting?.status === 'True' && promoting.reason !== 'NoFreight') return badge('Promoting', 'neutral')
  if (ready?.reason === 'NoFreight') return badge('No Freight yet', 'neutral')
  if (ready?.status === 'True') return badge('Ready', 'healthy')
  if (ready?.status !== 'False') return badge(ready?.reason || 'Unknown', 'unknown')

  const reason = ready.reason ?? ''
  if (STAGE_IN_PROGRESS_REASONS[reason]) return badge(STAGE_IN_PROGRESS_REASONS[reason], 'neutral')
  if (reason === 'LastPromotionFailed') return badge('Promotion failed', 'unhealthy')
  if (reason === 'LastPromotionErrored') return badge('Promotion errored', 'unhealthy')
  if (reason === 'LastPromotionAborted') return badge('Promotion aborted', 'neutral')
  if (reason === 'VerificationFailed' || reason === 'VerificationError') return badge('Verification failed', 'unhealthy')
  if (reason === 'VerificationInconclusive') return badge('Verification inconclusive', 'degraded')
  if (reason === 'VerificationAborted') return badge('Verification aborted', 'neutral')
  if (reason === 'Unknown') return badge('Health unknown', 'unknown')
  if (reason === 'Unhealthy') return badge('Unhealthy', 'unhealthy')
  return badge(reason || 'Not ready', 'unhealthy')
}

// ============================================================================
// Promotion
// ============================================================================

export function getPromotionStatus(promotion: KargoPromotion | null | undefined): StatusBadge {
  const phase = str(promotion?.status?.phase)
  if (!phase) return badge('Not reported', 'unknown')
  return badge(phase, getPromotionPhaseTone(phase))
}

export interface PromotionStepView {
  index: number
  /** Kargo's alias for the step: `as`, else task-N or step-N. */
  alias: string
  /** The step kind ("git-clone", "argocd-update") or the task it expands. */
  uses?: string
  /** Undefined when the step has not started. */
  status?: string
  tone: HealthLevel
  message?: string
  errorCount?: number
  startedAt?: string
  finishedAt?: string
  continueOnError: boolean
}

function stepStatusTone(status: string | undefined): HealthLevel {
  switch (status) {
    case 'Succeeded': return 'healthy'
    case 'Failed':
    case 'Errored': return 'unhealthy'
    case 'Running': return 'neutral'
    case 'Skipped':
    case 'Aborted': return 'neutral'
    default: return 'unknown'
  }
}

/** spec.steps zipped with status.stepExecutionMetadata, which Kargo keeps index-aligned. */
export function getPromotionSteps(promotion: KargoPromotion | null | undefined): PromotionStepView[] {
  const steps = promotion?.spec?.steps ?? []
  const exec = promotion?.status?.stepExecutionMetadata ?? []
  const count = Math.max(steps.length, exec.length)
  const out: PromotionStepView[] = []
  for (let i = 0; i < count; i++) {
    const spec = steps[i]
    const meta = exec[i]
    const alias = str(meta?.alias) ?? str(spec?.as) ?? (spec?.task ? `task-${i + 1}` : `step-${i + 1}`)
    const status = str(meta?.status)
    out.push({
      index: i,
      alias,
      uses: str(spec?.uses) ?? str(spec?.task?.name),
      status,
      tone: stepStatusTone(status),
      message: str(meta?.message),
      errorCount: typeof meta?.errorCount === 'number' && meta.errorCount > 0 ? meta.errorCount : undefined,
      startedAt: str(meta?.startedAt),
      finishedAt: str(meta?.finishedAt),
      continueOnError: meta?.continueOnError === true || spec?.continueOnError === true,
    })
  }
  return out
}

export interface PromotionError {
  /** The step that failed, when one did. */
  step?: PromotionStepView
  /** Undefined when Kargo recorded no message anywhere. */
  message?: string
}

/**
 * Why a Failed or Errored Promotion failed: the first failed step that does not
 * continue on error, else status.message. Null for any other phase.
 */
export function getPromotionError(promotion: KargoPromotion | null | undefined): PromotionError | null {
  const phase = promotion?.status?.phase
  if (phase !== 'Failed' && phase !== 'Errored') return null
  const step = getPromotionSteps(promotion).find(s => (s.status === 'Failed' || s.status === 'Errored') && !s.continueOnError)
  return { step, message: step?.message ?? str(promotion?.status?.message) }
}

// ============================================================================
// Warehouse
// ============================================================================

export interface WarehouseSubscription {
  /** image, git, chart, or the type name of a newer generic subscription. */
  type: string
  name?: string
  repoURL?: string
  /** Which versions it picks, as declared. Undefined means Kargo's default (newest). */
  constraint?: string
}

export function getWarehouseSubscriptions(warehouse: KargoWarehouse | null | undefined): WarehouseSubscription[] {
  return (warehouse?.spec?.subscriptions ?? []).flatMap((entry) => {
    if (!entry || typeof entry !== 'object') return []
    const typeKey = Object.keys(entry).find(k => k !== 'name')
    if (!typeKey) return []
    const body = entry[typeKey]
    const sub = body && typeof body === 'object' ? body as Record<string, unknown> : {}
    let constraint: string | undefined
    if (typeKey === 'image') {
      constraint = str(sub.constraint) ?? str(sub.semverConstraint) ?? (str(sub.allowTags) ? `tags matching ${str(sub.allowTags)}` : undefined)
    } else if (typeKey === 'git') {
      const branch = str(sub.branch)
      constraint = str(sub.semverConstraint) ?? (str(sub.allowTags) ? `tags matching ${str(sub.allowTags)}` : branch ? `branch ${branch}` : undefined)
    } else if (typeKey === 'chart') {
      constraint = str(sub.semverConstraint)
    }
    const repoURL = str(sub.repoURL)
    const name = str(entry.name) ?? str(sub.name)
    return [{ type: typeKey, name, repoURL, constraint }]
  })
}

/** The discovery error Kargo reports on the Warehouse (Healthy=False), if any. */
export function getWarehouseDiscoveryError(warehouse: KargoWarehouse | null | undefined): KargoCondition | undefined {
  const healthy = findCondition(warehouse?.status?.conditions, 'Healthy')
  return healthy?.status === 'False' ? healthy : undefined
}

const WAREHOUSE_IN_PROGRESS_REASONS = new Set(['DiscoveryInProgress', 'AwaitingFreightCreation'])

export function getWarehouseStatus(warehouse: KargoWarehouse | null | undefined): StatusBadge {
  const conditions = warehouse?.status?.conditions
  const error = getWarehouseDiscoveryError(warehouse)
  if (error) return badge(error.reason || 'Discovery failed', 'unhealthy')
  const ready = findCondition(conditions, 'Ready')
  if (!ready) return badge('Not reported', 'unknown')
  if (ready.status === 'True') return badge('Ready', 'healthy')
  if (WAREHOUSE_IN_PROGRESS_REASONS.has(ready.reason ?? '')) return badge('Discovering', 'neutral')
  if (ready.status === 'False') return badge(ready.reason || 'Not ready', 'unhealthy')
  return badge(ready.reason || 'Unknown', 'unknown')
}

// ============================================================================
// Pipeline order
// ============================================================================

export interface PipelineStage {
  name: string
  /** Column index: 0 for Stages fed only by Warehouses, else one past the deepest upstream Stage. */
  depth: number
  upstream: string[]
  warehouses: string[]
  /** Upstream Stages named in the spec but not in the given list (not readable, or deleted). */
  missingUpstream: string[]
}

/**
 * Stages in pipeline order, left to right, derived from spec.requestedFreight
 * sources the way Kargo's own graph lays them out. Sorted by depth, then name.
 * A cycle (Kargo rejects these) is cut where it is found rather than looping.
 */
export function getPipelineOrder(stages: KargoStage[]): PipelineStage[] {
  const byName = new Map<string, KargoStage>()
  for (const s of stages) {
    const name = s?.metadata?.name
    if (name) byName.set(name, s)
  }
  const depths = new Map<string, number>()
  const visiting = new Set<string>()
  const depthOf = (name: string): number => {
    const known = depths.get(name)
    if (known !== undefined) return known
    if (visiting.has(name)) return 0
    visiting.add(name)
    let depth = 0
    for (const src of getStageSources(byName.get(name))) {
      for (const up of src.upstreamStages) {
        if (byName.has(up)) depth = Math.max(depth, depthOf(up) + 1)
      }
    }
    visiting.delete(name)
    depths.set(name, depth)
    return depth
  }
  const out: PipelineStage[] = []
  for (const [name, stage] of byName) {
    const sources = getStageSources(stage)
    const upstream = [...new Set(sources.flatMap(s => s.upstreamStages))]
    out.push({
      name,
      depth: depthOf(name),
      upstream,
      warehouses: [...new Set(sources.filter(s => s.direct).map(s => s.warehouse).filter(Boolean))],
      missingUpstream: upstream.filter(u => !byName.has(u)),
    })
  }
  return out.sort((a, b) => a.depth - b.depth || a.name.localeCompare(b.name))
}

// ============================================================================
// Why a piece of Freight is not in a Stage yet
// ============================================================================

/** Go's time.Duration text ("1h30m0s", "90s", "1.5h") in milliseconds, or undefined. */
export function parseGoDuration(text: string | undefined): number | undefined {
  const trimmed = text?.trim()
  if (!trimmed) return undefined
  if (trimmed === '0') return 0
  const unitMs: Record<string, number> = { h: 3_600_000, m: 60_000, s: 1000, ms: 1, us: 0.001, 'µs': 0.001, ns: 0.000001 }
  const isDigit = (c: string | undefined) => c !== undefined && c >= '0' && c <= '9'
  let total = 0
  let i = 0
  while (i < trimmed.length) {
    const start = i
    while (isDigit(trimmed[i])) i++
    if (i === start) return undefined
    if (trimmed[i] === '.') {
      i++
      const fraction = i
      while (isDigit(trimmed[i])) i++
      if (i === fraction) return undefined
    }
    const value = Number(trimmed.slice(start, i))
    const two = trimmed.slice(i, i + 2)
    const unit = two in unitMs && two.length === 2 ? two : trimmed[i]
    if (unit === undefined || !(unit in unitMs)) return undefined
    total += value * unitMs[unit]
    i += unit.length
  }
  return total
}

/** Go's time.Duration text in words ("5m0s" as "5 minutes", "1h30m0s" as "1 hour 30 minutes"); other text as written. */
export function formatGoDurationWords(text: string | undefined): string | undefined {
  const ms = parseGoDuration(text)
  if (ms === undefined || (ms > 0 && ms < 1000) || ms % 1000 !== 0) return text?.trim() || undefined
  let seconds = ms / 1000
  if (seconds === 0) return '0 seconds'
  const parts: string[] = []
  for (const [size, unit] of [[86_400, 'day'], [3600, 'hour'], [60, 'minute'], [1, 'second']] as const) {
    const n = Math.floor(seconds / size)
    seconds -= n * size
    if (n > 0) parts.push(`${n} ${unit}${n === 1 ? '' : 's'}`)
  }
  return parts.join(' ')
}

export type FreightWait =
  | { kind: 'here' }
  | { kind: 'not-requested' }
  | { kind: 'last-promotion-failed'; promotion: string; phase: string; message?: string }
  | { kind: 'not-verified-upstream'; stages: string[]; strategy: 'All' | 'OneOf' }
  | { kind: 'not-approved' }
  | { kind: 'soaking'; stage: string; remainingMs: number }
  | { kind: 'manual-promotion' }
  | { kind: 'unknown'; reason: string }

/**
 * Why `freight` is not in `stage` yet, for the reasons Radar can establish from
 * the two objects: upstream not verified (OneOf vs All), not approved where
 * approval is the only way in, soak time not reached, auto-promotion off, or the
 * last promotion of this Freight failed. Anything else is `unknown` with a short
 * reason. Reads Freight.status (v1.12-only).
 */
export function explainFreightNotInStage(stage: KargoStage, freight: KargoFreight, now: number = Date.now()): FreightWait {
  const stageName = stage?.metadata?.name ?? ''
  const freightName = freight?.metadata?.name ?? freight?.name ?? ''
  const originName = freight?.origin?.name
  if (getStageCurrentFreight(stage).some(f => f.name === freightName)) return { kind: 'here' }
  const requests = getStageSources(stage).filter(s => s.warehouse === originName)
  if (requests.length === 0) return { kind: 'not-requested' }

  const current = getStageCurrentPromotion(stage)
  if (current) return { kind: 'unknown', reason: 'A promotion to this Stage is running.' }

  const last = getStageLastPromotion(stage)
  if (last && (last.phase === 'Failed' || last.phase === 'Errored') && last.freight?.name === freightName) {
    return { kind: 'last-promotion-failed', promotion: last.name, phase: last.phase, message: last.message }
  }

  if (isControlFlowStage(stage)) return { kind: 'unknown', reason: 'This Stage has no promotion steps, so Kargo moves Freight through it on its own.' }

  const status = freight?.status ?? {}
  const approved = !!status.approvedFor?.[stageName]
  const soakedMs = (up: string): number => {
    const longest = parseGoDuration(status.verifiedIn?.[up]?.longestSoak) ?? 0
    const since = Date.parse(status.currentlyIn?.[up]?.since ?? '')
    return Math.max(longest, Number.isFinite(since) ? now - since : 0)
  }

  let blocked: FreightWait | null = null
  let available = approved
  for (const req of requests) {
    if (available) break
    if (req.direct) { available = true; break }
    if (req.upstreamStages.length === 0) { blocked = blocked ?? { kind: 'not-approved' }; continue }
    const required = parseGoDuration(req.requiredSoakTime) ?? 0
    const verified = req.upstreamStages.filter(up => !!status.verifiedIn?.[up])
    const unverified = req.upstreamStages.filter(up => !status.verifiedIn?.[up])
    const remaining = (up: string) => Math.max(0, required - soakedMs(up))
    if (req.availabilityStrategy === 'All') {
      if (unverified.length > 0) { blocked = { kind: 'not-verified-upstream', stages: unverified, strategy: 'All' }; continue }
      const waiting = verified.map(up => ({ up, ms: remaining(up) })).filter(w => w.ms > 0).sort((a, b) => b.ms - a.ms)
      if (waiting.length > 0) { blocked = { kind: 'soaking', stage: waiting[0].up, remainingMs: waiting[0].ms }; continue }
      available = true
    } else {
      if (verified.length === 0) { blocked = { kind: 'not-verified-upstream', stages: req.upstreamStages, strategy: 'OneOf' }; continue }
      const waiting = verified.map(up => ({ up, ms: remaining(up) })).sort((a, b) => a.ms - b.ms)
      if (waiting[0].ms > 0) { blocked = { kind: 'soaking', stage: waiting[0].up, remainingMs: waiting[0].ms }; continue }
      available = true
    }
  }
  if (!available) return blocked ?? { kind: 'unknown', reason: 'Kargo has not made this Freight available to this Stage.' }

  if (stage?.status?.autoPromotionEnabled !== true) return { kind: 'manual-promotion' }
  if (requests.some(r => r.selectionPolicy === 'MatchUpstream')) {
    return { kind: 'unknown', reason: 'This Stage promotes whatever its upstream Stage holds, not the newest Freight.' }
  }
  // Kargo keys holds by origin ("Warehouse/<name>") and skips only that origin.
  const originKey = `${freight?.origin?.kind ?? 'Warehouse'}/${originName}`
  if (stage?.status?.effectiveAutoPromotionHolds?.[originKey] !== undefined) {
    return { kind: 'unknown', reason: 'Auto-promotion is on hold for this Stage.' }
  }
  return { kind: 'unknown', reason: 'Kargo has not promoted it yet, and the Stage does not say why.' }
}

function formatRemaining(ms: number): string {
  const minutes = Math.ceil(ms / 60_000)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  if (hours < 24) return rest ? `${hours}h ${rest}m` : `${hours}h`
  const days = Math.floor(hours / 24)
  return hours % 24 ? `${days}d ${hours % 24}h` : `${days}d`
}

function listStages(stages: string[]): string {
  if (stages.length <= 1) return stages[0] ?? ''
  return `${stages.slice(0, -1).join(', ')} and ${stages[stages.length - 1]}`
}

/** The FreightWait as one sentence with a tone, for a card or a drawer row. */
export function describeFreightWait(wait: FreightWait): { text: string; tone: HealthLevel } {
  switch (wait.kind) {
    case 'here': return { text: 'Already in this Stage.', tone: 'healthy' }
    case 'not-requested': return { text: 'This Stage does not take Freight from this Warehouse.', tone: 'neutral' }
    case 'last-promotion-failed':
      return { text: `The last promotion of this Freight ${wait.phase === 'Errored' ? 'errored' : 'failed'}${wait.message ? `: ${wait.message}` : '.'}`, tone: 'unhealthy' }
    case 'not-verified-upstream':
      return {
        text: wait.strategy === 'All' || wait.stages.length === 1
          ? `Not verified in ${listStages(wait.stages)} yet.`
          : `Not verified in any of ${listStages(wait.stages)} yet.`,
        tone: 'neutral',
      }
    case 'not-approved': return { text: 'Waiting for someone to approve it for this Stage.', tone: 'neutral' }
    case 'soaking': return { text: `Soaking in ${wait.stage}, ${formatRemaining(wait.remainingMs)} left.`, tone: 'neutral' }
    case 'manual-promotion': return { text: 'Ready to promote. Auto-promotion is off, so someone has to promote it.', tone: 'neutral' }
    case 'unknown': return { text: `Unknown: ${wait.reason}`, tone: 'unknown' }
  }
}

// ============================================================================
// Freight status as Kargo reports it (v1.12-only)
// ============================================================================

export interface FreightStageRecord {
  stage: string
  at?: string
}

/** Freight.status.currentlyIn / verifiedIn / approvedFor as sorted lists. v1.12-only. */
export function getFreightStageRecords(freight: KargoFreight | null | undefined): {
  currentlyIn: FreightStageRecord[]
  verifiedIn: FreightStageRecord[]
  approvedFor: FreightStageRecord[]
} {
  const read = <T>(rec: Record<string, T> | undefined, at: (v: T) => string | undefined): FreightStageRecord[] =>
    Object.entries(rec ?? {}).map(([stage, v]) => ({ stage, at: str(at(v)) })).sort((a, b) => a.stage.localeCompare(b.stage))
  const status = freight?.status
  return {
    currentlyIn: read(status?.currentlyIn, v => v?.since),
    verifiedIn: read(status?.verifiedIn, v => v?.verifiedAt),
    approvedFor: read(status?.approvedFor, v => v?.approvedAt),
  }
}

// ============================================================================
// Table columns
// ============================================================================

/** Kargo's resource status for any of the four kinds; null for anything else. */
export function getKargoStatus(data: { apiVersion?: unknown; kind?: unknown } | null | undefined): StatusBadge | null {
  if (!data || !isApiGroup(data.apiVersion, KARGO_GROUP)) return null
  switch (data.kind) {
    case 'Stage': return getStageStatus(data as KargoStage)
    case 'Promotion': return getPromotionStatus(data as KargoPromotion)
    case 'Warehouse': return getWarehouseStatus(data as KargoWarehouse)
    default: return null
  }
}

/** Text for a curated Kargo column, for filters and sorting. Undefined when the column is not Kargo's. */
export function getKargoColumnValue(resource: { apiVersion?: unknown; kind?: unknown } | null | undefined, column: string): string | undefined {
  if (!resource || !isApiGroup(resource.apiVersion, KARGO_GROUP)) return undefined
  if (resource.kind === 'Stage') {
    const stage = resource as KargoStage
    switch (column) {
      case 'status': return getStageStatus(stage).text
      case 'kargoFreight': return getStageCurrentFreight(stage).map(f => formatFreightLabel(f.ref, f.alias)).join('; ')
      case 'kargoHealth': return getStageHealth(stage).status ?? 'Not reported'
      case 'kargoLastPromotion': return getStageLastPromotion(stage)?.phase ?? ''
    }
  }
  if (resource.kind === 'Promotion') {
    const promotion = resource as KargoPromotion
    switch (column) {
      case 'status': return getPromotionStatus(promotion).text
      case 'kargoStage': return promotion.spec?.stage ?? ''
      case 'kargoFreight': return formatFreightLabel(promotion.status?.freight ?? { name: promotion.spec?.freight })
    }
  }
  return undefined
}
