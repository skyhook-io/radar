import type { CNPGPublisher, CNPGPublisherSlots } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, type CNPGRuntimeResponse } from '../../api/cnpg'

/** The publisher primary's replication slots from its runtime answer, never read as "no slots" when unread. */
export function cnpgPublisherSlotsFrom(rt: CNPGRuntimeResponse | undefined, error?: unknown): CNPGPublisherSlots {
  if (!rt) return { state: 'notRead', reason: error instanceof Error ? error.message : undefined }
  if (rt.permission.proxy === 'denied') return { state: 'denied', reason: rt.permission.grant }
  const primary = rt.instances.find((i) => i.role === 'primary')
  if (!primary) return { state: 'unavailable', reason: 'no primary reported' }
  const st = primary.status
  if (st.state !== 'ok' && st.state !== 'partial') return { state: st.state === 'denied' ? 'denied' : 'unavailable', reason: st.error ?? st.state }
  return {
    state: 'ok',
    slots: (st.slots ?? []).map((s) => ({ name: s.name, type: s.type, active: s.active, walStatus: s.walStatus, retainedBytes: s.retainedBytes, database: s.database })),
  }
}

export function useCNPGPublisherSlots(publisher: CNPGPublisher | undefined): CNPGPublisherSlots {
  const cluster = publisher?.kind === 'cluster' ? publisher : undefined
  const q = useCNPGRuntime(cluster?.namespace ?? '', cluster?.name ?? '', !!cluster)
  return cnpgPublisherSlotsFrom(q.data, q.error)
}
