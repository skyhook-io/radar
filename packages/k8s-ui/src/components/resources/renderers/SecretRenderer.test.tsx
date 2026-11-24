// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SecretRenderer } from './SecretRenderer'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const secret = {
  apiVersion: 'v1',
  kind: 'Secret',
  metadata: { name: 'db', namespace: 'shop', resourceVersion: '7' },
  type: 'Opaque',
  data: { password: btoa('old') },
}

let element: HTMLDivElement
let root: Root

beforeEach(() => {
  element = document.createElement('div')
  document.body.append(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

async function render(onSaveSecretValue?: (yaml: string) => Promise<void>) {
  await act(async () => root.render(<SecretRenderer data={secret} resourceData={secret} onSaveSecretValue={onSaveSecretValue} />))
}

function button(label: string): HTMLButtonElement {
  const match = [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === label || b.title === label)
  if (!match) throw new Error(`no button "${label}"`)
  return match
}

async function editAndSave(value: string) {
  await act(async () => button('Reveal').click())
  await act(async () => button('Edit value').click())
  const textarea = element.querySelector('textarea')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(textarea, value)
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => button('Save').click())
  await act(async () => button('Update').click())
}

const httpError = (status: number, message: string) => Object.assign(new Error(message), { status })

describe('SecretRenderer value edit', () => {
  it('keeps the edit open and says why when the Secret changed since it loaded', async () => {
    await render(vi.fn().mockRejectedValue(httpError(409, 'resource changed after review')))
    await editAndSave('new')
    expect(element.textContent).toContain('Not saved')
    expect(element.textContent).toContain('This Secret changed after it loaded. Once the view reloads it, save again')
    expect(element.querySelector('textarea')?.value).toBe('new')
  })

  it('says the Secret is gone when the save finds it deleted', async () => {
    await render(vi.fn().mockRejectedValue(httpError(404, 'secrets "db" not found')))
    await editAndSave('new')
    expect(element.textContent).toContain('Not saved')
    expect(element.textContent).toContain('This Secret no longer exists in the cluster.')
  })

  it('shows the server message for any other failure', async () => {
    await render(vi.fn().mockRejectedValue(httpError(403, 'secrets "db" is forbidden')))
    await editAndSave('new')
    expect(element.textContent).toContain('Not saved')
    expect(element.textContent).toContain('secrets "db" is forbidden')
  })

  it('closes the editor once the save handler is taken away', async () => {
    await render(vi.fn().mockResolvedValue(undefined))
    await act(async () => button('Reveal').click())
    await act(async () => button('Edit value').click())
    expect(element.querySelector('textarea')).not.toBeNull()
    await render(undefined)
    expect(element.querySelector('textarea')).toBeNull()
    expect(element.textContent).not.toContain('Save')
  })
})

describe('Secret certificate bundles', () => {
  const leaf = {
    subject: 'demo.example', organizations: ['Demo'], issuer: 'Demo CA', serialNumber: '01', keyType: 'Ed25519',
    notBefore: '2025-01-01T00:00:00Z', notAfter: '2026-01-01T00:00:00Z', daysLeft: -2, expired: true,
  }

  it('identifies repeated certificates by key and bundle position while data stays masked', async () => {
    const info = { certificates: [], additionalKeys: { 'z.pem': [leaf], 'a.pem': [leaf, leaf] } }
    await act(async () => root.render(<SecretRenderer data={secret} certificateInfo={info} />))
    const text = element.textContent!
    expect(text).toContain('Certificate · a.pem (1/2)')
    expect(text).toContain('Certificate · a.pem (2/2)')
    expect(text.indexOf('Certificate · a.pem')).toBeLessThan(text.indexOf('Certificate · z.pem'))
    expect(text).toContain('OrganizationDemo')
    expect(text).toContain(' · Expired')
    expect(text).not.toContain('Certificate has expired')
    expect(button('Reveal')).toBeTruthy()
    expect(text).not.toContain('old')
  })

  it('keeps the TLS leaf warning and removes replaced bundle details after a refresh', async () => {
    const data = { ...secret, type: 'kubernetes.io/tls' }
    await act(async () => root.render(<SecretRenderer data={data} certificateInfo={{ certificates: [leaf] }} />))
    expect(element.textContent).toContain('Certificate has expired')
    expect(element.textContent).toContain('Certificate · tls.crt')
    await act(async () => root.render(<SecretRenderer data={data} />))
    expect(element.textContent).not.toContain('Certificate · tls.crt')
    expect(element.textContent).not.toContain('Certificate has expired')
  })
})

it('keeps a large bundle behind one disclosure and leaves the data controls accessible', async () => {
  const cert = { subject: 'ca', issuer: 'ca', serialNumber: '1', keyType: 'Ed25519',
    notBefore: '2025-01-01T00:00:00Z', notAfter: '2035-01-01T00:00:00Z', daysLeft: 1000 }
  const info = { certificates: [], additionalKeys: { 'roots.pem': Array(100).fill(cert) } }
  await act(async () => root.render(<SecretRenderer data={secret} certificateInfo={info} />))
  expect(button('Certificates (100)').getAttribute('aria-expanded')).toBe('false')
  expect(element.textContent).not.toContain('roots.pem')
  expect(button('Reveal')).toBeTruthy()
  await act(async () => button('Certificates (100)').click())
  expect(element.textContent).toContain('Certificate · roots.pem (100/100)')
})
