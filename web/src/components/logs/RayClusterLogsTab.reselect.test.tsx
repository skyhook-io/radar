// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ query: {} as any }))
vi.mock('../../api/client', () => {
  class ApiError extends Error { constructor(message: string, public status: number) { super(message) } }
  return { ApiError, isStableClientError: (e: any) => e instanceof ApiError && [400, 403, 404, 409].includes(e.status), useCapabilities: () => ({ data: { features: { rayClusterPods: true, podSidecarMarks: true } } }), useRadarFeature: () => ({ support: 'supported', upgrade: null }), useWorkloadPods: () => state.query }
})
vi.mock('../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: true, logsUnknown: false }) }))
vi.mock('./MultiPodLogsTab', () => ({ MultiPodLogsTab: () => null }))
vi.mock('@skyhook-io/k8s-ui', () => ({ AlertBanner: () => null, PaneLoader: () => null, RadarUpgradeNote: () => null }))
const { RayClusterLogsTab } = await import('./RayClusterLogsTab')
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const history = [{ kind: 'Pod', namespace: 'ml', name: 'deleted-worker' }]
const owned = { pods: [{ name: 'failing-worker', ready: false, containers: [] }, { name: 'head', ready: true, containers: [] }], total: 2, truncated: false }

function mount(selectedPod: string, onSelectPod = vi.fn()) {
  const host = document.createElement('div')
  const root = createRoot(host)
  const render = () => act(() => root.render(<RayClusterLogsTab namespace="ml" name="c" uid="u" suspendRequested={false} rootLoading={false} rootError={null} historyPods={history} selectedPod={selectedPod} onSelectPod={onSelectPod} initialContainer={null} />))
  return { render, onSelectPod, unmount: () => act(() => root.unmount()) }
}

it('replaces a recorded Pod picked while the owned list was failing once that list arrives', () => {
  state.query = { error: new Error('cache warming') }
  const view = mount('deleted-worker')
  view.render()
  state.query = { data: owned }
  view.render()
  expect(view.onSelectPod).toHaveBeenCalledWith('failing-worker')
  view.unmount()
})

it('leaves a selection alone when the owned list was showing all along', () => {
  state.query = { data: owned }
  const view = mount('gone-after-selection')
  view.render()
  view.render()
  expect(view.onSelectPod).not.toHaveBeenCalled()
  view.unmount()
})
