import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/** Integrations' sidebar workspaces as one map; undefined when none applies. */
// shortcut: fixed arity, since useMemo needs a literal dependency list; add a parameter per new workspace integration.
export function useMergedSidebarWorkspaces(a: Workspaces | undefined, b: Workspaces | undefined, c?: Workspaces): Workspaces | undefined {
  return useMemo(() => (a || b || c ? { ...a, ...b, ...c } : undefined), [a, b, c])
}
