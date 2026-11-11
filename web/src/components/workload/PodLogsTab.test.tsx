// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mounts = vi.hoisted(() => [] as (string | undefined)[])
vi.mock('../logs/LogsViewer', () => ({
  LogsViewer: ({ initialContainer }: { initialContainer?: string }) => {
    useState(() => mounts.push(initialContainer))
    return null
  },
}))

const { PodLogsTab } = await import('./WorkloadView')
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const stuckPod = {
  spec: { initContainers: [{ name: 'migrate' }], containers: [{ name: 'app' }] },
  status: { phase: 'Pending', initContainerStatuses: [{ name: 'migrate', state: { waiting: { reason: 'CrashLoopBackOff' } } }] },
}

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
beforeEach(() => { mounts.length = 0; host = document.createElement('div'); root = createRoot(host) })
afterEach(() => act(() => root.unmount()))

describe('PodLogsTab', () => {
  it('opens on the stuck init container once the pod loads after the tab', () => {
    act(() => root.render(<PodLogsTab namespace="ns" name="job-pod" resource={undefined} />))
    act(() => root.render(<PodLogsTab namespace="ns" name="job-pod" resource={stuckPod} />))
    expect(mounts).toEqual([undefined, 'migrate'])
  })

  it('keeps the viewer mounted while the loaded pod changes', () => {
    act(() => root.render(<PodLogsTab namespace="ns" name="job-pod" resource={stuckPod} />))
    act(() => root.render(<PodLogsTab namespace="ns" name="job-pod" resource={{ ...stuckPod, status: { phase: 'Running' } }} />))
    expect(mounts).toEqual(['migrate'])
  })
})
