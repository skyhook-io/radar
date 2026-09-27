import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/usage-data'
import { exampleReport, exampleStatus } from '../../api/usage-data.fixtures'

vi.mock('../../api/usage-data', () => ({
  useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }),
  useMarkUsagePromptShown: () => vi.fn(),
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
    expect(html).toContain('Anonymous, with no third-party trackers.')
    expect(html).toContain('See what')
    expect(html).toContain('Send usage stats')
  })

  it('stays out of the way once answered or where the viewer cannot decide', () => {
    expect(render(status({ state: 'on', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'deployment', canChange: false }))).toBe('')
    expect(render(status({ state: 'off', source: 'env', canChange: false }))).toBe('')
    expect(render(status({ shared: true, canChange: false, ask: false }))).toBe('')
    expect(render(undefined)).toBe('')
  })

  it('waits when the server says it asked recently', () => {
    expect(render(status({ ask: false }))).toBe('')
  })
})
