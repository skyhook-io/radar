import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/** Integrations' sidebar workspaces as one map; undefined when none applies. Callers pass a fixed number. */
export function useMergedSidebarWorkspaces(...all: (Workspaces | undefined)[]): Workspaces | undefined {
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => (all.some(Boolean) ? Object.assign({}, ...all) : undefined), all)
}
