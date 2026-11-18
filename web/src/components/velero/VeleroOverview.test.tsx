import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { VELERO_NOTHING_NEEDS_ATTENTION, VeleroOverview, type VeleroNamespaceList } from './VeleroOverview'
import { VeleroSchedules } from './VeleroSchedules'
import { hoursAgo, location, schedule, workspace } from './velero.fixtures'

// The Overview and the Schedule kind's list read the same workspace data; a
// reader moves between them, so these checks cover what the two say together,
// the Overview first.
function render(data: VeleroWorkspaceResponse, installation: string | null = null) {
  const props = { data, installation, onInstallation: () => {}, onInspect: () => {}, inspected: null }
  return renderToStaticMarkup(
    <MemoryRouter>
      <VeleroOverview {...props} namespaces={undefined} />
      <VeleroSchedules {...props} />
    </MemoryRouter>,
  )
}

function renderOverview(data: VeleroWorkspaceResponse, namespaces: VeleroNamespaceList = undefined) {
  return renderToStaticMarkup(
    <MemoryRouter>
      <VeleroOverview data={data} installation={null} onInstallation={() => {}} onInspect={() => {}} inspected={null} namespaces={namespaces} />
    </MemoryRouter>,
  )
}

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

describe('Velero overview', () => {
  it('links the lists it summarises, from their labels and their values', () => {
    const html = text(renderOverview(workspace()))
    expect(html).toContain('Needs attention')
    expect(html).toContain('>Schedules</button>')
    expect(html).toContain('>Storage</button>')
    expect(html).toMatch(/>Storage<\/button><\/span>:<\/dt><dd[^>]*><button[^>]*>1 of 1 location available/)
  })

  it('shows each schedule’s protection below Needs attention, so a healthy installation has evidence', () => {
    // Backup sync recreated this run's object: created 2h ago, expiring a day later, while the schedule keeps runs 30 days.
    const run = { name: 'nightly-1', phase: 'Completed', created: hoursAgo(2), ranAt: hoursAgo(2), expires: new Date(Date.parse(hoursAgo(2)) + 24 * 3_600_000).toISOString() }
    const html = text(renderOverview(workspace({
      schedules: [schedule({ ttl: '720h0m0s', runs: { read: 'full', kept: 1, newest: run, newestCompleted: run } }), schedule({ name: 'weekly', runs: { read: 'full', kept: 1, newest: run, newestCompleted: run } })],
      locations: [location({ phase: 'Unavailable' })],
    })))
    const table = html.slice(html.indexOf('>Schedules</h2>'))
    expect(html.indexOf('Needs attention')).toBeLessThan(html.indexOf('>Schedules</h2>'))
    expect(table).toContain('nightly')
    expect(table).toContain('On schedule')
    expect(table).toContain('2h ago')
    expect(table).toContain('unavailable')
    expect(table).toContain('30 days')
    expect(table).not.toContain('1 day')
    // jsdom has no matchMedia: the narrow table says the retention under Stored in.
    expect(table).toContain('kept for 30 days')
    expect(table).toContain('kept for the server’s default')
    expect(table).toContain('Next run')
    expect(table).toMatch(/in 2[12]h/)
  })

  it('says the server is running only when a replica is ready', () => {
    const server = (replicas: number, readyReplicas?: number) =>
      text(renderOverview(workspace({ installations: [{ namespace: 'velero', server: { source: 'deployment', replicas, readyReplicas }, defaultSource: 'spec' }] })))
    expect(server(1, 1)).toMatch(/Velero server:<\/dt><dd[^>]*>running/)
    expect(server(1, 0)).toMatch(/Velero server:<\/dt><dd[^>]*>not ready/)
    expect(server(1)).toMatch(/Velero server:<\/dt><dd[^>]*>not ready/)
    expect(server(0)).toMatch(/Velero server:<\/dt><dd[^>]*>not running/)
  })

  it('leads an issue it recognises with a plain reason, Velero’s text folded under it', () => {
    const html = text(renderOverview(workspace({
      issues: [{ id: 'i1', severity: 'critical', category: 'location', kind: 'BackupStorageLocation', group: 'velero.io', namespace: 'velero', name: 'dr', reason: 'BackupStorageLocationUnavailable', message: 'BackupStorageLocation "dr" is unavailable: rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist' }],
    })))
    expect(html).toContain('The bucket doesn’t exist.')
    expect(html).toContain('Velero’s message')
  })

  it('lists late, probably-late and cannot-run schedules under Needs attention, each with its reason', () => {
    const html = text(render(workspace({
      schedules: [
        schedule({ name: 'hourly', state: 'late', attention: true, lastSubmitted: hoursAgo(4), missedRuns: 3 }),
        schedule({ name: 'offset', state: 'probablyLate', reason: 'zoneUnconfirmed', attention: true, nextRunCertain: false, zone: { name: 'UTC', source: 'unknown' } }),
        schedule({ name: `a-${'x'.repeat(70)}`, state: 'cannotRun', reason: 'nameTooLong', attention: true }),
        schedule({ name: 'quiet', state: 'possiblyLate', reason: 'zoneUnconfirmed', attention: false }),
      ],
    })))
    const attention = html.slice(html.indexOf('Needs attention'), html.indexOf('>Schedules</h2>'))
    expect(attention).toContain('3 scheduled times passed. Velero does not catch up on missed runs.')
    expect(attention).toContain('No backup submitted since')
    expect(attention).toContain('Looks late, but Radar couldn’t confirm the Velero server’s time zone.')
    expect(attention).toContain('This schedule’s name is longer than 63 characters, too long for the label Velero puts on its backups, so Velero submits no runs.')
    // possiblyLate stays in the table, not in Needs attention.
    expect(attention).not.toContain('quiet')
    expect(attention).not.toContain(VELERO_NOTHING_NEEDS_ATTENTION)
    // The table marks an estimated next run.
    expect(html).toContain('~in ')
    // Radar never calls a backup restorable, so no disclaimer repeats under every table.
    expect(html).not.toContain('It is not a test of a restore.')
    // The rows say what's wrong, not where Radar found it.
    expect(html).not.toContain('Radar’s reading of')
    expect(html).not.toContain('Detected by Radar')
  })

  it('reads runs Radar could not read as unknown, never as none', () => {
    const html = text(render(workspace({
      coverage: { schedules: { state: 'full' }, backups: { state: 'denied' }, backupStorageLocations: { state: 'full' }, restores: { state: 'full' }, backupRepositories: { state: 'full' } },
      schedules: [schedule({ runs: { read: 'unknown', kept: 0 } })],
    })))
    expect(html).toContain('Unknown — no access to backups')
    expect(html).not.toContain('None among the backups Velero still keeps')
    expect(html).toContain('Radar couldn’t read some of the Velero data on this screen')
  })

  it('says what was read when nothing needs attention, and never calls it healthy', () => {
    const html = text(render(workspace({ schedules: [schedule({ runs: { read: 'full', kept: 0 } })] })))
    expect(html).toContain(VELERO_NOTHING_NEEDS_ATTENTION)
    expect(html).toContain('None kept')
    expect(html.toLowerCase()).not.toContain('all healthy')
  })

  it('groups what needs attention by what it is about, and folds a long group', () => {
    const issues = Array.from({ length: 10 }, (_, i) => ({
      id: `i${i}`, severity: 'critical' as const, category: 'backup', kind: 'Backup', group: 'velero.io', namespace: 'velero', name: `run-${i}`, reason: 'BackupFailed', message: 'boom',
    }))
    const late = schedule({ name: 'late-one', state: 'late', attention: true, missedRuns: 2 })
    const html = text(render(workspace({ issues, schedules: [late] })))
    expect(html.indexOf('late-one')).toBeLessThan(html.indexOf('run-0'))
    expect(html.indexOf('Schedules <span')).toBeLessThan(html.indexOf('Backups and restores'))
    // One schedule row, then the rest of an eight-row budget for the runs.
    expect(html).toContain('Show 3 more')
  })

  it('answers "is Velero working?" in one line before the detail', () => {
    const late = schedule({ name: 'late-one', state: 'late', attention: true, missedRuns: 2, reason: 'serverDown' })
    const html = text(render(workspace({
      schedules: [late, schedule({ name: 'ok', runs: { read: 'full', kept: 1, newestCompleted: { name: 'ok-1', phase: 'Completed', created: hoursAgo(3), ranAt: hoursAgo(3) } } })],
      installations: [{ namespace: 'velero', server: { source: 'deployment', deployment: 'velero', replicas: 0 }, defaultSource: 'spec' }],
      locations: [location(), location({ name: 'dr', phase: 'Unavailable' })],
    })))
    expect(html).toContain('Velero server:</dt><dd class="font-medium text-theme-text-primary">not running')
    expect(html).toContain('1 of 2 need attention')
    expect(html).toContain('2h ago')
    expect(html).toContain('1 of 2 locations available')
    expect(html.indexOf('Velero at a glance')).toBeLessThan(html.indexOf('Needs attention'))
  })

  it('never reads unread kinds as none in the headline', () => {
    const denied = { state: 'denied' as const }
    const full = { state: 'full' as const }
    const html = text(render(workspace({
      coverage: { schedules: denied, backups: full, backupStorageLocations: denied, restores: full, backupRepositories: full },
      schedules: [],
      locations: [],
    })))
    expect(html).toMatch(/>Schedules<\/button><\/span>:<\/dt><dd[^>]*><button[^>]*>unknown — not read/)
    expect(html).toMatch(/>Storage<\/button><\/span>:<\/dt><dd[^>]*><button[^>]*>unknown — not read/)
    expect(html).not.toContain('no locations')
    expect(html).not.toContain('no schedules')
  })

  it('keeps every group visible but holds the whole list to eight rows', () => {
    const issues = Array.from({ length: 10 }, (_, i) => ({
      id: `i${i}`, severity: 'critical' as const, category: 'backup', kind: 'Backup', group: 'velero.io', namespace: 'velero', name: `run-${i}`, reason: 'BackupFailed', message: 'boom',
    }))
    const late = Array.from({ length: 9 }, (_, i) => schedule({ name: `late-${i}`, state: 'late', attention: true, missedRuns: 2 }))
    const html = text(render(workspace({ issues, schedules: late })))
    // The schedules take the budget; the runs group still shows its first row.
    expect(html).toContain('run-0')
    expect(html).toContain('Show 1 more')
    expect(html).toContain('Show 9 more')
  })

  it('does not call a schedule fine when Radar cannot read its cron', () => {
    const html = text(render(workspace({ schedules: [schedule({ state: 'unknown', attention: false })] })))
    expect(html).toContain('1, 1 with a cron Radar can’t read')
    expect(html).not.toContain('none need attention')
  })

  it('never reads unread backups as no successful backup', () => {
    const html = text(render(workspace({ schedules: [schedule({ runs: { read: 'unknown', kept: 0 } })] })))
    expect(html).toContain('unknown — no access to backups')
    expect(html).not.toContain('none kept</dd>')
  })

  it('names a stopped Velero server first, and as the cause of late schedules', () => {
    const late = schedule({ name: 'late-one', state: 'late', attention: true, missedRuns: 2, reason: 'serverDown' })
    const html = text(render(workspace({
      schedules: [late],
      installations: [{ namespace: 'velero', server: { source: 'deployment', deployment: 'velero', replicas: 0 }, defaultSource: 'spec' }],
    })))
    expect(html).toContain('Velero server not running')
    expect(html.indexOf('Velero server not running')).toBeLessThan(html.indexOf('late-one'))
    // Named once, in its own row: the late schedule below it doesn't repeat the cause.
    expect(html).not.toContain('The Velero server has no running replicas, so nothing submits backups.')
    expect(html).not.toContain('Deployment velero in velero')
  })

  it('reads a schedule due within its grace as on schedule, not as a problem', () => {
    const html = text(render(workspace({ schedules: [schedule({ state: 'due', nextRun: new Date(Date.now() - 30_000).toISOString() })] })))
    const list = html.slice(html.indexOf('>Schedules</h1>'))
    expect(list).toContain('On schedule')
    expect(list).toContain('due now')
    expect(list).not.toContain('>Due<')
    expect(list).not.toContain('was due')
  })

  it('says a schedule’s last run failed instead of a green state, with that run in the merged Last run cell', () => {
    const failed = { name: 'nightly-2', phase: 'Failed', created: hoursAgo(1), ranAt: hoursAgo(1) }
    const ok = { name: 'nightly-1', phase: 'Completed', created: hoursAgo(25), ranAt: hoursAgo(25) }
    const html = text(render(workspace({ schedules: [schedule({ runs: { read: 'full', kept: 2, newest: failed, newestCompleted: ok } })] })))
    const list = html.slice(html.indexOf('>Schedules</h1>'))
    expect(list).toContain('Last run failed')
    expect(list).not.toContain('On schedule')
    expect(list).toContain('>Last run<')
    expect(list).not.toContain('Last submission')
    expect(list).toContain('last success 1d ago')
  })

  it('keeps a healthy schedule out of green when Radar has not confirmed its zone', () => {
    const html = text(render(workspace({ schedules: [schedule({ zone: { name: 'UTC+02:00', source: 'inferred' } })] })))
    const list = html.slice(html.indexOf('>Schedules</h1>'))
    expect(list).toMatch(/<span class="badge-sm [^"]*text-theme-text-secondary[^"]*">On schedule/)
  })

  it('says a next run in the past was due, not that it ran', () => {
    const html = text(render(workspace({ schedules: [schedule({ state: 'late', attention: true, nextRun: new Date(Date.now() - 3 * 3_600_000).toISOString() })] })))
    expect(html).toContain('was due 3h ago')
  })

  it('says how many issues were left out when an installation was capped', () => {
    const issues = Array.from({ length: 50 }, (_, i) => ({
      id: `i${i}`, severity: 'critical' as const, category: 'backup', kind: 'Backup', group: 'velero.io', namespace: 'velero', name: `run-${i}`, reason: 'BackupFailed', message: 'boom',
    }))
    const html = text(render(workspace({ issues, issueTotals: { velero: 120 } })))
    expect(html).toContain('Showing 50 of 120 issues in velero, the newest.')
    expect(html).not.toContain(VELERO_NOTHING_NEEDS_ATTENTION)
  })

  it('narrows to one installation and names namespaces only when there is more than one', () => {
    const data = workspace({
      installations: [
        { namespace: 'velero', server: { source: 'deployment' }, defaultSource: 'spec' },
        { namespace: 'velero-dr', server: { source: 'unread' }, defaultSource: 'unknown' },
      ],
      schedules: [schedule(), schedule({ namespace: 'velero-dr', name: 'dr-daily' })],
      locations: [location(), location({ namespace: 'velero-dr', name: 'dr' })],
    })
    const all = text(render(data))
    expect(all).toContain('All installations')
    expect(all).toContain('dr-daily')
    const one = text(render(data, 'velero'))
    expect(one).not.toContain('dr-daily')
    expect(one).toContain('nightly')
  })

  it('says nothing about scope for one installation, names a forced namespace, and offers installations when there are several', () => {
    expect(text(render(workspace()))).not.toMatch(/namespace filter|Velero in namespace/)
    expect(text(render(workspace({ forcedNamespace: 'velero' })))).toContain('Radar is limited to namespace velero')
    const two = workspace()
    two.installations = [two.installations[0], { ...two.installations[0], namespace: 'kommander' }]
    expect(text(render(two))).toContain('kommander')
  })
})

