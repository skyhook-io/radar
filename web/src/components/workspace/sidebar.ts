import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

/** Several integrations' sidebar workspaces as one map; undefined when none applies. */
export function useMergedSidebarWorkspaces(
  ...maps: (Record<string, SidebarCategoryWorkspace> | undefined)[]
): Record<string, SidebarCategoryWorkspace> | undefined {
  return useMemo(() => {
    const present = maps.filter((m): m is Record<string, SidebarCategoryWorkspace> => !!m)
    return present.length ? Object.assign({}, ...present) : undefined
  }, maps) // eslint-disable-line react-hooks/exhaustive-deps
}
