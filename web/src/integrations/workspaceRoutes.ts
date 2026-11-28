import { capacityWorkspaceRoute } from '../components/capacity/workspaceRoute'
import { cnpgWorkspaceRoute } from '../components/cnpg/workspaceRoute'
import { veleroWorkspaceRoute } from '../components/velero/workspaceRoute'
import { kargoWorkspaceRoute } from '../components/kargo/workspaceRoute'
import type { WorkspaceRoute } from './workspaceRoute'

export const workspaceRoutes = [cnpgWorkspaceRoute, capacityWorkspaceRoute, veleroWorkspaceRoute, kargoWorkspaceRoute] as const

export const workspaceLabels = {
  cnpg: cnpgWorkspaceRoute.label,
  capacity: capacityWorkspaceRoute.label,
  velero: veleroWorkspaceRoute.label,
  kargo: kargoWorkspaceRoute.label,
} satisfies Record<WorkspaceRoute['id'], string>

export function workspaceForPath(pathname: string) {
  return workspaceRoutes.find(route => pathname === route.path || pathname.startsWith(`${route.path}/`))
}

export function workspaceForView(view: string) {
  return workspaceRoutes.find(route => route.id === view)
}

export function workspaceContextSwitchSearch(pathname: string, search: URLSearchParams): URLSearchParams {
  return workspaceForPath(pathname)?.contextSwitchSearch?.(pathname, search) ?? new URLSearchParams()
}
