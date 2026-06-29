import { useMemo } from 'react'
import { Home, Network, List, Clock, Package, Activity, Sun, Stethoscope, DollarSign, ShieldCheck, GitBranch, AlertTriangle, Boxes, Server } from 'lucide-react'
import { useNamespaces, useContexts } from '../../api/client'
import { CORE_RESOURCES, useAPIResources } from '../../api/apiResources'
import { getResourceIcon } from '../../utils/resource-icons'
import { parseContextName } from '../../utils/context-name'

// Drop the disambiguating " (source)" suffix the context list appends, so the
// GKE/EKS/AKS parser sees the bare context name (mirrors the cluster picker).
function stripSourceSuffix(name: string, source?: string): string {
  if (!source) return name
  const escaped = source.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return name.replace(new RegExp(`\\s+\\(${escaped}(?:\\s+#\\d+)?\\)$`), '')
}

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
  { view: 'traffic', label: 'Live Traffic', icon: Activity, shortcut: 'g f' },
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
        // Group shown only when it disambiguates (CRDs) — "core" is noise on
        // built-in kinds. priorityBonus still nudges core/well-known above CRDs.
        id: `kind-${r.name}-${r.group}`, label: r.kind, sublabel: r.group || undefined, category: 'Resource Kinds',
        icon: getResourceIcon(r.kind), action: () => cb.onNavigateKind(r.name, r.group),
        searchTerms: [r.name, r.kind], priorityBonus: groupPriorityBonus(r.group),
      })
    }

    if (contexts) {
      // Show the friendly parsed cluster name (like the cluster picker), not the
      // raw ARN/gke context string. provider/region may live in the cluster id
      // (e.g. EKS ARN) when the context name is already friendly — fall back to
      // it. Count display names so genuine duplicates (same cluster name from
      // different kubeconfig sources) stay distinguishable; unique ones stay clean.
      const parsedCtx = contexts.map((ctx) => {
        const parsed = parseContextName(stripSourceSuffix(ctx.name, ctx.source))
        const fromCluster = ctx.cluster ? parseContextName(ctx.cluster) : null
        const meta = [parsed.provider ?? fromCluster?.provider, parsed.region ?? fromCluster?.region].filter(Boolean).join(' · ')
        return { ctx, clusterName: parsed.clusterName, account: parsed.account, base: ctx.isCurrent ? 'current' : meta }
      })
      // Disambiguate on the FINAL visible (label, sublabel) pair, not just the
      // cluster name — same name + same provider/region from the same kubeconfig
      // file would otherwise render identically while switching different
      // contexts. Collisions fall back to the raw context name (unique by id).
      const pairCount = new Map<string, number>()
      for (const p of parsedCtx) pairCount.set(`${p.clusterName}\x00${p.base}`, (pairCount.get(`${p.clusterName}\x00${p.base}`) ?? 0) + 1)
      for (const { ctx, clusterName, account, base } of parsedCtx) {
        const collides = (pairCount.get(`${clusterName}\x00${base}`) ?? 0) > 1
        const sub = [base, collides ? ctx.name : ''].filter(Boolean).join(' · ')
        result.push({
          id: `context-${ctx.name}`,
          label: clusterName,
          sublabel: sub || undefined,
          category: 'Clusters',
          icon: Server,
          action: () => { if (!ctx.isCurrent) cb.onSwitchContext(ctx.name) },
          searchTerms: [ctx.name, account || ''].filter(Boolean),
        })
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
