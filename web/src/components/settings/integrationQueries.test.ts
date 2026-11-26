import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { invalidateIntegrationQueries } from './integrationQueries'

describe('invalidateIntegrationQueries', () => {
  it('refreshes metrics dependents and the selected Argo views after reconfirming both', async () => {
    const client = new QueryClient()
    const affected = [
      ['prometheus-status'], ['prometheus-rightsizing-scan'], ['workload-metrics'],
      ['substrate-health'], ['cnpg', 'history'], ['cnpg', 'fleet-metrics'],
      ['cnpg', 'storage'], ['cnpg', 'disk'], ['traffic-sources', 'recent'],
      ['traffic-flows', 'records'], ['traffic-source-active'], ['traffic-connection'],
      ['opencost-application-trend'], ['dashboard'], ['diagnostics'],
      ['argocd-status'], ['gitops-tree'], ['gitops-insights'], ['argo-resource-diff'],
      ['argo-sync-diff'], ['argo-revision-metadata'],
    ]
    const unaffected = [['cnpg', 'workspace'], ['cnpg', 'restore'], ['gitops-destination'], ['resources']]
    for (const key of [...affected, ...unaffected]) client.setQueryData(key, 'cached')

    await invalidateIntegrationQueries(client, ['metrics', 'argocd'])

    expect(affected.filter(key => !client.getQueryState(key)?.isInvalidated)).toEqual([])
    expect(unaffected.filter(key => client.getQueryState(key)?.isInvalidated)).toEqual([])
    client.clear()
  })

  it('refetches an active cost view after a currency or cost-source save', async () => {
    const client = new QueryClient()
    const queryKey = ['opencost-summary']
    client.setQueryData(queryKey, 'previous')
    client.setQueryData(['prometheus-status'], 'previous')
    const queryFn = vi.fn(async () => 'updated')
    const observer = new QueryObserver(client, { queryKey, queryFn, staleTime: Infinity })
    const unsubscribe = observer.subscribe(() => {})
    try {
      await invalidateIntegrationQueries(client, ['cost'])
      expect(queryFn).toHaveBeenCalledTimes(1)
      expect(client.getQueryData(queryKey)).toBe('updated')
      expect(client.getQueryState(['prometheus-status'])?.isInvalidated).toBe(false)
    } finally {
      unsubscribe()
      client.clear()
    }
  })

  it('refreshes dependents without polling status again when OpenObserve settles', async () => {
    const client = new QueryClient()
    client.setQueryData(['prometheus-status'], 'settled')
    client.setQueryData(['prometheus-cluster'], 'previous')

    await invalidateIntegrationQueries(client, ['metrics'], { includeStatus: false })

    expect(client.getQueryState(['prometheus-status'])?.isInvalidated).toBe(false)
    expect(client.getQueryState(['prometheus-cluster'])?.isInvalidated).toBe(true)
    client.clear()
  })
})
