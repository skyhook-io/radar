import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsIssuesBand } from './GitOpsInsightViews'
import type { GitOpsIssue } from '../../../types/gitops-insights'
import { SEVERITY_BADGE } from '../../../utils/badge-colors'

const guard: GitOpsIssue = {
  severity: 'critical',
  scope: 'condition',
  reason: 'AutoSyncBlockedEmpty',
  message: 'Auto-sync blocked: the render includes none of the managed resources',
  rawMessage: 'Skipping sync attempt to [8088f4c]: auto-sync will wipe out all resources',
  cause: 'Argo CD blocked auto-sync because the render includes none of the resources this Application manages.',
  action: 'The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation.',
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

// Argo's namespace-not-found cause ends in its own sync advice.
const failedSync: GitOpsIssue = {
  severity: 'critical',
  scope: 'operation',
  reason: 'Failed',
  message: 'one or more objects failed to apply, reason: namespaces demo not found',
  cause: 'The destination namespace does not exist. Create it, or enable CreateNamespace=true in the syncOptions so Argo creates it on sync.',
  action: 'Open Activity for operation details.',
}
const causeAdvice = 'so Argo creates it on sync'
const emptyRenderHazard = 'The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation. Check the source path, targetRevision, Helm values, and Kustomize or directory include/exclude settings first.'
const emptyRenderReference = 'See the empty-render guard below before syncing.'

describe('GitOpsIssuesBand failure card', () => {
  it('shows the failure’s next step after its cause', () => {
    const html = renderToString(<GitOpsIssuesBand issues={[failedSync]} />)
    expect(html).toContain(failedSync.action)
    expect(html.indexOf(causeAdvice)).toBeLessThan(html.indexOf(failedSync.action!))
    expect(html).not.toContain(SEVERITY_BADGE.warning)
  })

  it('warns of a sync hazard before the cause’s own sync advice', () => {
    const hazard = emptyRenderHazard
    const html = renderToString(<GitOpsIssuesBand issues={[{ ...failedSync, hazard }]} />)
    const at = html.indexOf(hazard)
    expect(at).toBeGreaterThan(-1)
    expect(at).toBeLessThan(html.indexOf(causeAdvice))
    expect(html.lastIndexOf(SEVERITY_BADGE.warning, at)).toBeGreaterThan(-1)
    expect(html).toContain(failedSync.action)
    expect(html).not.toContain(emptyRenderReference)
  })

  it.each([false, true])('points to the empty-render guard in the issue band (terminating=%s)', (terminating) => {
    const html = renderToString(<GitOpsIssuesBand issues={[{ ...failedSync, hazard: emptyRenderHazard }, guard]} terminating={terminating} />)
    expect(html).toContain(emptyRenderReference)
    expect(html).not.toContain(emptyRenderHazard)
    expect(html).toContain(guard.message)
    expect(html).toContain(guard.action)
    expect(html.indexOf(emptyRenderReference)).toBeLessThan(html.indexOf(causeAdvice))
  })

  it('keeps the full hazard when the guard is behind a collapsed row', () => {
    const comparisonError: GitOpsIssue = { severity: 'critical', scope: 'condition', reason: 'ComparisonError', message: 'Cannot render source.' }
    const html = renderToString(<GitOpsIssuesBand issues={[{ ...failedSync, hazard: emptyRenderHazard }, comparisonError, guard]} />)
    expect(html).toContain(emptyRenderHazard)
    expect(html).not.toContain(emptyRenderReference)
  })

  it('keeps shared-ownership hazards unchanged', () => {
    const hazard = 'Resolve shared ownership before syncing: some resources are also tracked by Application payments. A successful sync can take over their tracking unless it uses FailOnSharedResource=true. While Application payments still declares them, its next successful sync can take tracking back.'
    const shared: GitOpsIssue = { severity: 'warning', scope: 'condition', reason: 'SharedResourceWarning', message: 'Shared resource' }
    const html = renderToString(<GitOpsIssuesBand issues={[{ ...failedSync, hazard }, shared]} />)
    expect(html).toContain(hazard)
    expect(html).not.toContain(emptyRenderReference)
  })

  it('shows a remote failure’s destination-cluster instruction', () => {
    const action = 'Create namespace demo on the destination cluster, or add the CreateNamespace=true sync option.'
    const html = renderToString(<GitOpsIssuesBand issues={[{ ...failedSync, action }]} />)
    expect(html).toContain(action)
  })
})
