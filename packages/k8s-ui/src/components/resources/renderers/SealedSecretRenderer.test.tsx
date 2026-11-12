import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { SealedSecretRenderer, type SealedSecretsController } from './SealedSecretRenderer'

const sealed = (synced: 'True' | 'False') => ({
  apiVersion: 'bitnami.com/v1alpha1', kind: 'SealedSecret',
  metadata: { name: 'db', namespace: 'team' },
  spec: { encryptedData: { password: 'AgB…' } },
  status: { conditions: [{ type: 'Synced', status: synced }] },
})

const controller = (patch: Partial<SealedSecretsController> = {}): SealedSecretsController => ({
  installed: true,
  controller: { kind: 'Deployment', namespace: 'kube-system', name: 'sealed-secrets-controller', ready: true },
  controllerState: 'ok',
  keys: [
    { name: 'sealed-secrets-keynew', createdAt: '2026-11-10T08:00:00Z', notBefore: '2026-11-10T08:00:00Z', notAfter: '2036-11-08T08:00:00Z', active: true },
    { name: 'sealed-secrets-keybad', createdAt: '2026-11-11T08:00:00Z', notBefore: '2026-11-11T08:00:00Z', active: false, notUsed: true },
    { name: 'sealed-secrets-keyold', createdAt: '2024-05-24T08:00:00Z', notBefore: '2024-05-24T08:00:00Z', active: false },
  ],
  keysState: 'ok',
  counts: { total: 1438, synced: 1437, notSynced: 1, partial: false },
  ...patch,
})

const render = (synced: 'True' | 'False', c?: SealedSecretsController | null) =>
  renderToString(<SealedSecretRenderer data={sealed(synced)} controller={c} />).replace(/<!-- -->/g, '')

describe('SealedSecretRenderer controller section', () => {
  it('omits the section when the host does not pass a controller', () => {
    expect(render('True')).not.toContain('Controller and sealing keys')
  })

  it('shows controller, totals and the key that seals new secrets', () => {
    const html = render('False', controller())
    expect(html).toContain('Controller and sealing keys')
    expect(html).toContain('sealed-secrets-controller')
    expect(html).toContain('Ready')
    expect(html).toContain('1437 synced · 1 not synced')
    expect(html.split('Seals new secrets').length - 1).toBe(1)
    expect(html).toContain('Not used')
    expect(html).toContain('2026-11-10')
  })

  it('says partial totals are limited to what the caller can list', () => {
    expect(render('False', controller({ counts: { total: 3, synced: 3, notSynced: 0, partial: true } }))).toContain('in namespaces you can list')
  })

  it('names a denied or uncollected key list instead of showing none', () => {
    expect(render('False', controller({ keys: undefined, keysState: 'denied' }))).toContain('No access to Secrets in kube-system')
    expect(render('False', controller({ keys: undefined, keysState: 'notCollected' }))).toContain('Radar does not collect Secrets in kube-system')
    expect(render('False', controller({ controller: undefined, controllerState: 'denied', keys: undefined, keysState: 'denied' }))).toContain("No access to the controller")
    expect(render('False', controller({ controller: undefined, controllerState: 'notFound', keys: undefined, keysState: 'unknown' }))).toContain('No sealed-secrets controller found')
    expect(render('False', null)).toContain('Controller status could not be loaded.')
  })

  it('offers kubeseal --fetch-cert only on a SealedSecret that is not synced', () => {
    expect(render('False', controller())).toContain('kubeseal --controller-namespace kube-system --controller-name sealed-secrets-controller --fetch-cert')
    expect(render('False', controller({ controller: undefined, controllerState: 'denied' }))).toContain('kubeseal --fetch-cert')
    expect(render('True', controller())).not.toContain('--fetch-cert')
  })

  it('never calls a key expired or compromised', () => {
    const html = render('False', controller()).toLowerCase()
    expect(html).not.toContain('xpired')
    expect(html).not.toContain('ompromised')
  })
})

it('says the sealing keys are still loading rather than not collected', () => {
  const html = renderToString(<SealedSecretRenderer data={sealed('False')} controller={controller({ keys: undefined, keysState: 'loading' })} />)
  expect(html).toContain('still loading')
  expect(html).not.toContain('does not collect')
})
