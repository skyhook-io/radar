import type { ResourceRef, TimelineEvent } from '../types/core'
import { getKindLabel } from './api-resources'
import type { AppHistory, AppHistoryAnchor, AppSourceRef } from './applications'
import { isProblematicEvent } from './resource-hierarchy'

export type ApplicationHistoryCategory = 'deployment' | 'runtime' | 'problem'
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

const JOB_LIFECYCLE_REASONS = new Set([
  'Started',
  'Completed',
  'Complete',
  'Failed',
  'BackoffLimitExceeded',
  'DeadlineExceeded',
  'SuccessCriteriaMet',
])

const PROBLEM_TITLES: Record<string, string> = {
  FailedScheduling: "Can't be scheduled",
  FailedMount: "Volume can't be mounted",
  FailedAttachVolume: "Volume can't be attached",
  FailedPull: 'Image pull failed',
  ErrImagePull: 'Image pull failed',
  ImagePullBackOff: 'Image pull back-off',
  Unhealthy: 'Health check failed',
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
    detail: anchor.message || anchor.source,
    status: anchor.status,
    revision: anchor.revision,
    initiatedBy: anchor.initiatedBy,
    sourceRef,
  }
}

function eventTitle(event: TimelineEvent, problem: boolean): string {
  const kind = getKindLabel(event.kind)
  if (problem) {
    if (event.reason === 'PodScheduled' && event.message?.includes('nodes are available')) return "Can't be scheduled"
    return PROBLEM_TITLES[event.reason ?? ''] ?? event.reason ?? `${kind} needs attention`
  }
  if (event.kind === 'Job' && event.reason) {
    if (event.reason === 'Complete' || event.reason === 'Completed' || event.reason === 'SuccessCriteriaMet') return 'Job completed'
    if (event.reason === 'Started') return 'Job started'
    if (event.reason === 'Failed' || event.reason === 'BackoffLimitExceeded' || event.reason === 'DeadlineExceeded') return 'Job failed'
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

function historyEventItem(event: TimelineEvent): ApplicationHistoryItem | null {
  if (!validTimestamp(event.timestamp)) return null
  const unhealthy = event.healthState === 'degraded' || event.healthState === 'unhealthy'
  const leafRuntimeResource = event.kind === 'Pod' || event.kind === 'ReplicaSet'
  const problem = isProblematicEvent(event) || (unhealthy && (!leafRuntimeResource || Boolean(event.message?.trim())))
  if (event.kind === 'Pod' || event.kind === 'ReplicaSet') {
    if (!problem) return null
  } else if (!problem && event.kind === 'Job' && event.source === 'k8s_event') {
    if (!event.reason || !JOB_LIFECYCLE_REASONS.has(event.reason)) return null
  } else if (!problem && !ROOT_RUNTIME_KINDS.has(event.kind) && !RELATED_RUNTIME_KINDS.has(event.kind)) {
    return null
  }
  if (!problem && event.eventType === 'update' && !event.diff?.summary && !event.reason && !event.message) return null
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
    category: problem ? 'problem' : 'runtime',
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
  const anchors = (history?.anchors ?? [])
    .map((anchor, index) => anchorItem(anchor, history?.sourceRef, index))
    .filter((item): item is ApplicationHistoryItem => item !== null)
  const hasAnchors = anchors.length > 0
  const warningItems = new Map<string, ApplicationHistoryItem>()
  const runtimeItems: ApplicationHistoryItem[] = []

  for (const event of events) {
    if (hasAnchors && event.source !== 'k8s_event' && sourceMatchesEvent(history?.sourceRef, event)) continue
    const item = historyEventItem(event)
    if (!item) continue
    if (item.category !== 'problem') {
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
