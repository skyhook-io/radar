import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CNPGRefreshFailedNotice } from './shared'

describe('CNPGRefreshFailedNotice', () => {
  it('says a refresh failed and how old the data on screen is', () => {
    const html = renderToStaticMarkup(
      <CNPGRefreshFailedNotice queries={[{ isRefetchError: true, error: new Error('Gateway Timeout'), dataUpdatedAt: Date.now() - 5 * 60_000 }]} />,
    )
    expect(html).toContain('Last refresh failed: Gateway Timeout')
    expect(html).toContain('showing data from 5m ago')
  })
  it('names the oldest of several failed reads', () => {
    const html = renderToStaticMarkup(
      <CNPGRefreshFailedNotice
        queries={[
          { isRefetchError: true, error: new Error('recent'), dataUpdatedAt: Date.now() - 60_000 },
          { isRefetchError: true, error: new Error('older'), dataUpdatedAt: Date.now() - 3 * 3_600_000 },
          { isRefetchError: false, error: null, dataUpdatedAt: Date.now() - 10 * 3_600_000 },
        ]}
      />,
    )
    expect(html).toContain('older')
    expect(html).toContain('3h ago')
  })
  it('renders nothing while refreshes succeed', () => {
    expect(renderToStaticMarkup(<CNPGRefreshFailedNotice queries={[{ isRefetchError: false, error: null, dataUpdatedAt: Date.now() }]} />)).toBe('')
  })
})
