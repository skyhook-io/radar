import type { ComponentType } from 'react'
import { CapacityView } from '../components/capacity/CapacityView'
import { CNPGView } from '../components/cnpg/CNPGView'
import type { WorkspaceRoute, WorkspaceScreenProps } from './workspaceRoute'

const screens: Record<WorkspaceRoute['id'], ComponentType<WorkspaceScreenProps>> = {
  cnpg: CNPGView,
  capacity: CapacityView,
}

export function WorkspaceScreen({ workspace, ...props }: WorkspaceScreenProps & { workspace: WorkspaceRoute }) {
  const Component = screens[workspace.id]
  return <Component {...props} />
}
