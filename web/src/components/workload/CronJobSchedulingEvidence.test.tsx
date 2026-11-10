import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CronJobSchedulingContext, CronJobSchedulingDetails } from './CronJobSchedulingEvidence'
import type { CronJobSchedulingEvidence } from '../../api/client'

const report: CronJobSchedulingEvidence = {
  uid: 'current-cronjob', state: 'reported', reason: 'InvalidSchedule',
  message: 'original report from Kubernetes', last_reported_at: '2026-10-21T08:00:00Z',
}

describe('CronJob scheduling evidence', () => {
  it('does not duplicate an operational report with a second notice', () => {
    expect(renderToStaticMarkup(<CronJobSchedulingContext evidence={report} onViewTimeline={() => {}} />)).toBe('')
  })
  it('makes an edited future schedule neutral and keeps its evidence accessible', () => {
    const html = renderToStaticMarkup(<CronJobSchedulingContext evidence={{ ...report, state: 'configuration_changed', configuration_changed_at: '2026-10-21T08:05:00Z' }} onViewTimeline={() => {}} />)
    expect(html).toContain('Schedule changed after this error')
    expect(html).toContain('Kubernetes has not recorded a scheduled run since the change.')
    expect(html).toContain('Earlier report')
    expect(html).toContain(report.message)
    expect(html).toContain('View timeline')
    expect(html).not.toContain('Operational issues')
    expect(html).not.toContain('Warning')
  })
  it('does not call incomplete history recovery or a confirmed current failure', () => {
    const html = renderToStaticMarkup(<CronJobSchedulingContext evidence={{ ...report, state: 'history_incomplete', history_incomplete: true }} onViewTimeline={() => {}} />)
    expect(html).toContain('Scheduling history is incomplete')
    expect(html).toContain('cannot establish whether it still applies')
  })
  it('provides the report date and existing timeline navigation in expanded evidence', () => {
    const html = renderToStaticMarkup(<CronJobSchedulingDetails evidence={report} onViewTimeline={() => {}} />)
    expect(html).toContain('Last reported')
    expect(html).toContain('View timeline')
  })
})
