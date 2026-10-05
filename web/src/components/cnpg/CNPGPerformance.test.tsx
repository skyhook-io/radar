import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGPerformance } from './CNPGPerformance'
import type { CNPGRuntimeInstance } from '../../api/cnpg'

const live = vi.hoisted(() => ({ denied: false, empty: false, metrics: { state: 'partial', reason: '150 session groups; the largest 100 are listed and sessionsTotal counts all', sessionsTotal: 150, sessions: [{ state: 'active', database: 'app', user: 'app', application: 'client', count: 3 }], sessionsByState: { 'idle in transaction': 12 }, waitingBackends: 2, databases: [{ database: 'app', xactCommit: 90, xactRollback: 10 }], checkpoints: { source: 'pg_stat_checkpointer', timed: 7, requested: 2 }, extensionUpdates: [] } as CNPGRuntimeInstance['metrics'] }))
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({ data: { sampledAt: '2026-10-05T12:00:00Z', permission: { proxy: live.denied ? 'denied' : 'allowed' }, instances: live.empty ? [] : [{ pod: 'pg-1', role: 'primary', metrics: live.metrics }] } }) }))
vi.mock('../../api/cnpg-sessions', () => ({ useCNPGSessions: () => ({}) }))
vi.mock('../../api/cnpg-history', () => ({ useCNPGClusterHistory: () => ({}) }))
vi.mock('./CNPGBlockingSessions', () => ({ CNPGBlockingSessions: () => null }))
vi.mock('./CNPGTrends', () => ({ CNPGTrends: () => null, useSampleBuffer: () => [] }))
function render(section: string) {
  return renderToStaticMarkup(<MemoryRouter initialEntries={[`/?section=${section}`]}><CNPGPerformance namespace="pg" name="pg" /></MemoryRouter>)
}
it('keeps partial session measurements and the producer reason visible', () => {
  const html = render('sessions')
  expect(html).toContain('150 in use')
  expect(html).toContain('client')
  expect(html).toContain('12')
  expect(html).toContain(live.metrics.reason)
  expect(html).toContain('Sessions: partial')
})
it('keeps partial database and checkpoint measurements and qualifies each card', () => {
  const html = render('health')
  expect(html).toContain('10.0 %')
  expect(html).toContain('Timed')
  expect(html).toContain('>7<')
  expect(html).toContain('Database health: partial')
  expect(html).toContain('Checkpoints: partial')
  expect(html).toContain(live.metrics.reason)
  expect(html).not.toContain('Every installed extension')
})

it('keeps the Sessions card title and full-width shell when proxy access is denied', () => {
  live.denied = true
  const html = render('sessions')
  expect(html).toMatch(/<section[^>]*>.*Sessions on pg-1.*No access to live instance data.*<\/section>/)
  expect(html).toContain('needs access you do not have')
  live.denied = false
})
it('says checked when no instance answered and sampled when a measurement was captured', () => {
  live.empty = true
  const html = render('sessions')
  expect(html).toContain('No instance data yet; checked')
  expect(html).not.toContain('sampled')
  live.empty = false
  live.metrics.capturedAt = '2026-10-05T12:00:00Z'
  expect(render('sessions')).toContain('Live instance data · sampled')
})
