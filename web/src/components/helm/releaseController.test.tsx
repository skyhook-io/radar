import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ReleaseControllerBadge } from './releaseController'

const k3s = { tool: 'k3s', kind: 'HelmChart', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', valuesRef: { kind: 'HelmChartConfig', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', exists: false } }

describe('ReleaseControllerBadge', () => {
  it('on an observed row does not claim a values object is missing', () => {
    const html = renderToStaticMarkup(<ReleaseControllerBadge observed controller={k3s} />)
    expect(html).toContain('k3s')
  })
})
