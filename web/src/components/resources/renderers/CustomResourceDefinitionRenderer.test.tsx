// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { CustomResourceDefinitionRenderer } from './CustomResourceDefinitionRenderer'
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove() })
function Location() { const location = useLocation(); return <output>{location.pathname}{location.search}</output> }
it('navigates to instances within the router basename', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<MemoryRouter basename="/c/test" initialEntries={['/c/test/resources/customresourcedefinitions']}><CustomResourceDefinitionRenderer data={{ spec: { group: 'example.io', names: { kind: 'Widget', plural: 'widgets' }, versions: [{ name: 'v1', served: true }] } }} /><Location /></MemoryRouter>))
  act(() => Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('View Widget resources'))!.click())
  expect(host.querySelector('output')?.textContent).toBe('/resources/widgets?apiGroup=example.io')
})
