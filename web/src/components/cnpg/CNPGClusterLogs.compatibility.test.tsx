import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import { CNPGClusterLogs } from './CNPGClusterLogs'
import { guardRadarFeature, radarFeatureSupport } from '../../api/radarFeatures'
import { fetchJSON } from '../../api/client'

const state = vi.hoisted(() => ({ props: undefined as any, capabilities: { features: { resourceIssues: true } } as any, capabilitiesPending: false, fetchStatus: 'idle' as 'idle' | 'fetching' | 'paused', interval: null as null | { since: string; until: string; clear: () => void } }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), WorkloadLogsViewer: (props: any) => { state.props = props; return <div>logs</div> } }))
vi.mock('../../api/client', () => ({ fetchJSON: vi.fn(async () => ({ pods: [], logs: [] })), useCapabilities: () => ({ isPending: state.capabilitiesPending, isFetching: state.fetchStatus === 'fetching', fetchStatus: state.fetchStatus, refetch: vi.fn() }), useRadarFeature: () => {
  const support = radarFeatureSupport('cnpgWorkspace', state.capabilities, 'v1.16.0')
  return { support, guard: (fn: () => Promise<unknown>) => guardRadarFeature('cnpgWorkspace', support, {}, fn) }
} }))
vi.mock('../../hooks/useDesktopDownload', () => ({ useDesktopDownload: () => undefined }))
vi.mock('../../context/ThemeContext', () => ({ useTheme: () => ({ theme: 'light' }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: null }) }))
vi.mock('./CNPGTrends', () => ({ useCNPGIntervalParams: () => state.interval, CNPGIntervalBanner: ({ note }: { note: string }) => <div>{note}</div> }))
beforeEach(() => { state.props = undefined; state.capabilities = { features: { resourceIssues: true } }; state.capabilitiesPending = false; state.fetchStatus = 'idle'; state.interval = null; vi.clearAllMocks(); vi.unstubAllGlobals() })
function mount(search = '') { return renderToStaticMarkup(<MemoryRouter initialEntries={[{ pathname: '/cnpg/clusters/db/pg', search }]}><CNPGClusterLogs namespace="db" name="pg" /></MemoryRouter>) }

