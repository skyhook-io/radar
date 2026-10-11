// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { WorkloadView } from './WorkloadView'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const secret = {
  apiVersion: 'v1',
  kind: 'Secret',
  metadata: { name: 'db', namespace: 'shop', resourceVersion: '7' },
  type: 'Opaque',
  data: { password: btoa('old') },
}

function button(label: string): HTMLButtonElement | undefined {
  return [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === label || b.title === label)
}

it('saves a Secret value against the version on screen, and offers no edit once the Secret is gone', async () => {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const onUpdateResource = vi.fn().mockResolvedValue(undefined)
  const render = (resourceError: unknown) =>
    root.render(
      <WorkloadView
        kind="secrets"
        namespace="shop"
        name="db"
        onBack={vi.fn()}
        resource={secret}
        resourceError={resourceError}
        canUpdateSecrets
        onUpdateResource={onUpdateResource}
      />,
    )

  await act(async () => render(null))
  await act(async () => button('Reveal')!.click())
  await act(async () => button('Edit value')!.click())
  await act(async () => button('Save')!.click())
  await act(async () => button('Update')!.click())
  expect(onUpdateResource).toHaveBeenCalledWith(expect.objectContaining({
    kind: 'secrets',
    namespace: 'shop',
    name: 'db',
    reviewedResourceVersion: '7',
  }))

  await act(async () => button('Edit value')!.click())
  expect(element.querySelector('textarea')).not.toBeNull()
  await act(async () => render(Object.assign(new Error('secrets "db" not found'), { status: 404 })))
  expect(element.textContent).toContain('This Secret no longer exists in the cluster')
  expect(element.querySelector('textarea')).toBeNull()
  expect(button('Edit value')).toBeUndefined()

  await act(async () => root.unmount())
  element.remove()
})
