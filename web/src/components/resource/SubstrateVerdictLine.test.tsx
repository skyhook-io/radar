import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateFact, SubstrateHealth, SubstrateVerdict } from '../../api/substrateHealth'

let health: { data?: SubstrateHealth; error?: Error } = {}
let connected = true

vi.mock('../../api/client', async (orig) => ({
  ...(await orig<typeof import('../../api/client')>()),
  usePrometheusStatus: () => ({ data: { connected } }),
}))
vi.mock('../../api/substrateHealth', async (orig) => ({
  ...(await orig<typeof import('../../api/substrateHealth')>()),
  useSubstrateHealth: () => health,
}))

const { SubstrateVerdictLine } = await import('./SubstrateVerdictLine')

const pool = { kind: 'workerpool' as const, namespace: 'agents', name: 'pool' }
const node = { kind: 'node' as const, name: 'worker-1' }
const base = { range: '1h', start: 1000, end: 4600, stepSeconds: 60, charts: [] }

function withVerdict(verdict: SubstrateVerdict, status: SubstrateHealth['status'] = 'available') {
  health = { data: { ...base, status, verdict } }
}
const render = (target: typeof pool | typeof node) => renderToStaticMarkup(<SubstrateVerdictLine target={target} onOpenMetrics={() => {}} />)
const ok = (id: string, label: string, unit: string, value: number, extra: Partial<SubstrateFact> = {}): SubstrateFact => ({ id, label, unit, state: 'ok', value, ...extra })

describe('SubstrateVerdictLine', () => {
  it('flags a pool short of workers in amber and keeps its facts uncolored', () => {
    withVerdict({ state: 'short', facts: [ok('freeWorkers', 'Free workers', 'workers', 0, { of: 3 })] })
    const html = render(pool)
    expect(html).toContain('Capacity')
    expect(html).toContain('Short of workers')
    expect(html).toMatch(/bg-amber-100/)
    expect(html).toContain('0 of 3')
    expect(html).toContain('View metrics')
  })

  it('reads full as neutral and serving as plain text', () => {
    withVerdict({ state: 'full', facts: [] })
    expect(render(pool)).toContain('bg-theme-hover/50')
    withVerdict({ state: 'serving', facts: [] })
    const html = render(pool)
    expect(html).toContain('Serving')
    expect(html).not.toMatch(/bg-amber|bg-red|bg-emerald/)
  })

  it('shows fractional replica averages as approximate, never rounded', () => {
    withVerdict({ state: 'serving', facts: [ok('freeWorkers', 'Free workers', 'workers', 1.5, { of: 4 }), ok('crashes', 'Crashes', 'count', 2.4)] })
    const html = render(pool)
    expect(html).toContain('≈1.5 of 4')
    expect(html).toContain('Crashes (1h)')
    expect(html).toContain('≈2.4')
  })

  it('says Unavailable, never 0, for a fact Radar could not read, and None for one with nothing recorded', () => {
    withVerdict({ state: 'unknown', reason: 'reported through more than one scrape job', facts: [
      { id: 'freeWorkers', label: 'Free workers', unit: 'workers', state: 'unavailable', reason: 'reported through more than one scrape job' },
      { id: 'resumeP95', label: 'Resume p95', unit: 'seconds', state: 'empty', reason: 'no actor resumed on this pool in this range' },
    ] })
    const html = render(pool)
    expect(html).toContain('Unknown')
    expect(html).toContain('Unavailable')
    expect(html).toContain('None')
    expect(html).not.toMatch(/Free workers <\/span><span[^>]*>0/)
  })

  it('names overdue bands on a node, worst first', () => {
    withVerdict({ state: 'behind', facts: [ok('overdueUploads', 'Overdue uploads', 'count', 3, { parts: [{ label: 'over_15m', value: 1 }, { label: '1m_to_5m', value: 2 }] })] })
    const html = render(node)
    expect(html).toContain('Snapshots')
    expect(html).toContain('Uploads behind')
    expect(html).toContain('3 (over 15m: 1, 1–5m: 2)')
  })

  it('does not keep asserting a cached verdict after a failed refresh or a disconnect', () => {
    withVerdict({ state: 'serving', facts: [] })
    health = { ...health, error: new Error('timeout') }
    const html = render(pool)
    expect(html).toContain('Unknown')
    expect(html).not.toContain('Serving')
    health = { data: health.data }
    connected = false
    expect(render(pool)).toBe('')
    connected = true
  })

  it('claims no more than the overdue count proves on a node', () => {
    withVerdict({ state: 'current', facts: [] })
    const html = render(node)
    expect(html).toContain('No overdue uploads')
    expect(html).not.toContain('Up to date')
  })

  it('renders nothing without a verdict', () => {
    health = { data: { ...base, status: 'notFound' } }
    expect(render(pool)).toBe('')
    health = {}
    expect(render(pool)).toBe('')
  })
})
