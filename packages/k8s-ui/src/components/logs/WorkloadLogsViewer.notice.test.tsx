// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
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
