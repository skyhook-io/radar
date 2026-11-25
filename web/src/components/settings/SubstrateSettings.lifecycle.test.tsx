// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SubstrateSettings } from './SubstrateSettings'

// The form's lifecycle: what it saves against, what it keeps across refetches
// and cluster changes, and which access it shows.

/* eslint-disable @typescript-eslint/no-explicit-any */
const state = vi.hoisted(() => ({
  view: undefined as any,
  status: undefined as any,
  put: vi.fn(),
  refetch: vi.fn(),
  dirty: vi.fn(),
}))

vi.mock('../../api/substrateWorkspace', () => ({
  useSubstrateConnection: () => ({ data: state.view, refetch: state.refetch }),
  useSubstrateStatus: () => state.status,
  useUpdateSubstrateConnection: () => ({ mutate: state.put, reset() {}, isPending: false, error: null }),
  substrateErrorCode: (e: any) => e?.code,
}))
vi.mock('../substrate/shared', () => ({
  Term: ({ children }: any) => children,
  CommandChip: () => null,
}))
vi.mock('@skyhook-io/k8s-ui', () => ({
  Input: (props: any) => <input {...props} />,
  AlertBanner: ({ children, title }: any) => <div>{title}{children}</div>,
  Badge: ({ children }: any) => <span>{children}</span>,
  Collapse: ({ children }: any) => <div>{children}</div>,
  CollapseChevron: () => null,
  ConfirmDialog: ({ open, onConfirm, confirmLabel }: any) => (open ? <button onClick={onConfirm}>{confirmLabel}</button> : null),
  toneTextClass: () => '',
}))

let root: Root
let host: HTMLDivElement
const draw = () => act(async () => root.render(<SubstrateSettings active onDirtyChange={state.dirty} onBusyChange={() => {}} />))
const input = (placeholder: string) => host.querySelector<HTMLInputElement>(`input[placeholder="${placeholder}"]`)!
async function type(placeholder: string, value: string) {
  const el = input(placeholder)
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
const click = (label: string) => act(async () => Array.from(host.querySelectorAll('button')).find((b) => b.textContent === label)!.click())

const target = { binding: 'b', context: 'dev', fingerprint: 'fp', clientGeneration: 1, operationGeneration: 1 }
const localView = (over: any = {}) => ({
  view: 'local', saved: true, targetChanged: false, target, revision: 'r1',
  settings: { identity: 'tokenFile', tokenFile: '/old.token' },
  sharedPrincipal: 'system:serviceaccount:ate-system:ate-client',
  access: { state: 'connected' },
  ...over,
})
const status = (apiTier: any = { state: 'connected' }) => ({
  data: { installed: true, apiService: { namespace: 'ate-system', name: 'api', port: 443, found: true }, apiTier, coverage: { pools: { state: 'full' } }, canConfigure: true },
})

beforeEach(async () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  state.put.mockReset()
  state.refetch.mockReset()
  state.dirty.mockClear()
  state.view = localView()
  state.status = status()
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await draw()
})
afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
})

