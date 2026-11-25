import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/**
 * The Resources sidebar category the agent runtimes' kinds are listed under
 * (api-resources.ts). Agent Substrate and Agent Sandbox each contribute a
 * workspace to it, merged into one Integrations row.
 */
export const AGENTS_SIDEBAR_CATEGORY = 'Agents'

/** Integrations' sidebar workspaces as one map; undefined when none applies. */
export function useMergedSidebarWorkspaces(
  a: Workspaces | undefined,
  b?: Workspaces | undefined,
  c?: Workspaces | undefined,
  d?: Workspaces | undefined,
): Workspaces | undefined {
  return useMemo(() => mergeSidebarWorkspaces([a, b, c, d]), [a, b, c, d])
}

/**
 * Workspaces keyed by category, where several tools may contribute to one
 * category: their destinations are listed one tool after another, and the
 * row's count adds theirs up.
 */
export function mergeSidebarWorkspaces(list: (Workspaces | undefined)[]): Workspaces | undefined {
  if (!list.some(Boolean)) return undefined
  const byCategory = new Map<string, SidebarCategoryWorkspace[]>()
  for (const workspaces of list) {
    for (const [name, w] of Object.entries(workspaces ?? {})) byCategory.set(name, [...(byCategory.get(name) ?? []), w])
  }
  return Object.fromEntries([...byCategory].map(([name, parts]) => [name, parts.length === 1 ? parts[0] : combine(parts)]))
}

function shortRuntimeName(tools?: string): string | undefined {
  return tools ? tools.replace(/^Agent /, '') : undefined
}

function scopeOf(w: SidebarCategoryWorkspace): string | undefined {
  if (w.countScope) return w.countScope
  if (w.tools === 'Agent Substrate') return 'cluster-wide'
  return undefined
}

function runtimeCountBreakdown(w: SidebarCategoryWorkspace): string {
  const label = shortRuntimeName(w.tools)
  const val = w.count === null ? 'unknown' : `${w.countLowerBound ? '≥' : ''}${w.count}`
  const scope = scopeOf(w)
  return [label ? `${label}: ${val}` : val, scope].filter(Boolean).join(' · ')
}

function combine(parts: SidebarCategoryWorkspace[]): SidebarCategoryWorkspace {
  const named = (w: SidebarCategoryWorkspace, text: string) => (w.tools ? `${w.tools}: ${text}` : text)
  // A part with no count yet adds nothing; an unknown one makes the sum a lower bound.
  const counted = parts.filter((w) => w.count !== undefined)
  const sum = counted.reduce((n, w) => n + (w.count ?? 0), 0)
  const lowerBound = counted.some((w) => w.count === null || w.countLowerBound)
  const breakdown = counted.map(runtimeCountBreakdown).join(' · ')
  const notes = parts.flatMap((w) => (w.scopeNote ? [named(w, w.scopeNote)] : []))
  if (notes.length > 0) {
    for (const w of parts) {
      if (!w.scopeNote && scopeOf(w) === 'cluster-wide' && w.tools) {
        notes.push(`${w.tools}: cluster-wide`)
      }
    }
  }
  return {
    label: parts.find((w) => w.label)?.label,
    icon: parts.find((w) => w.icon)?.icon,
    tools: parts.flatMap((w) => (w.tools ? [w.tools] : [])).join(' · ') || undefined,
    destinations: parts.flatMap((w) => w.destinations),
    count: counted.length === 0 ? undefined : sum === 0 && lowerBound ? null : sum,
    countLowerBound: counted.length === 0 ? undefined : lowerBound,
    countTitle: counted.length ? `Attention items — ${breakdown}` : undefined,
    scopeNote: notes.length ? notes.join(' · ') : undefined,
  }
}
