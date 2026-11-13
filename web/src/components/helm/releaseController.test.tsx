import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ReleaseControllerBadge } from './releaseController'

const k3s = { tool: 'k3s', kind: 'HelmChart', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', valuesRef: { kind: 'HelmChartConfig', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', exists: undefined } }

describe('ReleaseControllerBadge', () => {
  it('labels the controller by its tool', () => {
    const html = renderToStaticMarkup(<ReleaseControllerBadge controller={k3s} />)
    expect(html).toContain('k3s')
  })
})
