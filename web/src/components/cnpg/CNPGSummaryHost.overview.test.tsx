import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { renderCNPGSummary } from './CNPGSummaryHost'

const state = vi.hoisted(() => ({ framed: undefined as boolean | undefined, row: {} as any, runtime: {} as any, literalPhase: false }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), CNPGClusterSummary: ({ framed, operationalFacts, literalPhase }: { framed?: boolean; operationalFacts?: ReactNode; literalPhase?: boolean }) => { state.framed = framed; state.literalPhase = !!literalPhase; return <div>{operationalFacts}</div> } }))
vi.mock('./useCNPGClusterAssessment', () => ({ useCNPGClusterAssessment: () => ({ query: {}, runtime: state.runtime, ha: {}, dimensions: [], row: state.row }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({}) }))
vi.mock('./actions/CNPGMaintenanceBanner', () => ({ CNPGMaintenanceBanner: () => null }))
vi.mock('./CNPGOperatorBanner', () => ({ CNPGOperatorBanner: () => null }))

const resource = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', status: { conditions: [{ type: 'Ready', status: 'True', reason: 'ClusterReady', message: 'Ready to serve', lastTransitionTime: '2026-10-04T00:00:00Z' }] } }

it('opts into framing and Operator conditions from the opened object only for the expanded host', () => {
  state.row = { cluster: { status: {} } }
  for (const context of ['drawer', 'expanded'] as const) {
    const html = renderToStaticMarkup(<MemoryRouter>{renderCNPGSummary({ apiKind: 'clusters', namespace: 'db', name: 'pg', resource, context })}</MemoryRouter>)
    expect(state.framed).toBe(context === 'expanded')
    expect(html.includes('Operator conditions')).toBe(context === 'expanded')
    if (context === 'expanded') for (const field of ['Ready', 'True', 'ClusterReady', 'Ready to serve', '2026-10-04T00:00:00Z']) expect(html).toContain(field)
  }
})
it('retains raw Operator conditions when the workspace summary cannot be read', () => {
  state.row = undefined
  const html = renderToStaticMarkup(<MemoryRouter>{renderCNPGSummary({ apiKind: 'clusters', namespace: 'db', name: 'pg', resource, context: 'expanded' })}</MemoryRouter>)
  expect(html).toContain('The CloudNativePG workspace summary could not be read.')
  expect(html).toContain('Operator conditions')
  expect(html).toContain('Ready to serve')
})

it('names standby cloning with its joining-only source and opts into literal phases', () => {
  state.row = { cluster: { status: {} } }
  state.runtime = { data: { permission: { proxy: 'allowed' }, instances: [{ pod: 'pg-1', role: 'primary', status: { state: 'ok', baseBackups: [] } }] } }
  const html = renderToStaticMarkup(<MemoryRouter>{renderCNPGSummary({ apiKind: 'clusters', namespace: 'db', name: 'pg', resource, context: 'expanded' })}</MemoryRouter>)
  expect(html).toContain('Standby cloning')
  expect(html).toContain('None running')
  expect(html).toContain('pg_basebackup on the primary, joining instances only')
  expect(html).not.toContain('Base backup')
  expect(state.literalPhase).toBe(true)
  state.runtime = {}
})
