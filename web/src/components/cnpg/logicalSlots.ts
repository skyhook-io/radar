import type { UseQueryResult } from '@tanstack/react-query'
import type { CNPGPublisher, CNPGPublisherSlots } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, type CNPGRuntimeResponse } from '../../api/cnpg'

/** The publisher primary's replication slots from its runtime answer, never read as "no slots" when unread. */
export function cnpgPublisherSlotsFrom(rt: CNPGRuntimeResponse | undefined, error?: unknown, refetchFailed = false): CNPGPublisherSlots {
  if (!rt) return { state: 'notRead', reason: error instanceof Error ? error.message : undefined }
  const stale = refetchFailed || undefined
  if (rt.permission.proxy === 'denied') return { state: 'denied', reason: rt.permission.grant }
  const primary = rt.instances.find((i) => i.role === 'primary')
  if (!primary) return { state: 'unavailable', reason: 'no primary reported' }
  const st = primary.status
  if (st.state !== 'ok' && st.state !== 'partial') return { state: st.state === 'denied' ? 'denied' : 'unavailable', reason: st.error ?? st.state, stale }
  if (!st.slots) return { state: 'unavailable', reason: st.reason ?? 'the report carried no slots', stale }
  return {
    state: st.state,
    reason: st.state === 'partial' ? st.reason : undefined,
    stale,
    slots: st.slots.map((s) => ({ name: s.name, type: s.type, active: s.active, walStatus: s.walStatus, retainedBytes: s.retainedBytes, database: s.database })),
  }
}

/** The slots plus the query, so the caller can say when a refresh failed over cached data. */
export function useCNPGPublisherSlots(publisher: CNPGPublisher | undefined): { observed: CNPGPublisherSlots; query: UseQueryResult<CNPGRuntimeResponse> } {
  const cluster = publisher?.kind === 'cluster' ? publisher : undefined
  const query = useCNPGRuntime(cluster?.namespace ?? '', cluster?.name ?? '', !!cluster)
  return { observed: cnpgPublisherSlotsFrom(query.data, query.error, query.isRefetchError), query }
}
