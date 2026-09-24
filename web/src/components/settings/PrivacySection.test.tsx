import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/telemetry'
import { exampleReport, exampleStatus } from '../../api/telemetry.fixtures'

let current: UsageDataStatus

vi.mock('../../api/telemetry', async (orig) => ({
  ...(await orig<typeof import('../../api/telemetry')>()),
  useUsageData: () => ({ data: current, isLoading: false, error: null }),
  useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }),
}))

import { PrivacySection } from './PrivacySection'

function make(patch: Partial<UsageDataStatus>): UsageDataStatus {
  return exampleStatus({ preview: exampleReport({ views: { topology: 3 } }), ...patch })
}

function render(status: UsageDataStatus) {
  current = status
  return renderToString(
    <QueryClientProvider client={new QueryClient()}>
      <PrivacySection active />
    </QueryClientProvider>,
  )
}

describe('PrivacySection', () => {
  it('shows an example report and promises nothing is sent while off', () => {
    const html = render(make({}))
    expect(html).toContain('What a report looks like')
    expect(html).toContain('Nothing is recorded or sent while this is off.')
    expect(html).toContain('aria-checked="false"')
  })

  it('shows the pending report and where it lives once on', () => {
    const html = render(make({ state: 'on', source: 'user', nextReportAt: '2026-09-30T12:00:00Z' }))
    expect(html).toContain('Next report')
    expect(html).toContain('usage-report.json')
    expect(html).toContain('&quot;topology&quot;: 3')
  })

  it('names the control that fixed the choice', () => {
    expect(render(make({ state: 'off', source: 'do-not-track', canChange: false }))).toContain('DO_NOT_TRACK is set')
    expect(render(make({ state: 'off', source: 'deployment', canChange: false }))).toContain('Radar Cloud manages')
    expect(render(make({ state: 'on', source: 'env', canChange: false, shared: true }))).toContain('Helm value telemetry.enabled')
    expect(render(make({ state: 'log', source: 'env', canChange: false }))).toContain('RADAR_TELEMETRY=log')
  })
})

describe('PrivacySection intro', () => {
  it('does not claim data stays on this machine for an in-cluster install', () => {
    const base = make({ state: 'off', source: 'deployment', canChange: false })
    const html = render({ ...base, preview: { ...base.preview, mode: 'in-cluster' } })
    expect(html).toContain('from inside it')
    expect(html).not.toContain('from this machine')
  })
})

describe('PrivacySection on a shared Radar', () => {
  it('says the switch is the team\'s and who last set it', () => {
    const html = render(make({ state: 'on', source: 'user', shared: true, decidedBy: 'dana@example.com', decidedAt: '2026-09-24T09:00:00Z' }))
    expect(html).toContain('applies to everyone who uses it')
    expect(html).toContain('Turned on by dana@example.com')
  })
})

describe('PrivacySection storage and log wording', () => {
  it('warns a shared Radar when the choice only lives in the pod', () => {
    const html = render(make({ state: 'on', source: 'user', shared: true, choiceStorage: 'pod' }))
    expect(html).toContain('a restart resets it')
    expect(render(make({ state: 'on', source: 'user', shared: true, choiceStorage: 'cluster' }))).not.toContain('a restart resets it')
  })

  it('says logs, not sends, for development builds and log mode', () => {
    const html = render(make({ state: 'on', source: 'user', developmentBuild: true, nextReportAt: '2026-09-25T12:00:00Z' }))
    expect(html).toContain('Logs')
    expect(html).toContain('written to the log')
    expect(render(make({ state: 'on', source: 'user', nextReportAt: '2026-09-25T12:00:00Z' }))).toContain('until it is <!-- -->sent')
  })
})
