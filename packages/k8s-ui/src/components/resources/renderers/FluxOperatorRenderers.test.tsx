import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { FluxInstanceRenderer, FluxReportRenderer, ResourceSetInputProviderRenderer, ResourceSetRenderer } from './FluxOperatorRenderers'

const text = (html: string) => html.replace(/<!-- -->/g, '')
const ready = { conditions: [{ type: 'Ready', status: 'True', reason: 'ReconciliationSucceeded', message: 'ok' }] }

describe('Flux Operator drawers', () => {
  it('FluxInstance: distribution, components, cluster, storage, history, inventory', () => {
    const html = text(renderToString(<FluxInstanceRenderer onNavigate={() => {}} data={{
      apiVersion: 'fluxcd.controlplane.io/v1', kind: 'FluxInstance', metadata: { name: 'flux', namespace: 'flux-system' },
      spec: { distribution: { version: '2.7.x', registry: 'ghcr.io/fluxcd' }, cluster: { type: 'kubernetes', multitenant: false, networkPolicy: true }, storage: { class: 'longhorn', size: '5Gi' } },
      status: { ...ready, lastAppliedRevision: 'v2.7.5@sha256:abc', components: [{ name: 'source-controller', tag: 'v1.7.4' }],
        history: [{ digest: 'sha256:04aade06d35135964597', lastReconciled: '2026-11-12T09:53:02Z', lastReconciledStatus: 'ReconciliationSucceeded', totalReconciliations: 83, metadata: { flux: 'v2.7.5' } }],
        inventory: { entries: [{ id: 'flux-system_source-controller_apps_Deployment', v: 'v1' }] } },
    }} />))
    for (const want of ['Distribution', '2.7.x', 'ghcr.io/fluxcd', 'v2.7.5@sha256:abc', 'source-controller', 'v1.7.4', 'kubernetes', 'longhorn', 'History', '83 runs', 'Inventory']) expect(html).toContain(want)
  })

  it('FluxReport: distribution, cluster, components and reconcilers', () => {
    const html = text(renderToString(<FluxReportRenderer data={{
      apiVersion: 'fluxcd.controlplane.io/v1', kind: 'FluxReport', metadata: { name: 'flux', namespace: 'flux-system' },
      spec: { distribution: { version: 'v2.7.5', status: 'Installed', managedBy: 'flux-operator' }, operator: { version: 'v0.62.1' },
        cluster: { serverVersion: 'v1.33.4+k3s1', platform: 'linux/amd64', nodes: 29 },
        components: [{ name: 'helm-controller', image: 'ghcr.io/fluxcd/helm-controller:v1.4.5@sha256:abc', ready: true, status: 'Current' }],
        reconcilers: [{ kind: 'HelmRelease', stats: { running: 143, failing: 1, suspended: 0, totalSize: '' } }, { kind: 'Bucket', stats: { running: 0, failing: 0, suspended: 0 } }] },
      status: ready,
    }} />))
    for (const want of ['v2.7.5', 'flux-operator', 'v0.62.1', 'v1.33.4+k3s1', 'helm-controller', 'v1.4.5', 'HelmRelease', '143', 'Reconcilers']) expect(html).toContain(want)
  })

  it('ResourceSet: inputs, providers, dependencies, inventory', () => {
    const html = text(renderToString(<ResourceSetRenderer onNavigate={() => {}} data={{
      apiVersion: 'fluxcd.controlplane.io/v1', kind: 'ResourceSet', metadata: { name: 'apps', namespace: 'flux-system' },
      spec: { inputs: [{ tenant: 'a' }, { tenant: 'b' }], inputsFrom: [{ kind: 'ResourceSetInputProvider', name: 'prs' }], dependsOn: [{ kind: 'Kustomization', name: 'infra' }] },
      status: { ...ready, inventory: { entries: [{ id: 'team_app_apps_Deployment', v: 'v1' }] } },
    }} />))
    for (const want of ['Inputs', '2 inline', 'prs', 'infra', 'Inventory']) expect(html).toContain(want)
  })

  it('ResourceSetInputProvider: type and a URL without credentials', () => {
    const html = text(renderToString(<ResourceSetInputProviderRenderer data={{
      apiVersion: 'fluxcd.controlplane.io/v1', kind: 'ResourceSetInputProvider', metadata: { name: 'prs', namespace: 'flux-system' },
      spec: { type: 'GitHubPullRequest', url: 'https://user:token@github.com/org/repo' }, status: ready,
    }} />))
    expect(html).toContain('GitHubPullRequest')
    expect(html).toContain('https://github.com/org/repo')
    expect(html).not.toContain('token')
  })
})
