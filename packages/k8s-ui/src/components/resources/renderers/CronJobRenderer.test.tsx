import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CronJobRenderer } from './CronJobRenderer'
import { getCronJobStatus } from '../resource-utils'

describe('CronJob scheduling and execution evidence', () => {
  it('does not turn a missing recorded completion into an execution failure', () => {
    const html = renderToStaticMarkup(<CronJobRenderer data={{
      spec: { schedule: '0 * * * *', timeZone: 'UTC', startingDeadlineSeconds: 0 },
      status: { lastScheduleTime: '2026-01-02T12:00:00Z', lastSuccessfulTime: '2026-01-02T11:00:00Z' },
    }} />)
    expect(html).toContain('Latest completion not verified')
    expect(html).not.toContain('Recent Jobs Failing')
    expect(html).toContain('>UTC<')
    expect(html).toContain('>0s<')
  })

  it('shows suspension without claiming an old Job recovered', () => {
    const html = renderToStaticMarkup(<CronJobRenderer data={{
      spec: { schedule: '* * * * *', suspend: true },
      status: { lastScheduleTime: '2026-01-02T12:00:00Z' },
    }} />)
    expect(html).toContain('CronJob Suspended')
    expect(html).not.toContain('Latest completion not verified')
    expect(html).toContain('Controller local time (zone not reported)')
  })

  it('does not grade enabled configuration as successful scheduling', () => {
    expect(getCronJobStatus({ spec: { schedule: '* * * * *' }, status: {} })).toMatchObject({ text: 'Enabled', level: 'neutral' })
  })
})
