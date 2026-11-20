// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CRDSchemaTab } from './CRDSchemaTab'

const { read, browser } = vi.hoisted(() => ({ read: vi.fn(), browser: vi.fn() }))
vi.mock('../../../api/client', () => ({ useResource: read }))
vi.mock('../../../context/ThemeContext', () => ({ useTheme: () => ({ theme: 'light' }) }))
vi.mock('@skyhook-io/k8s-ui/components/resources/renderers/CRDSchemaBrowser', () => ({ CRDSchemaBrowser: (props: unknown) => { browser(props); return <div>Schema browser</div> } }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.clearAllMocks() })
it('matches the full-view resource key and passes data and theme to the browser', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const data = { spec: { versions: [] } }
  read.mockReturnValue({ data, isLoading: false, error: null })
  act(() => root.render(<CRDSchemaTab name="widgets.example.io" />))
  expect(read).toHaveBeenCalledWith('customresourcedefinitions', '', 'widgets.example.io', 'apiextensions.k8s.io')
  expect(browser).toHaveBeenCalledWith({ data, theme: 'light' })
  read.mockReturnValue({ isLoading: true })
  act(() => root.render(<CRDSchemaTab name="widgets.example.io" />))
  expect(host.textContent).toContain('Loading schema')
  read.mockReturnValue({ isLoading: false, error: new Error('Access denied') })
  act(() => root.render(<CRDSchemaTab name="widgets.example.io" />))
  expect(host.textContent).toContain('Access denied')
})
