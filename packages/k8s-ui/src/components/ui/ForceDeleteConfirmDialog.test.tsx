// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ForceDeleteConfirmDialog, type CascadeDetail } from './ForceDeleteConfirmDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let cleanup: (() => void) | undefined
afterEach(() => cleanup?.())

async function render(detail: CascadeDetail | undefined, namespaceName = 'flux-system') {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <ForceDeleteConfirmDialog
        open
        onClose={() => {}}
        onConfirm={() => {}}
        resourceName="apps"
        resourceKind="Kustomization"
        namespaceName={namespaceName}
        isLoading={false}
        cascadeDependents={[{ kind: 'ReplicaSet', namespace: 'prod', name: 'web-abc' }]}
        cascadeDetail={detail}
      />,
    )
  })
  cleanup = () => {
    act(() => root.unmount())
    container.remove()
  }
  return () => document.body.textContent ?? ''
}

describe('ForceDeleteConfirmDialog', () => {
  it('separates certain, possible and GitOps-controller deletions', async () => {
    const text = await render({
      basis: 'ownerReferences',
      possibleDependents: [{ kind: 'Pod', namespace: 'prod', name: 'shared' }],
      controllerTeardown: { controller: 'Flux', action: 'prune', resources: [{ kind: 'Deployment', namespace: 'prod', name: 'web' }] },
    })
    expect(text()).toContain('Will also delete 1 dependent resource')
    expect(text()).toContain('May also delete 1 resource')
    expect(text()).toContain('Flux will also delete 1 managed resource')
  })

  it('says force delete stops the controller teardown', async () => {
    const text = await render({ basis: 'ownerReferences', controllerTeardown: { controller: 'Argo CD', action: 'prune', resources: [] } })
    const checkbox = document.body.querySelector('input[type="checkbox"]') as HTMLInputElement
    await act(async () => checkbox.click())
    expect(text()).toContain("Force delete removes Argo CD's finalizer")
    expect(text()).not.toContain('Argo CD will also delete')
  })

  it('describes a cluster-scoped resource without an empty namespace', async () => {
    const text = await render(undefined, '')
    expect(text()).toContain('cluster-scoped Kustomization "apps"')
    expect(text()).not.toContain('"" namespace')
  })
})
