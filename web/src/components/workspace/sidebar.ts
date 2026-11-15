import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/** Two integrations' sidebar workspaces as one map; undefined when neither applies. */
export function useMergedSidebarWorkspaces(a: Workspaces | undefined, b: Workspaces | undefined): Workspaces | undefined {
  return useMemo(() => (a || b ? { ...a, ...b } : undefined), [a, b])
}
