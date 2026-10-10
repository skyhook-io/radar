// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsDetailLayout, type GitOpsDetailLayoutProps } from './GitOpsDetailLayout'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'

const noop = () => {}
const reason = "Your role can't patch Argo CD Application demo in argocd."
const appPatch = { verb: 'patch', resource: 'applications', group: 'argoproj.io', namespace: 'argocd', kind: 'Application', name: 'demo' }
const argoDenial = { allowed: false, denied: [appPatch] }
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
    const html = renderToString(<GitOpsDetailLayout {...base} actionDisabledReasons={{ sync: reason, refresh: reason, suspend: reason }} actionPermissions={{ sync: argoDenial, refresh: argoDenial, suspend: argoDenial, validate: { allowed: true } }} />)
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
  it('names only the denied cross-namespace source grants for partially allowed Flux actions', () => {
    const source = { resource: 'gitrepositories', group: 'source.toolkit.fluxcd.io', namespace: 'gaps-r4-source', kind: 'GitRepository', name: 'repo', source: true }
    const html = renderToString(<GitOpsDetailLayout {...base}
      identity={{ ...base.identity, kind: 'kustomizations', group: 'kustomize.toolkit.fluxcd.io', namespace: 'gaps-r4-demo' }}
      isArgoApp={false} isFlux isFluxWorkload
      actionDisabledReasons={{ 'sync-with-source': 'per-action reason' }}
      actionPermissions={{ reconcile: { allowed: true }, 'sync-with-source': { allowed: false, denied: [{ ...source, verb: 'get' }, { ...source, verb: 'patch' }] } }}
    />)
    const doc = new DOMParser().parseFromString(html, 'text/html')
    const grants = [...doc.querySelectorAll('li')].map(item => item.textContent!.trim())
    expect(grants).toEqual(['get gitrepositories.source.toolkit.fluxcd.io in gaps-r4-source', 'patch gitrepositories.source.toolkit.fluxcd.io in gaps-r4-source'])
    expect(html).toContain('Some actions restricted for your role')
    expect(doc.body.textContent).toContain("Sync with source also reconciles GitRepository gaps-r4-source/repo — your role can't read or patch it.")
    expect(html).not.toContain('per-action reason')
    expect(html).toContain('class="whitespace-nowrap">gaps-r4-source</span>')
  })
  it('merges every denied operation into one sentence and says when all actions are restricted', () => {
    const appGet = { ...appPatch, verb: 'get' }
    const both = { allowed: false, denied: [appGet, appPatch] }
    const html = renderToString(<GitOpsDetailLayout {...base}
      actionDisabledReasons={{ refresh: reason, sync: 'sync reason', suspend: 'suspend reason' }}
      actionPermissions={{ refresh: argoDenial, sync: both, suspend: both }} />)
    const doc = new DOMParser().parseFromString(html, 'text/html')
    expect(doc.body.textContent).toContain('Actions restricted for your role')
    expect(doc.body.textContent).not.toContain('Some actions restricted')
    expect(doc.body.textContent).toContain("Your role can't read or patch Argo CD Application demo in argocd (Radar reads it with its own access).")
    expect(html).not.toContain('sync reason')
    expect([...doc.querySelectorAll('li')].map(item => item.textContent!.trim())).toEqual(['patch applications.argoproj.io in argocd', 'get applications.argoproj.io in argocd'])
  })
  it('keeps an unsupported action out of the role restrictions', () => {
    const html = renderToString(<GitOpsDetailLayout {...base}
      identity={{ ...base.identity, kind: 'helmreleases', group: 'helm.toolkit.fluxcd.io', namespace: 'apps' }}
      isArgoApp={false} isFlux isFluxWorkload
      flux={{ onReconcile: noop, onSyncWithSource: noop, onSuspend: noop, onResume: noop, reconciling: false, syncingWithSource: false, suspending: false, resuming: false }}
      actionDisabledReasons={{ 'sync-with-source': 'Sync with source is unsupported here.' }}
      actionPermissions={{ reconcile: { allowed: true }, 'sync-with-source': { unsupported: true } }}
    />)
    expect(html).not.toContain('restricted for your role')
    expect(actionButton(html, 'Sync with source')).toContain('disabled=""')
  })
  it('deduplicates permission tuples across actions, lists distinct verbs and scopes, and bounds the list', () => {
    const html = renderToString(<GitOpsDetailLayout {...base} actionDisabledReasons={{ sync: reason }} actionPermissions={{
      sync: argoDenial, refresh: argoDenial,
      suspend: { allowed: false, denied: [{ ...appPatch, verb: 'get' }] },
      resume: { allowed: false, denied: [{ ...appPatch, namespace: 'other' }] },
      rollback: { allowed: false, denied: [{ ...appPatch, group: 'other.io' }] },
      validate: { allowed: false, denied: [{ ...appPatch, resource: 'otherresources' }] },
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
