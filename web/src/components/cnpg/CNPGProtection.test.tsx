// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi, afterEach } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse, type CNPGKindCoverage } from '@skyhook-io/k8s-ui'
import { CNPGProtection } from './CNPGProtection'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
afterEach(() => vi.useRealTimers())
const now = Date.parse('2026-10-05T12:00:00Z')
const backup = (name: string, phase: string, ageDays: number) => ({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Backup', metadata: { namespace: 'pg', name }, spec: { cluster: { name: 'pg' } }, status: { phase, startedAt: new Date(now - ageDays * 86400000).toISOString() } })

function render(objects: CNPGWorkspaceResponse['objects'], coverage: CNPGKindCoverage = { state: 'full' }, query = '', scopeCluster?: { namespace: string; name: string }) {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: { ...Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' as const }])), backups: coverage }, objects, issues: [], audit: [], backupsOmitted: 0 }
  return renderToStaticMarkup(<CNPGProtection data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams(query)} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} scopeCluster={scopeCluster} />)
}

it('marks a blocked schedule amber and names its missing destination and unreported time', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'pg' }, spec: {} }
  const schedule = { metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'payments' }, schedule: '0 0 2 * * *' } }
  const html = render({ clusters: [cluster], scheduledBackups: [schedule] })
  expect(html).toContain('Enabled · 0 0 2 * * * · blocked: no backup destination')
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

it('explains absent WAL storage and suppresses ObjectStore-only footnotes on fleet and cluster views', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'pg' }, spec: {}, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', message: 'Continuous archiving is working', lastTransitionTime: '2026-10-01T12:00:00Z' }] } }
  for (const scope of [undefined, { namespace: 'pg', name: 'payments' }]) {
    const html = render({ clusters: [cluster] }, { state: 'full' }, '', scope)
    expect(html).toContain('Not archived: no destination configured')
    if (scope) {
      expect(html).toContain('point-in-time recovery is unavailable')
      expect(html).toContain('Operator report')
      expect(html).toContain('aria-expanded="false"')
    } else {
      expect(html).toContain('Recovery evidence for pg/payments')
      expect(html).not.toContain('Operator report')
      expect(html).toContain('without keeping it')
    }
    expect(html).toContain('None: no backup destination')
    expect(html).not.toContain('Recovery windows come from ObjectStore status')
    expect(html).not.toContain('ObjectStore has no health status')
  }
})
it('keeps ObjectStore notes when ObjectStore evidence is present', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'pg' }, spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } }
  const store = { apiVersion: 'barmancloud.cnpg.io/v1', kind: 'ObjectStore', metadata: { name: 'store', namespace: 'pg' }, spec: {} }
  for (const scope of [undefined, { namespace: 'pg', name: 'pg' }]) {
    const html = render({ clusters: [cluster], objectStores: [store] }, { state: 'full' }, '', scope)
    expect(html).toContain('Recovery windows come from ObjectStore status')
    expect(html).toContain('ObjectStore has no health status')
  }
})
it('gives schedule state space and wraps the long next-run heading without changing shared table headings', () => {
  const schedule = { metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'payments' } } }
  const html = render({ scheduledBackups: [schedule] }, { state: 'full' }, '', { namespace: 'pg', name: 'payments' })
  const table = html.slice(html.indexOf('Schedules</h2>'))
  expect(table).toContain('min-width:820px')
  expect(table).toContain('width:25%')
  expect(table).toContain('block whitespace-normal')
  expect(table).toContain('whitespace-nowrap')
})

it('uses the Configuration card gutter and SectionHeading in scoped recovery evidence', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'pg' }, spec: {}, status: {} }
  const host = document.createElement('div'); host.innerHTML = render({ clusters: [cluster] }, { state: 'full' }, '', { namespace: 'pg', name: 'pg' })
  const title = [...host.querySelectorAll('h3')].find((h) => h.textContent === 'Recovery evidence')!
  expect(title.classList.contains('uppercase')).toBe(true)
  expect(title.closest('section')?.classList.contains('px-4')).toBe(true)
  expect(title.closest('section')?.parentElement?.classList.contains('p-4')).toBe(true)
})

it('explains no-destination operator success once for the fleet table and provides full evidence access per row', () => {
  const clusters = ['orders', 'payments'].map((name) => ({ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name, namespace: 'pg' }, spec: {}, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', message: 'working' }] } }))
  const html = render({ clusters })
  expect(html.match(/CloudNativePG reports archiving as working because/g)).toHaveLength(1)
  expect(html.match(/Recovery evidence for pg\//g)).toHaveLength(2)
  expect(html).not.toContain('Operator report')
  expect(html).not.toContain('WAL is not archived to recovery storage')
})
