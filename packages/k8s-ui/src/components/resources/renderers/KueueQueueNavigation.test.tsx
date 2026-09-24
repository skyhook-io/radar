// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ClusterQueueRenderer, LocalQueueRenderer } from './KueueQueueRenderers'

it('navigates from a namespaced LocalQueue to exact cluster-scoped Kueue dependencies', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  const container = document.createElement('div')
  const root = createRoot(container)
  const onNavigate = vi.fn()
  try {
    await act(async () =>
      root.render(
        <LocalQueueRenderer
          data={{
            apiVersion: 'kueue.x-k8s.io/v1beta2',
            metadata: { namespace: 'ml' },
            spec: { clusterQueue: 'shared' },
          }}
          onNavigate={onNavigate}
        />,
      ),
    )
    const click = async (name: string) => {
      const button = [...container.querySelectorAll('button')].find((button) => button.textContent === name)
      expect(button).toBeDefined()
      await act(async () => button!.click())
    }
    await click('shared')
    expect(onNavigate).toHaveBeenLastCalledWith({
      kind: 'clusterqueues',
      namespace: '',
      name: 'shared',
      group: 'kueue.x-k8s.io',
    })
    await act(async () =>
      root.render(
        <ClusterQueueRenderer
          data={{
            apiVersion: 'kueue.x-k8s.io/v1beta2',
            spec: {
              resourceGroups: [{ flavors: [{ name: 'cpu-flavor', resources: [{ name: 'cpu', nominalQuota: '1' }] }] }],
              admissionChecksStrategy: { admissionChecks: [{ name: 'policy-check' }] },
            },
          }}
          onNavigate={onNavigate}
        />,
      ),
    )
    await click('cpu-flavor')
    expect(onNavigate).toHaveBeenLastCalledWith({
      kind: 'resourceflavors',
      namespace: '',
      name: 'cpu-flavor',
      group: 'kueue.x-k8s.io',
    })
    await click('policy-check')
    expect(onNavigate).toHaveBeenLastCalledWith({
      kind: 'admissionchecks',
      namespace: '',
      name: 'policy-check',
      group: 'kueue.x-k8s.io',
    })
  } finally {
    await act(async () => root.unmount())
    globalThis.IS_REACT_ACT_ENVIRONMENT = false
  }
})
