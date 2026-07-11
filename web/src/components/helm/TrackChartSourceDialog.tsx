import { useState } from 'react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { X, Plus, Trash2, Link2, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmOCISources, useAddOCISource, useRemoveOCISource, useClusterInfo } from '../../api/client'
import { Input } from '@skyhook-io/k8s-ui'
import type { UpgradeInfo } from '../../types'

interface TrackChartSourceDialogProps {
  open: boolean
  onClose: () => void
  /** Chart name of the release this was opened from, for the example prompt. */
  chartName?: string
  sourceIssue?: UpgradeInfo['sourceIssue']
  sourceError?: string
}

function getSourceIssueCopy(sourceIssue: UpgradeInfo['sourceIssue'], sourceError?: string) {
  switch (sourceIssue) {
    case 'repo_index_error':
      return {
        title: 'A Helm repo index failed',
        body: 'Fix, refresh, or remove the broken Helm repo index. If this release came from OCI, add its registry prefix and Radar will check OCI before reporting that repo error.',
      }
    case 'ambiguous_repository':
      return {
        title: 'Multiple Helm repos match',
        body: 'Radar will not guess between classic repos. Adding an OCI prefix will not enable direct upgrades while those repos remain ambiguous.',
      }
    case 'untracked':
      return {
        title: 'Source not tracked',
        body: 'Helm does not record the install source. Add the OCI prefix that contains this chart.',
      }
    default:
      return sourceError
        ? { title: 'Source unresolved', body: sourceError }
        : undefined
  }
}

// TrackChartSourceDialog lets the user register an OCI chart-source prefix — the
// OCI analog of `helm repo add`. Helm doesn't persist the ref a release was
// installed from, so for charts published to an OCI registry (and not managed by
// GitOps) Radar can only track upgrades once the user declares where they live.
// Registering a registry/org prefix lets Radar probe "<prefix>/<chartName>".
export function TrackChartSourceDialog({ open, onClose, chartName, sourceIssue, sourceError }: TrackChartSourceDialogProps) {
  const [value, setValue] = useState('')
  const { data: sources } = useHelmOCISources()
  const { data: clusterInfo } = useClusterInfo()
  const addSource = useAddOCISource()
  const removeSource = useRemoveOCISource()

  // In-cluster Radar has no `helm registry login` store (the pod's HELM_CONFIG_HOME
  // points at an empty /tmp), so private registries can't authenticate — only
  // public charts can be tracked. Be honest about it rather than silently failing.
  const inCluster = clusterInfo?.inCluster ?? false

  const trimmed = value.trim()
  const invalid = trimmed !== '' && !trimmed.startsWith('oci://')
  const normalizedInput = trimmed.replace(/\/+$/, '')
  const normalizedChartName = chartName?.trim().replace(/^\/+|\/+$/g, '') ?? ''
  const probedRef = normalizedInput && normalizedChartName ? `${normalizedInput}/${normalizedChartName}` : ''
  const looksLikeFullChartRef = Boolean(normalizedInput && normalizedChartName && normalizedInput.endsWith(`/${normalizedChartName}`))
  const sourceIssueCopy = getSourceIssueCopy(sourceIssue, sourceError)

  const handleAdd = () => {
    if (!trimmed || invalid) return
    addSource.mutate(trimmed, { onSuccess: () => setValue('') })
  }

  return (
    <DialogPortal open={open} onClose={onClose} className="max-w-lg w-full">
      <div className="flex items-start gap-3 p-4 border-b border-theme-border">
        <div className="flex items-center justify-center w-10 h-10 rounded-full shrink-0 bg-theme-hover">
          <Link2 className="w-5 h-5 text-theme-text-secondary" />
        </div>
        <div className="flex-1 min-w-0">
          <h3 className="text-lg font-semibold text-theme-text-primary">Track chart source</h3>
          <p className="text-sm text-theme-text-secondary mt-1">
            Helm doesn&apos;t record where a chart was installed from. Register your OCI
            registry prefix and Radar will check it for newer versions of your charts.
          </p>
        </div>
        <button
          onClick={onClose}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          <X className="w-5 h-5" />
        </button>
      </div>

      <div className="p-4 space-y-4">
        {sourceIssueCopy && (
          <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-sm">
            <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5 text-theme-text-secondary" />
            <div>
              <p className="font-medium text-theme-text-primary">{sourceIssueCopy.title}</p>
              <p className="text-theme-text-secondary">{sourceIssueCopy.body}</p>
            </div>
          </div>
        )}

        <div>
          <label className="block text-sm font-medium text-theme-text-secondary mb-2">
            OCI registry prefix
          </label>
          <div className="flex gap-2">
            <Input
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleAdd()}
              placeholder="oci://ghcr.io/myorg/charts"
              aria-invalid={invalid ? true : undefined}
              className={clsx(
                'flex-1 px-3 py-2 bg-theme-elevated border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2',
                invalid ? 'border-red-500/60 focus:ring-red-500' : 'border-theme-border-light focus:ring-accent',
              )}
            />
            <button
              onClick={handleAdd}
              disabled={!trimmed || invalid || addSource.isPending}
              className="btn-brand px-3 py-2 text-sm inline-flex items-center gap-1 disabled:opacity-50 disabled:pointer-events-none"
            >
              <Plus className="w-4 h-4" />
              Add
            </button>
          </div>
          <p className="mt-1 text-xs text-theme-text-tertiary">
            {invalid
              ? 'Must be an oci:// reference.'
              : looksLikeFullChartRef
                ? `This looks like a full chart ref. Radar would probe "${probedRef}"; remove the final "/${normalizedChartName}" if "${normalizedChartName}" is the chart name.`
              : chartName
                ? `Radar will probe "${probedRef || `<prefix>/${normalizedChartName}`}".`
                : 'Radar probes <prefix>/<chartName> for each untracked release.'}
          </p>
        </div>

        {sources && sources.length > 0 && (
          <div>
            <p className="text-xs font-medium text-theme-text-tertiary uppercase tracking-wide mb-2">
              Registered sources
            </p>
            <ul className="space-y-1">
              {sources.map((src) => (
                <li
                  key={src}
                  className="flex items-center justify-between gap-2 px-3 py-2 bg-theme-elevated rounded-lg"
                >
                  <span className="text-sm text-theme-text-primary font-mono truncate">{src}</span>
                  <button
                    onClick={() => removeSource.mutate(src)}
                    disabled={removeSource.isPending}
                    className="p-1 text-theme-text-secondary hover:text-red-400 hover:bg-red-500/10 rounded disabled:opacity-50"
                    aria-label={`Remove ${src}`}
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {inCluster ? (
          <div className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
            <AlertTriangle className="w-4 h-4 shrink-0 mt-px" />
            <span>
              Radar is running in-cluster, where it has no{' '}
              <span className="font-mono">helm registry login</span> credentials — only{' '}
              <strong>public</strong> charts can be tracked. Private-registry support for in-cluster
              Radar isn&apos;t available yet.
            </span>
          </div>
        ) : (
          <p className="text-xs text-theme-text-tertiary">
            Credentials are reused from your <span className="font-mono">helm registry login</span>.
            Radar stores no registry secrets.
          </p>
        )}
      </div>
    </DialogPortal>
  )
}
