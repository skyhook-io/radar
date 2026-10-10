// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsDetailLayout, type GitOpsDetailLayoutProps } from './GitOpsDetailLayout'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'

const noop = () => {}
const reason = "Your role can't patch Argo CD Applications in argocd."
const argoDenial = { allowed: false, verb: 'patch', resource: 'applications', group: 'argoproj.io', namespace: 'argocd' }
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
  const doc = new DOMParser().parseFromString(html, 'text/html')
  return [...doc.querySelectorAll('button')].find(button => button.textContent?.includes(label))?.outerHTML || ''
}

describe('GitOps permission gates on rendered controls', () => {
  it('disables refresh and sync while retaining buttons on the detail page', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} actionDisabledReasons={{ sync: reason, refresh: reason, suspend: reason }} actionPermissions={{ sync: argoDenial, refresh: argoDenial, suspend: argoDenial }} />)
    expect(html).toContain('Some actions restricted for your role')
    expect(html).toContain('Action permissions')
    expect(html).not.toContain('cloud.defaultRbac.gitopsActions')
    expect(html).not.toContain('Chart RBAC settings')
    expect(html).toContain('a RoleBinding')
    expect(html).toContain('applications.argoproj.io')
    expect(html).not.toContain('Sync with source also needs')
    for (const label of ['Sync…', 'Refresh', 'Hard refresh', 'Disable auto-sync']) {
      expect(actionButton(html, label)).toContain('disabled=""')
    }
  })
  it('shows the Cloud chart guidance only when the host identifies a Cloud deployment', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} isCloudDeployment actionDisabledReasons={{ refresh: reason }} actionPermissions={{ refresh: argoDenial }} />)
    expect(html).toContain('cloud.defaultRbac.gitopsActions')
    expect(html).toContain('Chart RBAC settings')
    expect(html).toContain('a RoleBinding')
  })
  it('names only the denied cross-namespace source grant for partially allowed Flux actions', () => {
    const html = renderToString(<GitOpsDetailLayout {...base}
      identity={{ ...base.identity, kind: 'kustomizations', group: 'kustomize.toolkit.fluxcd.io', namespace: 'gaps-r4-demo' }}
      isArgoApp={false} isFlux isFluxWorkload
      actionDisabledReasons={{ 'sync-with-source': "Your role can't patch the source." }}
      actionPermissions={{ reconcile: { allowed: true }, 'sync-with-source': {
        allowed: false, verb: 'patch', resource: 'gitrepositories', group: 'source.toolkit.fluxcd.io', namespace: 'gaps-r4-source',
      } }}
    />)
    const grants = new DOMParser().parseFromString(html, 'text/html').querySelector('ul')!.textContent!
    expect(grants).toContain('patch gitrepositories.source.toolkit.fluxcd.io in gaps-r4-source')
    expect(grants).not.toContain('kustomizations')
    expect(grants).not.toContain('gaps-r4-demo')
    expect(html).toContain('class="whitespace-nowrap">gaps-r4-source</span>')
  })
  it('deduplicates permission tuples across actions, lists distinct verbs and scopes, and bounds the list', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} actionDisabledReasons={{ sync: reason }} actionPermissions={{
      sync: argoDenial, refresh: argoDenial,
      suspend: { ...argoDenial, verb: 'get' },
      resume: { ...argoDenial, namespace: 'other' },
      rollback: { ...argoDenial, group: 'other.io' },
      validate: { ...argoDenial, resource: 'otherresources' },
      reconcile: { allowed: true }, unknown: {},
    }} />)
    const list = new DOMParser().parseFromString(html, 'text/html').querySelector('ul')!
    expect(list.querySelectorAll('li')).toHaveLength(5)
    const text = list.textContent!
    expect(text.match(/patch applications.argoproj.io in argocd/g)).toHaveLength(1)
    expect(text).toContain('get applications.argoproj.io in argocd')
    expect(text).toContain('patch applications.argoproj.io in other')
    expect(text).toContain('patch applications.other.io in argocd')
    expect(text).toContain('And 1 more denied permission.')
    expect(text).not.toContain('otherresources')
    expect(text).not.toContain('undefined')
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
      expect(actionButton(html, label)).toContain('aria-disabled="true"')
    }
  })
})
