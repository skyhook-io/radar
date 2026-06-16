import { useMemo } from 'react'
import { Home, Network, List, Clock, Package, Activity, Sun, Stethoscope, DollarSign, ShieldCheck, GitBranch, AlertTriangle, Boxes } from 'lucide-react'
import { useNamespaces, useContexts } from '../../api/client'
import { CORE_RESOURCES, useAPIResources } from '../../api/apiResources'
import { getResourceIcon } from '../../utils/resource-icons'

export type MainView = 'home' | 'topology' | 'resources' | 'timeline' | 'issues' | 'helm' | 'traffic' | 'cost' | 'checks' | 'gitops' | 'applications'

export interface CommandItem {
  id: string
  label: string
  sublabel?: string
  category: string
  icon?: React.ComponentType<{ className?: string }>
  shortcut?: string
  action: () => void
  /** Extra terms to match against during search (not displayed). */
  searchTerms?: string[]
  /** Small priority bonus added to the final score (only if the item matched). */
  priorityBonus?: number
}

// Built-in k8s API groups. Used to nudge these above CRDs on tied matches.
const CORE_GROUP_BONUS = 10
const WELL_KNOWN_GROUP_BONUS = 5
const WELL_KNOWN_GROUPS = new Set([
  'apps', 'batch', 'autoscaling', 'policy', 'networking.k8s.io', 'rbac.authorization.k8s.io',
  'storage.k8s.io', 'scheduling.k8s.io', 'coordination.k8s.io', 'apiextensions.k8s.io',
  'admissionregistration.k8s.io', 'apiregistration.k8s.io', 'certificates.k8s.io',
  'events.k8s.io', 'discovery.k8s.io', 'flowcontrol.apiserver.k8s.io', 'node.k8s.io',
  'authentication.k8s.io', 'authorization.k8s.io',
])

function groupPriorityBonus(group: string): number {
  if (!group) return CORE_GROUP_BONUS
  if (WELL_KNOWN_GROUPS.has(group)) return WELL_KNOWN_GROUP_BONUS
  return 0
}

// Fuzzy match scoring: exact > prefix > word boundary > substring. Within a
// tier, a coverage bonus (up to +20) breaks ties in favor of shorter labels.
export function scoreMatch(text: string, query: string): number {
  const lower = text.toLowerCase()
  const q = query.toLowerCase()
  if (!lower.includes(q)) return 0
  let base: number
  if (lower === q) base = 150
  else if (lower.startsWith(q)) base = 100
  else {
    const wordStart = lower.indexOf(q)
    const prev = lower[wordStart - 1]
    base = wordStart > 0 && (prev === ' ' || prev === '/' || prev === '-' || prev === '.') ? 75 : 50
  }
  return base + (q.length / lower.length) * 20
}

export function bestScore(item: CommandItem, query: string): number {
  let best = scoreMatch(item.label, query)
  const secondary = Math.floor(Math.max(scoreMatch(item.sublabel || '', query), scoreMatch(item.category, query)) * 0.6)
  best = Math.max(best, secondary)
  if (item.searchTerms) {
    for (const term of item.searchTerms) best = Math.max(best, scoreMatch(term, query))
  }
  return best > 0 ? best + (item.priorityBonus || 0) : 0
}

export interface CommandItemCallbacks {
  onNavigateView: (view: MainView) => void
  onNavigateKind: (kind: string, group: string) => void
  onSwitchContext: (name: string) => void
  onSetNamespaces: (ns: string[]) => void
  onToggleTheme: () => void
  onShowDiagnostics?: () => void
}

const VIEW_ENTRIES: { view: MainView; label: string; icon: React.ComponentType<{ className?: string }>; shortcut: string }[] = [
  { view: 'home', label: 'Home', icon: Home, shortcut: 'g h' },
  { view: 'resources', label: 'Resources', icon: List, shortcut: 'g r' },
  { view: 'issues', label: 'Issues', icon: AlertTriangle, shortcut: 'g i' },
  { view: 'topology', label: 'Topology', icon: Network, shortcut: 'g t' },
  { view: 'applications', label: 'Applications', icon: Boxes, shortcut: 'g a' },
  { view: 'timeline', label: 'Timeline', icon: Clock, shortcut: 'g l' },
  { view: 'helm', label: 'Helm', icon: Package, shortcut: 'g m' },
  { view: 'gitops', label: 'GitOps', icon: GitBranch, shortcut: 'g o' },
  { view: 'traffic', label: 'Traffic', icon: Activity, shortcut: 'g f' },
  { view: 'checks', label: 'Checks', icon: ShieldCheck, shortcut: 'g u' },
  { view: 'cost', label: 'Cost', icon: DollarSign, shortcut: 'g c' },
]

// The static command-palette items (Views, Resource Kinds, Contexts,
// Namespaces, Actions) — shared by the centered modal (embedded) and the
// standalone omnibar so the two never drift.
export function useCommandItems(cb: CommandItemCallbacks): CommandItem[] {
  const { data: namespacesData } = useNamespaces()
  const { data: contexts } = useContexts()
  const { data: apiResources } = useAPIResources()

  return useMemo<CommandItem[]>(() => {
    const result: CommandItem[] = []

    for (const v of VIEW_ENTRIES) {
      result.push({ id: `view-${v.view}`, label: `Go to ${v.label}`, category: 'Views', icon: v.icon, shortcut: v.shortcut, action: () => cb.onNavigateView(v.view) })
    }

    const resources = apiResources || CORE_RESOURCES
    const seenKinds = new Set<string>()
    for (const r of resources) {
      if (!r.verbs?.includes('list')) continue
      const kindKey = `${r.name}/${r.group}`
      if (seenKinds.has(kindKey)) continue
      seenKinds.add(kindKey)
      result.push({
        id: `kind-${r.name}-${r.group}`, label: r.kind, sublabel: r.group || 'core', category: 'Resource Kinds',
        icon: getResourceIcon(r.kind), action: () => cb.onNavigateKind(r.name, r.group),
        searchTerms: [r.name, r.kind], priorityBonus: groupPriorityBonus(r.group),
      })
    }

    if (contexts) {
      for (const ctx of contexts) {
        result.push({ id: `context-${ctx.name}`, label: ctx.name, sublabel: ctx.isCurrent ? 'current' : ctx.cluster, category: 'Contexts', action: () => { if (!ctx.isCurrent) cb.onSwitchContext(ctx.name) } })
      }
    }

    if (namespacesData) {
      for (const ns of namespacesData) {
        result.push({ id: `ns-${ns.name}`, label: ns.name, category: 'Namespaces', action: () => cb.onSetNamespaces([ns.name]) })
      }
      result.push({ id: 'ns-all', label: 'All Namespaces', category: 'Namespaces', action: () => cb.onSetNamespaces([]) })
    }

    result.push({ id: 'action-theme', label: 'Toggle Theme', category: 'Actions', icon: Sun, shortcut: 't', action: () => cb.onToggleTheme() })
    if (cb.onShowDiagnostics) {
      result.push({ id: 'action-diagnostics', label: 'Diagnostics', category: 'Actions', icon: Stethoscope, action: () => cb.onShowDiagnostics?.(), searchTerms: ['debug', 'health', 'status', 'snapshot'] })
    }

    return result
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiResources, contexts, namespacesData, cb.onNavigateView, cb.onNavigateKind, cb.onSwitchContext, cb.onSetNamespaces, cb.onToggleTheme, cb.onShowDiagnostics])
}
