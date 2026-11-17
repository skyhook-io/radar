// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { hoursAgo, location, schedule, workspace } from './velero.fixtures'

const state = vi.hoisted(() => ({
  runs: undefined as unknown,
  runsBy: {} as Record<string, unknown>,
  error: null as Error | null,
  workspace: undefined as unknown,
  activity: undefined as unknown,
  stored: undefined as unknown,
}))
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: 'supported', guard: (f: () => unknown) => f(), gatedKey: [] }),
}))
vi.mock('../../api/velero', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/velero')>()),
  // A filtered read answers from runsBy (keyed "kind:schedule" or "kind:backup"); the lists read `runs`.
  useVeleroRuns: (kind: string, _ns?: string, filter?: { schedule?: string; backup?: string }) => {
    const key = filter?.schedule ? `${kind}:${filter.schedule}` : filter?.backup ? `${kind}:${filter.backup}` : ''
    return { data: key ? state.runsBy[key] : state.runs, isPending: false, error: state.error }
  },
  useVeleroWorkspace: () => ({ data: state.workspace }),
  useVeleroScheduleActivity: () => ({ data: state.activity }),
}))
vi.mock('../../api/policy', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/policy')>()),
  useVeleroStoredBackups: () => ({ data: state.stored, isPending: false, error: null }),
}))

const { parseVeleroRoute, veleroDetailKindFor, veleroDetailPath } = await import('./routes')
const { VeleroBackupRestores, VeleroLocationBackups, locationState, renderVeleroSummary } = await import('./VeleroPages')
const { VeleroBackups } = await import('./VeleroRunLists')
const { VeleroScheduleStateBadge } = await import('@skyhook-io/k8s-ui')
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

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')
// The rendered text of static markup, as a reader sees it.
const plainText = (html: string) => new DOMParser().parseFromString(html, 'text/html').body.textContent ?? ''
// The same, with each element's text apart, as table cells read.
function plainWords(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  const words: string[] = []
  while (walker.nextNode()) words.push(walker.currentNode.textContent ?? '')
  return words.join(' ').replace(/\s+/g, ' ')
}

function page(apiKind: string, resource: unknown, leadContent?: React.ReactNode) {
  return text(
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>{renderVeleroSummary({ apiKind, resource, context: 'expanded', leadContent })}</MemoryRouter>
      </QueryClientProvider>,
    ),
  )
}

const runsOf = (runs: unknown[], extra: Record<string, unknown> = {}) => ({ observedAt: '', coverage: { state: 'full' }, runs, total: runs.length, ...extra })

