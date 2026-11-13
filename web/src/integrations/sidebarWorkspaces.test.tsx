import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

const cnpg = vi.hoisted(() => ({ value: undefined as Record<string, unknown> | undefined }))
vi.mock('../components/cnpg/useCNPGSidebarWorkspace', () => ({ useCNPGSidebarWorkspace: () => cnpg.value }))
vi.mock('../components/logging/useLoggingSidebarWorkspace', () => ({ useLoggingSidebarWorkspace: () => undefined }))

import { mergeSidebarWorkspaces, useSidebarCategoryWorkspaces } from './sidebarWorkspaces'

function probe() {
  let out: unknown
  function Probe() {
    out = useSidebarCategoryWorkspaces({ apiResources: [], namespaces: [], selectedKind: null })
    return null
  }
  renderToStaticMarkup(<Probe />)
  return out
}

const ws = (label: string): SidebarCategoryWorkspace => ({ destinations: [{ id: label, label, onSelect: () => {} }] })

describe('sidebar category workspaces', () => {
  it('is undefined when no integration is discovered', () => {
    expect(mergeSidebarWorkspaces([undefined, undefined])).toBeUndefined()
    cnpg.value = undefined
    expect(probe()).toBeUndefined()
  })
  it('merges every discovered integration by category', () => {
    const merged = mergeSidebarWorkspaces([{ CloudNativePG: ws('a') }, undefined, { Logging: ws('b') }])
    expect(Object.keys(merged!)).toEqual(['CloudNativePG', 'Logging'])
  })
  it('passes CloudNativePG entries through', () => {
    cnpg.value = { CloudNativePG: ws('overview') }
    expect(probe()).toEqual(cnpg.value)
  })
})
