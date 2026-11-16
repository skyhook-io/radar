// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { location, workspace } from './velero.fixtures'

const state = vi.hoisted(() => ({ runs: undefined as unknown, error: null as Error | null }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: 'supported', guard: (f: () => unknown) => f(), gatedKey: [] }),
}))
vi.mock('../../api/velero', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/velero')>()),
  useVeleroRuns: () => ({ data: state.runs, isPending: false, error: state.error }),
  useVeleroWorkspace: () => ({ data: undefined }),
  useVeleroScheduleActivity: () => ({ data: undefined }),
}))

const { parseVeleroRoute, veleroDetailKindFor, veleroDetailPath } = await import('./routes')
const { locationState, renderVeleroSummary } = await import('./VeleroPages')
const { VeleroBackups } = await import('./VeleroRunLists')
const { veleroResourceHost } = await import('./host')

describe('Velero page routes', () => {
  it('reads and writes an object page, and leaves the Overview alone', () => {
    expect(parseVeleroRoute('/velero/backups/velero/nightly-1').detail).toEqual({ plural: 'backups', namespace: 'velero', name: 'nightly-1' })
    expect(parseVeleroRoute('/velero').detail).toBeUndefined()
    expect(parseVeleroRoute('/velero/podvolumebackups/velero/x').detail).toBeUndefined()
    expect(veleroDetailPath({ plural: 'schedules', namespace: 'velero', name: 'a b' }, 'ctx-1', 'runs')).toBe('/velero/schedules/velero/a%20b?ctx=ctx-1&tab=runs')
  })

  it('claims only Velero’s own kinds, by exact group', () => {
    expect(veleroDetailKindFor('backups', 'velero.io')).toBe('backups')
    expect(veleroDetailKindFor('restores', 'other.io')).toBeNull()
    expect(veleroResourceHost.expandedPath?.({ kind: 'Backup', group: 'velero.io', namespace: 'velero', name: 'b' })).toBe('/velero/backups/velero/b')
    expect(veleroResourceHost.detailRedirect?.({ kind: 'Restore', group: 'velero.io', namespace: 'velero', name: 'r' }, new URLSearchParams('apiGroup=velero.io&tab=yaml'))).toBe('/velero/restores/velero/r?tab=yaml')
  })
})

describe('A backup’s storage location', () => {
  it('is missing only when the locations were read in full', () => {
    const read = workspace({ locations: [location({ name: 'default', phase: 'Available' })] })
    expect(locationState('default', 'velero', read).text).toBe('available')
    expect(locationState('gone', 'velero', read).text).toBe('not found: Velero has no such location')
    const partial = { ...read, coverage: { ...read.coverage, backupStorageLocations: { state: 'partial' as const } } }
    expect(locationState('gone', 'velero', partial).text).toBe('not read')
    expect(locationState('default', 'velero', undefined).text).toBe('not read')
  })
})

describe('The composed Overview', () => {
  it('belongs to the page; the drawer keeps the renderer', () => {
    expect(renderVeleroSummary({ apiKind: 'backups', resource: { metadata: {} }, context: 'drawer' })).toBeNull()
    expect(renderVeleroSummary({ apiKind: 'podvolumebackups', resource: { metadata: {} }, context: 'expanded' })).toBeNull()
  })

  it('says a restore is not verified, and shows a failed run’s messages once, in its issue', () => {
    const failed = { metadata: { namespace: 'velero', name: 'b' }, spec: {}, status: { phase: 'PartiallyFailed', errors: 2 } }
    const withIssue = renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter>{renderVeleroSummary({ apiKind: 'backups', resource: failed, context: 'expanded', leadContent: <div>ISSUE</div> })}</MemoryRouter></QueryClientProvider>)
    expect(withIssue).toContain('ISSUE')
    expect(withIssue).toContain('Not verified')
    expect(withIssue).not.toContain('What Velero reported')
    const noIssue = renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter>{renderVeleroSummary({ apiKind: 'backups', resource: failed, context: 'expanded' })}</MemoryRouter></QueryClientProvider>)
    expect(noIssue).toContain('What Velero reported')
  })
})

describe('The Backups list', () => {
  const run = (name: string, phase: string, schedule?: string, namespace = 'velero') => ({ namespace, name, uid: `${namespace}/${name}`, phase, schedule, started: '2026-11-16T10:00:00Z', errors: 0, warnings: 0 })
  const render = (url: string) =>
    renderToStaticMarkup(
      <MemoryRouter initialEntries={[url]}>
        <VeleroBackups data={workspace({ locations: [location({ name: 'default' })] })} installation={null} onInstallation={() => {}} onInspect={() => {}} inspected={null} onCreate={() => {}} />
      </MemoryRouter>,
    )
  const response = (runs: unknown[], coverage = 'full') => ({ observedAt: '', coverage: { state: coverage }, runs, total: runs.length })

  it('opens on the failed runs when any failed, with a way to create one', () => {
    state.runs = response([run('ok-1', 'Completed'), run('bad-1', 'PartiallyFailed'), run('rejected-1', 'FailedValidation')])
    const html = render('/resources/backups?apiGroup=velero.io')
    expect(html).toContain('bad-1')
    expect(html).toContain('rejected-1')
    expect(html).not.toContain('ok-1')
    expect(html).toContain('Create Backup')
    expect(html).toContain('Search backups by name')
  })

  it('shows every run of one schedule from a link, failed or not', () => {
    state.runs = response([run('a-1', 'Completed', 'a'), run('a-2', 'Failed', 'a'), run('b-1', 'Completed', 'b')])
    const html = render('/resources/backups?apiGroup=velero.io&labels=velero.io/schedule-name%3Da')
    expect(html).toContain('a-1')
    expect(html).toContain('a-2')
    expect(html).not.toContain('b-1')
    expect(html).toContain('Schedule a')
  })

  it('gives lower bounds over a partial read, and never says there are none', () => {
    state.runs = response([run('bad-1', 'Failed')], 'partial')
    const html = render('/resources/backups?apiGroup=velero.io')
    expect(html).toContain('≥1')
    expect(html).toContain('Counts are lower bounds')
  })

  it('keeps rows from before a failed refresh, and says so', () => {
    state.runs = response([run('bad-1', 'Failed')])
    state.error = new Error('boom')
    const html = render('/resources/backups?apiGroup=velero.io')
    expect(html).toContain('bad-1')
    expect(html).toContain('The last refresh of backups failed')
    state.error = null
  })
})
