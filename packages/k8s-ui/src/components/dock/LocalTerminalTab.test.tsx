// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { LocalTerminalTab, type LocalTerminalTabProps } from './LocalTerminalTab'

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
  send() {}
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
