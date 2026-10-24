import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceActionsBar } from './ResourceActionsBar'

const noop = () => {}

function actions(kind: string, apiVersion: string) {
  return renderToString(
    <ResourceActionsBar
      resource={{ kind, name: 'example', namespace: 'test' }}
      data={{ apiVersion, metadata: { name: 'example', namespace: 'test' } }}
      onFluxReconcile={noop}
      onFluxSuspend={noop}
      onFluxResume={noop}
      onArgoSync={noop}
      onArgoRefresh={noop}
      onArgoSuspend={noop}
      onArgoResume={noop}
      canPortForward
      renderPortForward={() => <button>Port forward</button>}
    />,
  )
}

describe('resource actions use the loaded API group', () => {
  it.each(['app.k8s.io/v1beta1', 'notargoproj.io/v1alpha1', ''])('does not offer Argo actions for Application in %s', (apiVersion) => {
    const html = actions('applications', apiVersion)
    expect(html).not.toContain('>Sync<')
    expect(html).not.toContain('>Refresh<')
    expect(html).not.toContain('Enable Auto-Sync')
  })

  it('retains Argo actions for its exact Application group', () => {
    const html = actions('applications', 'argoproj.io/v1alpha1')
    expect(html).toContain('>Sync<')
    expect(html).toContain('>Refresh<')
    expect(html).toContain('Enable Auto-Sync')
  })

  it.each(['serving.knative.dev/v1', 'vendor.serving.knative.dev/v1', 'vendor.io/v1', ''])('does not port-forward a non-core Service in %s', (apiVersion) => {
    expect(actions('services', apiVersion)).not.toContain('Port forward')
  })

  it('retains port forwarding for core Service', () => {
    expect(actions('services', 'v1')).toContain('Port forward')
  })
  it.each([
    ['gitrepositories', 'source.toolkit.fluxcd.io'],
    ['ocirepositories', 'source.toolkit.fluxcd.io'],
    ['helmrepositories', 'source.toolkit.fluxcd.io'],
    ['kustomizations', 'kustomize.toolkit.fluxcd.io'],
    ['helmreleases', 'helm.toolkit.fluxcd.io'],
    ['alerts', 'notification.toolkit.fluxcd.io'],
  ])('only offers Flux actions for %s in its exact group', (kind, group) => {
    expect(actions(kind, `${group}/v1`)).toContain('>Reconcile<')
    for (const apiVersion of ['', 'vendor.io/v1', `foreign.${group}/v1`, 'argoproj.io/v1alpha1']) {
      const html = actions(kind, apiVersion)
      expect(html).not.toContain('>Reconcile<')
      expect(html).not.toContain('>Suspend<')
      expect(html).not.toContain('>Resume<')
    }
  })

})
