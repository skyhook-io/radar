import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import type { HealthLevel } from '@skyhook-io/k8s-ui'
import { fetchJSON, useClusterInfo, useRadarFeature } from './client'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'
import type { WorkspaceKindCoverage } from '../components/workspace/coverage'
import type {
  KagentAgentSessions,
  SubstrateAPITier,
  SubstrateCoverage,
  SubstratePoolsCoverage,
  SubstratePoolRef,
  SubstrateRef,
} from './substrateWorkspace'

export interface KagentRef {
  group: string
  kind: string
  namespace: string
  name: string
}
export interface KagentReading {
  state: string
  tone: HealthLevel
  text: string
  summary: string
  condition?: string
  reason?: string
  message?: string
  since?: string
  egress?: string
}
export interface KagentDependency {
  ref: KagentRef
  state: string
  unread?: string
  reading?: KagentReading
}
export interface KagentReference {
  role: string
  target: KagentRef
  from: KagentRef
  field: string
  state: string
  unread?: string
  reading?: KagentReading
  policyUnknown?: boolean
  tools?: string[]
  key?: string
  fallback?: boolean
}
export interface KagentToolAsk {
  server: KagentRef
  tool: string
  agents?: KagentRef[]
}
export interface KagentAgent {
  ref: KagentRef
  reading: KagentReading
  latestChange: { state: string; message?: string }
  newSessionsRevision?: string
  desiredRevision?: string
  created?: string
  description?: string
  inlineTemplate: boolean
  harnessType?: string
  model?: KagentDependency
  mcpServers: { declared: number; failing: number; absent: number; unread: number; notPermitted: number }
  references: KagentReference[]
  toolsNotOffered?: KagentToolAsk[]
  incomplete?: boolean
  cycle?: string[]
}
export interface KagentAffected {
  agent: KagentRef
  effect: 'blocked' | 'mayRunWithoutTools' | 'usesIt'
  roles: string[]
  through?: string[]
}
export interface KagentImpact {
  agents: KagentAffected[]
  count: number
  lowerBound: boolean
  consumers: 'seen' | 'none' | 'noneSeen'
  byRole?: Record<string, number>
}
export interface KagentFinding {
  id: string
  subject: KagentRef
  severity: 'critical' | 'warning'
  reason: string
  kagentReason?: string
  message: string
  action: string
  since?: string
  secret?: KagentRef
  cause?: { target: KagentRef; role: string; form: string }
  affects?: KagentImpact
}
export interface KagentGroup {
  id: string
  severity: 'critical' | 'warning'
  secret?: KagentRef
  findings: string[]
  folded?: { finding: string; under: string }[]
}
export interface KagentMCPServer {
  ref: KagentRef
  reading: KagentReading
  url?: string
  protocol?: string
  toolsOffered: string[]
  toolsOfferedKnown: boolean
  failing: boolean
  headers?: { name: string; from?: KagentRef; key?: string; inline?: boolean }[]
  usedBy: KagentImpact
  askedNotOffered?: KagentToolAsk[]
}
export interface KagentCredential {
  mode: string
  secret?: KagentRef
  key?: string
}
export interface KagentModel {
  ref: KagentRef
  reading: KagentReading
  provider?: string
  model?: string
  credential: KagentCredential
  usedBy: KagentImpact
}
export interface KagentCount {
  n: number
  lowerBound: boolean
}
export type KagentCoverageKey =
  'agents' | 'agentTemplates' | 'harnesses' | 'models' | 'mcpServers' | 'egressPolicies' | 'modelProviders' | 'sandboxTemplates'
export interface KagentWorkspace {
  context: string
  installed: boolean
  namespaces: string[]
  forcedNamespace?: string
  coverage: Record<KagentCoverageKey, WorkspaceKindCoverage>
  referenceCoverage: Record<KagentCoverageKey, WorkspaceKindCoverage>
  agents: KagentAgent[]
  models: KagentModel[]
  mcpServers: KagentMCPServer[]
  modelProviders: { ref: KagentRef; reading: KagentReading; provider?: string }[]
  findings: KagentFinding[]
  groups: KagentGroup[]
  counts: Record<
    'findings' | 'critical' | 'agents' | 'agentsAffected' | 'mcpServers' | 'mcpServersFailing' | 'models' | 'modelsWithProblems',
    KagentCount
  > & { groups: { n: number; incomplete: boolean }; agentsByLatestChange: Record<string, number> }
}
export interface KagentSessionRow extends Pick<
  KagentAgentSessions,
  | 'agent'
  | 'revisions'
  | 'sessions'
  | 'onEarlierRevisions'
  | 'complete'
  | 'needAttention'
  | 'attention'
  | 'hintedEarlierRevisions'
  | 'hintShared'
> {
  crashed: number
  onNewSessionsRevision: number
  onOtherRevisions: number
  placement: {
    state: 'unknown' | 'mayFit' | 'noActiveWorkers' | 'full' | 'noRoom'
    reading: string
    template?: SubstrateRef
    pools: SubstratePoolRef[]
    complete: boolean
  }
}
export interface KagentSessionSummary {
  apiTier: SubstrateAPITier
  coverage: {
    actors: SubstrateCoverage
    templates: SubstrateCoverage
    workers: SubstrateCoverage
    atespaces: SubstrateCoverage
    kagentAgents: SubstratePoolsCoverage
  }
  rows: KagentSessionRow[]
  total: number
  lowerBound: boolean
  limit: number
  offset: number
}

export function useKagentWorkspace(namespaces: string[], enabled = true) {
  const feature = useRadarFeature('kagentWorkspace')
  const { data: cluster } = useClusterInfo()
  const params = new URLSearchParams()
  if (namespaces.length) params.set('namespaces', [...namespaces].sort().join(','))
  return useQuery<KagentWorkspace>({
    queryKey: ['kagent', 'workspace', getApiBase(), cluster?.context, params.toString(), ...feature.gatedKey],
    queryFn: ({ signal }) => feature.guard(() => fetchJSON<KagentWorkspace>(`/kagent/workspace?${params}`, signal)),
    enabled: enabled && feature.support === 'supported',
    staleTime: 10_000,
    refetchInterval: 30_000,
    retry: shouldRetryRadarQuery,
  })
}

export function useKagentSessionSummary(namespaces: string[], enabled = true) {
  const feature = useRadarFeature('kagentSessionSummary')
  const { data: cluster } = useClusterInfo()
  const scope = [...namespaces].sort().join(',')
  return useInfiniteQuery({
    queryKey: ['kagent', 'sessions', getApiBase(), cluster?.context, scope, ...feature.gatedKey],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) =>
      feature.guard(() =>
        fetchJSON<KagentSessionSummary>(
          `/substrate/kagent/summary?${new URLSearchParams({ namespaces: scope, offset: String(pageParam), limit: '1000' })}`,
          signal,
        ),
      ),
    getNextPageParam: (last) =>
      last.offset + last.rows.length < last.total && last.rows.length > 0 ? last.offset + last.rows.length : undefined,
    enabled: enabled && feature.support === 'supported',
    staleTime: 5_000,
    refetchInterval: 15_000,
    retry: false,
  })
}
