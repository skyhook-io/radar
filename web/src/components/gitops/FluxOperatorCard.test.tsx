import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'

const state: { reports: any; options: Record<string, any> } = { reports: { data: undefined, error: null, isSuccess: false }, options: {} }
vi.mock('../../api/client', () => ({
  useResources: (kind: string, _ns?: string, _group?: string, options?: any) => {
    state.options[kind] = options
    return kind === 'fluxreports' ? state.reports : { data: undefined, error: null, isSuccess: false }
  },
}))

import { FluxOperatorCard } from './FluxOperatorCard'

const render = () => renderToString(<MemoryRouter><FluxOperatorCard available /></MemoryRouter>).replace(/<!-- -->/g, '')
const report = { metadata: { namespace: 'flux-system', name: 'flux' }, spec: { distribution: { version: 'v2.7.5' }, components: [], reconcilers: [] } }

describe('FluxOperatorCard', () => {
  it('shows nothing when the report list failed or is empty', () => {
    state.reports = { data: undefined, error: new Error('forbidden'), isSuccess: false }
    expect(render()).not.toContain('Flux v')
    state.reports = { data: [], error: null, isSuccess: true }
    expect(render()).toBe('')
  })

  it('shows the card for a readable report', () => {
    state.reports = { data: [report], error: null, isSuccess: true }
    expect(render()).toContain('Flux v2.7.5')
  })

  it('refreshes the report and its FluxInstance at the same interval', () => {
    state.reports = { data: [report], error: null, isSuccess: true }
    renderToString(<MemoryRouter><FluxOperatorCard available refetchInterval={120_000} /></MemoryRouter>)
    expect(state.options.fluxreports.refetchInterval).toBe(120_000)
    expect(state.options.fluxinstances.refetchInterval).toBe(120_000)
  })
})
