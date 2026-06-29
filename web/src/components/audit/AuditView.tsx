import { useState, useCallback } from 'react'
import { useAudit, useAuditSettings, useUpdateAuditSettings, useCloudRole } from '../../api/client'
import type { SelectedResource } from '../../types'
import { ChecksView, PaneLoader, PageHeader, type CheckResourceRef } from '@skyhook-io/k8s-ui'
import { ShieldCheck, Settings } from 'lucide-react'
import { AuditSettingsDialog } from './AuditSettingsDialog'
import { Tooltip } from '../ui/Tooltip'

interface AuditViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

// The per-cluster Checks surface. Renders the same shared remediation queue
// (ChecksView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. The rollup + priority
// come pre-computed from radar's /api/audit (pkg/audit.BuildChecks); local
// ~/.radar settings are this cluster's "policy" and the row hide-menu writes to
// them.
export function AuditView({ namespaces, onNavigateToResource }: AuditViewProps) {
  const { data, isLoading, error } = useAudit(namespaces)
  const { data: auditSettings } = useAuditSettings()
  const updateSettings = useUpdateAuditSettings()
  // Audit policy is owner-gated (enforced server-side). Withhold the inline
  // hide affordances from non-owners so they don't click into a 403 — the
  // hide menus render only when these callbacks are passed.
  const { canAtLeast } = useCloudRole()
  const canEdit = canAtLeast('owner')
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
    <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <PageHeader
        icon={ShieldCheck}
        title="Checks"
        description="Security, reliability, and efficiency best practices (NSA/CISA, CIS, Polaris, Kubescape), grouped into a remediation queue."
        actions={
          <>
            {ignoredCount > 0 && (
              <button onClick={() => setShowSettings(true)} className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors">{ignoredCount} {ignoredCount === 1 ? 'namespace' : 'namespaces'} hidden</button>
            )}
            <Tooltip content="Checks settings">
            <button
              onClick={() => setShowSettings(true)}
              className="p-2 rounded-lg hover:bg-theme-hover text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            >
              <Settings className="w-4 h-4" />
            </button>
            </Tooltip>
          </>
        }
      />

      <ChecksView
        checks={data.groupedChecks ?? []}
        catalog={data.checks ?? {}}
        anyData
        onResourceClick={onResourceClick}
        onHideCheck={canEdit ? hideCheck : undefined}
        onHideCategory={canEdit ? hideCategory : undefined}
      />

      {showSettings && <AuditSettingsDialog namespaces={namespaces} onClose={() => setShowSettings(false)} />}
    </div>
  )
}
