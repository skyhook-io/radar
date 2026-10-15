import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { GitOpsInsight } from '../../../types'
import { GitOpsChangesView, GitOpsIssuesBand } from './GitOpsInsightViews'

function insight(rolledBack: boolean): GitOpsInsight {
  return {
    summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'demo', autoSyncMode: 'Manual', operationPhase: 'Succeeded' },
    issues: rolledBack ? [{
      reason: 'RolledBack', scope: 'operation', severity: 'info',
      message: `Rolled back to ${'a'.repeat(40)} by alice.`,
      cause: 'It stays on this revision with auto-sync off until you sync or turn auto-sync back on.',
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
    expect(html).toMatch(/<span class="[^"]*whitespace-normal break-words">Rolled back to/)
    expect(html).toMatch(/<p class="[^"]*whitespace-normal break-words">It stays on this revision with auto-sync off until you sync or turn auto-sync back on\.<\/p>/)
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
