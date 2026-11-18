// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourceDetailDrawer } from './ResourceDetailDrawer'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove() })

it('reports its width on mount and when a resize drag is released, not while dragging', () => {
  const onWidthChange = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(
    <ResourceDetailDrawer resource={{ kind: 'pods', namespace: 'test', name: 'web' }} onClose={() => {}} onWidthChange={onWidthChange}>
      {() => <div />}
    </ResourceDetailDrawer>,
  ))
  expect(onWidthChange.mock.calls).toEqual([[550]])

  const handle = host.querySelector<HTMLElement>('.cursor-ew-resize')!
  act(() => { handle.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, clientX: 1000 })) })
  act(() => { document.dispatchEvent(new MouseEvent('mousemove', { clientX: 950 })) })
  act(() => { document.dispatchEvent(new MouseEvent('mousemove', { clientX: 900 })) })
  expect(onWidthChange.mock.calls).toEqual([[550]])

  act(() => { document.dispatchEvent(new MouseEvent('mouseup', {})) })
  expect(onWidthChange.mock.calls).toEqual([[550], [650]])
})
