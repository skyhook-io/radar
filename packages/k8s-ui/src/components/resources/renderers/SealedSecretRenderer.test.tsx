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
    expect(render('False', controller({ controller: undefined, controllerState: 'denied' }))).not.toContain('kubeseal')
    expect(render('False', controller({ controller: undefined, controllerState: 'denied' }))).toContain('the sealed-secrets controller that serves this namespace')
    expect(render('True', controller())).not.toContain('--fetch-cert')
  })

  it('offers the re-seal hint only as a possible cause of a Synced=False', () => {
    const withStatus = (status: any) => renderToString(<SealedSecretRenderer data={{ ...sealed('False'), status }} controller={controller()} />)
    expect(withStatus(undefined)).not.toContain('--fetch-cert')
    expect(withStatus({ conditions: [] })).not.toContain('--fetch-cert')
    const exists = { conditions: [{ type: 'Synced', status: 'False', message: 'failed update: Resource "db" already exists and is not managed by SealedSecret' }] }
    expect(withStatus(exists)).not.toContain('--fetch-cert')
    const html = withStatus({ conditions: [{ type: 'Synced', status: 'False', message: 'no key could decrypt secret (password)' }] }).replace(/<!-- -->/g, '')
    expect(html).toContain('--fetch-cert')
    expect(html).toContain('If it was sealed')
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
  expect(html).not.toContain('reopen')
})

it('names a failed key load and an empty key list', () => {
  expect(renderToString(<SealedSecretRenderer data={sealed('False')} controller={controller({ keys: undefined, keysState: 'failed' })} />)).toContain('could not load Secrets')
  expect(renderToString(<SealedSecretRenderer data={sealed('False')} controller={controller({ keys: [] })} />).replace(/<!-- -->/g, '')).toContain('No sealing keys found in kube-system')
  // An agent that omits an empty list (or sends null) means the same.
  expect(renderToString(<SealedSecretRenderer data={sealed('False')} controller={controller({ keys: undefined })} />).replace(/<!-- -->/g, '')).toContain('No sealing keys found in kube-system')
})

it('counts SealedSecrets without a Synced condition instead of dropping them', () => {
  const html = renderToString(<SealedSecretRenderer data={sealed('False')} controller={controller({ counts: { total: 5, synced: 3, notSynced: 1, partial: false } })} />).replace(/<!-- -->/g, '')
  expect(html).toContain('3 synced · 1 not synced · 1 without a Synced condition')
})

it('keeps the re-seal hint when the controller status could not be loaded', () => {
  const html = render('False', null)
  expect(html).toContain('could not be loaded')
  expect(html).toContain('re-seal it with the current certificate')
  expect(html).not.toContain('kubeseal')
  expect(render('True', null)).not.toContain('re-seal')
})

it('names a controller lookup that is still loading or failed', () => {
  expect(render('False', controller({ controller: undefined, controllerState: 'loading', keys: undefined, keysState: 'unknown' }))).toContain('Deployments are still loading')
  expect(render('False', controller({ controller: undefined, controllerState: 'failed', keys: undefined, keysState: 'unknown' }))).toContain('could not load Deployments')
  expect(render('False', controller({ controller: undefined, controllerState: 'ambiguous', keys: undefined, keysState: 'unknown' }))).toContain('More than one sealed-secrets controller serves this namespace')
})

it('opens the section on a synced SealedSecret too', () => {
  expect(render('True', controller())).not.toContain('aria-expanded="false"')
})

it('builds the kubeseal command from the controller serving this namespace', () => {
  const ref = { kind: 'Deployment', namespace: 'team-x', name: 'sealed-secrets', ready: true }
  expect(render('False', controller({ controller: ref }))).toContain('kubeseal --controller-namespace team-x --controller-name sealed-secrets --fetch-cert')
})

it('offers the bare command to a user who cannot see the controller only when it is at kubeseal default location', () => {
  const denied = { controller: undefined, controllerState: 'denied' as const, keys: undefined, keysState: 'denied' as const }
  const atDefault = render('False', controller({ ...denied, atKubesealDefault: true }))
  expect(atDefault).toContain('kubeseal --fetch-cert')
  expect(atDefault).not.toContain('--controller-name')
  const elsewhere = render('False', controller({ ...denied, atKubesealDefault: false }))
  expect(elsewhere).not.toContain('kubeseal')
  expect(elsewhere).toContain('the sealed-secrets controller that serves this namespace')
})

it('shows no kubeseal command when Radar does not know the controller', () => {
  for (const state of ['ambiguous', 'notFound', 'notWatched', 'failed', 'loading', 'notCollected'] as const) {
    // atKubesealDefault only counts for a denied controller Radar did choose.
    const html = render('False', controller({ controller: undefined, controllerState: state, keys: undefined, keysState: 'unknown', atKubesealDefault: true }))
    expect(html).not.toContain('kubeseal')
    expect(html).toContain('the sealed-secrets controller that serves this namespace')
  }
  expect(render('False', null)).not.toContain('kubeseal')
})

it('says why a controller choice is ambiguous and when none serves the namespace', () => {
  expect(render('False', controller({ controller: undefined, controllerState: 'ambiguous', ambiguousReason: 'overlap', keys: undefined, keysState: 'unknown' }))).toContain("A cluster admin can check the controllers")
  expect(render('False', controller({ controller: undefined, controllerState: 'ambiguous', ambiguousReason: 'unknownScope', keys: undefined, keysState: 'unknown' }))).toContain("read which namespaces one of the sealed-secrets controllers serves")
  expect(render('False', controller({ controller: undefined, controllerState: 'notWatched', keys: undefined, keysState: 'unknown' }))).toContain('No sealed-secrets controller serves this namespace')
})

it('marks no sealing key when the key order is unknown, and says why', () => {
  const keys = controller().keys!.map((k) => ({ ...k, active: false }))
  const html = render('False', controller({ keys, keyOrder: 'unknown' }))
  expect(html).not.toContain('Seals new secrets')
  expect(html).toContain("read how the controller orders its keys")
  expect(render('False', controller({ keyOrder: 'certNotBefore' }))).not.toContain("read how the controller orders")
})

it('shows no validity start for a key whose certificate could not be read', () => {
  const keys = [{ name: 'sealed-secrets-keybroken', createdAt: '2026-11-12T08:00:00Z', active: false }]
  const html = render('False', controller({ keys }))
  expect(html).toContain('sealed-secrets-keybroken')
  expect(html).not.toContain('2026-11-12')
})