describe('Velero overview coverage', () => {
  const two = (overrides = {}) => workspace({ schedules: [schedule({ includedNamespaces: ['shop'] }), schedule({ name: 'weekly', state: 'paused', paused: true, includedNamespaces: ['db-*'] })], ...overrides })

  it('names the namespaces no schedule selects, leaving out Kubernetes’ own and Velero’s', () => {
    const html = text(renderOverview(two(), ['shop', 'db-main', 'web', 'kube-system', 'velero']))
    expect(html).toMatch(/Namespaces not in any schedule<\/span><\/span>:<\/span> <span[^>]*>web</)
    expect(html).not.toMatch(/not in any schedule.*kube-system/)
  })

  it('says none when every namespace is in a schedule, and unknown when the schedules were read in part', () => {
    expect(text(renderOverview(two(), ['shop', 'db-main']))).toMatch(/Namespaces not in any schedule<\/span><\/span>:<\/span> <span[^>]*>none</)
    const partial = two({ coverage: { ...workspace().coverage, schedules: { state: 'partial' } } })
    expect(text(renderOverview(partial, ['shop']))).toContain('unknown, Radar can’t read every schedule')
    expect(text(renderOverview(two(), 'error'))).toContain('unknown, Radar couldn’t list namespaces')
  })

  it('lists paused schedules as information, outside Needs attention', () => {
    const html = text(renderOverview(two(), ['shop']))
    expect(html).toMatch(/Paused:<\/span> <span[^>]*><span><button[^>]*>weekly<\/button><\/span> — no backups until it is resumed/)
    expect(html).toContain(VELERO_NOTHING_NEEDS_ATTENTION)
    expect(html).toContain('2, 1 paused, none need attention')
  })
})