describe('The composed Overview', () => {
  it('leads the page and the drawer alike, for the kinds that have a page', () => {
    expect(renderVeleroSummary({ apiKind: 'backups', resource: { metadata: {} }, context: 'drawer' })).not.toBeNull()
    expect(renderVeleroSummary({ apiKind: 'podvolumebackups', resource: { metadata: {} }, context: 'expanded' })).toBeNull()
  })

  it('in the drawer, reads as the page does, unframed, with a tab link that opens the page', () => {
    state.workspace = workspace()
    const html = text(
      renderToStaticMarkup(
        <QueryClientProvider client={new QueryClient()}>
          <MemoryRouter>
            {renderVeleroSummary({
              apiKind: 'backups',
              context: 'drawer',
              resource: { metadata: { namespace: 'velero', name: 'b' }, spec: { storageLocation: 'default', ttl: '24h0m0s' }, status: { phase: 'Completed', expiration: new Date(Date.now() + 3_600_000).toISOString() } },
            })}
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    )
    expect(html).toContain('At a glance')
    expect(html).toContain('Kept until')
    expect(html).toContain('Restores →')
    expect(html).not.toContain('rounded-xl border')
  })

  it('reads a completed backup: when it finished, its counts, and a restore Velero never tested', () => {
    state.workspace = workspace()
    const html = page('backups', {
      metadata: { namespace: 'velero', name: 'b' },
      spec: { includedNamespaces: ['demo'], storageLocation: 'default', ttl: '24h0m0s' },
      status: { phase: 'Completed', startTimestamp: hoursAgo(2.001), completionTimestamp: hoursAgo(2), expiration: new Date(Date.now() + 21.5 * 3_600_000).toISOString(), progress: { itemsBackedUp: 23, totalItems: 23 }, errors: 0, warnings: 1 },
    })
    expect(html).toContain('finished 2h ago · took 3s')
    expect(html).toContain('23 of 23 items backed up')
    expect(html).toContain('0 errors')
    expect(html).toContain('1 warning')
    expect(html).toContain('Show 1 warning')
    expect(html).toContain('>Namespaces<')
    expect(html).toContain('as declared')
    expect(html).toContain('in 21h')
    expect(html).toContain('Not verified — Velero never tests a restore')
    expect(html).toContain('Restores →')
    expect(html).toContain('On demand, not by a schedule')
  })

  it('shows a partly failed backup’s messages control on the page itself, whatever leads it', () => {
    state.workspace = undefined
    const html = page('backups', { metadata: { namespace: 'velero', name: 'b' }, spec: { labelSelector: { matchLabels: { app: 'x' } } }, status: { phase: 'PartiallyFailed', errors: 2 } }, <div>ISSUE</div>)
    expect(html).toContain('ISSUE')
    expect(html).toContain('Show 2 errors')
    expect(html).toContain('narrowed by resource or label filters')
    expect(html).toContain('Not verified')
  })

  it('names a restore Velero finished from the backup, where the answer is not a certain no', () => {
    state.workspace = workspace()
    const done = { phase: 'Completed', startTimestamp: hoursAgo(5), completionTimestamp: hoursAgo(5) }
    const backup = { metadata: { namespace: 'velero', name: 'b' }, spec: { storageLocation: 'default' }, status: done }
    const restore = (name: string, phase: string, completed?: string) => ({ namespace: 'velero', name, uid: name, phase, completed, errors: 0, warnings: 0 })
    state.runsBy = { 'restores:b': runsOf([restore('r-failed', 'Failed', hoursAgo(1)), restore('r-ok', 'PartiallyFailed', hoursAgo(2)), restore('r-old', 'Completed', hoursAgo(3))]) }
    const html = plainText(page('backups', backup))
    expect(html).toContain('Restored before: r-ok · partially failed · 2h ago')
    expect(html).not.toContain('Not verified')
    state.runsBy = { 'restores:b': runsOf([restore('r-failed', 'Failed', hoursAgo(1))]) }
    expect(page('backups', backup)).toContain('Not verified — Velero never tests a restore')
    state.runsBy = { 'restores:b': runsOf([restore('r-ok', 'Completed', hoursAgo(2))]) }
    state.workspace = workspace({ locations: [location({ name: 'default', phase: 'Unavailable' })] })
    const unavailable = page('backups', backup)
    expect(unavailable).toContain('No: its storage location is unavailable')
    expect(unavailable).not.toContain('Restored before')
    state.runsBy = {}
  })

  it('says a rejected backup backed up nothing, and neither keeps nor restores anything', () => {
    state.workspace = workspace()
    const html = page('backups', {
      metadata: { namespace: 'velero', name: 'r' },
      spec: { storageLocation: 'default' },
      status: { phase: 'FailedValidation', validationErrors: ['an existing backup storage location was not specified'], expiration: new Date(Date.now() + 3_600_000).toISOString() },
    })
    expect(html).toContain('Nothing</span> — Velero rejected it: an existing backup storage location was not specified')
    expect(html).not.toContain('Kept until')
    expect(html).not.toContain('>Restore<')
  })

  it('says no to a restore where it is certain, with the reason', () => {
    state.workspace = workspace({ locations: [location({ name: 'dr', phase: 'Unavailable' })] })
    const done = { phase: 'Completed', startTimestamp: hoursAgo(1), completionTimestamp: hoursAgo(1) }
    expect(page('backups', { metadata: { namespace: 'velero', name: 'b' }, spec: { storageLocation: 'dr' }, status: done })).toContain('No: its storage location is unavailable')
    expect(page('backups', { metadata: { namespace: 'velero', name: 'b' }, spec: { storageLocation: 'gone' }, status: done })).toContain('No: its storage location doesn’t exist')
    expect(page('backups', { metadata: { namespace: 'velero', name: 'b' }, spec: {}, status: { phase: 'Failed', startTimestamp: hoursAgo(1) } })).toContain('No: it did not complete')
    expect(page('backups', { metadata: { namespace: 'velero', name: 'b' }, spec: {}, status: { phase: 'InProgress', startTimestamp: hoursAgo(1) } })).toContain('No: it hasn’t finished')
  })

  it('leads with Velero’s issue as a plain banner, with its next step', () => {
    state.workspace = workspace({
      issues: [{
        id: 'i1', severity: 'critical', category: 'location', kind: 'BackupStorageLocation', group: 'velero.io', namespace: 'velero', name: 'dr', reason: 'BackupStorageLocationUnavailable',
        message: 'BackupStorageLocation "dr" is unavailable: rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist',
        action: 'Create the bucket or point the location at one that exists',
        affected: [{ kind: 'Backup', namespace: 'velero', name: 'r' }],
      }],
    })
    const html = page('backups', { metadata: { namespace: 'velero', name: 'r' }, spec: { storageLocation: 'dr' }, status: { phase: 'FailedValidation' } }, <div>GENERIC</div>)
    expect(html).not.toContain('GENERIC')
    expect(html).toContain('The bucket doesn’t exist.')
    expect(html).toContain('Next step: </span>Create the bucket or point the location at one that exists')
    expect(html).toContain('Velero’s message')
    expect(html).toContain('See in Issues →')
  })

  it('gives a banner without the Issue’s own action a next step from what its message means', () => {
    state.workspace = workspace({
      issues: [{
        id: 'i1', severity: 'critical', category: 'location', kind: 'BackupStorageLocation', group: 'velero.io', namespace: 'velero', name: 'dr', reason: 'BackupStorageLocationUnavailable',
        message: 'BackupStorageLocation "dr" is unavailable: rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist',
      }],
    })
    const html = page('backupstoragelocations', { metadata: { namespace: 'velero', name: 'dr' }, spec: {}, status: { phase: 'Unavailable' } })
    expect(html).toContain('Next step: </span>Create the bucket, or point the storage location at an existing one')
  })

  it('says the runs Velero keeps are unknown when the backups were not read, never "at least 0"', () => {
    const reading = schedule({ runs: { read: 'unknown', kept: 0 } })
    state.workspace = workspace({ schedules: [reading] })
    state.activity = { reading }
    for (const [coverage, says] of [
      [{ state: 'denied', deniedNamespaces: ['velero'] }, 'Unknown — no access in velero'],
      [{ state: 'uncached', uncachedNamespaces: ['velero'] }, 'Unknown — not cached by Radar in velero'],
    ] as const) {
      state.runsBy = { 'backups:nightly': { observedAt: '', coverage, runs: [], total: 0 } }
      const html = page('schedules', { metadata: { namespace: 'velero', name: 'nightly' }, spec: { template: {} } })
      expect(html).toContain(says)
      expect(html).not.toContain('At least 0')
      expect(html).not.toContain('0 completed')
    }
    // A partial read still bounds the count from below.
    state.runsBy = { 'backups:nightly': { observedAt: '', coverage: { state: 'partial' }, runs: [{ namespace: 'velero', name: 'nightly-1', uid: '1', phase: 'Completed', errors: 0, warnings: 0 }], total: 1 } }
    expect(page('schedules', { metadata: { namespace: 'velero', name: 'nightly' }, spec: { template: {} } })).toContain('At least 1 kept')
    state.runsBy = {}
  })

  it('reads a zero retention as the server default', () => {
    state.workspace = workspace()
    state.activity = { reading: schedule() }
    state.runsBy = { 'backups:nightly': runsOf([]) }
    const html = page('schedules', { metadata: { namespace: 'velero', name: 'nightly' }, spec: { template: { ttl: '0h0m0s' } } })
    expect(html).toContain('The Velero server’s default (30 days unless it was changed)')
    expect(html).not.toContain('0 hours')
    state.runsBy = {}
  })

  it('leads a schedule that cannot run with its Velero reading, not the generic issue row', () => {
    const reading = schedule({ name: 'very-long', state: 'cannotRun', attention: true, lastSubmitted: undefined, nextRun: undefined, runs: { read: 'full', kept: 0 } })
    state.workspace = workspace({ schedules: [reading] })
    state.activity = { reading }
    state.runsBy = { 'backups:very-long': runsOf([]) }
    const html = page('schedules', { metadata: { namespace: 'velero', name: 'very-long' }, spec: { template: {} } }, <div>GENERIC</div>)
    expect(html).not.toContain('GENERIC')
    expect(html).toContain('Schedule cannot run')
    expect(html).toContain('longer than 63 characters')
    expect(html).toContain('Next step: </span>Recreate the schedule with a name of 63 characters or fewer')
    // Said once, in the banner; the rows say what they are.
    expect(html.match(/longer than 63 characters/g)).toHaveLength(1)
    expect(html).toMatch(/Last success<\/dt><dd[^>]*><span[^>]*>Never</)
    expect(html).toMatch(/Next run<\/dt><dd[^>]*><span[^>]*>None</)
    expect(html).not.toContain('None kept')
    state.runsBy = {}
  })

  it('leads a location whose validation is overdue with its reading', () => {
    state.workspace = workspace({ locations: [location({ name: 'dr', attention: true, validationOverdue: true, lastValidation: hoursAgo(5), frequency: { seconds: 60, source: 'server' } })] })
    const html = page('backupstoragelocations', { metadata: { namespace: 'velero', name: 'dr' }, spec: {}, status: { phase: 'Available' } }, <div>GENERIC</div>)
    expect(html).not.toContain('GENERIC')
    expect(html).toContain('Storage location validation overdue')
    expect(html).toContain('Next step: </span>Check that the Velero server is running, and read its logs')
  })

  it('reads a restore: where it went, what it processed, and its warnings', () => {
    state.workspace = workspace()
    const html = page('restores', {
      metadata: { namespace: 'velero', name: 'r' },
      spec: { backupName: 'b', includedNamespaces: ['demo', 'shop'], namespaceMapping: { demo: 'demo-copy' } },
      status: { phase: 'PartiallyFailed', startTimestamp: hoursAgo(1), completionTimestamp: hoursAgo(1), progress: { itemsRestored: 10, totalItems: 12 }, warnings: 3, errors: 0 },
    })
    expect(html.indexOf('Restored into')).toBeLessThan(html.indexOf('Outcome'))
    expect(plainText(html)).toContain('demo → demo-copy, shop')
    expect(html).toMatch(/<button[^>]*>demo-copy<\/button>/)
    expect(html).toMatch(/<button[^>]*>shop<\/button>/)
    expect(html).toContain('unmapped namespaces keep their names')
    expect(html).toContain('10 of 12 items processed')
    expect(html).toContain('3 warnings')
    expect(html).not.toContain('items restored')
  })

  it('says a rejected restore restored nothing, and why', () => {
    state.workspace = workspace()
    const html = page('restores', {
      metadata: { namespace: 'velero', name: 'r' },
      spec: { backupName: 'b', includedNamespaces: ['*'] },
      status: { phase: 'FailedValidation', validationErrors: ['The BSL dr-replica is unavailable, cannot retrieve the backup'] },
    })
    expect(html).toContain('Nothing</span> — Velero rejected it: Its storage location dr-replica is unavailable')
    expect(html).not.toContain('Restored into')
    expect(html).not.toContain('Every namespace in the backup')
  })

  it('says a rejected restore restored nothing, on the Restores tab as on its page', () => {
    state.runsBy = { 'restores:b': runsOf([{ namespace: 'velero', name: 'r2', uid: 'r2', phase: 'FailedValidation', errors: 0, warnings: 0, includedNamespaces: [], excludedNamespaces: [] }]) }
    const html = text(renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter><VeleroBackupRestores namespace="velero" name="b" /></MemoryRouter></QueryClientProvider>))
    expect(html).toContain('Nothing — rejected')
    expect(html).not.toContain('Every namespace in the backup')
    state.runsBy = {}
  })

  it('shows where each restore from a backup went, on its Restores tab', () => {
    state.runsBy = { 'restores:b': runsOf([{ namespace: 'velero', name: 'r1', uid: 'r1', phase: 'Completed', errors: 0, warnings: 0, includedNamespaces: ['demo', 'shop'], excludedNamespaces: [], namespaceMapping: { demo: 'demo-copy' } }]) }
    const html = text(renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}><MemoryRouter><VeleroBackupRestores namespace="velero" name="b" /></MemoryRouter></QueryClientProvider>))
    expect(html).toContain('>Into<')
    expect(plainText(html)).toContain('demo → demo-copy, shop')
    expect(html).not.toContain('Nothing — rejected')
    // A name never breaks at its hyphen.
    expect(html).toMatch(/<span class="inline-block max-w-full truncate[^"]*">demo-copy,<\/span>/)
    state.runsBy = {}
  })

  it('leads a schedule with its last success, next run and recent runs, and says when its last run failed', () => {
    const failed = { name: 'nightly-2', phase: 'PartiallyFailed', created: hoursAgo(1), ranAt: hoursAgo(1) }
    const ok = { name: 'nightly-1', phase: 'Completed', created: hoursAgo(25), ranAt: hoursAgo(25) }
    const reading = schedule({ runs: { read: 'full', kept: 46, newest: failed, newestCompleted: ok, recent: [failed, ok] } })
    state.workspace = workspace({ schedules: [reading] })
    state.activity = { reading }
    state.runsBy = { 'backups:nightly': runsOf([{ namespace: 'velero', name: 'nightly-2', uid: '2', phase: 'PartiallyFailed', errors: 1, warnings: 0 }, { namespace: 'velero', name: 'nightly-1', uid: '1', phase: 'Completed', errors: 0, warnings: 0 }]) }
    const html = page('schedules', { metadata: { namespace: 'velero', name: 'nightly' }, spec: { template: { includedNamespaces: ['demo'], ttl: '720h0m0s' } } })
    expect(html.indexOf('Last success')).toBeLessThan(html.indexOf('Covers'))
    expect(html).toContain('1d ago')
    expect(html).toContain('the newest run since then failed')
    expect(html).toContain('Open backup nightly-2')
    expect(html).toContain('All 46 runs →')
    expect(html).toContain('46 kept · 1 completed · 1 failed or partially failed')
    expect(html).not.toContain('Schedule activity')
    expect(renderToStaticMarkup(<VeleroScheduleStateBadge reading={reading} />)).toContain('Last run failed')
  })

  it('reads an unavailable location in plain words, with Velero’s message a click away', () => {
    state.workspace = workspace({ locations: [location({ name: 'dr', phase: 'Unavailable', default: false, defaultSource: 'spec', message: 'rpc error: code = Unknown desc = AccessDenied: Access Denied status code: 403' })] })
    state.stored = { backups: [{ namespace: 'velero', name: 'x', phase: 'Failed' }, { namespace: 'velero', name: 'y', phase: 'Completed' }], restorable: 1, expired: 0, stored: 2 }
    const html = page('backupstoragelocations', {
      metadata: { namespace: 'velero', name: 'dr' },
      spec: { provider: 'aws', objectStorage: { bucket: 'radar-dr', prefix: 'velero' } },
      status: { phase: 'Unavailable', lastSyncedTime: hoursAgo(0.05) },
    })
    expect(html).toContain('Velero’s credentials can’t access the bucket')
    expect(html).toContain('Velero’s message')
    expect(html).toContain('last sync attempt 3m ago')
    const plain = plainText(html)
    expect(plain).toContain('No — default is the default location')
    expect(html).toContain('aws · s3://radar-dr/velero')
    expect(plain).toContain('2 backups · 1 completed · 0 expired · 1 failed')
    expect(plain).toContain('None of them can be restored while the location is unavailable')
    expect(plain).toMatch(/Last checked \d+s ago — failed/)
    expect(plain).not.toContain('Last validated')
    expect(plain).toMatch(/Every 1m · last sync attempt/)
  })

  it('lists a location’s backups with the failed ones a segment away', () => {
    state.stored = { backups: [{ namespace: 'velero', name: 'x', phase: 'Failed' }, { namespace: 'velero', name: 'y', phase: 'Completed' }, { namespace: 'velero', name: 'z', phase: 'PartiallyFailed' }], restorable: 1, expired: 0, stored: 3 }
    const html = plainWords(renderToStaticMarkup(<MemoryRouter><VeleroLocationBackups namespace="velero" name="dr" /></MemoryRouter>))
    expect(html).toContain('3 · 1 completed · 0 expired · 2 failed')
    expect(html).toMatch(/Failed 2/)
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

  it('opens on every run, newest first, with the failed ones counted and a way to create one', () => {
    state.runs = response([run('running-1', 'InProgress'), run('ok-1', 'Completed'), { ...run('bad-1', 'PartiallyFailed'), reason: 'found a backup with status "InProgress" during the server starting, mark it as "Failed"' }, run('rejected-1', 'FailedValidation')])
    const html = text(render('/resources/backups?apiGroup=velero.io'))
    expect(html).toContain('running-1')
    expect(html).toContain('ok-1')
    expect(html).toContain('bad-1')
    expect(html.indexOf('All')).toBeLessThan(html.indexOf('Failed'))
    expect(html).toMatch(/Failed<span[^>]*><span class="text-red[^"]*">2</)
    expect(html).toContain('Velero restarted while this backup ran')
    expect(html).toContain('On demand')
    // jsdom has no matchMedia, so this is the narrow list: nothing past Started.
    expect(html).toContain('>Started<')
    expect(html).not.toContain('>Expires<')
    expect(html).not.toContain('>Errors · warnings<')
    expect(html).toContain('Create Backup')
    expect(html).toContain('Search backups')
  })

  it('on a narrow screen folds where a backup is stored and when it expires into Started, its counts under its outcome, and cuts run names in the middle', () => {
    state.runs = response([{ ...run('nightly-20261117041210', 'PartiallyFailed', 'nightly'), storageLocation: 'default', errors: 1, expiration: new Date(Date.now() + 5 * 3_600_000).toISOString() }])
    const html = text(render('/resources/backups?apiGroup=velero.io'))
    expect(html).not.toContain('>Stored in<')
    expect(html).toContain('in default')
    expect(html).toMatch(/expires <span[^>]*><span>in [45]h</)
    expect(html).toMatch(/text-red[^"]*">1 error</)
    expect(html).not.toContain('0 warnings')
    expect(html).toContain('<span class="min-w-0 truncate">nightly-20261117</span><span class="shrink-0">041210</span>')
  })

  it('takes no label filter from a link: a schedule’s runs are on its page', () => {
    state.runs = response([run('a-1', 'Completed', 'a'), run('b-1', 'Completed', 'b')])
    const html = render('/resources/backups?apiGroup=velero.io&labels=velero.io/schedule-name%3Da')
    expect(html).toContain('a-1')
    expect(html).toContain('b-1')
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
