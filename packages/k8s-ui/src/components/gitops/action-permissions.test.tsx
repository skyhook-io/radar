import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsDetailLayout, type GitOpsDetailLayoutProps } from './GitOpsDetailLayout'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'

const noop = () => {}
const reason = "Your role can't patch Argo CD Applications in argocd."
const base: GitOpsDetailLayoutProps = {
  identity: { kind: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'demo', toolLabel: 'ArgoCD', kindLabel: 'Application' },
  status: { sync: 'Synced', health: 'Healthy', suspended: false },
  terminating: false, terminatingChipTooltip: 'Deleting', terminatingActionTooltip: 'Deleting',
  detail: {}, insight: null, insightLoading: false,
  isArgoApp: true, isFlux: false, isFluxWorkload: false,
  argo: { onSyncRequested: noop, onRefresh: noop, onTerminate: noop, onSuspend: noop, onResume: noop,
    syncing: false, refreshing: false, refreshingKind: 'normal', terminating: false,
    suspending: false, resuming: false, autoSyncEnabled: true, isRunning: false },
  activeTab: 'topology', onTabChange: noop, fullscreen: false, onToggleFullscreen: noop,
  renderTabBody: () => null, onNavigateRoot: noop,
}

function actionButton(html: string, label: string): string {
  return [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)].map(match => match[0]).find(button => button.includes(label)) || ''
}

describe('GitOps permission gates on rendered controls', () => {
  it('disables refresh and sync while retaining buttons on the detail page', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} actionDisabledReasons={{ sync: reason, refresh: reason, suspend: reason }} />)
    for (const label of ['Sync…', 'Refresh', 'Hard refresh', 'Disable auto-sync']) {
      expect(actionButton(html, label)).toContain('disabled=""')
    }
  })
  it('preserves enabled refresh during deletion for an allowed caller', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} terminating />)
    expect(actionButton(html, 'Sync…')).toContain('disabled=""')
    expect(actionButton(html, 'Refresh')).not.toContain('disabled=""')
  })
  it('gates compact resource controls with the same permission map', () => {
    const html = renderToString(<ResourceActionsBar
      resource={{ kind: 'applications', namespace: 'argocd', name: 'demo' }} data={{ spec: { syncPolicy: { automated: {} } } }}
      onArgoSync={noop} onArgoRefresh={noop} onArgoSuspend={noop}
      gitOpsActionDisabledReasons={{ sync: reason, refresh: reason, suspend: reason }}
    />)
    for (const label of ['Sync', 'Refresh', 'Suspend']) {
      expect(actionButton(html, label)).toContain('disabled=""')
    }
  })
})
