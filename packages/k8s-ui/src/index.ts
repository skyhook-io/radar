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

// Checks remediation queue (ChecksView, shared types + severity vocabulary).
// Host-agnostic: Hub feeds fleet-resolved data, OSS can feed a single-cluster
// resolve.
export * from './components/checks'

// Live issues queue (IssuesView — the grouped operational-issue triage queue,
// shared by OSS single-cluster and the hub fleet view; sibling to the Checks
// queue)
export * from './components/issues'

// Cluster switcher (shared trigger+dropdown for OSS Radar and Radar Hub)
export * from './components/cluster-switcher'

// Namespace picker (shared scope-filter trigger+dropdown for OSS Radar and
// Radar Hub — pure presentation, data injected via props)
export * from './components/namespace-switcher'

// Scope pill — the shared bordered shell that groups the cluster + namespace
// segments into one unit (OSS header + Radar Hub cluster top bar)
export * from './components/scope-pill'

// Filter-state contract — shared URL-synced filter state for list views (OSS +
// Hub), router-agnostic via an injected FilterLocation adapter
export * from './filter-state'

// Applications (shared host-agnostic list + detail shell for the deployable-
// software surface; OSS renders single-cluster, Cloud adds the fleet layer)
export * from './components/applications'

// Compare (ResourceCompareView, CompareResourcePicker, normalize utilities)
export * from './components/compare'

// Perf instrumentation (ELK + structureKey timers, surfaced in diagnostics overlay)
export * from './perf'
