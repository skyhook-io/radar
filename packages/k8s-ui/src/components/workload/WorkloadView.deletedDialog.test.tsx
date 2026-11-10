// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { WorkloadView } from './WorkloadView'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const deployment = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: { name: 'web', namespace: 'shop' },
  spec: { replicas: 1, template: { spec: { containers: [{ name: 'app', image: 'nginx' }] } } },
  status: { replicas: 1, readyReplicas: 1, availableReplicas: 1 },
}

it('closes an open Delete dialog when the object is deleted elsewhere', async () => {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const render = (resourceError: unknown) =>
    root.render(
      <WorkloadView
        kind="deployments"
        namespace="shop"
        name="web"
        onBack={vi.fn()}
        resource={deployment}
        resourceError={resourceError}
        actionsBarProps={{ onDelete: vi.fn() }}
      />,
    )
  const dialogOpen = () => document.body.textContent?.includes('Are you sure you want to delete') ?? false

  await act(async () => render(null))
  await act(async () => { element.querySelector<HTMLButtonElement>('button:has(svg.lucide-trash-2)')!.click() })
  expect(dialogOpen()).toBe(true)

  await act(async () => render(Object.assign(new Error('deployments.apps "web" not found'), { status: 404 })))
  expect(dialogOpen()).toBe(false)

  await act(async () => root.unmount())
  element.remove()
})
