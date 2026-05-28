import { useState, useCallback } from 'react'
import { useAudit, useAuditSettings, useUpdateAuditSettings } from '../../api/client'
import type { SelectedResource } from '../../types'
import { ChecksView, PaneLoader, type CheckResourceRef } from '@skyhook-io/k8s-ui'
import { ArrowLeft, ClipboardCheck, Settings } from 'lucide-react'
import { AuditSettingsDialog } from './AuditSettingsDialog'

interface AuditViewProps {
  namespaces: string[]
  onBack: () => void
  onNavigateToResource: (resource: SelectedResource) => void
}

// The per-cluster Checks surface. Renders the same shared remediation queue
// (ChecksView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. The rollup + priority
// come pre-computed from radar's /api/audit (pkg/audit.BuildChecks); local
// ~/.radar settings are this cluster's "policy" and the row hide-menu writes to
// them.
export function AuditView({ namespaces, onBack, onNavigateToResource }: AuditViewProps) {
  const { data, isLoading, error } = useAudit(namespaces)
  const { data: auditSettings } = useAuditSettings()
  const updateSettings = useUpdateAuditSettings()
  const [showSettings, setShowSettings] = useState(false)

  const ignoredCount = auditSettings?.ignoredNamespaces?.length ?? 0

  // Inline hide actions — persist to local settings immediately.
  const hideCheck = useCallback((checkID: string) => {
    if (!auditSettings) return
    const current = auditSettings.disabledChecks || []
    if (current.includes(checkID)) return
    updateSettings.mutate({ ...auditSettings, disabledChecks: [...current, checkID] })
  }, [auditSettings, updateSettings])

  const hideCategory = useCallback((category: string) => {
    if (!auditSettings || !data?.checks) return
    const checksInCategory = Object.values(data.checks)
      .filter((c) => data.findings.some((f) => f.checkID === c.id && f.category === category))
      .map((c) => c.id)
    const current = auditSettings.disabledChecks || []
    const toAdd = checksInCategory.filter((id) => !current.includes(id))
    if (toAdd.length === 0) return
    updateSettings.mutate({ ...auditSettings, disabledChecks: [...current, ...toAdd] })
  }, [auditSettings, data, updateSettings])

  if (isLoading) {
    return <PaneLoader label="Loading checks…" className="flex-1" />
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load checks</p>
      </div>
    )
  }

  if (!data) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>No check data available</p>
      </div>
    )
  }

  const onResourceClick = (ref: CheckResourceRef) =>
    onNavigateToResource({ kind: ref.kind, namespace: ref.namespace, name: ref.name, group: ref.group })

  return (
    <div className="flex-1 flex flex-col min-h-0 p-6 gap-6 overflow-auto">
      {/* Header */}
      <div className="flex items-center gap-4">
        <button
          onClick={onBack}
          className="p-1.5 rounded-lg hover:bg-theme-hover transition-colors"
        >
          <ArrowLeft className="w-5 h-5 text-theme-text-secondary" />
        </button>
        <div className="flex-1">
          <div className="flex items-center gap-2">
            <ClipboardCheck className="w-5 h-5 text-theme-text-secondary" />
            <h1 className="text-lg font-semibold text-theme-text-primary">Checks</h1>
          </div>
          <p className="text-sm text-theme-text-tertiary mt-1 ml-7">
            Security, reliability, and efficiency best practices (NSA/CISA, CIS, Polaris, Kubescape), grouped into a remediation queue.
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {ignoredCount > 0 && (
            <button onClick={() => setShowSettings(true)} className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors">{ignoredCount} {ignoredCount === 1 ? 'namespace' : 'namespaces'} hidden</button>
          )}
          <button
            onClick={() => setShowSettings(true)}
            className="p-2 rounded-lg hover:bg-theme-hover text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            title="Checks settings"
          >
            <Settings className="w-4 h-4" />
          </button>
        </div>
      </div>

      <ChecksView
        checks={data.groupedChecks ?? []}
        catalog={data.checks ?? {}}
        anyData
        onResourceClick={onResourceClick}
        onHideCheck={hideCheck}
        onHideCategory={hideCategory}
      />

      {showSettings && <AuditSettingsDialog namespaces={namespaces} onClose={() => setShowSettings(false)} />}
    </div>
  )
}
