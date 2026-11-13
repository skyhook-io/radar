import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { K3sHelmChartConfigRenderer, K3sHelmChartRenderer } from './K3sHelmRenderers'

const text = (html: string) => html.replace(/<!-- -->/g, '')
const spec = { valuesContent: 'image: x\npassword: SECRET\n', set: { 'auth.token': 'SECRET' }, failurePolicy: 'reinstall' }

describe('k3s Helm drawers', () => {
  it('never renders values content or set values', () => {
    const html = text(renderToString(<K3sHelmChartRenderer data={{
      apiVersion: 'helm.cattle.io/v1', kind: 'HelmChart',
      metadata: { name: 'traefik', namespace: 'kube-system', annotations: { 'helmcharts.cattle.io/managed-by': 'helm-controller' } },
      spec: { chart: 'traefik', ...spec },
      status: { jobName: 'helm-install-traefik', conditions: [{ type: 'JobCreated', status: 'True' }] },
    }} />))
    expect(html).not.toContain('SECRET')
    expect(html).not.toContain('password')
    expect(html).toContain('2 lines')
    expect(html).toContain('auth.token')
    expect(html).toContain('helm-controller')
    const config = text(renderToString(<K3sHelmChartConfigRenderer data={{ apiVersion: 'helm.cattle.io/v1', kind: 'HelmChartConfig', metadata: { name: 'traefik', namespace: 'kube-system' }, spec }} />))
    expect(config).not.toContain('SECRET')
    expect(config).toContain('2 lines')
    expect(config).toContain('reinstall')
  })

  it('links the install Job and the HelmChartConfig target', () => {
    const refs: any[] = []
    const onNavigate = (r: any) => refs.push(r)
    const html = text(renderToString(<K3sHelmChartRenderer onNavigate={onNavigate} onOpenHelmRelease={() => {}} data={{
      apiVersion: 'helm.cattle.io/v1', kind: 'HelmChart', metadata: { name: 'traefik', namespace: 'kube-system' },
      spec: { chart: 'traefik' }, status: { jobName: 'helm-install-traefik' },
    }} />))
    expect(html).toContain('helm-install-traefik')
    expect(html).toContain('Open Helm release')
    const config = text(renderToString(<K3sHelmChartConfigRenderer onNavigate={onNavigate} data={{ apiVersion: 'helm.cattle.io/v1', kind: 'HelmChartConfig', metadata: { name: 'traefik', namespace: 'kube-system' }, spec: {} }} />))
    expect(config).toContain('Target HelmChart')
    expect(config).toContain('traefik')
  })
})
