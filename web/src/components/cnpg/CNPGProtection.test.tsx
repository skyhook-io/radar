import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi, afterEach } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse, type CNPGKindCoverage } from '@skyhook-io/k8s-ui'
import { CNPGProtection } from './CNPGProtection'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
afterEach(() => vi.useRealTimers())
const now = Date.parse('2026-10-05T12:00:00Z')
const backup = (name: string, phase: string, ageDays: number) => ({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Backup', metadata: { namespace: 'pg', name }, spec: { cluster: { name: 'pg' } }, status: { phase, startedAt: new Date(now - ageDays * 86400000).toISOString() } })

function render(objects: CNPGWorkspaceResponse['objects'], coverage: CNPGKindCoverage = { state: 'full' }, query = '', scopeCluster?: { namespace: string; name: string }) {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: { ...Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' as const }])), backups: coverage }, objects, issues: [], audit: [], backupsOmitted: 0 }
  return renderToStaticMarkup(<CNPGProtection data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams(query)} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} scopeCluster={scopeCluster} />)
}

it('keeps an enabled schedule neutral and names its missing destination and unreported time', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'pg' }, spec: {} }
  const schedule = { metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'payments' }, schedule: '0 0 2 * * *' } }
  const html = render({ clusters: [cluster], scheduledBackups: [schedule] })
  expect(html).toContain('Enabled · not run yet')
  expect(html).toContain('No backup destination')
  expect(html).toContain('Next run reported by the operator')
  expect(html).toContain('Not reported')
  const table = html.slice(html.indexOf('Schedules</h2>'))
  expect(table).not.toContain('bg-emerald-100')
  const unknown = render({ scheduledBackups: [schedule] })
  expect(unknown).not.toContain('No backup destination')
})

it('uses green only for a completed Backup from the latest schedule firing', () => {
  vi.useFakeTimers(); vi.setSystemTime(now)
  const schedule = { metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'pg' } }, status: { lastScheduleTime: new Date(now).toISOString() } }
  const completed = { ...backup('ok', 'completed', 0), metadata: { namespace: 'pg', name: 'ok', labels: { 'cnpg.io/scheduled-backup': 'nightly' } } }
  const html = render({ scheduledBackups: [schedule], backups: [completed] })
  const table = html.slice(html.indexOf('Schedules</h2>'))
  expect(table).toMatch(/bg-emerald-100[^>]*>completed/)
  expect(table).not.toMatch(/bg-emerald-100[^>]*>Enabled/)
  const older = { ...completed, status: { ...completed.status, startedAt: new Date(now - 3600000).toISOString() } }
  const olderTable = render({ scheduledBackups: [schedule], backups: [older] }).split('Schedules</h2>')[1]
  expect(olderTable).toContain('its Backup is not loaded')
  expect(olderTable).not.toContain('bg-emerald-100')
})

it('says restore tests are not recorded once inside the cluster evidence card', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'pg' }, spec: {} }
  const html = render({ clusters: [cluster] }, { state: 'full' }, '', { namespace: 'pg', name: 'payments' })
  expect(html.match(/Kubernetes (?:does not record|records no) restore tests/g)).toHaveLength(1)
})

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
