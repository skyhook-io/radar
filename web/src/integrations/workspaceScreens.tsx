import type { ComponentType } from 'react'
import { AgentSandboxView } from '../components/agentsandbox/AgentSandboxView'
import { CapacityView } from '../components/capacity/CapacityView'
import { CNPGView } from '../components/cnpg/CNPGView'
import { SubstrateView } from '../components/substrate/SubstrateView'
import { VeleroView } from '../components/velero/VeleroView'
import type { WorkspaceRoute, WorkspaceScreenProps } from './workspaceRoute'

const screens: Record<WorkspaceRoute['id'], ComponentType<WorkspaceScreenProps>> = {
  cnpg: CNPGView,
  capacity: ({ onOpenResource }) => <CapacityView onOpenResource={onOpenResource} />,
  velero: VeleroView,
  substrate: SubstrateView,
  'agent-sandbox': AgentSandboxView,
}

export function WorkspaceScreen({ workspace, ...props }: WorkspaceScreenProps & { workspace: WorkspaceRoute }) {
  const Component = screens[workspace.id]
  return <Component {...props} />
}
