import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { NotCheckedBadge, notCheckedTooltip } from './NotCheckedBadge'

vi.mock('../../api/client', () => ({
  useRadarFeature: () => ({ support: 'supported' }),
}))

const { HelmSummary } = await import('../home/HelmSummary')

describe('the "not checked" mark on a release', () => {
  it('shows the count when objects were left out', () => {
    expect(renderToStaticMarkup(<NotCheckedBadge count={2} />)).toContain('2 objects not checked')
  })

  it('is absent when everything was checked', () => {
    expect(renderToStaticMarkup(<NotCheckedBadge count={0} />)).toBe('')
    expect(renderToStaticMarkup(<NotCheckedBadge />)).toBe('')
  })

  it('does not call an unanswered check a denial', () => {
    expect(notCheckedTooltip(1, 'unconfirmed')).not.toMatch(/can’t read/)
    expect(notCheckedTooltip(1, 'denied')).toMatch(/can’t read/)
    expect(notCheckedTooltip(2, 'either')).toMatch(/can’t read them, or Radar couldn’t confirm/)
  })

  it('separates unchecked object status from release health', () => {
    const reasons: Parameters<typeof notCheckedTooltip>[1][] = ['denied', 'either']
    for (const reason of reasons) {
      expect(notCheckedTooltip(2, reason)).toContain('Release health uses workload and Pod status.')
      expect(notCheckedTooltip(2, reason)).toContain('This count can include other objects.')
      expect(notCheckedTooltip(2, reason)).not.toContain('Health covers only')
    }
  })

  it('explains that a release badge counts only health-relevant objects', () => {
    expect(notCheckedTooltip(1, 'either', 'health')).toContain('workloads or Pods')
    expect(notCheckedTooltip(1, 'either', 'health')).not.toContain('can include other objects')
  })
})

describe('the Home Helm card', () => {
  const row = { namespace: 'team-a', chart: 'platform-app', chartVersion: '0.1.0', status: 'deployed' }

  it('marks only the release whose health left objects out', () => {
    const html = renderToStaticMarkup(
      <HelmSummary
        onNavigate={() => {}}
        data={{
          total: 2,
          releases: [
            { ...row, name: 'payments', resourceHealth: 'unknown', statusUnreadCount: 1 },
            { ...row, name: 'billing', resourceHealth: 'healthy' },
          ],
        }}
      />,
    )
    expect(html.match(/not checked/g)).toHaveLength(1)
    expect(html.indexOf('1 object not checked')).toBeGreaterThan(html.indexOf('payments'))
    expect(html.indexOf('1 object not checked')).toBeLessThan(html.indexOf('billing'))
  })
})
