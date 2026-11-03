// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { WorkloadView } from './WorkloadView'

vi.mock('../shared/ResourceRendererDispatch', async (original) => ({ ...await original<typeof import('../shared/ResourceRendererDispatch')>(), ResourceRendererDispatch: () => null }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const resource = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'uid' }, spec: {}, status: {} }
const pods = [{ kind: 'Pod', namespace: 'ml', name: 'cluster-head-abc' }]

function mount(renderLogsTab?: () => ReactNode) {
  const host = document.createElement('div')
  const root = createRoot(host)
  const onTabChange = vi.fn()
  act(() => root.render(<WorkloadView kind="rayclusters" group="ray.io" namespace="ml" name="cluster" resource={resource} onBack={() => {}}
    expanded activeTab="logs" onTabChange={onTabChange} relationships={{ pods }} renderLogsTab={renderLogsTab} />))
  const tabs = [...host.querySelectorAll('[role="tab"]')].map(tab => tab.textContent)
  act(() => root.unmount())
  return { tabs, onTabChange }
}

it('drops the Logs tab and sends a logs deep link to Overview when the host supplies no logs view, even with known Pods', () => {
  const { tabs, onTabChange } = mount()
  expect(tabs).not.toContain('Logs')
  expect(onTabChange).toHaveBeenCalledWith('overview', { replace: true })
})

it('keeps the Logs tab when the host supplies one', () => {
  const { tabs, onTabChange } = mount(() => <div>logs</div>)
  expect(tabs).toContain('Logs')
  expect(onTabChange).not.toHaveBeenCalled()
})
