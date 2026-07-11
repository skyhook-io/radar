import { CheckCircle2, CircleDot, GitBranch, HeartPulse, LayoutGrid, List, Search } from 'lucide-react'

import { GitOpsFacetButton, GitOpsFilterSection } from './GitOpsTableView'
import { Input } from '../ui/Input'
import type { GitOpsResourceTree } from '../../types/gitops-tree'
import type { GitOpsTreePreset } from './tree'

// =============================================================================
// GitOpsGraphFilterRail — the left-side filter sidebar that pairs with
// <GitOpsTreeGraph> in the Topology tab of the GitOps detail page.
//
// Originally inline in OSS radar/web/src/components/gitops/GitOpsView.tsx;
// extracted here so Radar Hub's fleet detail page can mount the same rail
// next to the same graph. Stateless — caller owns preset/search/filter
// state and passes it down; click handlers thread back through callbacks.
//
// buildTreeFacets is the data-prep helper that turns a ResourceTree into
// the facet count buckets the rail consumes. Exported so callers can
// build it once + pass into both this rail and any sibling display.
// =============================================================================

export interface GitOpsTreeFacets {
  kinds: Array<{ name: string; count: number }>
  sync: Array<{ name: string; count: number }>
  health: Array<{ name: string; count: number }>
  namespaces: Array<{ name: string; count: number }>
  roles: Array<{ name: string; count: number }>
}

export function buildTreeFacets(tree: GitOpsResourceTree | null | undefined): GitOpsTreeFacets {
  const nodes = tree?.nodes ?? []
  return {
    kinds: countValues(
      nodes.filter((n) => n.role !== 'group').map((n) => n.ref.kind).filter(Boolean),
    ),
    sync: countValues(nodes.map((n) => n.sync || 'Unknown')),
    health: countValues(nodes.map((n) => n.health || 'Unknown')),
    namespaces: countValues(nodes.map((n) => n.ref.namespace || '(cluster)')),
    roles: countValues(nodes.map((n) => n.role)),
  }
}

export interface GitOpsGraphFilterRailProps {
  facets: GitOpsTreeFacets
  preset: GitOpsTreePreset
  onPresetChange: (preset: GitOpsTreePreset) => void
  search: string
  onSearchChange: (value: string) => void
  kinds: Set<string>
  onToggleKind: (value: string) => void
  sync: Set<string>
  onToggleSync: (value: string) => void
  health: Set<string>
  onToggleHealth: (value: string) => void
  namespaces: Set<string>
  onToggleNamespace: (value: string) => void
  roles: Set<string>
  onToggleRole: (value: string) => void
}

export function GitOpsGraphFilterRail({
  facets,
  preset,
  onPresetChange,
  search,
  onSearchChange,
  kinds,
  onToggleKind,
  sync,
  onToggleSync,
  health,
  onToggleHealth,
  namespaces,
  onToggleNamespace,
  roles,
  onToggleRole,
}: GitOpsGraphFilterRailProps) {
  return (
    <aside className="min-h-0 overflow-y-auto bg-theme-surface/90 max-lg:h-48 max-lg:max-h-48">
      <div className="border-b border-theme-border px-3 py-3">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-theme-text-tertiary" />
          <Input
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder="Filter resources..."
            className="h-8 w-full rounded-md border border-theme-border bg-theme-base pl-8 pr-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
          />
        </div>
      </div>
      <GitOpsFilterSection icon={GitBranch} title="Graph">
        <div className="grid grid-cols-2 gap-1">
          {(['compact', 'workloads', 'app', 'full'] as GitOpsTreePreset[]).map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => onPresetChange(value)}
              className={`rounded-md px-2 py-1.5 text-left text-[11px] font-medium transition-colors ${
                preset === value
                  ? 'bg-skyhook-500 text-white'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
              }`}
            >
              {value === 'app' ? 'Declared' : value[0].toUpperCase() + value.slice(1)}
            </button>
          ))}
        </div>
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={List} title="Kinds">
        {facets.kinds.slice(0, 14).map((item) => (
          <GitOpsFacetButton
            key={item.name}
            label={item.name}
            count={item.count}
            active={kinds.has(item.name)}
            onClick={() => onToggleKind(item.name)}
          />
        ))}
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={CheckCircle2} title="Sync">
        {facets.sync.map((item) => (
          <GitOpsFacetButton
            key={item.name}
            label={item.name}
            count={item.count}
            active={sync.has(item.name)}
            tone={syncTone(item.name)}
            onClick={() => onToggleSync(item.name)}
          />
        ))}
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={HeartPulse} title="Health">
        {facets.health.map((item) => (
          <GitOpsFacetButton
            key={item.name}
            label={item.name}
            count={item.count}
            active={health.has(item.name)}
            tone={healthTone(item.name)}
            onClick={() => onToggleHealth(item.name)}
          />
        ))}
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={CircleDot} title="Role">
        {facets.roles.map((item) => (
          <GitOpsFacetButton
            key={item.name}
            label={roleLabel(item.name)}
            count={item.count}
            active={roles.has(item.name)}
            onClick={() => onToggleRole(item.name)}
          />
        ))}
      </GitOpsFilterSection>
      <GitOpsFilterSection icon={LayoutGrid} title="Namespaces">
        {facets.namespaces.slice(0, 12).map((item) => (
          <GitOpsFacetButton
            key={item.name}
            label={item.name}
            count={item.count}
            active={namespaces.has(item.name)}
            onClick={() => onToggleNamespace(item.name)}
          />
        ))}
      </GitOpsFilterSection>
    </aside>
  )
}

// ----- Helpers shared with consumers -----

// toggleSet is the standard Set<string> add/remove pair used to back the
// rail's facet click handlers. Caller owns the Set state; this just keeps
// the call-site terse.
export function toggleSet(set: Set<string>, setter: (next: Set<string>) => void, value: string) {
  const next = new Set(set)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  setter(next)
}

function countValues(values: string[]): Array<{ name: string; count: number }> {
  const map = new Map<string, number>()
  for (const v of values) {
    if (!v) continue
    map.set(v, (map.get(v) ?? 0) + 1)
  }
  return Array.from(map.entries())
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count)
}

function syncTone(value: string): 'neutral' | 'success' | 'warning' | 'error' | 'info' {
  if (value === 'Synced') return 'success'
  if (value === 'OutOfSync') return 'warning'
  if (value === 'Reconciling') return 'info'
  return 'neutral'
}

function healthTone(value: string): 'neutral' | 'success' | 'warning' | 'error' | 'info' {
  if (value === 'Healthy') return 'success'
  if (value === 'Degraded' || value === 'Missing') return 'error'
  if (value === 'Progressing') return 'info'
  if (value === 'Suspended') return 'warning'
  return 'neutral'
}

function roleLabel(value: string): string {
  return (
    {
      root: 'Root',
      declared: 'Declared',
      generated: 'Generated',
      group: 'Groups',
    } as Record<string, string>
  )[value] ?? value
}
