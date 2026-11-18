import { useCallback, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, Server } from 'lucide-react'
import { PaneLoader, type WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import { substrateErrorCode, useSubstratePool, type SubstratePoolRef, type SubstrateSubject } from '../../api/substrateWorkspace'
import type { SelectedResource } from '../../types'
import { refToSelectedResource } from '../../utils/navigation'
import { WorkloadView } from '../workload/WorkloadView'
import { Notice, RefreshFailedNotice } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { SUBSTRATE_GROUP, SUBSTRATE_PATH, SUBSTRATE_POOLS_PATH } from './routes'
import { WorkersTable } from './SubstrateWorkers'
import { useOpenActor } from './SubstratePoolSummary'
import { definitiveFailure, poolReadFailure, tierReading } from './readings'
import { NotInContext, useContextPin } from './contextPin'

function PoolWorkers({ pool, onOpenResource }: { pool: SubstratePoolRef; onOpenResource: (resource: SelectedResource) => void }) {
  const query = useSubstratePool(pool)
  const code = substrateErrorCode(query.error)
  const data = definitiveFailure(code) || code === 'incomplete' ? undefined : query.data
  const openActor = useOpenActor()
  const open = (s: SubstrateSubject) => {
    if (s.kind === 'Pod' || s.kind === 'Worker') onOpenResource(refToSelectedResource({ kind: 'Pod', group: '', namespace: s.namespace ?? '', name: s.name ?? '' }))
    if (s.kind === 'Actor' && s.atespace && s.name) openActor({ atespace: s.atespace, name: s.name })
  }
  if (query.isLoading) return <PaneLoader label="Loading workers…" />
  if (!data) return <div className="p-4"><Notice>{poolReadFailure(code, query.error instanceof Error ? query.error.message : undefined)}</Notice></div>
  if (data.apiTier.state !== 'connected') return <div className="p-4"><Notice>{tierReading(data.apiTier).detail}</Notice></div>
  return (
    <div className="h-full space-y-3 overflow-y-auto p-4">
      <RefreshFailedNotice queries={[query]} />
      <WorkersTable data={{ workers: data.workers, coverage: { workers: data.coverage.workers } }} onOpenSubject={open} />
    </div>
  )
}

/**
 * A WorkerPool's page: Radar's detail view of the Kubernetes object, framed
 * by Agent Substrate, with its workers from Substrate's API. `ctx` pins the
 * context, as on an actor's page.
 */
export function SubstratePoolPage({ pool, onOpenResource }: { pool: SubstratePoolRef; onOpenResource: (resource: SelectedResource) => void }) {
  const navigate = useWorkspaceNavigate()
  const pin = useContextPin()
  const queryClient = useQueryClient()
  const refresh = useCallback(() => queryClient.refetchQueries({ type: 'active', queryKey: ['substrate'] }), [queryClient])

  const extraTabs = useMemo<WorkloadExtraTab[]>(
    () => [{ id: 'workers', label: 'Workers', after: 'overview', icon: <Server className="h-4 w-4" />, render: () => <PoolWorkers pool={pool} onOpenResource={onOpenResource} /> }],
    [pool, onOpenResource],
  )
  const titlePrefix = (
    <nav aria-label="Location" className="flex shrink-0 items-center gap-1.5 text-sm text-theme-text-tertiary">
      <Bot className="h-3.5 w-3.5" />
      <button type="button" onClick={() => navigate(SUBSTRATE_PATH)} className="hover:text-theme-text-primary hover:underline">Agent Substrate</button>
      <span>/</span>
      <button type="button" onClick={() => navigate(SUBSTRATE_POOLS_PATH)} className="hover:text-theme-text-primary hover:underline">Pools</button>
      <span>/</span>
    </nav>
  )
  // Before the Kubernetes view mounts: its reads and its controls would act on the other cluster's namesake.
  if (pin.mismatch) {
    return <NotInContext name={pool.name} what={`WorkerPool ${pool.namespace} / ${pool.name}`} pinnedContext={pin.pinned!} activeContext={pin.active} />
  }
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <WorkloadView
        key={`${pool.namespace}/${pool.name}`}
        kind="workerpools"
        namespace={pool.namespace}
        name={pool.name}
        group={SUBSTRATE_GROUP}
        expanded
        onBack={() => navigate(SUBSTRATE_POOLS_PATH)}
        hideBackButton
        titlePrefix={titlePrefix}
        inlineBadges
        hideKindBadge
        truncateName
        onNavigateToResource={onOpenResource}
        onRefresh={refresh}
        extraTabs={extraTabs}
      />
    </div>
  )
}
