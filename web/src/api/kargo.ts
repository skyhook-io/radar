import { useQuery } from '@tanstack/react-query'
import type { Issue, KargoFreight } from '@skyhook-io/k8s-ui'
import type { WorkspaceKindCoverage } from '../components/workspace/coverage'
import { fetchJSON, useRadarFeature } from './client'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

// Wire types of internal/server/kargo_workspace.go.

export interface KargoProjectSummary {
  name: string
  stages: number
  /** The project's Kargo findings in Issues, each counted once. */
  problems: number
  /** The namespaces its Stages' Argo CD Applications deploy to in this cluster. */
  deployNamespaces?: string[]
}

export interface KargoWorkspaceResponse {
  installed: boolean
  projects: KargoProjectSummary[]
  /** "stages": where the caller's projects were looked for. */
  coverage: Record<string, WorkspaceKindCoverage>
}

export interface KargoFreightRefView {
  name: string
  /** The Warehouse it came from. */
  origin: string
  /** Set only when the caller can read the Freight. */
  alias?: string
  versions: string[]
  /** When the Stage received it; absent when unknown. */
  since?: string
}

export interface KargoLagView {
  origin: string
  state: 'current' | 'behind' | 'unknown'
  reason: string
  cause?: string
  message: string
  newest?: KargoFreightRefView
  stages?: string[]
  promotion?: string
  /** The AnalysisRuns of failed upstream verifications. */
  analysisRuns?: { namespace: string; name: string }[]
}

export interface KargoPromotionView {
  name: string
  phase?: string
  finishedAt?: string
  freight?: string
  failedStep?: string
  /** Kargo's full failure message. */
  failure?: string
  /** The failure without Kargo's own wrappers, so the step's error leads. */
  failureCause?: string
}

export interface KargoStageView {
  name: string
  depth: number
  upstream?: string[]
  warehouses?: string[]
  missingUpstream?: string[]
  phase: string
  controlFlow?: boolean
  autoPromotion: boolean
  current: KargoFreightRefView[]
  health?: { status: string; issues?: string[] }
  verificationConfigured: boolean
  verification?: { phase: string; message?: string; analysisRun?: { namespace: string; name: string }; finishedAt?: string }
  lastPromotion?: KargoPromotionView
  currentPromotion?: KargoPromotionView
  argoCDApps?: { namespace: string; name: string; health?: string; sync?: string }[]
  whyNotHere: KargoLagView[]
}

export interface KargoWarehouseView {
  name: string
  conditions?: { type: string; status?: string; reason?: string; message?: string }[]
  latest?: KargoFreightRefView
  freightCount: number
}

export interface KargoVerificationRun {
  name: string
  namespace: string
  stage: string
  phase?: string
  finishedAt?: string
}

export interface KargoProjectResponse {
  name: string
  /** "stages", "freight" and "warehouses" for this project. */
  coverage: Record<string, WorkspaceKindCoverage>
  freightRead: boolean
  stages: KargoStageView[]
  warehouses: KargoWarehouseView[]
  /** Newest first. */
  freight: KargoFreight[]
  freightTotal: number
  /** How many Freight each Warehouse has in all; `freight` holds each one's newest. Set when Freight was read. */
  freightByWarehouse?: Record<string, number>
  /** By Freight name; absent when Stages were not read. */
  verificationRuns?: Record<string, KargoVerificationRun[]>
  issues: Issue[]
}

export interface KargoFreightAliasesResponse {
  /** "namespace/freightName" -> alias. */
  aliases: Record<string, string>
  coverage: WorkspaceKindCoverage
}

// Radar Hub points one query client at many clusters, so the API base is in
// every key and a previous answer is kept only for the same cluster.

// /api/kargo/workspace: the projects the caller can read Stages in.
export function useKargoWorkspace(options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('kargoWorkspace')
  const base = getApiBase()
  return useQuery<KargoWorkspaceResponse>({
    queryKey: ['kargo', 'workspace', base, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<KargoWorkspaceResponse>('/kargo/workspace', signal)),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === base ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}

// /api/kargo/projects/{project}: the pipeline, Warehouses, Freight and
// findings of one project. Also feeds the Kargo drawers.
export function useKargoProject(project: string | undefined, options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('kargoWorkspace')
  const base = getApiBase()
  return useQuery<KargoProjectResponse>({
    queryKey: ['kargo', 'project', base, project, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<KargoProjectResponse>(`/kargo/projects/${encodeURIComponent(project ?? '')}`, signal)),
    enabled: (options?.enabled ?? true) && !!project,
    staleTime: 10_000,
    refetchInterval: 30_000,
    // Another project's answer must never stand in for this one while it loads.
    placeholderData: (prev, prevQuery) => (prev && prevQuery?.queryKey[2] === base && prev.name === project ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}

// /api/kargo/freight-aliases: Freight aliases for the Stage and Promotion lists.
export function useKargoFreightAliases(namespaces: string[], options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('kargoWorkspace')
  const base = getApiBase()
  const query = namespaces.length ? `?namespaces=${encodeURIComponent(namespaces.join(','))}` : ''
  return useQuery<KargoFreightAliasesResponse>({
    queryKey: ['kargo', 'freight-aliases', base, namespaces, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<KargoFreightAliasesResponse>(`/kargo/freight-aliases${query}`, signal)),
    enabled: options?.enabled ?? true,
    staleTime: 30_000,
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === base ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}
