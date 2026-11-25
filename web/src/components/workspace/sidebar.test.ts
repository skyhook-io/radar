import { describe, expect, it } from 'vitest'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import { mergeSidebarWorkspaces } from './sidebar'

const part = (tools: string, section: string, over: Partial<SidebarCategoryWorkspace> = {}): SidebarCategoryWorkspace => ({
  tools,
  destinations: [{ id: `${section}-overview`, label: 'Overview', section, onSelect: () => {} }],
  ...over,
})

describe('mergeSidebarWorkspaces', () => {
  it('is undefined when no integration applies, and passes a lone workspace through as it is', () => {
    expect(mergeSidebarWorkspaces([undefined, undefined])).toBeUndefined()
    const velero = { Velero: part('Velero', 'Velero', { label: 'Backups' }) }
    const agents = { Agents: part('Agent Sandbox', 'Sandbox', { count: 2 }) }
    const merged = mergeSidebarWorkspaces([velero, undefined, agents])!
    expect(merged.Velero).toBe(velero.Velero)
    expect(merged.Agents).toBe(agents.Agents)
  })

  it('combines tools sharing a category into one row: both tools beneath, destinations one tool after another', () => {
    const merged = mergeSidebarWorkspaces([
      { Agents: part('Agent Substrate', 'Substrate', { count: 3, countTitle: '3 attention items.' }) },
      { Agents: part('Agent Sandbox', 'Sandbox', { count: 2, countTitle: '2 attention items.', countScope: 'namespace payments', scopeNote: 'Counts for namespace payments' }) },
    ])!.Agents
    expect(merged.tools).toBe('Agent Substrate · Agent Sandbox')
    expect(merged.destinations.map((d) => d.id)).toEqual(['Substrate-overview', 'Sandbox-overview'])
    expect(merged).toMatchObject({ count: 5, countLowerBound: false })
    expect(merged.countTitle).toBe('Attention items — Substrate: 3 · cluster-wide · Sandbox: 2 · namespace payments')
    // Only Sandbox's counts follow the namespace filter, so the note says whose they are.
    expect(merged.scopeNote).toBe('Agent Sandbox: Counts for namespace payments · Agent Substrate: cluster-wide')
  })

  it('adds up to a lower bound when either part is one or is unknown, and to unknown when nothing was found', () => {
    const merge = (a: Partial<SidebarCategoryWorkspace>, b: Partial<SidebarCategoryWorkspace>) =>
      mergeSidebarWorkspaces([{ Agents: part('A', 'a', a) }, { Agents: part('B', 'b', b) }])!.Agents
    expect(merge({ count: 3, countLowerBound: true }, { count: 2 })).toMatchObject({ count: 5, countLowerBound: true })
    expect(merge({ count: null, countLowerBound: true }, { count: 2 })).toMatchObject({ count: 2, countLowerBound: true })
    expect(merge({ count: null, countLowerBound: true }, { count: 0 })).toMatchObject({ count: null, countLowerBound: true })
    expect(merge({ count: 0 }, { count: 0 })).toMatchObject({ count: 0, countLowerBound: false })
    // A part that has not counted yet adds nothing; with neither, the row has no badge.
    expect(merge({}, { count: 2 })).toMatchObject({ count: 2, countLowerBound: false })
    expect(merge({}, {}).count).toBeUndefined()
  })
})
