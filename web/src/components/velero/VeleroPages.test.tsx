// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { location, workspace } from './velero.fixtures'

const state = vi.hoisted(() => ({ backups: [] as any[] }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useResources: () => ({ data: state.backups, isPending: false, error: null }),
  useRadarFeature: () => ({ support: 'supported' }),
}))

const { parseVeleroRoute, veleroDetailKindFor, veleroDetailPath } = await import('./routes')
const { renderVeleroSummary, restoreVerdict } = await import('./VeleroPages')
const { VeleroBackups } = await import('./VeleroRunLists')
const { veleroResourceHost } = await import('./host')

const inHours = (h: number) => new Date(Date.now() + h * 3600_000).toISOString()

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

describe('Restore from it', () => {
  const backup = (phase: string, expiration = inHours(5)) => ({ status: { phase, expiration } })
  const available = location({ name: 'default', phase: 'Available' })
  it('says yes only as far as Velero reports, and never that a restore was tested', () => {
    const v = restoreVerdict(backup('Completed'), available)
    expect(v.text).toBe('Yes, as far as Velero reports')
    expect(v.detail).toContain('Velero never tests a restore')
  })
  it('says no for what Velero cannot restore from', () => {
    expect(restoreVerdict(backup('Failed'), available).text).toBe('No: it did not complete')
    expect(restoreVerdict(backup('Deleting'), available).text).toBe('No: Velero is deleting it')
    expect(restoreVerdict(backup('Completed', inHours(-1)), available).text).toBe('No: past its retention')
    expect(restoreVerdict(backup('Completed'), location({ name: 'default', phase: 'Unavailable' })).text).toBe('No: its storage location is unavailable')
  })
  it('says partly for a partial run, and does not call an unread location available', () => {
    expect(restoreVerdict(backup('PartiallyFailed'), available).text).toBe('Partly: some items were not backed up')
    const unread = restoreVerdict(backup('Completed'), undefined)
    expect(unread.tone).toBe('unknown')
    expect(unread.detail).toContain('could not be read')
  })
})

describe('The composed Overview', () => {
  it('belongs to the page; the drawer keeps the renderer', () => {
    expect(renderVeleroSummary({ apiKind: 'backups', resource: { metadata: {} }, context: 'drawer' })).toBeNull()
    expect(renderVeleroSummary({ apiKind: 'podvolumebackups', resource: { metadata: {} }, context: 'expanded' })).toBeNull()
  })
})

describe('The Backups list', () => {
  const run = (name: string, phase: string, schedule?: string) => ({
    metadata: { name, namespace: 'velero', uid: name, labels: schedule ? { 'velero.io/schedule-name': schedule } : {} },
    spec: { storageLocation: 'default' },
    status: { phase, startTimestamp: '2026-11-16T10:00:00Z' },
  })
  const render = (url: string) =>
    renderToStaticMarkup(
      <MemoryRouter initialEntries={[url]}>
        <VeleroBackups data={workspace({ locations: [location({ name: 'default' })] })} installation={null} onInstallation={() => {}} onInspect={() => {}} inspected={null} />
      </MemoryRouter>,
    )

  it('opens on the failed runs when any failed', () => {
    state.backups = [run('ok-1', 'Completed'), run('bad-1', 'PartiallyFailed'), run('rejected-1', 'FailedValidation')]
    const html = render('/resources/backups?apiGroup=velero.io')
    expect(html).toContain('bad-1')
    expect(html).toContain('rejected-1')
    expect(html).not.toContain('ok-1')
  })

  it('narrows to one schedule’s runs from a link, and says so', () => {
    state.backups = [run('a-1', 'Completed', 'a'), run('b-1', 'Completed', 'b')]
    const html = render('/resources/backups?apiGroup=velero.io&labels=velero.io/schedule-name%3Da')
    expect(html).toContain('a-1')
    expect(html).not.toContain('b-1')
    expect(html).toContain('Schedule a')
  })
})