describe('the local Substrate connection form', () => {
  it('saves an edit against the revision it was read at, even after another save lands', async () => {
    await type('research, support-bots', 'research')
    expect(state.dirty).toHaveBeenLastCalledWith(true)
    state.view = localView({ revision: 'r2', settings: { identity: 'tokenFile', tokenFile: '/someone-elses.token' } })
    await draw()
    expect(host.textContent).toContain('This connection changed since you started editing')
    await click('Save changes')
    // The server refuses r1 with conflict; the form never pairs its old draft with r2.
    expect(state.put.mock.calls[0][0]).toMatchObject({ revision: 'r1', settings: { tokenFile: '/old.token', atespaces: ['research'] } })
  })

  it('loads the other save on Discard', async () => {
    await type('research, support-bots', 'research')
    state.view = localView({ revision: 'r2', settings: { identity: 'tokenFile', tokenFile: '/new.token' } })
    await draw()
    await click('Discard')
    expect(input('~/ate.token').value).toBe('/new.token')
    expect(state.dirty).toHaveBeenLastCalledWith(false)
  })

  it('follows a newer save when it has no edits', async () => {
    state.view = localView({ revision: 'r2', settings: { identity: 'tokenFile', tokenFile: '/new.token' } })
    await draw()
    expect(input('~/ate.token').value).toBe('/new.token')
  })

  it('follows a new target generation while clean, and saves against it', async () => {
    state.view = localView({ target: { ...target, clientGeneration: 2, operationGeneration: 2 } })
    await draw()
    await type('research, support-bots', 'research')
    await click('Save changes')
    expect(state.put.mock.calls[0][0].target).toMatchObject({ clientGeneration: 2, operationGeneration: 2 })
  })

  it('adopts a later return to the revision a save replaced, once the save was seen', async () => {
    state.put.mockImplementationOnce((_update: unknown, callbacks: any) =>
      callbacks.onSuccess(localView({ revision: 'r2', settings: { identity: 'tokenFile', tokenFile: '/old.token', atespaces: ['research'] } })))
    await type('research, support-bots', 'research')
    await click('Save changes')
    // The query still holds r1 for a moment: not adopted.
    await draw()
    expect(input('research, support-bots').value).toBe('research')
    // It catches up to r2, then another Radar restores r1's contents.
    state.view = localView({ revision: 'r2', settings: { identity: 'tokenFile', tokenFile: '/old.token', atespaces: ['research'] } })
    await draw()
    state.view = localView({ revision: 'r1' })
    await draw()
    expect(input('research, support-bots').value).toBe('')
    await type('research, support-bots', 'support-bots')
    await click('Save changes')
    expect(state.put.mock.calls[1][0].revision).toBe('r1')
  })

  it('keeps an edited draft on screen while the next cluster’s view loads, and doesn’t adopt it', async () => {
    await type('research, support-bots', 'research')
    state.view = undefined
    await draw()
    expect(input('research, support-bots').value).toBe('research')
    expect(state.dirty).toHaveBeenLastCalledWith(true)
    state.view = localView({ target: { ...target, binding: 'other', context: 'other', fingerprint: 'other-fp' } })
    await draw()
    expect(input('research, support-bots').value).toBe('research')
  })

  it('shows the polled access, not the one the view was read with', async () => {
    state.view = localView({ access: { state: 'connecting' } })
    state.status = status({ state: 'connected', endpoint: '127.0.0.1:18443' })
    await draw()
    expect(host.textContent).toContain('Connected')
    expect(host.textContent).not.toContain('Connecting')
  })

  it('says a failed status request failed, and still offers the form', async () => {
    state.status = { isError: true, error: new Error('HTTP 503') }
    await draw()
    expect(host.textContent).toContain('Radar couldn’t check Substrate’s status on this cluster: HTTP 503.')
    expect(host.textContent).toContain('Who Radar reads Substrate as')
  })

  it('offers Disconnect for a connection saved on a cluster that no longer serves Substrate', async () => {
    state.status = { data: { ...status().data, installed: false } }
    await draw()
    expect(host.textContent).toContain('a connection to it is still saved here')
    expect(host.textContent).toContain('Disconnect API')
  })

  it('says only that Substrate isn’t installed when nothing is saved here', async () => {
    state.view = localView({ saved: false, settings: null })
    state.status = { data: { ...status().data, installed: false } }
    await draw()
    expect(host.textContent).toContain('Agent Substrate isn’t installed on this cluster')
    expect(host.textContent).not.toContain('Disconnect API')
  })

  it('leaves a clean, empty form after a disconnect', async () => {
    state.put.mockImplementationOnce((_update: unknown, callbacks: any) =>
      callbacks.onSuccess(localView({ saved: false, settings: null, revision: 'r2', access: { state: 'notConfigured' } })))
    await click('Disconnect API')
    await click('Disconnect')
    expect(input('~/ate.token').value).toBe('')
    expect(state.dirty).toHaveBeenLastCalledWith(false)
    expect(host.textContent).toContain('Disconnected.')
  })

  it('resends the same draft, target and revision with explicit destination consent', async () => {
    await type('api.example.com:443', 'new.example.com:443')
    state.put.mockImplementationOnce((_update: unknown, callbacks: any) => callbacks.onError({ code: 'confirmDestination', message: 'The saved credential would go to a changed address: endpoint 127.0.0.1:18443 → new.example.com:443.' }))
    await click('Save changes')
    const original = state.put.mock.calls[0][0]
    expect(original.confirmDestination).toBeUndefined()
    await click('Send and save')
    expect(state.put.mock.calls[1][0]).toEqual({ ...original, confirmDestination: true })
  })
})

describe('the managed Substrate summary', () => {
  it('refreshes when this person’s access moves, so atespaces and denials stay current', async () => {
    state.view = {
      view: 'managed', sharedPrincipal: 'system:serviceaccount:ate-system:ate-client', access: { state: 'connecting' },
      managed: { enabled: true, readsAs: 'eachUser', endpoint: 'api.ate-system.svc:443', serverName: 'api.ate-system.svc', trust: 'clusterTrustBundle', helm: { api: 'substrate.api.enabled', endpoint: '', serverName: '', ca: '', atespaces: '' } },
    }
    state.status = status({ state: 'connected' })
    await draw()
    expect(state.refetch).toHaveBeenCalled()
    expect(host.textContent).toContain('Connected')
  })
})
