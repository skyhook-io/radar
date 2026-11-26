import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { SubstrateWorkspace } from '../../api/substrateWorkspace'
import type { APIResource } from '../../types'
import { RadarFeatureUnsupportedError, type RadarFeatureSupport } from '../../api/radarFeatures'
import { SubstrateScreenGate } from './shared'
import { SUBSTRATE_SIDEBAR_LABEL, useSubstrateSidebarWorkspace } from './useSubstrateSidebarWorkspace'
import { notConnectedWorkspace, workspace } from './substrate.fixtures'

const state = vi.hoisted(() => ({ support: 'supported' as RadarFeatureSupport, data: undefined as SubstrateWorkspace | undefined }))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: state.support }),
}))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateWorkspace: () => ({ data: state.data }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-demo' } }) }))

const substrate: APIResource[] = [{ group: 'ate.dev', version: 'v1alpha1', kind: 'WorkerPool', name: 'workerpools', namespaced: true, isCrd: true, verbs: ['list'] } as unknown as APIResource]

function sidebar(apiResources: APIResource[] | undefined = substrate): SidebarCategoryWorkspace | undefined {
  let out: Record<string, SidebarCategoryWorkspace> | undefined
  function Probe() {
    out = useSubstrateSidebarWorkspace({ apiResources, active: 'actors' })
    return null
  }
  renderToStaticMarkup(<MemoryRouter><Probe /></MemoryRouter>)
  return out?.['Agent Substrate']
}

describe('Agent Substrate in the sidebar', () => {
  it('is one Integrations row named for its job, opening to its screens and Radar’s pool table', () => {
    state.data = workspace()
    const w = sidebar()!
    expect(w.label).toBe(SUBSTRATE_SIDEBAR_LABEL)
    expect(w.count).toBe(3)
    expect(w.countTitle).toContain("3 findings need attention in Overview")
    expect(w.destinations.map((d) => d.label)).toEqual(['Overview', 'Actors', 'Templates', 'Workers', 'Pools'])
    // The badge counts actors needing attention, as the Actors screen shows them: the fixture's 14 crashed.
    expect(w.destinations.find((d) => d.id === 'actors')).toMatchObject({ active: true, count: 14 })
    // The Pools row is the WorkerPool kind's list, so the kind is not listed again under Other kinds.
    expect(w.destinations.find((d) => d.id === 'pools')?.kind).toEqual({ name: 'workerpools', kind: 'WorkerPool', group: 'ate.dev' })
  })

  it('reads unknown counts, not zero, while Substrate’s API is not connected', () => {
    state.data = notConnectedWorkspace()
    const w = sidebar()!
    expect(w.count).toBeNull()
    expect(w.destinations.find((d) => d.id === 'actors')?.count).toBeNull()
  })

  it('is absent where Substrate is not installed or the Radar predates the views', () => {
    state.data = workspace()
    expect(sidebar([])).toBeUndefined()
    state.support = 'unsupported'
    expect(sidebar()).toBeUndefined()
    state.support = 'supported'
  })
})

describe('Agent Substrate screen gate', () => {
  const ready = { data: undefined as unknown, error: null as unknown, isLoading: false, isFetching: false, refetch: vi.fn(), isRefetchError: false, dataUpdatedAt: 0 }
  const gate = (query: Partial<typeof ready>, discovered = true) =>
    renderToStaticMarkup(
      <SubstrateScreenGate query={{ ...ready, ...query } as never} discoveryPending={false} discovered={discovered}>
        {() => <div>screen</div>}
      </SubstrateScreenGate>,
    )

  it('shows the upgrade note on a Radar that predates the views, a retry for a failed read, and not installed', () => {
    expect(gate({ error: new RadarFeatureUnsupportedError('substrateWorkspace', { currentVersion: 'v1.16.0' }) })).toContain('Agent Substrate needs a newer Radar')
    expect(gate({ error: new Error('boom') })).toContain('Retry')
    expect(gate({}, false)).toContain('Agent Substrate is not installed')
    expect(gate({ data: workspace() })).toContain('screen')
  })
})
it('counts attention actors and templates from the fleet and distinct restarted workers', () => {
  const ws = workspace()
  ws.summary.actors!.attentionCount = 24
  ws.summary.templates!.attentionCount = 5
  ws.needsAttention.push(...['a', 'b'].map((pod) => ({
    id: `workerRestarting:${pod}`, rule: 'workerRestarting' as const, severity: 'critical' as const, count: 3,
    title: 'Restarted', reading: 'Sandboxes lost', nextStep: 'Review recovery',
    subject: { kind: 'Actors' as const, filter: { worker: pod, pool: 'agents/pool', impact: 'restarted' } },
  })))
  ws.needsAttention.push({ ...ws.needsAttention.at(-1)!, id: 'workerDraining:c', rule: 'workerDraining', severity: 'info' })
  state.data = ws
  const destinations = sidebar()!.destinations
  expect(destinations.find((d) => d.id === 'actors')?.count).toBe(24)
  expect(destinations.find((d) => d.id === 'templates')?.count).toBe(5)
  expect(destinations.find((d) => d.id === 'workers')?.count).toBe(2)
})
