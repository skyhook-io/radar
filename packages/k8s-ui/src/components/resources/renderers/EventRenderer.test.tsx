import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { EventRenderer } from './EventRenderer'

describe('EventRenderer', () => {
  it.each([
    ['Warning', 'status-degraded'],
    ['Normal', 'status-neutral'],
  ])('uses the theme-aware status tone for %s events', (type, statusClass) => {
    const html = renderToString(<EventRenderer data={{
      type,
      reason: 'FailedScheduling',
      message: 'No nodes are available',
    }} />)

    expect(html).toContain(statusClass)
    expect(html).toContain('FailedScheduling')
    expect(html).toContain('No nodes are available')
    expect(html).not.toMatch(/text-(?:amber|blue)-(?:200|300|400)/)
    expect(html).not.toContain('opacity-')
  })
})
