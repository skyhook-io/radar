// @vitest-environment jsdom
import { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { useResetViewsOnContextChange } from './useResetViewsOnContextChange'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('resets the views once when the connected cluster changes without a context change reaching the tab', () => {
  const reset = vi.fn()
  let applied: ((context: string) => void) | undefined
  function Probe({ state, context }: { state: string; context: string }) {
    const shown = useRef<string | null>(null)
    applied = (c) => { shown.current = c }
    useResetViewsOnContextChange(state, context, shown, reset)
    return null
  }
  const root = createRoot(document.createElement('div'))
  act(() => root.render(<Probe state="connected" context="prod" />))
  expect(reset).not.toHaveBeenCalled()

  act(() => root.render(<Probe state="connecting" context="prod" />))
  act(() => root.render(<Probe state="connected" context="prod" />))
  expect(reset).not.toHaveBeenCalled() // a reconnect to the same cluster

  act(() => root.render(<Probe state="connected" context="workload" />))
  expect(reset).toHaveBeenCalledExactlyOnceWith('workload')

  // A change the tab's own handler already applied is not applied again.
  applied!('staging')
  act(() => root.render(<Probe state="connected" context="staging" />))
  expect(reset).toHaveBeenCalledTimes(1)
  act(() => root.unmount())
})
