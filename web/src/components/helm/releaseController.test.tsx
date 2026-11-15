import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { HiddenK3sBadge, ReleaseControllerBadge, managedByOtherController } from './releaseController'

const k3s = { tool: 'k3s', kind: 'HelmChart', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', valuesRef: { kind: 'HelmChartConfig', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', exists: undefined } }

describe('ReleaseControllerBadge', () => {
  it('labels the controller by its tool', () => {
    const html = renderToStaticMarkup(<ReleaseControllerBadge controller={k3s} />)
    expect(html).toContain('k3s')
  })
})

describe('managedByOtherController', () => {
  it('hides upgrade hints for a k3s release whose HelmChart is withheld, not for Flux or plain Helm', () => {
    expect(managedByOtherController({ managedByK3s: true })).toBe(true)
    expect(managedByOtherController({ controller: k3s })).toBe(true)
    expect(managedByOtherController({ managedByFluxHelmRelease: 'flux-system/app' })).toBe(false)
    expect(managedByOtherController({})).toBe(false)
  })
  it('the hidden k3s badge names no HelmChart', () => {
    const html = renderToStaticMarkup(<HiddenK3sBadge />)
    expect(html).toContain('k3s')
    expect(html).not.toContain('kube-system')
  })
})
