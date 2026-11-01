import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { GitOpsInsight } from '../../../types'
import type { RadarUpgradeRequirement } from '../../../types/fetch-error'
import { GitOpsActivityInsightView } from './GitOpsInsightViews'

function render(insight: GitOpsInsight, rollbackUpgrade?: RadarUpgradeRequirement | null): string {
  return renderToString(<GitOpsActivityInsightView insight={insight} onRollback={() => {}} rollbackUpgrade={rollbackUpgrade} />).replace(/<!-- -->/g, '')
}

function rollbackButtons(insight: GitOpsInsight, rollbackUpgrade?: RadarUpgradeRequirement | null): { disabled: boolean }[] {
  const html = render(insight, rollbackUpgrade)
  return [...html.matchAll(/<button([^>]*)>Rollback<\/button>/g)].map((m) => ({ disabled: /\sdisabled=""/.test(m[1]) }))
}

function insight(autoSyncMode: string): GitOpsInsight {
  return {
    summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'guestbook', autoSyncMode },
    capabilities: { sync: true, refresh: true, terminate: true, suspend: true, resume: true, syncWithSource: false, selectiveSync: true, rollback: true },
    history: [
      { phase: 'Succeeded', revision: 'eee555' },
      { id: '2', revision: 'ccc333', rollbackable: true },
      { id: '1', revision: 'bbb222', rollbackable: false },
      { id: '0', revision: 'aaa111' },
    ],
  }
}

describe('GitOpsActivityInsightView rollback', () => {
  // The rollback API refuses an entry with no source, so its button is
  // disabled; the operation row has no id and gets no button.
  it('disables Rollback only on entries the backend marks not rollbackable', () => {
    expect(rollbackButtons(insight('Manual'))).toEqual([{ disabled: false }, { disabled: true }, { disabled: false }])
  })

  it('disables every Rollback while auto-sync is on', () => {
    expect(rollbackButtons(insight('Auto · prune'))).toEqual([{ disabled: true }, { disabled: true }, { disabled: true }])
  })

  // An older Radar writes a rollback the API server drops (and refuses id 0),
  // so a newer UI disables every entry and says which Radar it needs.
  describe('on a Radar without argoRollbackSync', () => {
    const upgrade = { feature: 'Argo CD rollbacks', currentVersion: 'v1.16.3', latestVersion: 'v1.17.0' }

    it('disables every Rollback and shows the upgrade note', () => {
      expect(rollbackButtons(insight('Manual'), upgrade)).toEqual([{ disabled: true }, { disabled: true }, { disabled: true }])
      // RadarUpgradeNote sets the feature in its own <span>.
      expect(render(insight('Manual'), upgrade)).toContain('Argo CD rollbacks</span> need the latest Radar (v1.17) on this cluster. It runs v1.16.3.')
    })

    it('shows no note when the Radar advertises the behavior', () => {
      expect(render(insight('Manual'), null)).not.toContain('Argo CD rollbacks need')
    })
  })
})
