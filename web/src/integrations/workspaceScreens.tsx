import type { ComponentType } from 'react'
import { CapacityView } from '../components/capacity/CapacityView'
import { CNPGView } from '../components/cnpg/CNPGView'
import { LoggingView } from '../components/logging/LoggingView'
import type { WorkspaceRoute, WorkspaceScreenProps } from './workspaceRoute'

const screens: Record<WorkspaceRoute['id'], ComponentType<WorkspaceScreenProps>> = {
  cnpg: CNPGView,
  capacity: ({ onOpenResource }) => <CapacityView onOpenResource={onOpenResource} />,
  logging: LoggingView,
}

export function WorkspaceScreen({ workspace, ...props }: WorkspaceScreenProps & { workspace: WorkspaceRoute }) {
  const Component = screens[workspace.id]
  return <Component {...props} />
}
