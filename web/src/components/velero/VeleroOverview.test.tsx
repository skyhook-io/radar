import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { VELERO_NOTHING_NEEDS_ATTENTION, VeleroOverview } from './VeleroOverview'
import { hoursAgo, location, schedule, workspace } from './velero.fixtures'

function render(data: VeleroWorkspaceResponse, installation: string | null = null) {
  return renderToStaticMarkup(
    <VeleroOverview data={data} installation={installation} onInstallation={() => {}} onInspect={() => {}} inspected={null} />,
  )
}

// renderToStaticMarkup escapes apostrophes; compare against the text a reader sees.
const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

describe('Velero overview', () => {
  it('lists late, probably-late and cannot-run schedules under Needs attention, each with its reason', () => {
    const html = text(render(workspace({
      schedules: [
        schedule({ name: 'hourly', state: 'late', attention: true, lastSubmitted: hoursAgo(4), missedRuns: 3 }),
        schedule({ name: 'offset', state: 'probablyLate', reason: 'zoneUnconfirmed', attention: true, nextRunCertain: false, zone: { name: 'UTC', source: 'unknown' } }),
        schedule({ name: `a-${'x'.repeat(70)}`, state: 'cannotRun', reason: 'nameTooLong', attention: true }),
        schedule({ name: 'quiet', state: 'possiblyLate', reason: 'zoneUnconfirmed', attention: false }),
      ],
    })))
    const attention = html.slice(html.indexOf('Needs attention'), html.indexOf('Schedules</h2>'))
    expect(attention).toContain('3 scheduled times passed. Velero does not catch up on missed runs.')
    expect(attention).toContain('No backup submitted since')
    expect(attention).toContain('Looks late, but Radar couldn’t confirm the Velero server’s time zone.')
    expect(attention).toContain('This schedule’s name is longer than 63 characters, so Velero cannot label its backups and every run fails.')
    // possiblyLate stays in the table, not in Needs attention.
    expect(attention).not.toContain('quiet')
    expect(attention).not.toContain(VELERO_NOTHING_NEEDS_ATTENTION)
    // The table marks an estimated next run.
    expect(html).toContain('~in ')
    expect(html).toContain('Completed is what Velero reports. It is not a test of a restore.')
  })

  it('reads runs Radar could not read as unknown, never as none', () => {
    const html = text(render(workspace({
      coverage: { schedules: { state: 'full' }, backups: { state: 'denied' }, backupStorageLocations: { state: 'full' }, restores: { state: 'full' }, backupRepositories: { state: 'full' } },
      schedules: [schedule({ runs: { read: 'unknown', kept: 0 } })],
    })))
    expect(html).toContain('Unknown — no access to backups')
    expect(html).not.toContain('None among the backups Velero still keeps')
    expect(html).toContain('Some Velero data used on this screen was not read.')
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
    expect(html).toContain('Show 6 more')
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
    expect(html).toContain('Schedules:</dt><dd class="font-medium text-theme-text-primary">unknown — not read')
    expect(html).toContain('Storage:</dt><dd class="font-medium text-theme-text-primary">unknown — not read')
    expect(html).not.toContain('no locations')
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
    expect(html).toContain('The Velero server has no running replicas, so nothing submits backups.')
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

  it('states the scope, and the forced namespace when Radar is limited to one', () => {
    expect(text(render(workspace()))).toContain('All Velero installations you can read. The namespace filter does not apply here.')
    expect(text(render(workspace({ forcedNamespace: 'velero' })))).toContain('Radar is limited to namespace velero')
  })
})
