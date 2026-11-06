import type { WorkloadRevision } from '../types/core'
import { coalesceHistoryAnchors } from './application-history'
import type { AppHistoryAnchor, AppSourceRef } from './applications'
import { pluralize } from './pluralize'

// One deploy history for an app across its workloads and its Helm/GitOps source.
// "Live" and "Newest" answer different questions: which revision's pods are
// serving, and which revision the workload was last told to run. They are only
// asserted from a signal the kind actually has, so a row can be Live 'unknown'.

export interface AppDeployRowWorkload {
  kind: string
  namespace: string
  name: string
  group?: string
}

export type AppDeploySource = 'revision' | 'helm' | 'gitops'

export interface AppDeployRow {
  id: string
  /** The workload, or the Helm/GitOps object for a source row. */
  workload: AppDeployRowWorkload
  revision?: number
  version?: string
  image?: string
  source: AppDeploySource
  /** Where it came from: "Helm revision 12", "Argo CD sync by alice". On a revision, "Near …": a record that lines up with it, which no record ties to this resource. */
  via?: string
  /** A change-cause note the previous revision had too. Kubernetes copies the
   *  workload's note onto each new revision, so it may not be about this one. */
  repeatedCause?: string
  at?: string
  live: boolean | 'unknown'
  newest: boolean
  /** From the last list read, after a refresh failed: nothing here is a claim about now. */
  stale?: boolean
  status?: string
  /** The source's own message for this deploy. */
  detail?: string
  ready?: number
  desired?: number
  template?: string
  previousTemplate?: string
  previousRevision?: number
}

export interface AppDeployWorkloadInput {
  ref: AppDeployRowWorkload
  /** Undefined when the history couldn't be read; [] when there is none. */
  revisions?: WorkloadRevision[]
  /** Desired pods of the workload; ControllerRevisions carry no replica count. */
  desired?: number
  /** `revisions` is the last list read, after a refresh failed: still worth showing, but not a claim about now. */
  stale?: boolean
  /** The first read is still under way. */
  loading?: boolean
  /** Left out to bound the reads a large app makes. */
  notRead?: boolean
}

export interface BuildAppDeploysInput {
  workloads: AppDeployWorkloadInput[]
  /** Undefined when the source history wasn't fetched. */
  anchors?: AppHistoryAnchor[]
  /** The app's Helm/GitOps source, when it has one. */
  sourceRef?: AppSourceRef
  partialSources?: string[]
  /** The last source history read, after a refresh failed. */
  sourceStale?: boolean
  /** The first read of the source history is still under way. */
  sourceLoading?: boolean
}

export interface AppDeploys {
  rows: AppDeployRow[]
  partial: boolean
  /** One sentence per missing source, for display. */
  partialReasons: string[]
  /** One sentence per kind of read still under way, for display. */
  stillLoading: string[]
  /** Revisions came back without the per-revision signals a current Radar agent always sends. */
  agentOutdated?: boolean
}

// A Helm or GitOps deploy records its time when it finishes, so the ReplicaSet
// it created is usually older than the record, by up to the time it waited.
const ANCHOR_AFTER_REVISION_MS = 5 * 60 * 1000
const ANCHOR_BEFORE_REVISION_MS = 60 * 1000

const REVISIONED_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout'])

/** Whether this workload kind has a revision history at all. */
export function workloadHasRevisions(kind: string): boolean {
  return REVISIONED_KINDS.has(kind)
}

function hasPerRevisionReplicas(kind: string): boolean {
  return kind === 'Deployment' || kind === 'Rollout'
}

/** The tag of an image ref, or its digest prefix when it's pinned by digest. */
export function imageVersion(image: string | undefined): string | undefined {
  if (!image) return undefined
  const digest = image.indexOf('@')
  if (digest >= 0) return image.slice(digest + 1, digest + 1 + 'sha256:'.length + 12)
  const tagStart = image.lastIndexOf(':')
  return tagStart > image.lastIndexOf('/') ? image.slice(tagStart + 1) : undefined
}

// Only a Deployment proves it: a StatefulSet or DaemonSet mid-update reports
// neither field on a current agent too.
function predatesRevisionSignals({ ref, revisions = [] }: AppDeployWorkloadInput): boolean {
  return ref.kind === 'Deployment' && revisions.length > 0 && revisions.every((r) => r.serving === undefined && r.readyReplicas === undefined)
}

function liveOf(kind: string, revision: WorkloadRevision, revisions: WorkloadRevision[]): boolean | 'unknown' {
  if (kind === 'Rollout') {
    if (!revisions.some((r) => r.isStable)) return 'unknown'
    if (revision.readyReplicas === 0) return false
    // Canary and preview pods can be ready without taking traffic.
    return revision.isStable && revision.readyReplicas !== undefined ? true : 'unknown'
  }
  return revision.serving ?? 'unknown'
}

function timeOf(value: string | undefined): number | undefined {
  if (!value) return undefined
  const at = new Date(value).getTime()
  return Number.isFinite(at) ? at : undefined
}

