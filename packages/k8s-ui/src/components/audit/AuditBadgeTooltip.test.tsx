import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { AuditBadgeTooltip } from './AuditBadgeTooltip'

const msgs = [
  { severity: 'warning', message: 'Service selector matches no pods' },
  { severity: 'danger', message: 'Ingress references missing Service' },
  { severity: 'warning', message: 'Uses a deprecated API version' },
  { severity: 'warning', message: 'A fourth finding' },
]

describe('AuditBadgeTooltip', () => {
  it('lists each finding message up to the cap', () => {
    const html = renderToString(<AuditBadgeTooltip messages={msgs.slice(0, 2)} />)
    expect(html).toContain('Service selector matches no pods')
    expect(html).toContain('Ingress references missing Service')
    expect(html).not.toContain('more')
  })

  it('caps the list and collapses the rest into "+N more"', () => {
    const html = renderToString(<AuditBadgeTooltip messages={msgs} max={3} />)
    expect(html).toContain('+1 more')
    expect(html).not.toContain('A fourth finding')
  })

  it('shows the click hint by default and omits it when disabled', () => {
    expect(renderToString(<AuditBadgeTooltip messages={msgs.slice(0, 1)} />)).toContain('Click to open')
    expect(renderToString(<AuditBadgeTooltip messages={msgs.slice(0, 1)} clickHint={false} />)).not.toContain('Click to open')
  })
})
