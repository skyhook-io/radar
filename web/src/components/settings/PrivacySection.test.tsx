import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/usage-data'
import { exampleReport, exampleStatus } from '../../api/usage-data.fixtures'

let current: UsageDataStatus

vi.mock('../../api/usage-data', async (orig) => ({
  ...(await orig<typeof import('../../api/usage-data')>()),
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
    expect(html).toContain('See an example report')
    expect(html).toContain('What isn')
    expect(html).not.toContain('usage-report.json')
    expect(html).toContain('aria-checked="false"')
  })

  it('shows the pending report and where it lives once on', () => {
    const html = render(make({ state: 'on', source: 'user', nextReportAt: '2026-09-30T12:00:00Z' }))
    expect(html).toContain('See the next report')
    expect(html).toContain('usage-report.json')
    expect(html).toContain('&quot;topology&quot;: 3')
  })

  it('names the control that fixed the choice', () => {
    expect(render(make({ state: 'off', source: 'deployment', canChange: false }))).toContain('Radar Cloud manages')
    const helm = make({ state: 'on', source: 'env', canChange: false, shared: true })
    expect(render({ ...helm, preview: { ...helm.preview, mode: 'in-cluster' } })).toContain('Helm value usageReporting.enabled')
    expect(render(make({ state: 'off', source: 'env', canChange: false }))).toContain('RADAR_USAGE_REPORTING=off')
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
  it('points at the Helm value while nothing set it', () => {
    const base = make({ shared: true, canChange: false })
    const html = render({ ...base, preview: { ...base.preview, mode: 'in-cluster' } })
    expect(html).toContain('Applies to everyone using this Radar.')
    expect(html).toContain('usageReporting.enabled')
    expect(html).toContain('aria-checked="false"')
  })

  it('uses the environment variable outside a cluster', () => {
    expect(render(make({ shared: true, canChange: false }))).toContain('RADAR_USAGE_REPORTING=on')
  })
})

describe('PrivacySection storage and log wording', () => {
  it('says logs, not sends, for development builds', () => {
    const html = render(make({ state: 'on', source: 'user', developmentBuild: true, nextReportAt: '2026-09-25T12:00:00Z' }))
    expect(html).toContain('logs')
    expect(html).toContain('reports go to the log, never sent')
    expect(render(make({ state: 'on', source: 'user', nextReportAt: '2026-09-25T12:00:00Z' }))).toContain('sends')
  })
})
