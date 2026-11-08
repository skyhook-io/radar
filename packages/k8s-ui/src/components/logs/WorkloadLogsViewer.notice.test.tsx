// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { VirtuosoMockContext } from 'react-virtuoso'
import { ToastProvider } from '../ui/Toast'
import { WorkloadLogsViewer } from './WorkloadLogsViewer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class FakeEventSource extends EventTarget {
  static last: FakeEventSource | undefined
  constructor(readonly url: string) {
    super()
    FakeEventSource.last = this
  }
  close() {}
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
}

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource)
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

it('clears the unreadable-sources notice when the stream ends', async () => {
  const notice = '1 source could not be read: web-0/app: Kubernetes did not return this container\'s logs'
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer
          name="web"
          fetchAll={async () => ({ pods: [], logs: [] })}
          createStream={() => new EventSource('/stream')}
          autoStream
        />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')

  await act(async () => source.emit('connected', { pods: [{ name: 'web-0', containers: ['app'] }] }))
  await act(async () => source.emit('notice', { notice }))
  expect(element.textContent).toContain(notice)

  await act(async () => source.emit('end', { reason: 'no pods found' }))
  expect(element.textContent).not.toContain(notice)
})

it('names each line\'s container, marks init containers and says why the app has no output', async () => {
  await act(async () =>
    root.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 20 }}>
        <ToastProvider>
          <WorkloadLogsViewer
            name="predictor"
            fetchAll={async () => ({ pods: [], logs: [] })}
            createStream={() => new EventSource('/stream')}
            autoStream
          />
        </ToastProvider>
      </VirtuosoMockContext.Provider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', {
    pods: [{
      name: 'predictor-abc-xyz',
      containers: ['storage-initializer', 'kserve-container'],
      ready: false,
      containerStatuses: [
        { name: 'storage-initializer', init: true, ready: false, restartCount: 4, state: 'waiting', reason: 'CrashLoopBackOff' },
        { name: 'kserve-container', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' },
      ],
    }],
  }))
  await act(async () => source.emit('log', { pod: 'predictor-abc-xyz', container: 'storage-initializer', timestamp: '2026-11-08T00:00:00Z', content: 'Storage initialization failed' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })

  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
  const label = element.querySelector('[title="Init container storage-initializer"]')
  expect(label?.textContent).toBe('storage-initializerinit')
  const option = [...element.querySelectorAll('option')].find(o => o.value === 'storage-initializer')
  expect(option?.textContent).toBe('storage-initializer (init)')

  await act(async () => source.emit('log', { pod: 'predictor-abc-xyz', container: 'kserve-container', timestamp: '2026-11-08T00:01:00Z', content: 'model server listening' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.textContent).not.toContain("hasn't started")

  await act(async () => source.emit('pod_removed', { pod: 'predictor-abc-xyz', reason: 'terminated' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.querySelector('[title="Init container storage-initializer"]')).not.toBeNull()
})

it('keeps a started container out of the notice after the buffer is cleared', async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="predictor" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', {
    pods: [{
      name: 'predictor-abc-xyz',
      containers: ['storage-initializer', 'kserve-container'],
      ready: false,
      containerStatuses: [
        { name: 'storage-initializer', init: true, ready: false, restartCount: 4, state: 'waiting', reason: 'CrashLoopBackOff' },
        { name: 'kserve-container', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' },
      ],
    }],
  }))
  expect(element.textContent).toContain("hasn't started")
  await act(async () => source.emit('log', { pod: 'predictor-abc-xyz', container: 'kserve-container', timestamp: '2026-11-08T00:01:00Z', content: 'model server listening' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  const clear = element.querySelector('button[aria-label="Clear logs"]') as HTMLButtonElement | null
  if (!clear) throw new Error('clear button not found')
  await act(async () => clear.click())
  expect(element.textContent).not.toContain("hasn't started")
})

it('treats a replaced Pod with the same name as a new incarnation', async () => {
  const initializing = {
    name: 'web-0',
    containers: ['migrate', 'app'],
    ready: false,
    containerStatuses: [
      { name: 'migrate', init: true, ready: false, restartCount: 1, state: 'waiting', reason: 'CrashLoopBackOff' },
      { name: 'app', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' },
    ],
  }
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', { pods: [{ ...initializing, containerStatuses: [{ ...initializing.containerStatuses[0], state: 'terminated', reason: 'Completed' }, { name: 'app', ready: true, restartCount: 0, state: 'running' }] }] }))
  await act(async () => source.emit('log', { pod: 'web-0', container: 'app', timestamp: '2026-11-08T00:00:00Z', content: 'serving' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.textContent).not.toContain("hasn't started")

  await act(async () => source.emit('pod_removed', { pod: 'web-0', reason: 'replaced' }))
  await act(async () => source.emit('pod_added', { pods: [initializing] }))
  expect(element.textContent).toContain("app hasn't started: init container migrate is in CrashLoopBackOff.")
})

it('keeps the init marker when a Pod is added, logs and is removed in one batch', async () => {
  await act(async () =>
    root.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 20 }}>
        <ToastProvider>
          <WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
        </ToastProvider>
      </VirtuosoMockContext.Provider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', { pods: [] }))
  await act(async () => {
    source.emit('pod_added', { pods: [{ name: 'web-1', containers: ['migrate', 'app'], ready: false, containerStatuses: [{ name: 'migrate', init: true, ready: false, restartCount: 0, state: 'running' }, { name: 'app', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' }] }] })
    source.emit('log', { pod: 'web-1', container: 'migrate', timestamp: '2026-11-08T00:00:00Z', content: 'migrating' })
    source.emit('pod_removed', { pod: 'web-1', reason: 'terminated' })
  })
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.querySelector('[title="Init container migrate"]')).not.toBeNull()
})

it('clears the notice when the stream reports the Pod initialized, even before the app logs', async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="predictor" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  const statuses = (initState: object, appState: object) => [{ name: 'storage-initializer', init: true, ready: false, restartCount: 0, ...initState }, { name: 'kserve-container', ready: false, restartCount: 0, ...appState }]
  await act(async () => source.emit('connected', { pods: [{ name: 'p-0', containers: ['storage-initializer', 'kserve-container'], ready: false, containerStatuses: statuses({ state: 'running' }, { state: 'waiting', reason: 'PodInitializing' }) }] }))
  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is still running.")
  await act(async () => source.emit('pod_updated', { pods: [{ name: 'p-0', containers: ['storage-initializer', 'kserve-container'], ready: true, containerStatuses: statuses({ state: 'terminated', reason: 'Completed' }, { state: 'running', started: true, ready: true }) }] }))
  expect(element.textContent).not.toContain("hasn't started")
})

it('marks each line by its container\'s role when it arrived, across a same-name replacement', async () => {
  await act(async () =>
    root.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 20 }}>
        <ToastProvider>
          <WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
        </ToastProvider>
      </VirtuosoMockContext.Provider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', { pods: [{ name: 'web-0', containers: ['setup', 'app'], ready: false, containerStatuses: [{ name: 'setup', init: true, ready: false, restartCount: 0, state: 'running' }, { name: 'app', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' }] }] }))
  await act(async () => source.emit('log', { pod: 'web-0', container: 'setup', timestamp: '2026-11-08T00:00:00Z', content: 'old init line' }))
  await act(async () => source.emit('pod_removed', { pod: 'web-0', reason: 'replaced' }))
  await act(async () => source.emit('pod_added', { pods: [{ name: 'web-0', containers: ['setup', 'app'], ready: true, containerStatuses: [{ name: 'setup', ready: true, restartCount: 0, state: 'running' }, { name: 'app', ready: true, restartCount: 0, state: 'running' }] }] }))
  await act(async () => source.emit('log', { pod: 'web-0', container: 'setup', timestamp: '2026-11-08T00:01:00Z', content: 'new app line' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  const labels = [...element.querySelectorAll('[title$="ontainer setup"]')].map(label => label.getAttribute('title'))
  expect(labels).toEqual(['Init container setup', 'Container setup'])
})

it('drops the initializing notice when the stream ends with no Pods left', async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="predictor" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  await act(async () => source.emit('connected', { pods: [{ name: 'p-0', containers: ['init', 'app'], ready: false, containerStatuses: [{ name: 'init', init: true, ready: false, restartCount: 0, state: 'running' }, { name: 'app', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' }] }] }))
  expect(element.textContent).toContain("app hasn't started")
  await act(async () => source.emit('end', { reason: 'no pods found' }))
  expect(element.textContent).not.toContain("hasn't started")
})

it('hides the previous stream\'s initializing notice while a new stream connects', async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="predictor" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const first = FakeEventSource.last
  if (!first) throw new Error('stream was not opened')
  await act(async () => first.emit('connected', { pods: [{ name: 'p-0', containers: ['init', 'app'], ready: false, containerStatuses: [{ name: 'init', init: true, ready: false, restartCount: 0, state: 'running' }, { name: 'app', ready: false, restartCount: 0, state: 'waiting', reason: 'PodInitializing' }] }] }))
  expect(element.textContent).toContain("app hasn't started")
  const select = element.querySelector('select') as HTMLSelectElement
  await act(async () => {
    select.value = 'app'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(FakeEventSource.last).not.toBe(first)
  expect(element.textContent).not.toContain("hasn't started")
})

it('shows the notice again when fresh status says the Pod is re-initializing', async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer name="predictor" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  const pod = (initState: object, appState: object) => ({ name: 'p-0', containers: ['init', 'app'], ready: false, containerStatuses: [{ name: 'init', init: true, ready: false, restartCount: 0, ...initState }, { name: 'app', ready: false, restartCount: 0, ...appState }] })
  await act(async () => source.emit('connected', { pods: [pod({ state: 'terminated', reason: 'Completed' }, { state: 'running', started: true })] }))
  await act(async () => source.emit('log', { pod: 'p-0', container: 'app', timestamp: '2026-11-08T00:00:00Z', content: 'serving' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  await act(async () => source.emit('pod_updated', { pods: [pod({ state: 'waiting', reason: 'CrashLoopBackOff' }, { state: 'waiting', reason: 'PodInitializing' })] }))
  expect(element.textContent).toContain("app hasn't started: init container init is in CrashLoopBackOff.")
})

it('names each line\'s Pod only while the view holds more than one Pod', async () => {
  await act(async () =>
    root.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 20 }}>
        <ToastProvider>
          <WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
        </ToastProvider>
      </VirtuosoMockContext.Provider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  const running = (name: string) => ({ name, containers: ['app'], ready: true, containerStatuses: [{ name: 'app', ready: true, restartCount: 0, state: 'running' }] })
  await act(async () => source.emit('connected', { pods: [running('web-0')] }))
  await act(async () => source.emit('log', { pod: 'web-0', container: 'app', timestamp: '2026-11-08T00:00:00Z', content: 'serving' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.textContent).toContain('serving')
  expect(element.querySelector('[title="web-0"]')).toBeNull()

  await act(async () => source.emit('pod_added', { pods: [running('web-1')] }))
  await act(async () => source.emit('log', { pod: 'web-1', container: 'app', timestamp: '2026-11-08T00:01:00Z', content: 'also serving' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
  expect(element.querySelector('[title="web-0"]')?.textContent).toBe('[web-0]')
  expect(element.querySelector('[title="web-1"]')?.textContent).toBe('[web-1]')
})