it('retains ordinary snapshot selectors and follows on the actual missing-flag capability state', async () => {
  mount()
  await state.props.fetchAll({ container: 'postgres', tailLines: 40, sinceSeconds: 60 })
  expect(fetchJSON).toHaveBeenCalledWith('/cnpg/clusters/db/pg/logs?container=postgres&tailLines=40&sinceSeconds=60', { signal: undefined })
  const eventSource = vi.fn(function (url: string) { return { url } })
  vi.stubGlobal('EventSource', eventSource)
  state.props.createStream({ container: 'postgres' })
  expect(eventSource).toHaveBeenCalledWith('/api/cnpg/clusters/db/pg/logs/stream?container=postgres&tailLines=50', { withCredentials: false })
})
it('uses the old postgres default for empty and all selectors in both snapshot and SSE requests', async () => {
  mount()
  expect(state.props.initialContainer).toBe('postgres')
  expect(state.props.allowAllContainers).toBe(false)
  mount('?container=all')
  expect(state.props.initialContainer).toBe('postgres')
  expect(state.props.allowAllContainers).toBe(false)
  const eventSource = vi.fn(function (url: string) { return { url } })
  vi.stubGlobal('EventSource', eventSource)
  for (const container of [undefined, 'all']) {
    await state.props.fetchAll({ container, tailLines: 40, sinceSeconds: 60 })
    const path = vi.mocked(fetchJSON).mock.lastCall![0]
    expect(Object.fromEntries(new URL(path, 'http://radar.test').searchParams)).toEqual({ tailLines: '40', sinceSeconds: '60' })
    state.props.createStream({ container, sinceSeconds: 60 })
    const streamPath = eventSource.mock.lastCall![0]
    expect(Object.fromEntries(new URL(streamPath, 'http://radar.test').searchParams)).toEqual({ tailLines: '50', sinceSeconds: '60' })
  }
})
it('retains exact plugin-container selection supported by the old producer', async () => {
  mount('?container=plugin-barman-cloud')
  expect(state.props.initialContainer).toBe('plugin-barman-cloud')
  await state.props.fetchAll({ container: 'plugin-barman-cloud' })
  expect(fetchJSON).toHaveBeenCalledWith('/cnpg/clusters/db/pg/logs?container=plugin-barman-cloud', { signal: undefined })
  const eventSource = vi.fn(function (url: string) { return { url } })
  vi.stubGlobal('EventSource', eventSource)
  state.props.createStream({ container: 'plugin-barman-cloud' })
  expect(eventSource).toHaveBeenCalledWith('/api/cnpg/clusters/db/pg/logs/stream?container=plugin-barman-cloud&tailLines=50', { withCredentials: false })
})
it('only requests all-container snapshots and SSE after confirmed support', async () => {
  const eventSource = vi.fn(function (url: string) { return { url } })
  vi.stubGlobal('EventSource', eventSource)
  state.capabilities = undefined
  mount()
  expect(state.props.initialContainer).toBe('postgres')
  expect(state.props.allowAllContainers).toBe(false)
  await state.props.fetchAll({})
  expect(fetchJSON).toHaveBeenLastCalledWith('/cnpg/clusters/db/pg/logs', { signal: undefined })
  state.props.createStream({})
  expect(eventSource).toHaveBeenLastCalledWith('/api/cnpg/clusters/db/pg/logs/stream?tailLines=50', { withCredentials: false })
  state.capabilities = { features: { cnpgWorkspace: true } }
  mount()
  expect(state.props.initialContainer).toBeUndefined()
  expect(state.props.allowAllContainers).toBe(true)
  mount('?container=all')
  expect(state.props.initialContainer).toBeUndefined()
  await state.props.fetchAll({})
  expect(fetchJSON).toHaveBeenLastCalledWith('/cnpg/clusters/db/pg/logs?container=all', { signal: undefined })
  state.props.createStream({})
  expect(eventSource).toHaveBeenLastCalledWith('/api/cnpg/clusters/db/pg/logs/stream?container=all&tailLines=50', { withCredentials: false })
})
it('refuses both-bound interval semantics on that old server without an unbounded request', async () => {
  state.interval = { since: '2026-10-13T00:00:00Z', until: '2026-10-13T01:00:00Z', clear: vi.fn() }
  expect(mount()).toContain('Clear it to read ordinary logs')
  await expect(state.props.fetchAll({ tailLines: 40 })).rejects.toMatchObject({ feature: 'cnpgWorkspace' })
  expect(fetchJSON).not.toHaveBeenCalled()
  expect(state.props.createStream).toBeUndefined()
})
it('uses the advertised interval contract without tailing or streaming', async () => {
  state.capabilities = { features: { cnpgWorkspace: true } }
  state.interval = { since: '2026-10-13T00:00:00Z', until: '2026-10-13T01:00:00Z', clear: vi.fn() }
  mount()
  await state.props.fetchAll({ tailLines: 40, sinceSeconds: 60 })
  const path = vi.mocked(fetchJSON).mock.calls[0][0]
  const params = new URL(path, 'http://radar.test').searchParams
  expect(Object.fromEntries(params)).toEqual({ sinceTime: state.interval.since, untilTime: state.interval.until, container: 'all' })
  expect(state.props.createStream).toBeUndefined()
})
it('waits for capabilities before offering interval semantics, while ordinary logs remain immediate', () => {
  state.capabilities = undefined
  state.capabilitiesPending = true
  state.interval = { since: '2026-10-13T00:00:00Z', until: '2026-10-13T01:00:00Z', clear: vi.fn() }
  expect(mount()).toContain('Checking interval support')
  expect(state.props).toBeUndefined()
  state.interval = null
  mount()
  expect(state.props.createStream).toBeTypeOf('function')
})
it('settles a failed capabilities read without loading forever or issuing an interval request', () => {
  state.capabilities = undefined
  state.interval = { since: '2026-10-13T00:00:00Z', until: '2026-10-13T01:00:00Z', clear: vi.fn() }
  const html = mount()
  expect(html).toContain('Interval log support could not be checked')
  expect(html).toContain('Clear this interval to read ordinary logs')
  expect(html).toContain('Retry')
  expect(html).not.toContain('Checking interval support')
  expect(state.props).toBeUndefined()
  expect(fetchJSON).not.toHaveBeenCalled()
})
it('keeps the clear-interval fallback available while an offline request is paused', () => {
  state.capabilities = undefined
  state.capabilitiesPending = true
  state.fetchStatus = 'paused'
  state.interval = { since: '2026-10-13T00:00:00Z', until: '2026-10-13T01:00:00Z', clear: vi.fn() }
  const html = mount()
  expect(html).toContain('Clear this interval to read ordinary logs')
  expect(html).toContain('Waiting for connection')
  expect(html).not.toContain('Checking interval support')
  expect(state.props).toBeUndefined()
  expect(fetchJSON).not.toHaveBeenCalled()
})
