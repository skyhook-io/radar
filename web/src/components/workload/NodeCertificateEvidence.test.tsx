import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { NodeCertificateContext, NodeCertificateDetails } from './NodeCertificateEvidence'
import type { NodeCertificateReport } from '../../api/client'

const report: NodeCertificateReport = {
  uid: 'current-node', event_id: 'certificate-event', state: 'reported',
  reason: 'CertificateExpirationWarning', message: 'Original node monitor warning',
  last_reported_at: '2026-10-31T08:00:00Z', expires_at: '2027-02-05T10:00:00Z',
}

describe('Node certificate reports', () => {
  it('shows a dated renewal report without inventing an operational issue', () => {
    const html = renderToStaticMarkup(<NodeCertificateContext evidence={report} hasIssue={false} onViewTimeline={() => {}} />)
    expect(html).toContain('Node certificate warning reported')
    expect(html).toContain('Last reported')
    expect(html).toContain('Reported expiry')
    expect(html).toContain(report.message)
    expect(html).toContain('View timeline')
    expect(html).not.toContain('API is down')
  })
  it('does not duplicate an urgent Issue or claim current health after an OK report', () => {
    expect(renderToStaticMarkup(<NodeCertificateContext evidence={report} hasIssue onViewTimeline={() => {}} />)).toBe('')
    expect(renderToStaticMarkup(<NodeCertificateContext evidence={{ ...report, state: 'ok' }} hasIssue={false} onViewTimeline={() => {}} />)).toBe('')
    const details = renderToStaticMarkup(<NodeCertificateDetails evidence={report} onViewTimeline={() => {}} includeOriginalReport={false} />)
    expect(details).toContain('Reported expiry')
    expect(details).not.toContain(report.message)
  })
  it('retains stale evidence with an explicit unverified state', () => {
    const html = renderToStaticMarkup(<NodeCertificateContext evidence={{ ...report, state: 'unverified' }} hasIssue={false} onViewTimeline={() => {}} />)
    expect(html).toContain('Node certificate status unverified')
    expect(html).toContain('current certificate state is unverified')
    expect(html).toContain(report.message)
    expect(html).not.toContain('renewed')
    expect(html).not.toContain('healthy')
  })
  it('explains history and parsing gaps', () => {
    const html = renderToStaticMarkup(<NodeCertificateDetails evidence={{ ...report, history_incomplete: true, validity_unknown: true }} onViewTimeline={() => {}} />)
    expect(html).toContain('a later report may be missing')
    expect(html).toContain('could not read the validity dates')
    const context = renderToStaticMarkup(<NodeCertificateContext evidence={{ ...report, validity_unknown: true }} hasIssue={false} onViewTimeline={() => {}} />)
    expect(context).toContain('Review the original monitor report')
    expect(context).not.toContain('Review the reported validity dates')
  })
})
