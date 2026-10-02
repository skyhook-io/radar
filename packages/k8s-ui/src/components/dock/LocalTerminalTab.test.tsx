// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { LocalTerminalTab, type LocalTerminalTabProps } from './LocalTerminalTab'
import { TerminalTab } from './TerminalTab'

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    rows = 24
    cols = 80
    loadAddon() {}
    open() {}
    resize() {}
    focus() {}
    dispose() {}
    write() {}
    onData() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { proposeDimensions() { return { rows: 24, cols: 80 } } } }))
vi.mock('@xterm/addon-web-links', () => ({ WebLinksAddon: class {} }))
vi.mock('./terminalClipboard', () => ({ setupTerminalClipboard: () => () => {}, copyTerminalSelection: vi.fn() }))
vi.mock('./TerminalClipboardToolbar', () => ({ TerminalClipboardToolbar: () => null }))
vi.mock('./useMultilinePasteConfirm', () => ({ useMultilinePasteConfirm: () => ({ confirmPaste: vi.fn(), pasteDialog: null }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class Socket {
  static OPEN = 1
  static instances: Socket[] = []
  readyState = 1
  onopen?: () => void
  onclose?: () => void
  onmessage?: (event: { data: string }) => void
  send = vi.fn()
  close() { this.readyState = 3; this.onclose?.() }
  constructor() { Socket.instances.push(this) }
  session(context: string) {
    this.onmessage?.({ data: JSON.stringify({ type: 'session', context, kubeconfigIsolated: true }) })
  }
}
let root: Root
let element: HTMLDivElement
beforeEach(() => {
  Socket.instances = []
  vi.stubGlobal('WebSocket', Socket)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('requestAnimationFrame', () => 0)
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})
async function render(props: Partial<LocalTerminalTabProps> = {}, strict = false) {
  await act(async () => {
    const terminal = <LocalTerminalTab createSession={async () => ({ wsUrl: 'ws://localhost' })} {...props} />
    root.render(strict ? <StrictMode>{terminal}</StrictMode> : terminal)
  })
}

it('reports the current session, clears metadata on reconnect, and ignores old socket messages', async () => {
  const firstCallback = vi.fn()
  await render({ onSessionInfo: firstCallback })
  expect(firstCallback).toHaveBeenCalledWith(null)
  const old = Socket.instances[0]
  await act(async () => { old.onopen?.(); old.session('production') })
  expect(firstCallback).toHaveBeenLastCalledWith({ context: 'production', kubeconfigIsolated: true })
  const currentCallback = vi.fn()
  await render({ onSessionInfo: currentCallback })
  await act(async () => old.close())
  await act(async () => element.querySelector<HTMLButtonElement>('button')!.click())
  expect(currentCallback).toHaveBeenLastCalledWith(null)
  const current = Socket.instances[1]
  await act(async () => { current.onopen?.(); current.session('staging'); old.session('production') })
  expect(currentCallback).toHaveBeenLastCalledWith({ context: 'staging', kubeconfigIsolated: true })
  expect(currentCallback).toHaveBeenCalledTimes(2)
})

it('does not open an obsolete session when Strict Mode creations resolve out of order', async () => {
  let resolveOld!: (value: { wsUrl: string }) => void
  const pending = new Promise<{ wsUrl: string }>(resolve => { resolveOld = resolve })
  const createSession = vi.fn().mockReturnValueOnce(pending).mockResolvedValueOnce({ wsUrl: 'ws://localhost/current' })
  const onSessionInfo = vi.fn()
  await render({ createSession, onSessionInfo }, true)
  expect(createSession).toHaveBeenCalledTimes(2)
  expect(Socket.instances).toHaveLength(1)
  await act(async () => resolveOld({ wsUrl: 'ws://localhost/obsolete' }))
  expect(Socket.instances).toHaveLength(1)
  await act(async () => Socket.instances[0].session('current'))
  expect(onSessionInfo).toHaveBeenLastCalledWith({ context: 'current', kubeconfigIsolated: true })
})

it('sends the initial command once and does not replay it on reconnect', async () => {
  vi.useFakeTimers()
  await render({ initialCommand: 'aws sso login' })
  const first = Socket.instances[0]
  await act(async () => first.onopen?.())
  expect(first.send).not.toHaveBeenCalledWith(JSON.stringify({ type: 'input', data: 'aws sso login\n' }))
  await act(async () => vi.advanceTimersByTime(300))
  const inputs = () => Socket.instances.flatMap(socket => socket.send.mock.calls.map(([data]) => JSON.parse(data))).filter(message => message.type === 'input')
  expect(inputs()).toEqual([{ type: 'input', data: 'aws sso login\n' }])

  await act(async () => first.close())
  await act(async () => element.querySelector<HTMLButtonElement>('button')!.click())
  await act(async () => Socket.instances[1].onopen?.())
  await act(async () => vi.advanceTimersByTime(300))
  expect(inputs()).toEqual([{ type: 'input', data: 'aws sso login\n' }])
})

it('keeps an unsent initial command available when the first connection closes before delivery', async () => {
  vi.useFakeTimers()
  await render({ initialCommand: 'gcloud auth login' })
  const first = Socket.instances[0]
  await act(async () => first.onopen?.())
  await act(async () => vi.advanceTimersByTime(100))
  await act(async () => first.close())
  await act(async () => element.querySelector<HTMLButtonElement>('button')!.click())
  const second = Socket.instances[1]
  await act(async () => second.onopen?.())
  await act(async () => vi.advanceTimersByTime(200))
  const input = JSON.stringify({ type: 'input', data: 'gcloud auth login\n' })
  expect(first.send).not.toHaveBeenCalledWith(input)
  expect(second.send).not.toHaveBeenCalledWith(input)
  await act(async () => vi.advanceTimersByTime(100))
  expect(second.send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toEqual([[input]])
})

it('sends the initial command independently in each terminal tab', async () => {
  vi.useFakeTimers()
  await act(async () => {
    root.render(<>
      <LocalTerminalTab createSession={async () => ({ wsUrl: 'ws://localhost/first' })} initialCommand="aws sso login" />
      <LocalTerminalTab createSession={async () => ({ wsUrl: 'ws://localhost/second' })} initialCommand="aws sso login" />
    </>)
  })
  expect(Socket.instances).toHaveLength(2)
  await act(async () => Socket.instances.forEach(socket => socket.onopen?.()))
  await act(async () => vi.advanceTimersByTime(300))
  const input = JSON.stringify({ type: 'input', data: 'aws sso login\n' })
  for (const socket of Socket.instances) {
    expect(socket.send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toEqual([[input]])
  }
})

it('keeps a blocked reconnect intact until the host permits it', async () => {
  vi.useFakeTimers()
  let allowed = true
  const createSession = vi.fn(async () => ({ wsUrl: 'ws://localhost' }))
  const onSessionInfo = vi.fn()
  await render({ createSession, canConnect: () => allowed, initialCommand: 'auth-command', onSessionInfo })
  await act(async () => Socket.instances[0].onopen?.())
  await act(async () => vi.advanceTimersByTime(300))
  await act(async () => Socket.instances[0].close())
  const terminal = element.querySelector('div.absolute')
  expect(terminal).not.toBeNull()
  const metadataCalls = onSessionInfo.mock.calls.length
  allowed = false
  await act(async () => element.querySelector<HTMLButtonElement>('button')!.click())
  expect(createSession).toHaveBeenCalledTimes(1)
  expect(onSessionInfo).toHaveBeenCalledTimes(metadataCalls)
  expect(element.contains(terminal)).toBe(true)
  allowed = true
  await act(async () => element.querySelector<HTMLButtonElement>('button')!.click())
  await act(async () => Socket.instances[1].onopen?.())
  await act(async () => vi.advanceTimersByTime(300))
  expect(createSession).toHaveBeenCalledTimes(2)
  expect(Socket.instances.flatMap(s => s.send.mock.calls).filter(([data]) => JSON.parse(data).type === 'input')).toHaveLength(1)
})

it.each(['local', 'pod'])('allows Retry after a %s terminal creation error', async kind => {
  const createSession = vi.fn()
    .mockRejectedValueOnce(new Error('creation failed'))
    .mockResolvedValueOnce({ wsUrl: 'ws://localhost' })
  await act(async () => root.render(kind === 'local'
    ? <LocalTerminalTab createSession={createSession} />
    : <TerminalTab namespace="default" podName="pod" containerName="app" containers={['app']} createSession={createSession} />))
  expect(element.textContent).toContain('creation failed')
  const retry = [...element.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Retry')!
  await act(async () => retry.click())
  expect(createSession).toHaveBeenCalledTimes(2)
  expect(Socket.instances).toHaveLength(1)
  await act(async () => Socket.instances[0].onopen?.())
  expect(element.textContent).not.toContain('creation failed')
})