/** The version a source recorded: a Helm chart, or a GitOps revision. */
function anchorVersion(anchor: AppHistoryAnchor): string | undefined {
  return anchor.type === 'helm' ? anchor.source : anchor.revision
}

const withoutV = (version: string) => version.replace(/^v/, '')

// Evidence beyond timing: the source recorded the version this revision runs.
// A Helm record lists its app version; a CI-built image often carries the commit.
function anchorNamesVersion(anchor: AppHistoryAnchor, version: string | undefined): boolean {
  if (!version || version === 'latest') return false
  if (anchor.type === 'helm') {
    return (anchor.source ?? '').split(' · ').some((part) => withoutV(part) === withoutV(version))
  }
  const revision = anchor.revision
  if (!revision) return false
  return revision === version || version.split(/[-_.+]/).some((part) => /^[0-9a-f]{7,40}$/.test(part) && revision.startsWith(part))
}

function anchorFailed(anchor: AppHistoryAnchor): boolean {
  return /fail|error/i.test(anchor.status ?? '')
}

interface AnchorPairing {
  anchor: AppHistoryAnchor
  /** Both describe one deploy, so the record needs no row of its own. Anything
   *  less leaves it an event of its own: a failed attempt, or a record beside a
   *  restart that kept the version. */
  sameDeploy: boolean
}

// One record lines up with one revision of a workload, though it can line up
// with a revision in each workload it touched. Evidence goes first, then closeness.
function pairAnchors(revisions: Array<{ revision: WorkloadRevision; previous?: WorkloadRevision }>, anchors: AppHistoryAnchor[]): Map<WorkloadRevision, AnchorPairing> {
  const candidates: Array<AnchorPairing & { revision: WorkloadRevision; named: boolean; gap: number }> = []
  for (const { revision, previous } of revisions) {
    const created = timeOf(revision.createdAt)
    if (created === undefined) continue
    const version = imageVersion(revision.image)
    // Without the previous revision there's no telling whether the version moved.
    const versionChanged = previous !== undefined && version !== imageVersion(previous.image)
    for (const anchor of anchors) {
      const at = timeOf(anchor.timestamp)
      if (at === undefined) continue
      const gap = at - created
      if (gap > ANCHOR_AFTER_REVISION_MS || gap < -ANCHOR_BEFORE_REVISION_MS) continue
      const named = anchorNamesVersion(anchor, version)
      candidates.push({ revision, anchor, named, sameDeploy: named && versionChanged && !anchorFailed(anchor), gap: Math.abs(gap) })
    }
  }
  candidates.sort((a, b) => Number(b.named) - Number(a.named) || a.gap - b.gap)
  const pairings = new Map<WorkloadRevision, AnchorPairing>()
  const taken = new Set<AppHistoryAnchor>()
  for (const { revision, anchor, sameDeploy } of candidates) {
    if (pairings.has(revision) || taken.has(anchor)) continue
    pairings.set(revision, { anchor, sameDeploy })
    taken.add(anchor)
  }
  return pairings
}

function anchorVia(anchor: AppHistoryAnchor): string {
  return anchor.initiatedBy ? `${anchor.title} by ${anchor.initiatedBy}` : anchor.title
}

// A paired source can lose its own row, so what it recorded has to survive here.
// Always "Near": a record lists no resources, so a match in time or version is
// a correlation, never a cause.
function pairedVia({ anchor }: AnchorPairing, version: string | undefined): string {
  const recorded = anchorVersion(anchor)
  return `Near ${recorded && recorded !== version ? `${anchorVia(anchor)} · ${recorded}` : anchorVia(anchor)}`
}

function anchorRow(anchor: AppHistoryAnchor, index: number, sourceRef: AppSourceRef | undefined): AppDeployRow {
  return {
    id: `${anchor.type}:${index}:${anchor.revision ?? anchor.timestamp ?? ''}`,
    workload: {
      kind: sourceRef?.kind ?? '',
      namespace: sourceRef?.namespace ?? '',
      name: sourceRef?.name ?? '',
      group: sourceRef?.group,
    },
    version: anchorVersion(anchor),
    source: anchor.type === 'helm' ? 'helm' : 'gitops',
    via: anchorVia(anchor),
    at: anchor.timestamp,
    // A source record says what was applied, not which pods are serving.
    live: 'unknown',
    newest: false,
    status: anchor.status,
    detail: anchor.message,
  }
}

interface SortableRow {
  row: AppDeployRow
  // A rollback bumps an old revision's number but keeps its creation time, so a
  // row can't sort earlier than any revision it superseded.
  sortAt: number
}

function ownCauseOf(revision: WorkloadRevision, previous: WorkloadRevision | undefined): string | undefined {
  return revision.changeCause !== previous?.changeCause ? revision.changeCause : undefined
}

