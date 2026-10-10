import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsIssuesBand } from './GitOpsInsightViews'
import type { GitOpsIssue } from '../../../types/gitops-insights'

const guard: GitOpsIssue = {
  severity: 'critical',
  scope: 'condition',
  reason: 'AutoSyncBlockedEmpty',
  message: 'Auto-sync blocked: the rendered desired state is empty',
  rawMessage: 'Skipping sync attempt to [8088f4c]: auto-sync will wipe out all resources',
  cause: 'Argo CD refuses to auto-sync because syncing to an empty desired state would prune every managed resource.',
  action: 'First confirm intent: a manual sync with pruning would delete every managed resource.',
}

// The band's top row is the most severe issue. Its next step must be visible
// without expanding, or a lower row's "click Sync" is the only advice on screen.
describe('GitOpsIssuesBand headline', () => {
  it('shows the top issue’s next step when it stands alone', () => {
    const html = renderToString(<GitOpsIssuesBand issues={[guard]} />)
    expect(html).toContain(guard.action)
  })

  it('shows the top issue’s next step above a collapsed stack', () => {
    const drift: GitOpsIssue = {
      severity: 'warning',
      scope: 'operation',
      reason: 'OutOfSync',
      message: 'Application is OutOfSync.',
      action: 'Review the drift.',
    }
    const html = renderToString(<GitOpsIssuesBand issues={[guard, drift]} />)
    expect(html).toContain(guard.action)
    expect(html.indexOf(guard.action!)).toBeLessThan(html.indexOf(drift.action!))
  })

  it('renders no next-step line when the top issue has none', () => {
    const html = renderToString(
      <GitOpsIssuesBand issues={[{ severity: 'warning', scope: 'condition', reason: 'X', message: 'only a message' }]} />,
    )
    expect(html).toContain('only a message')
    expect(html.match(/pl-\[22px\]/g)).toBeNull()
  })
})
