// @vitest-environment jsdom
import { act, StrictMode, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { SelectedResource } from '../../types'
import { useWorkspaceDrawer } from './useWorkspaceDrawer'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
let root: Root | null = null
let container: HTMLDivElement | null = null
afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})
const proxy: SelectedResource = {
  kind: 'httpproxies',
  group: 'networking.datumapis.com',
  namespace: 'project',
  name: 'web',
}
const connector: SelectedResource = {
  kind: 'connectors',
  group: 'networking.datumapis.com',
  namespace: 'project',
  name: 'edge',
}
function Probe({ initial }: { initial: SelectedResource | null }) {
  const [selected, setSelected] = useState(initial)
  const location = useLocation()
  const { inspect } = useWorkspaceDrawer(selected, setSelected, () =>
    setSelected(null),
  )
  return (
    <>
      <output>{JSON.stringify({ search: location.search, selected })}</output>
      <button onClick={() => inspect(proxy)}>inspect</button>
      <button onClick={() => setSelected(connector)}>related</button>
      <button onClick={() => setSelected(proxy)}>back</button>
      <button onClick={() => setSelected(null)}>close</button>
    </>
  )
}
function mount(path: string, initial: SelectedResource | null = null) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() =>
    root!.render(
      <StrictMode>
        <MemoryRouter initialEntries={[path]}>
          <Probe initial={initial} />
        </MemoryRouter>
      </StrictMode>,
    ),
  )
}
function value() {
  return JSON.parse(container!.querySelector('output')!.textContent!)
}
function click(text: string) {
  act(() =>
    Array.from(container!.querySelectorAll('button'))
      .find((x) => x.textContent === text)!
      .click(),
  )
}
describe('workspace drawer URL ownership', () => {
  it.each(['/datum', '/cnpg'])(
    'clears a selection left by another view on first mount at %s without a drawer link',
    (path) => {
      mount(path + '?namespaces=project', proxy)
      expect(value()).toEqual({ search: '?namespaces=project', selected: null })
    },
  )
  it('opens an exact-group drawer deep link in MemoryRouter', () => {
    mount('/datum?drawer=httpproxies:networking.datumapis.com:project:web')
    expect(value().selected).toEqual(proxy)
  })
  it('round-trips related drawer trails, back and close without dropping the filter', () => {
    mount('/datum?namespaces=project')
    click('inspect')
    click('related')
    expect(new URLSearchParams(value().search).get('drawer')).toBe(
      'httpproxies:networking.datumapis.com:project:web~connectors:networking.datumapis.com:project:edge',
    )
    click('back')
    expect(value().selected).toEqual(proxy)
    expect(new URLSearchParams(value().search).get('drawer')).toBe(
      'httpproxies:networking.datumapis.com:project:web',
    )
    click('close')
    expect(value()).toEqual({ search: '?namespaces=project', selected: null })
  })
})
