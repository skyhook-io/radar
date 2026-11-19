// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RadarFeatureUnsupportedError, guardRadarFeature, type RadarFeatureSupport } from '../../api/radarFeatures'
import { workspace } from './velero.fixtures'

const state = vi.hoisted(() => ({ support: 'supported' as RadarFeatureSupport, fetches: [] as string[] }))

vi.mock('../../api/client', () => ({
  useRadarFeature: () => ({
    support: state.support,
    guard: <T,>(request: () => Promise<T>) => guardRadarFeature('veleroWorkspace', state.support, { currentVersion: 'v1.16.0' }, request),
    gatedKey: state.support === 'unsupported' ? ['radar-feature-unsupported'] : [],
  }),
  fetchJSON: async (path: string) => {
    state.fetches.push(path)
    const { workspace: build } = await import('./velero.fixtures')
    return build()
  },
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

import { useVeleroWorkspace } from '../../api/velero'
import { VeleroScreenGate } from './shared'

async function settle(done: () => boolean) {
  for (let i = 0; i < 50 && !done(); i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 10)) })
  }
}

afterEach(() => {
  state.support = 'supported'
  state.fetches = []
})

type GateQuery = ComponentProps<typeof VeleroScreenGate>['query']
const ready: GateQuery = { data: undefined, error: null, isLoading: false, isFetching: false, refetch: vi.fn(), isRefetchError: false, dataUpdatedAt: 0 }

function gate(query: Partial<GateQuery>, opts = { discoveryPending: false, discovered: true }) {
  return renderToStaticMarkup(
    <VeleroScreenGate query={{ ...ready, ...query }} discoveryPending={opts.discoveryPending} discovered={opts.discovered}>
      {() => <div>screen</div>}
    </VeleroScreenGate>,
  )
}

describe('Velero screen gate', () => {
  it('shows the upgrade note, not an error, on a Radar that predates the views', () => {
    const html = gate({ error: new RadarFeatureUnsupportedError('veleroWorkspace', { currentVersion: 'v1.16.0' }) })
    expect(html).toContain('Velero needs a newer Radar on this cluster')
    expect(html).toContain('This cluster runs v1.16.')
    expect(html).not.toContain('Velero data unavailable')
  })

  it('offers a retry for a failed read, and says when Velero is not installed', () => {
    expect(gate({ error: new Error('boom') })).toContain('Retry')
    expect(gate({ data: workspace({ installed: false }) })).toContain('Velero is not installed')
    expect(gate({}, { discoveryPending: false, discovered: false })).toContain('Velero is not installed')
    expect(gate({}, { discoveryPending: true, discovered: false })).toContain('Loading Velero')
    expect(gate({ data: workspace() })).toContain('screen')
  })
})

describe('useVeleroWorkspace', () => {
  it('stops asking a Radar once it is known to predate the views, on a shared query client', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const seen: { data?: unknown; error?: unknown }[] = []
    function Probe() {
      const q = useVeleroWorkspace()
      seen.push({ data: q.data, error: q.error })
      return null
    }
    const root = createRoot(document.createElement('div'))
    await act(async () => {
      root.render(<QueryClientProvider client={queryClient}><Probe /></QueryClientProvider>)
    })
    await settle(() => !!seen.at(-1)?.data)
    expect(state.fetches).toEqual(['/velero/workspace'])
    expect(seen.at(-1)?.data).toBeTruthy()

    state.support = 'unsupported'
    await act(async () => {
      root.render(<QueryClientProvider client={queryClient}><Probe key="after" /></QueryClientProvider>)
    })
    await settle(() => !!seen.at(-1)?.error)
    expect(state.fetches).toEqual(['/velero/workspace'])
    expect(seen.at(-1)?.error).toBeInstanceOf(RadarFeatureUnsupportedError)
    // The previous cluster answer is not kept as a placeholder once the gate decides.
    expect(seen.at(-1)?.data).toBeUndefined()
    root.unmount()
  })
})
