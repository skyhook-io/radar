import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/** The integrations' sidebar workspaces as one map; undefined when none applies. */
export function useMergedSidebarWorkspaces(a: Workspaces | undefined, b: Workspaces | undefined, c?: Workspaces | undefined): Workspaces | undefined {
  return useMemo(() => (a || b || c ? { ...a, ...b, ...c } : undefined), [a, b, c])
}
