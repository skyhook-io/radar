import type { ResourceRef, TimelineEvent } from '../types/core'
import { getKindLabel } from './api-resources'
import type { AppHistory, AppHistoryAnchor, AppSourceRef } from './applications'
import { isProblematicEvent } from './resource-hierarchy'

export type ApplicationHistoryCategory = 'deployment' | 'change' | 'problem'
export type ApplicationHistoryRange = '24h' | '7d' | '30d' | 'all'

export interface ApplicationHistoryItem {
  id: string
  category: ApplicationHistoryCategory
  title: string
  timestamp: string
  detail?: string
  status?: string
  revision?: string
  initiatedBy?: string
  count?: number
  resource?: ResourceRef
  sourceRef?: AppSourceRef
}

const ROOT_RUNTIME_KINDS = new Set([
  'Deployment',
  'DaemonSet',
  'StatefulSet',
  'Rollout',
  'Job',
  'CronJob',
  'Workflow',
  'CronWorkflow',
])

const RELATED_RUNTIME_KINDS = new Set([
  'Service',
  'Ingress',
  'Gateway',
  'HTTPRoute',
  'GRPCRoute',
  'TCPRoute',
  'TLSRoute',
  'ConfigMap',
  'Secret',
  'SealedSecret',
  'HorizontalPodAutoscaler',
  'ScaledObject',
  'ScaledJob',
  'PersistentVolumeClaim',
  'PodDisruptionBudget',
  'NetworkPolicy',
  'ServiceAccount',
  'ServiceMonitor',
  'PodMonitor',
])

const NORMAL_BATCH_LIFECYCLE_REASONS = new Set([
  'started',
  'created',
  'completed',
  'complete',
  'successcriteriamet',
])

const CONFIG_DATA_KINDS = new Set(['ConfigMap', 'Secret', 'SealedSecret'])
const CONFIG_DATA_PATH = /^(?:spec\.)?(?:data|stringData|binaryData|encryptedData)(\W|$)/
const EPHEMERAL_RUN_KINDS = new Set(['Job', 'Workflow'])
const SOURCE_BURST_WINDOW_MS = 2 * 60 * 1000

const PROBLEM_TITLES: Record<string, string> = {
  failedscheduling: "Can't be scheduled",
  failedmount: "Volume can't be mounted",
  failedattachvolume: "Volume can't be attached",
  failedpull: 'Image pull failed',
  errimagepull: 'Image pull failed',
  imagepullbackoff: 'Image pull back-off',
  unhealthy: 'Health check failed',
}

function validTimestamp(value: string | undefined): value is string {
  return Boolean(value && !value.startsWith('0001-01-01T00:00:00') && Number.isFinite(new Date(value).getTime()))
}

function sourceMatchesEvent(source: AppSourceRef | undefined, event: TimelineEvent): boolean {
  if (!source) return false
  return source.kind.toLowerCase() === event.kind.toLowerCase()
    && source.namespace === event.namespace
    && source.name === event.name
}

function anchorItem(anchor: AppHistoryAnchor, sourceRef: AppSourceRef | undefined, index: number): ApplicationHistoryItem | null {
  if (!validTimestamp(anchor.timestamp)) return null
  return {
    id: `source:${anchor.type}:${anchor.timestamp}:${anchor.revision ?? index}`,
    category: 'deployment',
    title: anchor.title,
    timestamp: anchor.timestamp,
    detail: anchor.source || anchor.message,
    status: anchor.status,
    revision: anchor.revision,
    initiatedBy: anchor.initiatedBy,
    sourceRef,
  }
}

function eventTitle(event: TimelineEvent, problem: boolean): string {
  const kind = getKindLabel(event.kind)
  if (problem) {
    const reason = event.reason?.toLowerCase()
    if (reason === 'podscheduled' && event.message?.includes('nodes are available')) return "Can't be scheduled"
    if (reason === 'failedcreate') {
      if (event.kind === 'CronJob' || event.kind === 'CronWorkflow') return 'Scheduled run could not be created'
      if (ROOT_RUNTIME_KINDS.has(event.kind)) return 'Pod could not be created'
      return `${kind} could not create a resource`
    }
    if (EPHEMERAL_RUN_KINDS.has(event.kind)) {
      if (reason === 'backofflimitexceeded') return `${kind} failed after repeated retries`
      if (reason === 'deadlineexceeded') return `${kind} exceeded its deadline`
      if (reason === 'failed') return `${kind} failed`
    }
    return PROBLEM_TITLES[reason ?? ''] ?? event.reason ?? `${kind} needs attention`
  }
  if (event.eventType === 'add') return `${kind} created`
  if (event.eventType === 'delete') return `${kind} deleted`
  if (event.reason && event.reason !== 'Update' && event.reason !== 'Updated') return event.reason
  return `${kind} updated`
}

function eventDetail(event: TimelineEvent): string | undefined {
  const message = event.message?.trim()
  const summary = event.diff?.summary?.trim()
  if (message && summary && message !== summary) return `${message} · ${summary}`
  return message || summary || undefined
}

function hasDesiredStateChange(event: TimelineEvent): boolean {
  return Boolean(event.diff?.fields.some(({ path }) => {
    if (path === 'spec' || path.startsWith('spec.')) return true
    if (!CONFIG_DATA_KINDS.has(event.kind)) return false
    return path === 'immutable' || (event.kind === 'Secret' && path === 'type') || CONFIG_DATA_PATH.test(path)
  }))
}

