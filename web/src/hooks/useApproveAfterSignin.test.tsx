// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { beforeEach, expect, it, vi } from 'vitest'
import { useApproveAfterSignin } from './useAgentSignin'
import type { AgentSigninStatus } from '../api/diagnose'

vi.mock('../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('../api/diagnose', () => ({ fetchAgentSignin: vi.fn() }))

type Actions = ReturnType<typeof useApproveAfterSignin>
const seen = {} as { actions: Actions }

function Probe(props: Parameters<typeof useApproveAfterSignin>[0]) {
  seen.actions = useApproveAfterSignin(props)
  return null
}

function deferred() {
  let resolve!: (s: AgentSigninStatus | undefined) => void
  const promise = new Promise<AgentSigninStatus | undefined>((r) => { resolve = r })
  return { promise, resolve }
}

const signedOut: AgentSigninStatus = { agent: 'claude', state: 'signed-out', signinCmd: 'claude auth login' }

let root: Root
let approve: ReturnType<typeof vi.fn<() => void>>
let cancel: ReturnType<typeof vi.fn<() => void>>

async function render(pending: boolean, settle: () => Promise<AgentSigninStatus | undefined>, attemptKey = 'claude:safeguarded') {
  await act(async () => {
    root.render(<Probe approve={approve} cancel={cancel} signin={{ pending, settle }} attemptKey={attemptKey} />)
  })
}

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  root = createRoot(document.createElement('div'))
  approve = vi.fn()
  cancel = vi.fn()
})

it('approves at once when no check is running', async () => {
  const settle = vi.fn()
  await render(false, settle)
  await act(async () => seen.actions.approve())
  expect(approve).toHaveBeenCalledTimes(1)
  expect(settle).not.toHaveBeenCalled()
  root.unmount()
})

it('waits for a running check and stops on signed out', async () => {
  const answer = deferred()
  await render(true, () => answer.promise)
  await act(async () => seen.actions.approve())
  expect(seen.actions.waiting).toBe(true)
  expect(approve).not.toHaveBeenCalled()
  await act(async () => answer.resolve(signedOut))
  expect(seen.actions.waiting).toBe(false)
  expect(approve).not.toHaveBeenCalled()
  root.unmount()
})

it.each<[string, AgentSigninStatus | undefined]>([
  ['signed in', { agent: 'claude', state: 'signed-in' }],
  ['unknown', { agent: 'claude', state: 'unknown' }],
  ['no answer (timeout or failure)', undefined],
])('approves after a running check answers %s', async (_label, status) => {
  const answer = deferred()
  await render(true, () => answer.promise)
  await act(async () => seen.actions.approve())
  await act(async () => answer.resolve(status))
  expect(approve).toHaveBeenCalledTimes(1)
  root.unmount()
})

it('Cancel abandons the wait', async () => {
  const answer = deferred()
  await render(true, () => answer.promise)
  await act(async () => seen.actions.approve())
  await act(async () => seen.actions.cancel())
  expect(cancel).toHaveBeenCalledTimes(1)
  expect(seen.actions.waiting).toBe(false)
  await act(async () => answer.resolve({ agent: 'claude', state: 'signed-in' }))
  expect(approve).not.toHaveBeenCalled()
  root.unmount()
})

it('switching agent or profile abandons the wait', async () => {
  const answer = deferred()
  await render(true, () => answer.promise)
  await act(async () => seen.actions.approve())
  await render(true, () => answer.promise, 'codex:safeguarded')
  expect(seen.actions.waiting).toBe(false)
  await act(async () => answer.resolve(undefined))
  expect(approve).not.toHaveBeenCalled()
  root.unmount()
})

it('leaving the card abandons the wait', async () => {
  const answer = deferred()
  await render(true, () => answer.promise)
  await act(async () => seen.actions.approve())
  root.unmount()
  await act(async () => answer.resolve(undefined))
  expect(approve).not.toHaveBeenCalled()
})
