import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { VeleroScheduleActivity } from './VeleroScheduleActivity'
import { veleroMomentText, veleroScheduleReasonText, veleroScheduleTone, veleroRetentionText } from './reading'
import type { VeleroScheduleReading } from './types'

const ago = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
const ahead = (m: number) => new Date(Date.now() + m * 60_000).toISOString()

function reading(overrides: Partial<VeleroScheduleReading> = {}): VeleroScheduleReading {
  return {
    namespace: 'velero',
    name: 'nightly',
    created: ago(60 * 24),
    cron: '0 2 * * *',
    zone: { name: 'UTC', source: 'serverDefault' },
    state: 'onSchedule',
    attention: false,
    nextRun: ahead(90),
    nextRunCertain: true,
    runs: { read: 'full', kept: 0 },
    location: { name: 'default' },
    ...overrides,
  }
}

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

describe('VeleroScheduleActivity', () => {
  it('names the unstarted blocker holding a schedule and lists recent runs as links', () => {
    const blocker = { name: 'nightly-20261115020000', phase: 'New', created: ago(30), ranAt: ago(30) }
    const html = text(renderToStaticMarkup(
      <VeleroScheduleActivity
        reading={reading({
          state: 'held', reason: 'unstartedBlocker', attention: true, blocker,
          runs: { read: 'full', kept: 2, newest: blocker, newestCompleted: { name: 'nightly-20261114020000', phase: 'Completed', created: ago(1440), ranAt: ago(1440) }, recent: [blocker, { name: 'nightly-20261114020000', phase: 'Completed', created: ago(1440), ranAt: ago(1440) }] },
        })}
        onNavigate={() => {}}
      />,
    ))
    expect(html).toContain('Its backup hasn’t started for 30m; Velero submits nothing new until it does.')
    expect(html).toContain('Held by')
    expect(html).toContain('nightly-20261114020000')
    // The backup it waits on is named once, in its own row.
    expect(html.split('nightly-20261115020000').length - 1).toBe(2)
  })

  it('reads unread runs as unknown and marks an uncertain next run', () => {
    const html = text(renderToStaticMarkup(
      <VeleroScheduleActivity reading={reading({ runs: { read: 'unknown', kept: 0 }, nextRunCertain: false, zone: { name: 'UTC', source: 'unknown' } })} />,
    ))
    expect(html).toContain('Unknown — no access to backups')
    expect(html).not.toContain('None kept')
    expect(html).toContain('~in ')
    expect(html).toContain('time zone not confirmed')
  })

  it('uses the Schedule’s age when the drawer holds it', () => {
    expect(veleroScheduleReasonText(reading({ state: 'notValidated', reason: 'validationPending', attention: true }), { created: ago(60) }))
      .toBe('Velero hasn’t validated this schedule since it was created 1h ago, so its controller may not be processing schedules.')
  })

  it('keeps a hold calm until the server calls it attention, and words future and past moments', () => {
    expect(veleroScheduleTone({ state: 'held', attention: false })).toBe('neutral')
    expect(veleroScheduleTone({ state: 'held', attention: true })).toBe('alert')
    expect(veleroScheduleTone({ state: 'late', attention: true })).toBe('unhealthy')
    expect(veleroMomentText(ahead(125))).toBe('in 2h')
    expect(veleroMomentText(ago(5))).toBe('5m ago')
  })
})

it('says retention the way people do', () => {
  expect(veleroRetentionText('720h0m0s')).toBe('30 days')
  expect(veleroRetentionText('24h0m0s')).toBe('1 day')
  expect(veleroRetentionText('36h0m0s')).toBe('36 hours')
  expect(veleroRetentionText('1h30m0s')).toBe('1h30m0s')
})
