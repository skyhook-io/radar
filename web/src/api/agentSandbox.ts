import { useQuery } from '@tanstack/react-query'
import type { HealthLevel } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

// Wire types of GET /api/agentsandbox/workspace: internal/server
// AgentSandboxWorkspaceResponse and pkg/agentsandbox Summary.

export type AgentSandboxCoverageState = 'full' | 'partial' | 'denied' | 'uncached' | 'notInstalled' | 'syncing' | 'error'

export interface AgentSandboxKindCoverage {
  state: AgentSandboxCoverageState
  /** Named only when the caller named the namespaces. */
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  /** For partial coverage: the namespaces that were read. */
  allowedNamespaces?: string[]
}

export type AgentSandboxWorkspaceKey = 'sandboxes' | 'claims' | 'warmPools' | 'templates'

export interface AgentSandboxRef {
  kind: string
  group: string
  namespace?: string
  name: string
}

/** What Radar can say about an object a row names: absent only where its kind was read in full in that namespace. */
export type AgentSandboxPresence = 'present' | 'absent' | 'unread'

export interface AgentSandboxRelation {
  role: 'warmPool' | 'template' | 'sandbox' | 'owner'
  ref: AgentSandboxRef
  presence: AgentSandboxPresence
}

export type AgentSandboxCategory = 'controller' | 'sandbox' | 'claim' | 'warmPool'

/** Which rule raised a row; branch on this, never on the title. */
export type AgentSandboxRule =
  | 'controllerNotRunning'
  | 'failed'
  | 'reconcilerError'
  | 'suspensionBlocked'
  /**
   * In progress for longer than 30 minutes (IncidentAfter). Whether Issues list
   * it is the row's `issue`: a claim forwarding its Sandbox's progress is not
   * listed, since Issues report that on the Sandbox.
   */
  | 'stalled'
  /** In progress past 5 minutes: an advisory, never an Issue. */
  | 'slow'
  | 'cannotBind'
  | 'templateMissing'
  | 'reserveBelowTarget'

export interface AgentSandboxAttention {
  id: string
  category: AgentSandboxCategory
  rule: AgentSandboxRule
  /**
   * The controller's own reason where it reported one (PodFailed,
   * Unschedulable, WarmPoolNotFound); an AgentSandbox-prefixed reason where
   * Radar found it (AgentSandboxWarmPoolNotFound, AgentSandboxControllerNotReady).
   */
  reason?: string
  /** The prominence ladder GitOps, Velero, CNPG and Substrate rows use. */
  severity: 'critical' | 'alert' | 'warning'
  /** Carries no elapsed time; `since` does. */
  title: string
  reading?: string
  /** What limits or contradicts the row in Radar's own reads; never folded away. */
  caveat?: string
  /** The controller's or scheduler's own words, folded under the reading. */
  rawDetail?: string
  rawDetailLabel?: string
  /** `scheduler`: rawDetail can be put plainly with summarizeSchedulerMessage. */
  rawDetailSource?: 'scheduler' | 'controller'
  nextStep: string
  /** The one shell command nextStep names, verbatim and in parentheses there: a client may show it as a copyable chip. */
  command?: string
  subject: AgentSandboxRef
  related?: { lead: string; refs: AgentSandboxRef[] }
  relations?: AgentSandboxRelation[]
  /**
   * When the state began: the latest transition among the conditions that
   * report it, so no earlier than this. Absent where no condition records one
   * (a refused suspension, a pool's reserve, Radar's own missing-object joins).
   */
  since?: string
  /** Radar's Issues list the subject for the same state: the authoritative parity bit, which no rule implies. */
  issue: boolean
  /**
   * On a warm pool's reserve row when no sandbox in it is ready: its unbound
   * claims still in progress, which can't take a warm sandbox. Absent when
   * none of the namespace's claims were read.
   */
  waitingClaims?: AgentSandboxCount
  /** On an unbound claim's row: the warm pool it names, read, with no sandbox ready. */
  emptyPool?: AgentSandboxRef
}

export type AgentSandboxControllerState = 'running' | 'notReady' | 'scaledToZero' | 'notFound' | 'unread' | 'notInstalled'

export interface AgentSandboxController {
  state: AgentSandboxControllerState
  namespace: string
  name: string
  /** Set only when the Deployment was read. */
  ready?: number
  desired?: number
  /**
   * Why it was not read, when state is unread: a pkg/readreason code
   * (user_forbidden, outside_namespace_pin, radar_namespace_not_collected,
   * radar_kind_not_collected, kind_sync_pending, kind_sync_timed_out,
   * read_error); readActionFor gives its action.
   */
  code?: string
  /** When the Deployment's Available condition last turned False. */
  since?: string
}

