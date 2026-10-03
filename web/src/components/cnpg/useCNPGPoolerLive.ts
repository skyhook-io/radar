import type { UseQueryResult } from '@tanstack/react-query'
import type { CNPGPoolerLive } from '@skyhook-io/k8s-ui'
import { useCNPGPoolerRuntime } from '../../api/cnpg'
import { useCNPGPgBouncerState, useCNPGPoolerCapabilities } from '../../api/cnpg-sessions'

/**
 * The live reads the Pooler summary shows beside the spec, and the queries
 * behind them so a host can say when a refresh failed over cached values.
 */
export function useCNPGPoolerLive(namespace: string, name: string): { live: CNPGPoolerLive; queries: Pick<UseQueryResult<unknown>, 'isRefetchError' | 'error' | 'dataUpdatedAt'>[] } {
  const caps = useCNPGPoolerCapabilities(namespace, name)
  const runtime = useCNPGPoolerRuntime(namespace, name)
  const observeDenied = caps.data?.actions.observeState.permission === 'denied'
  const state = useCNPGPgBouncerState(namespace, name, !!caps.data && !observeDenied)

  const live: CNPGPoolerLive = {}
  if (caps.data) {
    live.deployment = caps.data.facts.deployment
    live.service = caps.data.facts.service
  }
  if (runtime.data) {
    const denied = runtime.data.permission.proxy === 'denied'
    live.pressure = {
      state: denied ? 'denied' : 'ok',
      reason: denied ? `needs ${runtime.data.permission.grant ?? 'get pods/proxy'}` : undefined,
      pods: runtime.data.pods,
    }
  } else {
    live.pressure = runtime.isLoading
      ? { state: 'loading', pods: [] }
      : { state: 'error', reason: runtime.error instanceof Error ? runtime.error.message : 'read failed', pods: [] }
  }
  if (caps.data) {
    if (observeDenied) {
      live.observed = { state: 'denied', grant: caps.data.actions.observeState.grant, pods: [] }
    } else if (state.data) {
      live.observed = { state: state.data.permission.exec === 'denied' ? 'denied' : 'ok', grant: state.data.permission.grant, pods: state.data.pods }
    } else {
      live.observed = state.isLoading
        ? { state: 'loading', pods: [] }
        : { state: 'error', reason: state.error instanceof Error ? state.error.message : undefined, pods: [] }
    }
  }
  return { live, queries: [caps, runtime, state] }
}
