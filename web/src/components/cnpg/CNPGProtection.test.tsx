import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi, afterEach } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse, type CNPGKindCoverage } from '@skyhook-io/k8s-ui'
import { CNPGProtection } from './CNPGProtection'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
afterEach(() => vi.useRealTimers())
const now = Date.parse('2026-10-05T12:00:00Z')
const backup = (name: string, phase: string, ageDays: number) => ({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Backup', metadata: { namespace: 'pg', name }, spec: { cluster: { name: 'pg' } }, status: { phase, startedAt: new Date(now - ageDays * 86400000).toISOString() } })

function render(objects: CNPGWorkspaceResponse['objects'], coverage: CNPGKindCoverage = { state: 'full' }, query = '') {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: { ...Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' as const }])), backups: coverage }, objects, issues: [], audit: [], backupsOmitted: 0 }
  return renderToStaticMarkup(<CNPGProtection data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams(query)} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />)
}

it('uses the same suspended, due-now and overdue next-run wording as the drawer', () => {
  vi.useFakeTimers(); vi.setSystemTime(now)
  const schedule = (name: string, minutes: number, suspend = false) => ({ metadata: { name, namespace: 'pg' }, spec: { suspend }, status: { nextScheduleTime: new Date(now - minutes * 60000).toISOString() } })
  const html = render({ scheduledBackups: [schedule('paused', 90, true), schedule('due', 5), schedule('late', 15)] })
  expect(html).toContain('suspended')
  expect(html).toContain('due now')
  expect(html).toContain('overdue by 15m')
  expect(html).not.toContain('90m overdue')
})

it('retains old in-flight runs with their age qualification and drops old settled runs', () => {
  vi.useFakeTimers(); vi.setSystemTime(now)
  const html = render({ backups: [backup('old-running', 'running', 10), backup('old-completed', 'completed', 10), backup('old-failed', 'failed', 10)] })
  expect(html).toContain('old-running')
  expect(html).toContain('started more than a week ago')
  expect(html).not.toContain('old-completed')
  expect(html).not.toContain('old-failed')
  expect(html).toContain('All 1')
})

it('marks backup run counts as lower bounds or unknown when coverage is incomplete', () => {
  vi.useFakeTimers(); vi.setSystemTime(now)
  const html = render({ backups: [backup('failed-read', 'failed', 1)] }, { state: 'partial', allowedNamespaces: ['pg'] })
  expect(html).toContain('Failed ≥1')
  expect(html).toContain('All ≥1')
  expect(html).toContain('Backups not read')
  const denied = render({}, { state: 'denied' })
  expect(denied).toContain('Failed ?')
  expect(denied).toContain('All ?')
  expect(denied).toContain('No access to Backups')
  const scoped = render({ backups: [backup('failed-read', 'failed', 1)] }, { state: 'partial', allowedNamespaces: ['pg'] }, 'cluster=pg/pg')
  expect(scoped).toContain('Failed 1')
  expect(scoped).not.toContain('Failed ≥1')
})
