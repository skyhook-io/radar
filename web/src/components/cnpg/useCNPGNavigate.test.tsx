// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { useCNPGNavigate } from './useCNPGNavigate'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null
let container: HTMLDivElement | null = null
afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
  root = null
})

function Probe({ to, onLocation }: { to: string; onLocation: (path: string) => void }) {
  const navigate = useCNPGNavigate()
  const location = useLocation()
  useEffect(() => {
    onLocation(location.pathname + location.search)
  }, [location, onLocation])
  return (
    <button type="button" onClick={() => navigate(to)}>
      go
    </button>
  )
}

function landAt(start: string, to: string): string {
  let at = ''
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() =>
    root!.render(
      <MemoryRouter initialEntries={[start]}>
        <Probe to={to} onLocation={(p) => (at = p)} />
      </MemoryRouter>,
    ),
  )
  act(() => container!.querySelector('button')!.click())
  return at
}

describe('useCNPGNavigate', () => {
  it('keeps the namespace filter, which the app would otherwise reset to All namespaces', () => {
    expect(landAt('/cnpg?namespaces=pg', '/cnpg/operator')).toBe('/cnpg/operator?namespaces=pg')
    expect(landAt('/cnpg?namespaces=pg', '/workload/services/pg/pg-rw?tab=reachability')).toBe('/workload/services/pg/pg-rw?tab=reachability&namespaces=pg')
  })
  it('leaves a destination that names its own namespaces alone', () => {
    expect(landAt('/cnpg?namespaces=pg', '/cnpg?namespaces=other')).toBe('/cnpg?namespaces=other')
  })
  it('keeps namespace and investigation parameters when opening cluster-scoped Declarations', () => {
    expect(landAt('/cnpg/clusters/db/pg?tab=spec&namespaces=db&ai-run=run-1', '/cnpg/declarations?cluster=db%2Fpg')).toBe('/cnpg/declarations?cluster=db%2Fpg&namespaces=db&ai-run=run-1')
  })
})