function hasConfigurationDataChange(event: TimelineEvent): boolean {
  return CONFIG_DATA_KINDS.has(event.kind) && Boolean(event.diff?.fields.some(({ path }) =>
    path === 'immutable' || CONFIG_DATA_PATH.test(path),
  ))
}

function isEphemeralRun(event: TimelineEvent): boolean {
  return EPHEMERAL_RUN_KINDS.has(event.kind) && Boolean(event.owner)
}

function historyEventItem(event: TimelineEvent): ApplicationHistoryItem | null {
  if (!validTimestamp(event.timestamp)) return null
  const unhealthy = event.healthState === 'degraded' || event.healthState === 'unhealthy'
  const leafRuntimeResource = event.kind === 'Pod' || event.kind === 'ReplicaSet'
  const reason = event.reason?.toLowerCase()
  const normalBatchLifecycle = EPHEMERAL_RUN_KINDS.has(event.kind)
    && event.eventType !== 'Warning'
    && Boolean(reason && NORMAL_BATCH_LIFECYCLE_REASONS.has(reason))
  const problem = !normalBatchLifecycle
    && (isProblematicEvent(event) || (unhealthy && (!leafRuntimeResource || Boolean(event.message?.trim()))))
  if (!problem && event.source === 'k8s_event') return null
  if (event.kind === 'Pod' || event.kind === 'ReplicaSet') {
    if (!problem) return null
  } else if (!problem && !ROOT_RUNTIME_KINDS.has(event.kind) && !RELATED_RUNTIME_KINDS.has(event.kind)) {
    return null
  }
  if (!problem && event.eventType === 'update' && !hasDesiredStateChange(event)) return null
  if (!problem && (event.eventType === 'add' || event.eventType === 'delete') && isEphemeralRun(event)) return null
  const resource = problem && event.owner
    ? {
        kind: event.owner.kind,
        namespace: event.namespace,
        name: event.owner.name,
      }
    : {
        kind: event.kind,
        namespace: event.namespace,
        name: event.name,
        group: event.apiVersion?.includes('/') ? event.apiVersion.split('/')[0] : undefined,
      }
  return {
    id: `event:${event.id}`,
    category: problem ? 'problem' : 'change',
    title: eventTitle(event, problem),
    timestamp: event.timestamp,
    detail: eventDetail(event),
    count: event.count,
    resource,
  }
}

function warningKey(item: ApplicationHistoryItem): string {
  const resource = item.resource
  return `${item.title}\u0000${resource?.kind ?? ''}\u0000${resource?.namespace ?? ''}\u0000${resource?.name ?? ''}\u0000${item.detail ?? ''}`
}

export function buildApplicationHistoryItems(
  history: AppHistory | undefined,
  events: TimelineEvent[],
): ApplicationHistoryItem[] {
  const anchorsByChange = new Map<string, AppHistoryAnchor>()
  for (const anchor of history?.anchors ?? []) {
    if (!validTimestamp(anchor.timestamp)) continue
    const key = `${anchor.type}\u0000${anchor.title}\u0000${anchor.timestamp}\u0000${anchor.revision ?? ''}`
    const existing = anchorsByChange.get(key)
    if (!existing) {
      anchorsByChange.set(key, anchor)
      continue
    }
    anchorsByChange.set(key, {
      ...existing,
      status: anchor.status ?? existing.status,
      source: existing.source ?? anchor.source,
      message: anchor.message ?? existing.message,
      initiatedBy: anchor.initiatedBy ?? existing.initiatedBy,
    })
  }
  const anchors = [...anchorsByChange.values()]
    .map((anchor, index) => anchorItem(anchor, history?.sourceRef, index))
    .filter((item): item is ApplicationHistoryItem => item !== null)
  const hasAnchors = anchors.length > 0
  const anchorTimes = anchors.map(({ timestamp }) => new Date(timestamp).getTime())
  const warningItems = new Map<string, ApplicationHistoryItem>()
  const runtimeItems: ApplicationHistoryItem[] = []

  for (const event of events) {
    const item = historyEventItem(event)
    if (!item) continue
    if (item.category !== 'problem' && sourceMatchesEvent(history?.sourceRef, event)) continue
    if (item.category !== 'problem') {
      const eventTime = new Date(item.timestamp).getTime()
      if (!hasConfigurationDataChange(event)
        && hasAnchors
        && anchorTimes.some((anchorTime) => Math.abs(eventTime - anchorTime) <= SOURCE_BURST_WINDOW_MS)) continue
      runtimeItems.push(item)
      continue
    }
    const key = warningKey(item)
    const existing = warningItems.get(key)
    if (!existing) {
      warningItems.set(key, item)
      continue
    }
    existing.count = Math.max(existing.count ?? 1, item.count ?? 1)
    if (new Date(item.timestamp).getTime() > new Date(existing.timestamp).getTime()) {
      existing.timestamp = item.timestamp
      existing.id = item.id
    }
  }

  return [...anchors, ...runtimeItems, ...warningItems.values()].sort((a, b) => {
    const byTime = new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
    return byTime || a.id.localeCompare(b.id)
  })
}
