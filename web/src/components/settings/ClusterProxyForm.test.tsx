// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ portForwards: 0, terminals: 0, mutate: vi.fn(), retry: vi.fn() }))
vi.mock('../../api/client', () => ({
  useContexts: () => ({ data: [
    { name: 'staging', cluster: 'c', user: 'u', namespace: '', isCurrent: false },
    { name: 'prod', cluster: 'c', user: 'u', namespace: '', isCurrent: true, proxyUrl: 'socks5://bastion:1080' },
  ] }),
  useSetContextProxy: () => ({ mutate: state.mutate, isPending: false }),
  fetchSessionCounts: async () => ({ portForwards: state.portForwards }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ retry: state.retry }) }))
vi.mock('../dock', () => ({ useDock: () => ({ tabs: Array.from({ length: state.terminals }, () => ({ type: 'terminal' })) }) }))

const { ClusterProxyList } = await import('./ClusterProxyForm')
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
beforeEach(() => {
  state.portForwards = 0; state.terminals = 0; state.mutate.mockReset(); state.retry.mockReset()
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host)
})
afterEach(() => { act(() => root.unmount()); host.remove() })

const rows = () => [...host.querySelectorAll('li')]
const row = (name: string) => rows().find((li) => li.textContent?.startsWith(name))!

async function submit(name: string, url: string) {
  const li = row(name)
  await act(async () => { [...li.querySelectorAll('button')].find((b) => /Set proxy|Change/.test(b.textContent ?? ''))!.click() })
  const input = li.querySelector('input')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, url)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => { li.querySelector('form')!.requestSubmit() })
}

describe('ClusterProxyList', () => {
  it('lists every context, the current one first, with its saved proxy', () => {
    act(() => root.render(<ClusterProxyList />))
    expect(rows().map((li) => li.textContent)).toEqual([
      expect.stringContaining('prodcurrentsocks5://bastion:1080Change'),
      expect.stringContaining('stagingNo proxy savedSet proxy'),
    ])
  })

  it('saves a proxy for another context without asking or reconnecting', async () => {
    state.portForwards = 2
    act(() => root.render(<ClusterProxyList />))
    await submit('staging', 'http://proxy:3128')
    expect(state.mutate).toHaveBeenCalledWith({ name: 'staging', proxyUrl: 'http://proxy:3128' }, expect.anything())
    expect(document.body.textContent).not.toContain('Reconnect to use the proxy?')
    state.mutate.mock.calls[0][1].onSuccess({ context: 'staging', current: false })
    expect(state.retry).not.toHaveBeenCalled()
  })

  it('asks before a save for the current context would end open sessions', async () => {
    state.portForwards = 1
    state.terminals = 2
    act(() => root.render(<ClusterProxyList />))
    await submit('prod', 'http://proxy:3128')
    expect(state.mutate).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('ends 1 port forward and 2 terminal sessions')
    const confirm = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.includes('Save and reconnect'))!
    await act(async () => { confirm.click() })
    expect(state.mutate).toHaveBeenCalledTimes(1)
    await act(async () => { state.mutate.mock.calls[0][1].onSuccess({ context: 'prod', current: true }) })
    expect(state.retry).toHaveBeenCalledTimes(1)
  })

  it('wraps a long context name and saved proxy instead of overflowing the card', async () => {
    act(() => root.render(<ClusterProxyList />))
    const li = row('prod')
    await act(async () => { [...li.querySelectorAll('button')].find((b) => b.textContent === 'Change')!.click() })
    const codes = [...li.querySelectorAll('label .inline-code, p .inline-code')]
    expect(codes.map((c) => c.textContent)).toEqual(['prod', 'socks5://bastion:1080'])
    for (const c of codes) expect(c.className).toContain('break-all')
    expect(li.querySelector('form button[type="button"]')!.className).toContain('btn-secondary')
  })
})
