import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsDetailLayout, type GitOpsDetailLayoutProps } from './GitOpsDetailLayout'

const noop = () => {}

const shell: Omit<GitOpsDetailLayoutProps, 'identity' | 'isArgoApp'> = {
  status: null,
  terminating: false,
  terminatingChipTooltip: '',
  terminatingActionTooltip: '',
  insight: null,
  insightLoading: false,
  isFlux: false,
  isFluxWorkload: false,
  activeTab: 'topology',
  onTabChange: noop,
  fullscreen: false,
  onToggleFullscreen: noop,
  renderTabBody: () => null,
  onNavigateRoot: noop,
}

describe('GitOpsDetailLayout', () => {
  it('shows the tool chip, facts and Argo operations for an Argo CD Application', () => {
    const html = renderToString(
      <GitOpsDetailLayout
        {...shell}
        identity={{ kind: 'applications', group: 'argoproj.io', namespace: 'shop', name: 'wordpress', toolLabel: 'ArgoCD', kindLabel: 'Application' }}
        detail={{ project: 'default', destination: 'in-cluster' }}
        isArgoApp
        argo={{
          onSyncRequested: noop, onRefresh: noop, onTerminate: noop, onSuspend: noop, onResume: noop,
          syncing: false, refreshing: false, refreshingKind: 'normal', terminating: false, suspending: false, resuming: false,
          isRunning: false, autoSyncEnabled: false,
        }}
      />,
    )
    expect(html).toContain('ArgoCD')
    expect(html).toContain('Project')
    expect(html).toContain('Sync…')
  })

  // A root that is not a GitOps resource gets one state: its name and why
  // there is no GitOps view, with no tool label, facts or operations.
  it('shows only the refusal for a root that is not a GitOps resource', () => {
    const message = 'Application.app.k8s.io shop/wordpress is not a supported Argo CD or Flux resource, so it has no GitOps view'
    const html = renderToString(
      <GitOpsDetailLayout
        {...shell}
        identity={{ kind: 'applications', group: 'app.k8s.io', namespace: 'shop', name: 'wordpress', kindLabel: 'Application' }}
        isArgoApp={false}
        resourceError={new Error(message)}
      />,
    )
    expect(html).toContain('wordpress')
    expect(html).toContain(message)
    expect(html).not.toContain('ArgoCD')
    expect(html).not.toContain('FluxCD')
    expect(html).not.toContain('Project')
    expect(html).not.toContain('Sync…')
    expect(html).not.toContain('Reconcile')
  })
})
