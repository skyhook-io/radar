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
    expect(element.textContent).toContain('Not saved. This Secret changed after it loaded')
    expect(element.querySelector('textarea')?.value).toBe('new')
  })

  it('says the Secret is gone when the save finds it deleted', async () => {
    await render(vi.fn().mockRejectedValue(httpError(404, 'secrets "db" not found')))
    await editAndSave('new')
    expect(element.textContent).toContain('Not saved. This Secret no longer exists in the cluster.')
  })

  it('shows the server message for any other failure', async () => {
    await render(vi.fn().mockRejectedValue(httpError(403, 'secrets "db" is forbidden')))
    await editAndSave('new')
    expect(element.textContent).toContain('Not saved: secrets "db" is forbidden')
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
