// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ logs: {} as Record<string, { data?: unknown; error?: unknown }>, viewer: [] as string[] }))
vi.mock('../../api/client', async (original) => ({
  ...await original<typeof import('../../api/client')>(),
  usePodLogs: (_ns: string, pod: string) => state.logs[pod] ?? {},
  useResource: () => ({ data: { status: { phase: 'Running' } } }),
}))
vi.mock('../logs/LogsViewer', () => ({ LogsViewer: ({ podName }: { podName: string }) => { state.viewer.push(podName); return <div>viewer:{podName}</div> } }))

const { ApiError } = await import('../../api/client')
const { MultiPodLogsTab } = await import('./WorkloadView')
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const pods = [{ kind: 'Pod', namespace: 'ml', name: 'gone-pod' }, { kind: 'Pod', namespace: 'ml', name: 'live-pod' }]
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
beforeEach(() => { state.logs = {}; state.viewer = []; host = document.createElement('div'); root = createRoot(host) })
afterEach(() => act(() => root.unmount()))

function render(selected: string, onSelectPod = vi.fn()) {
  act(() => root.render(<MultiPodLogsTab pods={pods} namespace="ml" selectedPod={selected} onSelectPod={onSelectPod} />))
  return onSelectPod
}

describe('MultiPodLogsTab', () => {
  it('explains a Pod Radar can no longer find instead of rendering a blank pane, and keeps the other Pods', () => {
    state.logs['gone-pod'] = { error: new ApiError('Pod not found', 404) }
    const onSelect = render('gone-pod')
    expect(host.textContent).toContain("Radar can't find gone-pod")
    expect(state.viewer).toEqual([])
    const live = [...host.querySelectorAll('button')].find(button => button.textContent === 'live-pod')!
    act(() => live.click())
    expect(onSelect).toHaveBeenCalledWith('live-pod')
  })

  it('shows the server reason for a denied log read', () => {
    state.logs['live-pod'] = { error: new ApiError('no access to pod logs in namespace ml: requires get pods/log', 403) }
    render('live-pod')
    expect(host.textContent).toContain('requires get pods/log')
    expect(host.textContent).not.toContain("can't find")
  })

  it('keeps the viewer for a Pod whose containers loaded even if a later refetch fails', () => {
    state.logs['live-pod'] = { data: { containers: ['app'] }, error: new ApiError('Pod not found', 404) }
    render('live-pod')
    expect(host.textContent).toContain('viewer:live-pod')
    expect(host.textContent).not.toContain("can't find")
  })
})
