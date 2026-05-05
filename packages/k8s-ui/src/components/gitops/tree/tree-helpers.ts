import type { GitOpsTreeNode } from '../../../types'

export interface GitOpsTreeFilters {
  kinds?: Set<string> | string[]
  namespaces?: Set<string> | string[]
  sync?: Set<string> | string[]
  health?: Set<string> | string[]
  roles?: Set<string> | string[]
}

export function gitOpsFilterSet(values?: Set<string> | string[]): Set<string> | undefined {
  if (!values) return undefined
  const set = values instanceof Set ? values : new Set(values)
  return set.size > 0 ? set : undefined
}

export function matchesGitOpsTreeFilters(node: GitOpsTreeNode, filters?: GitOpsTreeFilters): boolean {
  if (!filters) return true
  const kinds = gitOpsFilterSet(filters.kinds)
  const namespaces = gitOpsFilterSet(filters.namespaces)
  const sync = gitOpsFilterSet(filters.sync)
  const health = gitOpsFilterSet(filters.health)
  const roles = gitOpsFilterSet(filters.roles)

  if (kinds && !kinds.has(node.ref.kind)) return false
  if (namespaces && !namespaces.has(node.ref.namespace || '(cluster)')) return false
  if (sync && !sync.has(node.sync || 'Unknown')) return false
  if (health && !health.has(node.health || 'Unknown')) return false
  if (roles && !roles.has(node.role)) return false
  return true
}

export function hasGitOpsTreeFilters(filters?: GitOpsTreeFilters): boolean {
  if (!filters) return false
  return Boolean(
    gitOpsFilterSet(filters.kinds) ||
    gitOpsFilterSet(filters.namespaces) ||
    gitOpsFilterSet(filters.sync) ||
    gitOpsFilterSet(filters.health) ||
    gitOpsFilterSet(filters.roles),
  )
}
