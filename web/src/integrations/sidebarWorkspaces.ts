import { useMemo } from 'react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../types'
import { useCNPGSidebarWorkspace } from '../components/cnpg/useCNPGSidebarWorkspace'
import { useLoggingSidebarWorkspace } from '../components/logging/useLoggingSidebarWorkspace'

export interface SidebarWorkspaceArgs {
  apiResources: APIResource[] | undefined
  namespaces: string[]
  selectedKind: { group?: string } | null
}

type Workspaces = Record<string, SidebarCategoryWorkspace> | undefined

export function mergeSidebarWorkspaces(parts: Workspaces[]): Workspaces {
  const present = parts.filter((p): p is Record<string, SidebarCategoryWorkspace> => !!p)
  return present.length ? Object.assign({}, ...present) : undefined
}

/**
 * The Resources sidebar's workspace destinations, one hook per integration.
 * The list is fixed, so the hooks run in the same order on every render; each
 * returns undefined when its integration isn't discovered.
 */
export function useSidebarCategoryWorkspaces({ apiResources, namespaces, selectedKind }: SidebarWorkspaceArgs): Workspaces {
  const cnpg = useCNPGSidebarWorkspace({ apiResources, namespaces, withMetrics: selectedKind?.group === 'postgresql.cnpg.io' })
  const logging = useLoggingSidebarWorkspace({ apiResources, namespaces, selectedKind })
  return useMemo(() => mergeSidebarWorkspaces([cnpg, logging]), [cnpg, logging])
}
