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
  /** How it got there: "Helm revision 12", "Argo CD sync by alice". */
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
}

export interface AppDeploys {
  rows: AppDeployRow[]
  rolloutInProgress: boolean
  partial: boolean
  /** One sentence per missing source, for display. */
  partialReasons: string[]
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

function closestAnchorFor(createdAt: string, anchors: AppHistoryAnchor[]): AppHistoryAnchor | undefined {
  const created = timeOf(createdAt)
  if (created === undefined) return undefined
  let closest: AppHistoryAnchor | undefined
  let closestGap = Infinity
  for (const anchor of anchors) {
    const at = timeOf(anchor.timestamp)
    if (at === undefined) continue
    const gap = at - created
    if (gap > ANCHOR_AFTER_REVISION_MS || gap < -ANCHOR_BEFORE_REVISION_MS) continue
    if (Math.abs(gap) < closestGap) {
      closest = anchor
      closestGap = Math.abs(gap)
    }
  }
  return closest
}

function anchorVia(anchor: AppHistoryAnchor): string {
  return anchor.initiatedBy ? `${anchor.title} by ${anchor.initiatedBy}` : anchor.title
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
    version: anchor.type === 'helm' ? anchor.source : anchor.revision,
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
    const anchor = closestAnchorFor(revision.createdAt, anchors)
    if (anchor) matched.add(anchor)
    sortAt = Math.max(sortAt, timeOf(revision.createdAt) ?? -Infinity)
    rows.push({
      sortAt,
      row: {
        id: `${ref.group ?? ''}/${ref.kind}/${ref.namespace}/${ref.name}#${revision.number}`,
        workload: ref,
        revision: revision.number,
        version: imageVersion(revision.image),
        image: revision.image,
        source: 'revision',
        via: anchor ? anchorVia(anchor) : revision.changeCause,
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

export function buildAppDeploys({ workloads, anchors, sourceRef, partialSources }: BuildAppDeploysInput): AppDeploys {
  const revisioned = workloads.filter((workload) => workloadHasRevisions(workload.ref.kind))
  const matched = new Set<AppHistoryAnchor>()
  const sortable = revisioned.flatMap((workload) => revisionRows(workload, anchors ?? [], matched))

  ;(anchors ?? []).forEach((anchor, index) => {
    if (matched.has(anchor)) return
    sortable.push({ row: anchorRow(anchor, index, sourceRef), sortAt: timeOf(anchor.timestamp) ?? -Infinity })
  })

  sortable.sort((a, b) => b.sortAt - a.sortAt || (b.row.revision ?? 0) - (a.row.revision ?? 0) || a.row.id.localeCompare(b.row.id))

  const partialReasons = [...(partialSources ?? [])]
  for (const workload of revisioned) {
    if (workload.revisions === undefined) {
      partialReasons.push(`Revision history for ${workload.ref.kind} ${workload.ref.name} couldn't be read.`)
    }
  }
  if (sourceRef && anchors === undefined) {
    partialReasons.push(`${sourceRef.type === 'helm' ? 'Helm' : 'GitOps'} history for ${sourceRef.name} isn't available here.`)
  }

  return {
    rows: sortable.map(({ row }) => row),
    rolloutInProgress: workloads.some((workload) => workload.rolloutActive),
    partial: partialReasons.length > 0,
    partialReasons,
  }
}
