import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { AuditAlerts, type AuditFinding } from './AuditAlerts'
import { AuditCard } from './AuditCard'
import { AuditFindingsTable } from './AuditFindingsTable'

const findings: AuditFinding[] = [
  {
    kind: 'Deployment',
    namespace: 'prod',
    name: 'api',
    checkID: 'privileged',
    category: 'Security',
    severity: 'danger',
    message: 'Container is privileged',
  },
  {
    kind: 'Deployment',
    namespace: 'prod',
    name: 'api',
    checkID: 'cpuRequestMissing',
    category: 'Efficiency',
    severity: 'warning',
    message: 'CPU request is missing',
  },
]

describe('legacy audit severity presentation', () => {
  it('labels raw summary counts as High and Medium', () => {
    const html = renderToString(
      <AuditCard
        data={{
          passing: 3,
          danger: 1,
          warning: 2,
          categories: {
            Security: { passing: 1, danger: 1, warning: 0 },
            Efficiency: { passing: 2, danger: 0, warning: 2 },
          },
        }}
        onNavigate={() => {}}
      />,
    )
    expect(html).toMatch(/1(?:<!-- -->)? high/)
    expect(html).toMatch(/2(?:<!-- -->)? medium/)
    expect(html).toContain('bg-orange-500/10')
    expect(html).not.toContain('critical')
  })

  it('uses High and Medium in resource audit summaries', () => {
    const html = renderToString(<AuditAlerts findings={findings} />)
    expect(html).toMatch(/1(?:<!-- -->)? high/)
    expect(html).toMatch(/1(?:<!-- -->)? medium/)
    expect(html).not.toContain('critical')
  })

  it('uses High and Medium in audit totals and filters', () => {
    const html = renderToString(<AuditFindingsTable findings={findings} />)
    expect(html).toContain('High')
    expect(html).toContain('Medium')
    expect(html).not.toContain('Critical')
    expect(html).not.toContain('Warning')
  })
})
