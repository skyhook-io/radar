import { useState, useCallback } from 'react'
import { useAudit, useAuditSettings, useUpdateAuditSettings } from '../../api/client'
import type { SelectedResource } from '../../types'
import { AuditFindingsTable, PaneLoader } from '@skyhook-io/k8s-ui'
import { ArrowLeft, ClipboardCheck, Settings } from 'lucide-react'
import { AuditSettingsDialog } from './AuditSettingsDialog'

interface AuditViewProps {
  namespaces: string[]
  onBack: () => void
  onNavigateToResource: (resource: SelectedResource) => void
}

export function AuditView({ namespaces, onBack, onNavigateToResource }: AuditViewProps) {
  const { data, isLoading, error } = useAudit(namespaces)
  const { data: auditSettings } = useAuditSettings()
  const updateSettings = useUpdateAuditSettings()
  const [showSettings, setShowSettings] = useState(false)

  const ignoredCount = auditSettings?.ignoredNamespaces?.length ?? 0

  // Inline hide actions — persist to settings immediately
  const hideCheck = useCallback((checkID: string) => {
    if (!auditSettings) return
    const current = auditSettings.disabledChecks || []
    if (current.includes(checkID)) return
    updateSettings.mutate({ ...auditSettings, disabledChecks: [...current, checkID] })
  }, [auditSettings, updateSettings])

  const hideCategory = useCallback((category: string) => {
    if (!auditSettings || !data?.checks) return
    const checksInCategory = Object.values(data.checks).filter(c => {
      // Match checks whose findings are in this category
      return data.findings.some(f => f.checkID === c.id && f.category === category)
    }).map(c => c.id)
    const current = auditSettings.disabledChecks || []
    const toAdd = checksInCategory.filter(id => !current.includes(id))
    if (toAdd.length === 0) return
    updateSettings.mutate({ ...auditSettings, disabledChecks: [...current, ...toAdd] })
  }, [auditSettings, data, updateSettings])

  const hideNamespace = useCallback((ns: string) => {
    if (!auditSettings) return
    const current = auditSettings.ignoredNamespaces || []
    if (current.includes(ns)) return
    updateSettings.mutate({ ...auditSettings, ignoredNamespaces: [...current, ns] })
  }, [auditSettings, updateSettings])

  if (isLoading) {
    return <PaneLoader label="Loading audit data…" className="flex-1" />
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load audit data</p>
      </div>
    )
  }

  if (!data) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>No audit data available</p>
      </div>
    )
  }

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
            <h1 className="text-lg font-semibold text-theme-text-primary">Cluster Audit</h1>
          </div>
          <p className="text-sm text-theme-text-tertiary mt-1 ml-7">
            Security, reliability, and efficiency checks based on Kubernetes best practices from NSA/CISA guidelines, CIS benchmarks, and industry tools like Polaris and Kubescape.
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {ignoredCount > 0 && (
            <button onClick={() => setShowSettings(true)} className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors">{ignoredCount} {ignoredCount === 1 ? 'namespace' : 'namespaces'} hidden</button>
          )}
          <button
            onClick={() => setShowSettings(true)}
            className="p-2 rounded-lg hover:bg-theme-hover text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            title="Audit settings"
          >
            <Settings className="w-4 h-4" />
          </button>
        </div>
      </div>

      <AuditFindingsTable
        groups={data.groups}
        checks={data.checks}
        onResourceClick={(kind, namespace, name) =>
          onNavigateToResource({ kind, namespace, name })
        }
        onHideCheck={hideCheck}
        onHideCategory={hideCategory}
        onHideNamespace={hideNamespace}
      />

      {showSettings && <AuditSettingsDialog namespaces={namespaces} onClose={() => setShowSettings(false)} />}
    </div>
  )
}
