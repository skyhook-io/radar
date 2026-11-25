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

describe('CostCard unavailable answers', () => {
  async function renderWithSummary(summary: unknown) {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://radar.invalid')
      if (url.pathname === '/api/opencost/summary') {
        return Promise.resolve(new Response(JSON.stringify(summary), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      }
      return new Promise<Response>(() => {})
    }))
    const container = document.createElement('div')
    const localRoot = createRoot(container)
    await act(async () => {
      localRoot.render(
        <QueryClientProvider client={client}>
          <CostCard />
        </QueryClientProvider>,
      )
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    return { container, unmount: () => act(async () => localRoot.unmount()) }
  }

  it('shows why costs were withheld in a shared metrics store', async () => {
    const { container, unmount } = await renderWithSummary({
      available: false,
      reason: 'cluster_scope',
      currency: 'USD',
      clusterScope: { state: 'several_clusters', note: 'This metrics store holds several clusters.' },
    })
    const note = container.querySelector('[role="note"]')
    expect(note?.getAttribute('data-cluster-scope')).toBe('several_clusters')
    expect(container.textContent).toContain('Costs withheld for this cluster')
    expect(container.textContent).toContain('This metrics store holds several clusters.')
    await unmount()
  })

  it.each([
    { label: 'no metrics', summary: { available: false, reason: 'no_metrics', currency: 'USD' } },
    { label: 'partial answer', summary: { available: false, reason: 'partial_data', currency: 'USD', clusterScope: { state: 'scoped_verified', note: 'ok' } } },
    { label: 'older Radar without a scope', summary: { available: false, reason: 'cluster_scope', currency: 'USD' } },
  ])('still hides for $label', async ({ summary }) => {
    const { container, unmount } = await renderWithSummary(summary)
    expect(container.innerHTML).toBe('')
    await unmount()
  })
})
