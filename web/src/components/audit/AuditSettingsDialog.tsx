import { useState, useEffect, useMemo } from 'react'
import { X, Plus, Trash2, Lock } from 'lucide-react'
import { clsx } from 'clsx'
import { useAuditSettings, useUpdateAuditSettings, useAudit, useCloudRole } from '../../api/client'
import type { CheckMeta } from '@skyhook-io/k8s-ui'
import { validateRFC1123Label, type ValidationResult } from '@skyhook-io/k8s-ui/utils/validators'
import { Tooltip } from '../ui/Tooltip'
import { Input } from '@skyhook-io/k8s-ui'

interface AuditSettingsDialogProps {
  namespaces: string[]
  onClose: () => void
}

export function AuditSettingsDialog({ namespaces, onClose }: AuditSettingsDialogProps) {
  const { data: settings } = useAuditSettings()
  const { data: auditData } = useAudit(namespaces)
  const updateSettings = useUpdateAuditSettings()
  // Audit policy is cluster-shared, so writes are owner-gated (enforced
  // server-side too). Non-owners get a read-only view. Non-Cloud callers
  // have no role and pass.
  const { canAtLeast } = useCloudRole()
  const canEdit = canAtLeast('owner')
  const [ignoredNs, setIgnoredNs] = useState<string[]>([])
  const [disabledChecks, setDisabledChecks] = useState<string[]>([])
  const [newNs, setNewNs] = useState('')

  useEffect(() => {
    if (settings) {
      setIgnoredNs(settings.ignoredNamespaces || [])
      setDisabledChecks(settings.disabledChecks || [])
    }
  }, [settings])

  // Get all available checks from the audit response
  const allChecks: CheckMeta[] = auditData?.checks
    ? Object.values(auditData.checks).sort((a, b) => a.title.localeCompare(b.title))
    : []

  // Validate the staged namespace input against RFC 1123. Saving a bogus
  // entry would silently never match anything in the audit pipeline,
  // leaving the user thinking the ignore filter doesn't work.
  const newNsTrimmed = newNs.trim()
  const newNsValidation = useMemo<ValidationResult>(
    () => (newNsTrimmed === '' ? { valid: true } : validateRFC1123Label(newNsTrimmed)),
    [newNsTrimmed],
  )
  const newNsError = newNsValidation.valid ? null : newNsValidation.error
  const newNsDuplicate = newNsTrimmed !== '' && ignoredNs.includes(newNsTrimmed)
  const canAddNamespace =
    newNsTrimmed !== '' && newNsValidation.valid && !newNsDuplicate

  const addNamespace = () => {
    if (!canAddNamespace) return
    setIgnoredNs([...ignoredNs, newNsTrimmed])
    setNewNs('')
  }

  const toggleCheck = (checkID: string) => {
    if (disabledChecks.includes(checkID)) {
      setDisabledChecks(disabledChecks.filter(c => c !== checkID))
    } else {
      setDisabledChecks([...disabledChecks, checkID])
    }
  }

  const handleSave = () => {
    updateSettings.mutate(
      { ignoredNamespaces: ignoredNs, disabledChecks },
      { onSuccess: () => onClose() },
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div className="bg-theme-surface rounded-xl shadow-xl w-full max-w-lg mx-4 max-h-[80vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-4 border-b border-theme-border shrink-0">
          <h2 className="text-sm font-semibold text-theme-text-primary">Checks Settings</h2>
          <button onClick={onClose} className="p-1 rounded-lg hover:bg-theme-hover transition-colors">
            <X className="w-4 h-4 text-theme-text-tertiary" />
          </button>
        </div>

        <div className="px-5 py-4 overflow-y-auto flex-1">
          {!canEdit && (
            <div className="mb-4 rounded-lg border border-theme-border bg-theme-elevated/50 p-3 flex items-start gap-2.5">
              <Lock className="w-3.5 h-3.5 mt-0.5 shrink-0 text-theme-text-tertiary" />
              <p className="text-xs text-theme-text-tertiary">
                Audit policy is shared across everyone using this Radar instance, so editing
                is limited to owners. You can review the current settings here.
              </p>
            </div>
          )}
          {/* Ignored Namespaces */}
          <div className="mb-6">
            <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider">
              Ignored Namespaces
            </label>
            <p className="text-xs text-theme-text-tertiary mt-1 mb-3">
              Findings in these namespaces are hidden from all views.
            </p>

            <div className="flex flex-col gap-1.5 mb-3">
              {ignoredNs.map(ns => (
                <div key={ns} className="flex items-center justify-between px-3 py-1.5 bg-theme-elevated rounded-lg">
                  <span className="text-sm text-theme-text-primary">{ns}</span>
                  <button
                    onClick={() => setIgnoredNs(ignoredNs.filter(n => n !== ns))}
                    disabled={!canEdit}
                    className="p-1 rounded hover:bg-theme-hover text-theme-text-tertiary hover:text-red-400 transition-colors disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-theme-text-tertiary"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </div>
              ))}
              {ignoredNs.length === 0 && (
                <div className="text-xs text-theme-text-tertiary py-2">No namespaces ignored.</div>
              )}
            </div>

            <div className="flex gap-2">
              <Input
                value={newNs}
                onChange={e => setNewNs(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') addNamespace() }}
                placeholder="Add namespace..."
                disabled={!canEdit}
                aria-invalid={newNsError ? true : undefined}
                aria-describedby="new-ns-help"
                className={clsx(
                  'flex-1 px-3 py-1.5 bg-theme-elevated border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2',
                  newNsError || newNsDuplicate
                    ? 'border-red-500/60 focus:ring-red-500'
                    : 'border-theme-border-light focus:ring-skyhook-500',
                )}
              />
              <button
                onClick={addNamespace}
                disabled={!canEdit || !canAddNamespace}
                className="px-3 py-1.5 text-sm btn-brand rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Plus className="w-4 h-4" />
              </button>
            </div>
            {(newNsError || newNsDuplicate) && (
              <p id="new-ns-help" className="mt-1.5 text-xs text-red-400">
                {newNsDuplicate
                  ? `"${newNsTrimmed}" is already in the list.`
                  : `Namespace ${newNsError}.`}
              </p>
            )}
          </div>

          {/* Disabled Checks */}
          <div>
            <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider">
              Enabled Checks
            </label>
            <p className="text-xs text-theme-text-tertiary mt-1 mb-3">
              Uncheck to disable specific checks globally across all views.
            </p>

            <div className="flex flex-col gap-0.5">
              {allChecks.map(check => {
                const disabled = disabledChecks.includes(check.id)
                return (
                  <label
                    key={check.id}
                    className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-theme-hover/50 cursor-pointer transition-colors"
                  >
                    <input
                      type="checkbox"
                      checked={!disabled}
                      onChange={() => toggleCheck(check.id)}
                      disabled={!canEdit}
                      className="w-4 h-4 rounded border-theme-border text-skyhook-500 focus:ring-skyhook-500 disabled:opacity-40 disabled:cursor-not-allowed"
                    />
                    <div className="flex-1 min-w-0">
                      <span className="text-sm text-theme-text-primary">{check.title}</span>
                      <p className="text-xs text-theme-text-tertiary truncate">{check.description}</p>
                    </div>
                  </label>
                )
              })}
            </div>
          </div>
        </div>

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-theme-border shrink-0">
          <button
            onClick={onClose}
            className="px-4 py-1.5 text-sm text-theme-text-secondary hover:text-theme-text-primary bg-theme-elevated hover:bg-theme-hover border border-theme-border rounded-lg transition-colors"
          >
            Cancel
          </button>
          <Tooltip
            content={
              !canEdit
                ? 'Audit settings can only be changed by owners'
                : newNsError
                  ? 'Fix or clear the pending namespace input before saving'
                  : newNsDuplicate
                    ? 'Clear the duplicate pending input before saving'
                    : ''
            }
          >
          <button
            onClick={handleSave}
            // Block save while the namespace input has unfixed pending
            // text — otherwise the user clicks Save expecting their
            // entry to be included and it's silently dropped.
            disabled={
              !canEdit || updateSettings.isPending || newNsError !== null || newNsDuplicate
            }
            className="px-4 py-1.5 text-sm btn-brand rounded-lg disabled:opacity-50 disabled:cursor-not-allowed disabled:pointer-events-none"
          >
            {updateSettings.isPending ? 'Saving...' : 'Save'}
          </button>
          </Tooltip>
        </div>
      </div>
    </div>
  )
}