function revisionRows(input: AppDeployWorkloadInput, anchors: AppHistoryAnchor[], matched: Set<AppHistoryAnchor>): SortableRow[] {
  const { ref, revisions = [], desired, stale } = input
  const newestFirst = [...revisions].sort((a, b) => b.number - a.number)
  // The revision's own note is firmer than any record paired by time or version.
  const pairings = pairAnchors(
    newestFirst.map((revision, i) => ({ revision, previous: newestFirst[i + 1] })).filter(({ revision, previous }) => !ownCauseOf(revision, previous)),
    anchors,
  )
  const rows: SortableRow[] = []
  let sortAt = -Infinity
  for (let i = newestFirst.length - 1; i >= 0; i--) {
    const revision = newestFirst[i]
    const previous = newestFirst[i + 1]
    const cause = revision.changeCause
    const ownCause = ownCauseOf(revision, previous)
    const pairing = pairings.get(revision)
    const folded = pairing?.sameDeploy ? pairing.anchor : undefined
    if (folded) matched.add(folded)
    const version = imageVersion(revision.image)
    sortAt = Math.max(sortAt, timeOf(revision.createdAt) ?? -Infinity)
    const row: AppDeployRow = {
      id: `${ref.group ?? ''}/${ref.kind}/${ref.namespace}/${ref.name}#${revision.number}`,
      workload: ref,
      revision: revision.number,
      version,
      image: revision.image,
      source: 'revision',
      via: pairing ? pairedVia(pairing, version) : ownCause,
      repeatedCause: !pairing && !ownCause ? cause : undefined,
      at: revision.createdAt,
      live: liveOf(ref.kind, revision, revisions),
      newest: revision.isCurrent,
      status: folded?.status,
      detail: folded?.message,
      ready: revision.readyReplicas,
      desired: hasPerRevisionReplicas(ref.kind) ? revision.replicas : revision.isCurrent ? desired : undefined,
      template: revision.template,
      previousTemplate: previous?.template,
      previousRevision: previous?.number,
    }
    rows.push({
      sortAt,
      row: stale ? { ...row, live: 'unknown', newest: false, ready: undefined, desired: undefined, stale: true } : row,
    })
  }
  return rows
}

/** Pass `scope` to answer for one workload; Helm and GitOps records cover the whole app and stay. */
export function buildAppDeploys(
  { workloads, anchors, sourceRef, partialSources, sourceStale, sourceLoading }: BuildAppDeploysInput,
  scope?: AppDeployRowWorkload,
): AppDeploys {
  const revisioned = workloads.filter(
    ({ ref }) => workloadHasRevisions(ref.kind) && (!scope || (ref.kind === scope.kind && ref.namespace === scope.namespace && ref.name === scope.name)),
  )
  const records = coalesceHistoryAnchors(anchors ?? [])
  const matched = new Set<AppHistoryAnchor>()
  const sortable = revisioned.flatMap((workload) => revisionRows(workload, records, matched))

  records.forEach((anchor, index) => {
    if (matched.has(anchor)) return
    sortable.push({ row: anchorRow(anchor, index, sourceRef), sortAt: timeOf(anchor.timestamp) ?? -Infinity })
  })

  sortable.sort((a, b) => b.sortAt - a.sortAt || (b.row.revision ?? 0) - (a.row.revision ?? 0) || a.row.id.localeCompare(b.row.id))

  const partialReasons = [...(partialSources ?? [])]
  const notRead = revisioned.filter((workload) => workload.notRead).length
  if (notRead > 0) {
    partialReasons.push(`Showing revisions for ${revisioned.length - notRead} of ${pluralize(revisioned.length, 'workload')}. Open a workload to see its own.`)
  }
  const reading = revisioned.filter((workload) => workload.loading).length
  for (const workload of revisioned) {
    const label = `${workload.ref.kind} ${workload.ref.name}`
    if (workload.notRead || workload.loading) continue
    if (workload.revisions === undefined) {
      partialReasons.push(`Revision history for ${label} couldn't be read.`)
    } else if (workload.stale) {
      partialReasons.push(`Couldn't refresh revisions for ${label}. This is the last list read, so its Live, Newest and ready counts aren't shown.`)
    }
  }
  const stillLoading = reading > 0 ? [`Still loading revisions for ${pluralize(reading, 'workload')}.`] : []
  if (sourceRef) {
    const sourceHistory = `${sourceRef.type === 'helm' ? 'Helm' : 'GitOps'} history for ${sourceRef.name}`
    if (sourceLoading) stillLoading.push(`Still loading ${sourceHistory}.`)
    else if (anchors === undefined) partialReasons.push(`${sourceHistory} isn't available here.`)
    else if (sourceStale) partialReasons.push(`Couldn't refresh ${sourceHistory}. This is the last list read.`)
  }

  return {
    rows: sortable.map(({ row }) => row),
    partial: partialReasons.length > 0,
    partialReasons,
    stillLoading,
    agentOutdated: revisioned.some(predatesRevisionSignals),
  }
}