/** Lower bound: read in part, so every count is at least this and says nothing about the rest. */
export interface AgentSandboxSandboxCounts {
  total: number
  lowerBound: boolean
  running: number
  /** In progress, other than unschedulable. */
  starting: number
  /** In progress, and the scheduler can't place its Pod (PodScheduled=False with the scheduler's reason). */
  unschedulable: number
  /** Confirmed suspended, or suspending. */
  suspended: number
  /** The controller refuses the suspension (PodNotOwned); its Pod may still be running. */
  suspensionBlocked: number
  expired: number
  finished: number
  failed: number
  terminating: number
  unknown: number
}

export interface AgentSandboxClaimCounts {
  total: number
  lowerBound: boolean
  bound: number
  waiting: number
  expired: number
}

export interface AgentSandboxWarmPoolCounts {
  total: number
  lowerBound: boolean
  ready: number
  /** Only pools that set spec.replicas. */
  desired: number
}

/** A kind is absent when nothing of it was read. */
export interface AgentSandboxCounts {
  sandboxes?: AgentSandboxSandboxCounts
  claims?: AgentSandboxClaimCounts
  warmPools?: AgentSandboxWarmPoolCounts
}

/** `count` is null when the read was partial and found nothing. */
export interface AgentSandboxCount {
  count: number | null
  lowerBound: boolean
}

export interface AgentSandboxSidebar {
  total: AgentSandboxCount
  sandboxes: AgentSandboxCount
  claims: AgentSandboxCount
  warmPools: AgentSandboxCount
  /** The total includes the controller's row, shared by every namespace. */
  controller: boolean
}

export interface AgentSandboxWarmPoolRow {
  namespace: string
  name: string
  state: { text: string; level: HealthLevel }
  ready: number
  /** Absent when the pool sets no spec.replicas. */
  desired?: number
  template?: string
  templatePresence?: AgentSandboxPresence
  /** Absent when the namespace's claims were not read. */
  claims?: { total: number; bound: number }
  created?: string
}

export interface AgentSandboxExpiredEntry {
  ref: AgentSandboxRef
  /** The configured shutdown time. */
  at: string
}

export interface AgentSandboxExpired {
  /**
   * Within the last 7 days, newest first, capped. An expiry with no shutdown
   * time recorded can't be placed in the week and is left out; the status
   * line's expired count still includes it.
   */
  recent: AgentSandboxExpiredEntry[]
  recentTotal: number
  lowerBound: boolean
}

export interface AgentSandboxWorkspaceResponse {
  context: string
  installed: boolean
  /** The namespaces asked for, after Radar's --namespace-scope clamp; empty for all the caller may read. */
  namespaces: string[]
  forcedNamespace?: string
  coverage: Record<AgentSandboxWorkspaceKey, AgentSandboxKindCoverage>
  controller: AgentSandboxController
  counts: AgentSandboxCounts
  /** One list ordered by severity, then rule, then newest first; never grouped by kind. */
  attention: AgentSandboxAttention[]
  attentionTotal: number
  /** Every kind and the controller were read in full: no rows means nothing needs attention. */
  attentionComplete: boolean
  sidebar: AgentSandboxSidebar
  warmPools: AgentSandboxWarmPoolRow[]
  warmPoolsTotal: number
  expired: AgentSandboxExpired
}

// /api/agentsandbox/workspace?namespaces=
//
// The Overview, Needs attention and the sidebar's badges, computed on the
// server from what the caller can read. The namespace filter applies. The API
// base is in the key because Radar Hub points one query client at many
// clusters.
export function useAgentSandboxWorkspace(namespaces: string[], options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('agentSandboxWorkspace')
  const base = getApiBase()
  const ns = [...namespaces].sort().join(',')
  return useQuery<AgentSandboxWorkspaceResponse>({
    queryKey: ['agent-sandbox', 'workspace', base, ns, ...gatedKey],
    queryFn: ({ signal }) =>
      guard(() => fetchJSON<AgentSandboxWorkspaceResponse>(`/agentsandbox/workspace${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal)),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    // Keep the previous answer while refetching, but never another cluster's
    // or another namespace filter's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === base && prevQuery?.queryKey[3] === ns ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}
