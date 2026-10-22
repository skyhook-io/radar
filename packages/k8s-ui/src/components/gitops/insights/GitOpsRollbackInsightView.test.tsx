import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { GitOpsInsight } from '../../../types'
import { GitOpsChangesView, GitOpsIssuesBand } from './GitOpsInsightViews'

function insight(rolledBack: boolean): GitOpsInsight {
  return {
    summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'demo', autoSyncMode: 'Manual', operationPhase: 'Succeeded' },
    issues: rolledBack ? [{
      reason: 'RolledBack', scope: 'operation', severity: 'info',
      message: `Last rollback deployed ${'a'.repeat(40)} by alice.`,
      cause: "Auto-sync is off, so Argo won't deploy a new revision on its own. Changes made since the rollback can also show as drift. Syncing or enabling auto-sync deploys the current revision and can undo the rollback.",
    }] : [],
    changes: [{
      ref: { group: 'apps', kind: 'Deployment', namespace: 'default', name: 'demo' },
      sync: 'OutOfSync', category: 'OutOfSync', hasDesired: true, hasLive: true, partial: false,
    }],
  }
}

describe('rollback insights', () => {
  it('puts the consequence on a separate line from the full revision', () => {
    const html = renderToString(<GitOpsIssuesBand issues={insight(true).issues} />)
    expect(html).toContain('a'.repeat(40))
    expect(html).toMatch(/<span class="[^"]*whitespace-normal break-words">Last rollback deployed/)
    expect(html).toMatch(/<p class="[^"]*whitespace-normal break-words">Auto-sync is off/)
    expect(html).toContain('Changes made since the rollback can also show as drift.')
  })

  it('shows the mismatch revisions, compatibility explanation, and recovery advice in full', () => {
    const message = `Last rollback deployed different sources or revisions than requested: ${'b'.repeat(40)} instead of ${'a'.repeat(40)}.`
    const cause = "Argo CD 2.11 can ignore a multi-source history entry's sources and revisions and sync the current ones."
    const action = 'Check the Argo CD controller version, then sync the intended revision manually.'
    const html = renderToString(<GitOpsIssuesBand issues={[{
      reason: 'RollbackRevisionMismatch', scope: 'operation', severity: 'warning', message, cause, action,
    }]} />)
    expect(html).toMatch(/<span class="[^"]*whitespace-normal break-words">Last rollback deployed/)
    expect(html).toContain(message)
    expect(html).toMatch(/<p class="[^"]*whitespace-normal break-words">Argo CD 2.11/)
    expect(html).toContain(action)
  })

  it('explains that resource Sync can undo the rollback', () => {
    const html = renderToString(<GitOpsChangesView insight={insight(true)} />)
    expect(html).toContain('Live state differs from Git after a rollback. Sync applies the desired state and can undo the rollback.')
    expect(html).not.toContain('Click Sync to apply the desired state.')
  })

  it('keeps ordinary manual-drift resource copy unchanged', () => {
    expect(renderToString(<GitOpsChangesView insight={insight(false)} />)).toContain('Live state differs from Git. Click Sync to apply the desired state.')
  })
})
