// @skyhook/k8s-ui — Shared Kubernetes UI types, utilities, and components
// Used by both radar (OSS) and koala-frontend (Skyhook platform)

// Types
export * from './types'

// Utilities
export * from './utils'

// Resource utilities (status extractors, formatters)
export * from './components/resources'

// UI primitives
export * from './components/ui'

// Hooks
export * from './hooks'

// Logs
export * from './components/logs'

// Timeline
export * from './components/timeline'

// GitOps
export * from './components/gitops'

// Shared components (ResourceRendererDispatch, EditableYamlView, ResourceActionsBar)
export * from './components/shared'

// Workload components (WorkloadView, ResourceDetailDrawer)
export * from './components/workload'

// Dock (DockProvider, BottomDock, useDock, useOpenLogs, useOpenWorkloadLogs, useOpenTerminal)
export * from './components/dock'

// Topology (TopologyGraph, TopologySearch, TopologyFilterSidebar, layout utilities)
export * from './components/topology'

// Cluster audit (AuditCard, AuditAlerts, AuditFindingsTable)
export * from './components/audit'

// Cluster switcher (shared trigger+dropdown for OSS Radar and Radar Hub)
export * from './components/cluster-switcher'

// Compare (ResourceCompareView, CompareResourcePicker, normalize utilities)
export * from './components/compare'
