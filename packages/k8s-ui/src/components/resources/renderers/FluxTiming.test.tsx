import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { KustomizationRenderer } from './KustomizationRenderer'
import { FluxHelmReleaseRenderer } from './FluxHelmReleaseRenderer'
import { GitRepositoryRenderer } from './GitRepositoryRenderer'
import { OCIRepositoryRenderer } from './OCIRepositoryRenderer'
import { HelmRepositoryRenderer } from './HelmRepositoryRenderer'
import { FluxHelmReleaseCell } from './flux-cells'
import { ArgoApplicationRenderer } from './ArgoApplicationRenderer'

const marker = '2026-10-07T09:00:00Z'
const base = { metadata: { name: 'checkout', namespace: 'flux-system' }, spec: { interval: '10m' }, status: { lastHandledReconcileAt: marker, conditions: [] } }
const helm = {
  ...base, apiVersion: 'helm.toolkit.fluxcd.io/v2', kind: 'HelmRelease',
  spec: { ...base.spec, chartRef: { kind: 'OCIRepository', name: 'charts' } },
  status: { ...base.status, history: [
    { version: 5, chartVersion: '1.3.0', status: 'failed', lastDeployed: '2026-10-30T20:59:00Z' },
    { version: 4, chartVersion: '1.2.0', status: 'deployed', lastDeployed: '2026-10-27T21:00:00Z' },
  ] },
}

describe('Flux drawer and resource timing', () => {
  it('never labels a request marker as reconciliation time in any Flux drawer', () => {
    const html = [
      renderToString(<KustomizationRenderer data={{ ...base, apiVersion: 'kustomize.toolkit.fluxcd.io/v1', kind: 'Kustomization' }} />),
      renderToString(<FluxHelmReleaseRenderer data={helm} />),
      renderToString(<GitRepositoryRenderer data={base} />),
      renderToString(<OCIRepositoryRenderer data={base} />),
      renderToString(<HelmRepositoryRenderer data={base} />),
    ].join('')
    expect(html).not.toContain('Last Reconciled')
    expect(html).not.toContain(marker)
    expect(html).toContain('10m')
  })

  it('shows the deployed snapshot in the drawer and resource cells after a failed upgrade', () => {
    const drawer = renderToString(<FluxHelmReleaseRenderer data={helm} />)
    const current = drawer.slice(drawer.indexOf('Current Release'), drawer.indexOf('History ('))
    expect(current).toContain('1.2.0')
    expect(current).not.toContain('1.3.0')
    expect(renderToString(<FluxHelmReleaseCell resource={helm} column="revision" />)).toContain('#4')
    expect(renderToString(<FluxHelmReleaseCell resource={helm} column="version" />)).toContain('1.2.0')
    expect(renderToString(<FluxHelmReleaseCell resource={{ ...helm, status: {} }} column="version" />)).not.toContain('*')
  })

  it('preserves a failed first install as history and shows Paused for suspended Flux', () => {
    const failed = { ...helm, status: { ...helm.status, history: [helm.status.history[0]] } }
    const html = renderToString(<FluxHelmReleaseRenderer data={failed} />)
    expect(html).toContain('History (1 revision)')
    expect(html).toContain('1.3.0')
    expect(html).not.toContain('Current Release')
    const history = html.match(/<button\b[\s\S]*?<\/button>/g)?.find(button => button.includes('History (1 revision)'))
    expect(history).toContain('aria-expanded="true"')
    const deployed = renderToString(<FluxHelmReleaseRenderer data={helm} />)
    const deployedHistory = deployed.match(/<button\b[\s\S]*?<\/button>/g)?.find(button => button.includes('History (2 revisions)'))
    expect(deployedHistory).toContain('aria-expanded="false"')
    expect(renderToString(<FluxHelmReleaseRenderer data={{ ...helm, spec: { ...helm.spec, suspend: true } }} />)).toContain('Paused')
    expect(renderToString(<KustomizationRenderer data={{ ...base, spec: { ...base.spec, suspend: true } }} />)).toContain('Paused')
  })

  it('does not invent an Argo reconciliation interval from the last comparison', () => {
    const html = renderToString(<ArgoApplicationRenderer data={{ ...base, spec: { source: {}, destination: {}, syncPolicy: { automated: {} } }, status: { sync: { status: 'Synced' }, health: { status: 'Healthy' }, reconciledAt: '2026-10-30T20:55:00Z' } }} />)
    expect(html).not.toContain('Every 5m')
    expect(html).toContain('Last checked')
    expect(html).not.toContain('Last Reconciled')
  })
})
