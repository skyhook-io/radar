import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/telemetry'
import { exampleReport, exampleStatus } from '../../api/telemetry.fixtures'

vi.mock('../../api/telemetry', () => ({
  useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }),
}))

import { UsageDataAsk } from './UsageDataAsk'

function status(patch: Partial<UsageDataStatus>): UsageDataStatus {
  return exampleStatus({ preview: exampleReport({ views: {} }), ...patch })
}

const render = (s: UsageDataStatus | undefined) =>
  renderToString(<UsageDataAsk usageData={s} onReadMore={() => {}} />)

describe('UsageDataAsk', () => {
  it('asks an undecided user who can decide', () => {
    const html = render(status({}))
    expect(html).toContain('Help improve Radar')
    expect(html).toContain('Send anonymous usage stats.')
    expect(html).toContain('Read more')
    expect(html).toContain('Send usage stats')
  })

  it('tells a shared Radar\'s users the choice covers the team', () => {
    expect(render(status({ shared: true }))).toContain('from this shared Radar')
    expect(render(status({}))).not.toContain('shared Radar')
  })

  it('stays out of the way once answered or where the viewer cannot decide', () => {
    expect(render(status({ state: 'on', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'deployment', canChange: false }))).toBe('')
    expect(render(status({ state: 'off', source: 'do-not-track', canChange: false }))).toBe('')
    expect(render(undefined)).toBe('')
  })
})
