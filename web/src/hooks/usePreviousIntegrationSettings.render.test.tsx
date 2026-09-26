// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { usePreviousIntegrationSettings, previousIntegrationSettingsKey } from './usePreviousIntegrationSettings'

const state = vi.hoisted(() => ({ context: 'first', switching: false, embedded: false, management: 'local', connected: true }))
vi.mock('../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context, state: state.connected ? 'connected' : 'disconnected' } }) }))
vi.mock('../context/ContextSwitchContext', () => ({ useContextSwitch: () => ({ isSwitching: state.switching }) }))
vi.mock('../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: state.embedded }) }))
vi.mock('../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({ configManagement: state.management }) }))
vi.mock('../api/client', () => ({ fetchJSON: vi.fn() }))
import { fetchJSON } from '../api/client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
const element = document.createElement('div')
let root = createRoot(element)
let client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
function Subject({ relevant = true }: { relevant?: boolean }) {
  const offers = usePreviousIntegrationSettings(relevant)
  return <span>{offers.metrics ? 'offer' : 'ordinary'}</span>
}
async function render(relevant = true) {
  await act(async () => { root.render(<QueryClientProvider client={client}><Subject relevant={relevant} /></QueryClientProvider>) })
}
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  client = new QueryClient()
  root = createRoot(element)
  Object.assign(state, { context: 'first', switching: false, embedded: false, management: 'local', connected: true })
  vi.mocked(fetchJSON).mockReset()
})

it.each(['embedded', 'operator', 'cloud', 'loading', 'switching', 'disconnected', 'irrelevant'])('does not fetch or display cached offers when %s', async gate => {
  if (gate === 'embedded') state.embedded = true
  if (['operator', 'cloud', 'loading'].includes(gate)) state.management = gate === 'loading' ? '' : gate
  if (gate === 'switching') state.switching = true
  if (gate === 'disconnected') state.connected = false
  client.setQueryData([previousIntegrationSettingsKey, '/api', 'first'], { metrics: true })
  await render(gate !== 'irrelevant')
  expect(element.textContent).toBe('ordinary')
  expect(fetchJSON).not.toHaveBeenCalled()
})

it('drops a cached offer immediately while switching and aborts the old fetch', async () => {
  const signals: AbortSignal[] = []
  vi.mocked(fetchJSON).mockImplementation((_path, signal) => { signals.push(signal as AbortSignal); return new Promise(() => {}) })
  client.setQueryData([previousIntegrationSettingsKey, '/api', 'first'], { metrics: true }, { updatedAt: 1 })
  await render()
  expect(element.textContent).toBe('offer')
  state.switching = true
  await render()
  expect(element.textContent).toBe('ordinary')
  state.context = 'second'
  state.switching = false
  await render()
  expect(element.textContent).toBe('ordinary')
  expect(signals[0].aborted).toBe(true)
  expect(signals).toHaveLength(2)
})
