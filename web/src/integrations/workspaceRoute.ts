import type { SelectedResource } from '../types'

export interface WorkspaceScreenProps {
  namespaces: string[]
  selectedResource: SelectedResource | null
  onOpenResource: (resource: SelectedResource) => void
  onCloseResource: () => void
  onClearNamespaces: () => void
}

export interface WorkspaceRoute {
  id: 'cnpg' | 'capacity' | 'logging'
  path: string
  label: string
  navView: 'resources' | 'capacity'
  namespaceScope: 'view' | 'cluster'
  namespaceFilterTooltip?: string
  pageTitle: (pathname: string) => string
  contextSwitchSearch?: (pathname: string, search: URLSearchParams) => URLSearchParams
}
