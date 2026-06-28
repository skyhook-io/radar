import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ProblemAlerts, OperationalIssuesShownContext } from './drawer-components'

const problems = [
  { color: 'red' as const, message: 'Application is Degraded' },
  { color: 'yellow' as const, message: 'Application is OutOfSync' },
]

describe('ProblemAlerts', () => {
  it('renders every problem', () => {
    const html = renderToString(<ProblemAlerts problems={problems} />)
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })

  it('renders nothing when there are no problems', () => {
    expect(renderToString(<ProblemAlerts problems={[]} />)).toBe('')
  })

  // Regression guard: ProblemAlerts is used only by GitOps renderers, whose
  // problems the live-Issues pipeline does not comprehensively emit. It must NOT
  // suppress itself under the Operational-Issues context — doing so hid real
  // GitOps warnings (e.g. a manual Argo app's OutOfSync). Pod/Workload renderers
  // self-gate their own arrays; this component never should.
  it('still renders under OperationalIssuesShownContext (does not self-suppress)', () => {
    const html = renderToString(
      <OperationalIssuesShownContext.Provider value={true}>
        <ProblemAlerts problems={problems} />
      </OperationalIssuesShownContext.Provider>
    )
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })
})
