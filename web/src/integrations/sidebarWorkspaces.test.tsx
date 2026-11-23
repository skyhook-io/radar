import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import { useIntegrationSidebars } from './sidebarWorkspaces'

const calls = vi.hoisted(() => ({} as Record<string, any>))
const part = (name: string) => ({ [name]: { tools: name, destinations: [{ id: name, label: name, onSelect: () => {} }] } as SidebarCategoryWorkspace })

vi.mock('../components/cnpg/useCNPGSidebarWorkspace', () => ({ useCNPGSidebarWorkspace: (o: any) => { calls.cnpg = o; return part('CNPG') } }))
vi.mock('../components/velero/useVeleroSidebarWorkspace', () => ({ useVeleroSidebarWorkspace: (o: any) => { calls.velero = o; return part('Velero') } }))
vi.mock('../components/substrate/useSubstrateSidebarWorkspace', () => ({ useSubstrateSidebarWorkspace: (o: any) => { calls.substrate = o; return part('Substrate') } }))
vi.mock('../components/agentsandbox/useAgentSandboxSidebarWorkspace', () => ({ useAgentSandboxSidebarWorkspace: (o: any) => { calls.agentSandbox = o; return part('AgentSandbox') } }))
vi.mock('../components/kagent/useKagentSidebarWorkspace', () => ({ useKagentSidebarWorkspace: (o: any) => { calls.kagent = o; return part('Kagent') } }))

const SRC = join(__dirname, '..')

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return name === 'node_modules' ? [] : sourceFiles(path)
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : []
  })
}

describe('useIntegrationSidebars', () => {
  it('merges every integration’s part, in order, passing each only its own options', () => {
    let merged: Record<string, SidebarCategoryWorkspace> | undefined
    function Host() {
      merged = useIntegrationSidebars({ apiResources: [], namespaces: ['team'], cnpg: { withMetrics: true }, velero: { active: true }, substrate: { active: 'actors' }, agentSandbox: { active: true } })
      return null
    }
    renderToStaticMarkup(<Host />)
    expect(Object.keys(merged ?? {})).toEqual(['CNPG', 'Velero', 'Substrate', 'AgentSandbox', 'Kagent'])
    expect(calls.cnpg).toEqual({ apiResources: [], namespaces: ['team'], withMetrics: true })
    expect(calls.velero).toEqual({ apiResources: [], active: true })
    expect(calls.substrate).toEqual({ apiResources: [], active: 'actors' })
    expect(calls.agentSandbox).toEqual({ apiResources: [], namespaces: ['team'], active: true })
    expect(calls.kagent).toEqual({ apiResources: [], namespaces: ['team'] })
  })

  // An integration's sidebar hook added beside the others, or a view that
  // calls the hooks itself, is how a screen ends up without an integration's
  // row: every hook goes through useIntegrationSidebars, and nothing else
  // calls one.
  it('is the only caller of every integration sidebar hook', () => {
    const files = sourceFiles(SRC)
    const hooks = files.flatMap((f) => [...readFileSync(f, 'utf8').matchAll(/export function (use\w+SidebarWorkspace)\(/g)].map((m) => m[1]))
    expect(hooks.sort()).toEqual(['useAgentSandboxSidebarWorkspace', 'useCNPGSidebarWorkspace', 'useKagentSidebarWorkspace', 'useSubstrateSidebarWorkspace', 'useVeleroSidebarWorkspace'])
    const own = readFileSync(join(SRC, 'integrations/sidebarWorkspaces.ts'), 'utf8')
    const strays: string[] = []
    for (const hook of [...hooks, 'useMergedSidebarWorkspaces']) {
      expect(own, `${hook} is not merged by useIntegrationSidebars`).toMatch(new RegExp(`\\b${hook}\\(`))
      for (const f of files) {
        if (f.endsWith('integrations/sidebarWorkspaces.ts')) continue
        const text = readFileSync(f, 'utf8')
        if (new RegExp(`(?<!function )\\b${hook}\\(`).test(text)) strays.push(`${relative(SRC, f)}: ${hook}`)
      }
    }
    expect(strays).toEqual([])
  })
})
