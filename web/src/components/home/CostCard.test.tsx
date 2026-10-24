// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useOpenCostSummary } from '../../api/client'
import { CostCard } from './CostCard'

let root: ReturnType<typeof createRoot>
let client: QueryClient
let requested: URL[]

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  requested = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    requested.push(new URL(String(input), 'http://radar.invalid'))
    return new Promise<Response>(() => {})
  }))
  root = createRoot(document.createElement('div'))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
})

describe('CostCard namespace scope', () => {
  it('requests the picked namespaces', async () => {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <CostCard namespaces={['team-a', 'team-b']} />
        </QueryClientProvider>,
      )
    })

    const summary = requested.find((url) => url.pathname === '/api/opencost/summary')
    expect(summary?.searchParams.get('namespaces')).toBe('team-a,team-b')
  })

  it('fetches a separate query when the pick changes', async () => {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <CostCard namespaces={['team-a']} />
        </QueryClientProvider>,
      )
    })
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <CostCard namespaces={['team-b']} />
        </QueryClientProvider>,
      )
    })

    expect(requested.filter((url) => url.pathname === '/api/opencost/summary')
      .map((url) => url.searchParams.get('namespaces'))).toEqual(['team-a', 'team-b'])
    expect(client.getQueryCache().findAll({ queryKey: ['opencost-summary'] })).toHaveLength(2)
  })

  it.each([
    { label: 'omitted', namespaces: undefined },
    { label: 'empty', namespaces: [] },
  ])('keeps the unfiltered request for no namespaces ($label)', async ({ namespaces }) => {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <CostCard namespaces={namespaces} />
        </QueryClientProvider>,
      )
    })

    const summary = requested.find((url) => url.pathname === '/api/opencost/summary')
    expect(summary?.pathname).toBe('/api/opencost/summary')
    expect(summary?.search).toBe('')
  })

  it('keeps an unscoped consumer separate from the Home card', async () => {
    function UnscopedCost() {
      useOpenCostSummary()
      return null
    }

    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <CostCard namespaces={['team-a']} />
          <UnscopedCost />
        </QueryClientProvider>,
      )
    })

    expect(requested.filter((url) => url.pathname === '/api/opencost/summary')
      .map((url) => url.searchParams.get('namespaces'))).toEqual(['team-a', null])
  })
})
