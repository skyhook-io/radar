import { FactGrid, FactRow, WorkspaceCard, formatDuration } from '@skyhook-io/k8s-ui'
import { substrateErrorCode, useSubstrateWorkers } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { slotsText } from './SubstratePoolSummary'
import { coverageInPart, coverageReason, definitiveFailure, tierReading } from './readings'
import { substrateActorsPath, substratePoolPath } from './routes'
import { WorkerStateChip } from './shared'

export function SubstrateWorkerPodContext({ namespace, pool, pod, resource }: {
  namespace: string
  pool: string
  pod: string
  resource: { metadata?: { deletionTimestamp?: string }; status?: { conditions?: { type: string; status: string }[] } }
}) {
  const poolKey = `${namespace}/${pool}`
  const query = useSubstrateWorkers(poolKey)
  const { connection } = useConnection()
  const navigate = useWorkspaceNavigate()
  const data = definitiveFailure(substrateErrorCode(query.error)) ? undefined : query.data
  const worker = data?.workers.find((w) => w.namespace === namespace && w.pool === pool && w.pod === pod)
  const ready = resource.status?.conditions?.find((c) => c.type === 'Ready')?.status
  const podState = resource.metadata?.deletionTimestamp ? 'Kubernetes Pod terminating'
    : ready === 'True' ? 'Kubernetes Pod ready' : ready === 'False' ? 'Kubernetes Pod not ready' : 'Kubernetes Pod readiness unreported'
  const absent = query.isLoading ? 'Reading Substrate worker state…'
    : query.error && !data ? query.error.message
    : !data ? 'Substrate worker state unavailable'
    : data.apiTier.state !== 'connected' ? tierReading(data.apiTier).detail
    : coverageInPart(data.coverage.workers) ? coverageReason(data.coverage.workers, 'workers')
    : 'No worker registration reported for this Pod'
  const actorsPartial = coverageInPart(data?.coverage.actors)
  const assigned = worker?.actors?.length
  return (
    <WorkspaceCard title="Substrate worker">
      <FactGrid>
        <FactRow label="State">
          {worker ? <><WorkerStateChip w={worker} />{worker.heldForSeconds !== undefined && <span className="ml-1.5 text-xs text-theme-text-tertiary">for at least {formatDuration(worker.heldForSeconds * 1000)}</span>}</> : <span className="text-theme-text-secondary">{absent}</span>}
          <span className="ml-1.5 text-xs text-theme-text-secondary">· {podState}</span>
        </FactRow>
        <FactRow label="Pool">
          <button type="button" className="font-mono text-accent-text hover:underline" onClick={() => navigate(substratePoolPath({ namespace, name: pool }, connection.context || undefined))}>{pool} →</button>
          {worker && <span className="text-xs text-theme-text-tertiary"> · {slotsText(worker)}</span>}
        </FactRow>
        {worker && <FactRow label="Assignments">
          <button type="button" className="text-accent-text hover:underline" onClick={() => navigate(substrateActorsPath({ pool: poolKey, worker: worker.name }))}>
            {assigned === undefined || (actorsPartial && assigned === 0) ? 'View assigned actors' : `Show ${actorsPartial ? '≥' : ''}${assigned} assigned ${assigned === 1 ? 'actor' : 'actors'}`} →
          </button>
        </FactRow>}
      </FactGrid>
      <p className="mt-2 text-xs text-theme-text-tertiary">Pod logs, metrics and restarts cover the shared worker process.</p>
    </WorkspaceCard>
  )
}
