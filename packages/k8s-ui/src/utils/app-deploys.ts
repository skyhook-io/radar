import type { WorkloadRevision } from '../types/core'
import type { AppHistoryAnchor, AppSourceRef, AppWorkload } from './applications'
import { isRolloutActivityVisible } from './workload-rollout'

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
  /** Where it came from: "Helm revision 12", "Argo CD sync by alice". "Near …" means only that the times line up. */
  via?: string
  at?: string
  live: boolean | 'unknown'
  newest: boolean
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
  rolloutActive?: boolean
  /** Desired pods of the workload; ControllerRevisions carry no replica count. */
  desired?: number
}

export interface BuildAppDeploysInput {
  workloads: AppDeployWorkloadInput[]
  /** Undefined when the source history wasn't fetched. */
  anchors?: AppHistoryAnchor[]
  /** The app's Helm/GitOps source, when it has one. */
  sourceRef?: AppSourceRef
  partialSources?: string[]
  /** The last revisions read, after a refresh failed: still worth showing, but not a claim about now. */
  stale?: boolean
}

export interface AppDeploys {
  rows: AppDeployRow[]
  rolloutInProgress: boolean
  partial: boolean
  /** One sentence per missing source, for display. */
  partialReasons: string[]
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
    return revisions.some((r) => r.isStable) ? Boolean(revision.isStable) : 'unknown'
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

interface AnchorPairing {
  anchor: AppHistoryAnchor
  /** The source recorded this revision's version, rather than just being close in time. */
  proven: boolean
}

function pairAnchor(revision: WorkloadRevision, anchors: AppHistoryAnchor[]): AnchorPairing | undefined {
  const created = timeOf(revision.createdAt)
  if (created === undefined) return undefined
  const version = imageVersion(revision.image)
  let closest: AnchorPairing | undefined
  let closestGap = Infinity
  for (const anchor of anchors) {
    const at = timeOf(anchor.timestamp)
    if (at === undefined) continue
    const gap = at - created
    if (gap > ANCHOR_AFTER_REVISION_MS || gap < -ANCHOR_BEFORE_REVISION_MS) continue
    const proven = anchorNamesVersion(anchor, version)
    if (!closest || (proven === closest.proven ? Math.abs(gap) < closestGap : proven)) {
      closest = { anchor, proven }
      closestGap = Math.abs(gap)
    }
  }
  return closest
}

function anchorVia(anchor: AppHistoryAnchor): string {
  return anchor.initiatedBy ? `${anchor.title} by ${anchor.initiatedBy}` : anchor.title
}

// A paired source loses its own row, so what it recorded has to survive here.
function pairedVia({ anchor, proven }: AnchorPairing, version: string | undefined): string {
  const recorded = anchorVersion(anchor)
  const via = recorded && recorded !== version ? `${anchorVia(anchor)} · ${recorded}` : anchorVia(anchor)
  return proven ? via : `Near ${via}`
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

function revisionRows(input: AppDeployWorkloadInput, anchors: AppHistoryAnchor[], matched: Set<AppHistoryAnchor>): SortableRow[] {
  const { ref, revisions = [], desired } = input
  const newestFirst = [...revisions].sort((a, b) => b.number - a.number)
  const rows: SortableRow[] = []
  let sortAt = -Infinity
  for (let i = newestFirst.length - 1; i >= 0; i--) {
    const revision = newestFirst[i]
    const pairing = pairAnchor(revision, anchors)
    const anchor = pairing?.anchor
    if (anchor) matched.add(anchor)
    const version = imageVersion(revision.image)
    sortAt = Math.max(sortAt, timeOf(revision.createdAt) ?? -Infinity)
    rows.push({
      sortAt,
      row: {
        id: `${ref.group ?? ''}/${ref.kind}/${ref.namespace}/${ref.name}#${revision.number}`,
        workload: ref,
        revision: revision.number,
        version,
        image: revision.image,
        source: 'revision',
        via: pairing ? pairedVia(pairing, version) : revision.changeCause,
        at: revision.createdAt,
        live: liveOf(ref.kind, revision, revisions),
        newest: revision.isCurrent,
        status: anchor?.status,
        detail: anchor?.message,
        ready: revision.readyReplicas,
        desired: hasPerRevisionReplicas(ref.kind) ? revision.replicas : revision.isCurrent ? desired : undefined,
        template: revision.template,
        previousTemplate: newestFirst[i + 1]?.template,
        previousRevision: newestFirst[i + 1]?.number,
      },
    })
  }
  return rows
}

/** Host-side input for one workload, so every host reads the same rollout signal. */
export function appDeployWorkloadInput(workload: AppWorkload, revisions?: WorkloadRevision[]): AppDeployWorkloadInput {
  return {
    ref: { kind: workload.kind, namespace: workload.namespace, name: workload.name, group: workload.group },
    revisions,
    rolloutActive: workload.rollout ? isRolloutActivityVisible(workload.rollout) : false,
    desired: workload.desired,
  }
}

export function buildAppDeploys({ workloads, anchors, sourceRef, partialSources, stale }: BuildAppDeploysInput): AppDeploys {
  const revisioned = workloads.filter((workload) => workloadHasRevisions(workload.ref.kind))
  const matched = new Set<AppHistoryAnchor>()
  const sortable = revisioned.flatMap((workload) => revisionRows(workload, anchors ?? [], matched))

  ;(anchors ?? []).forEach((anchor, index) => {
    if (matched.has(anchor)) return
    sortable.push({ row: anchorRow(anchor, index, sourceRef), sortAt: timeOf(anchor.timestamp) ?? -Infinity })
  })

  sortable.sort((a, b) => b.sortAt - a.sortAt || (b.row.revision ?? 0) - (a.row.revision ?? 0) || a.row.id.localeCompare(b.row.id))

  const partialReasons = [...(partialSources ?? [])]
  // Nothing read yet means there's no last list; the per-workload line below says so.
  if (stale && revisioned.some((workload) => workload.revisions !== undefined)) {
    partialReasons.push("Couldn't refresh revisions. This is the last list read, so Live and Newest aren't shown.")
  }
  for (const workload of revisioned) {
    if (workload.revisions === undefined) {
      partialReasons.push(`Revision history for ${workload.ref.kind} ${workload.ref.name} couldn't be read.`)
    }
  }
  if (sourceRef && anchors === undefined) {
    partialReasons.push(`${sourceRef.type === 'helm' ? 'Helm' : 'GitOps'} history for ${sourceRef.name} isn't available here.`)
  }

  return {
    rows: sortable.map(({ row }) => (stale && row.source === 'revision' ? { ...row, live: 'unknown' as const, newest: false } : row)),
    rolloutInProgress: workloads.some((workload) => workload.rolloutActive),
    partial: partialReasons.length > 0,
    partialReasons,
    agentOutdated: workloads.some(predatesRevisionSignals),
  }
}
