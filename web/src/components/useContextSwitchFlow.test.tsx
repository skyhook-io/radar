// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ContextInfo } from '../types'

const mocks = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  setFailedSwitch: vi.fn(),
  startSwitch: vi.fn(),
}))
vi.mock('../api/client', () => ({
  useSwitchContext: () => ({ mutateAsync: mocks.mutateAsync, isPending: false, isError: false, error: null }),
  fetchSessionCounts: async () => ({ portForwards: 0 }),
  SwitchContextError: class extends Error {},
}))
vi.mock('../context/ContextSwitchContext', () => ({
  useContextSwitch: () => ({
    startSwitch: mocks.startSwitch,
    endSwitch: vi.fn(),
    setOpenAfterSwitch: vi.fn(),
    setFailedSwitch: mocks.setFailedSwitch,
  }),
}))
vi.mock('./ui/Toast', () => ({ useToast: () => ({ showError: vi.fn() }) }))
vi.mock('./dock', () => ({ useDock: () => ({ tabs: [] }) }))

const { useContextSwitchFlow } = await import('./useContextSwitchFlow')

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let flow: ReturnType<typeof useContextSwitchFlow>

function Probe() {
  flow = useContextSwitchFlow()
  return null
}

beforeEach(() => {
  vi.clearAllMocks()
  root = createRoot(document.createElement('div'))
  act(() => root.render(<Probe />))
})
afterEach(() => act(() => root.unmount()))

const ctx = (name: string, isCurrent: boolean) => ({ name, cluster: name, user: 'u', namespace: '', isCurrent }) as ContextInfo

describe('useContextSwitchFlow', () => {
  it('picking the cluster you are still on dismisses a refused switch without switching', async () => {
    await act(() => flow.requestSwitch(ctx('current', true)))
    expect(mocks.setFailedSwitch).toHaveBeenCalledWith(null)
    expect(mocks.startSwitch).not.toHaveBeenCalled()
    expect(mocks.mutateAsync).not.toHaveBeenCalled()
  })

  it('picking another cluster still switches', async () => {
    await act(() => flow.requestSwitch(ctx('staging', false)))
    expect(mocks.mutateAsync).toHaveBeenCalledWith({ name: 'staging' })
  })
})
