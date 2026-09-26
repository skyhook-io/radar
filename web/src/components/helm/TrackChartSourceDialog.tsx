import { useState, type FormEvent } from 'react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { AlertTriangle, Link2, Plus, Trash2, X } from 'lucide-react'
import { Input } from '@skyhook-io/k8s-ui'
import { useAddHelmRepository, useAddOCISource, useCapabilities, useClusterInfo, useHelmOCISources, useHelmSourceStatus, useRemoveOCISource, useSetHelmSource } from '../../api/client'
import { OperatorManagedNotice } from '../settings/OperatorManagedNotice'
import type { UpgradeInfo } from '../../types'

interface TrackChartSourceDialogProps {
  open: boolean
  onClose: () => void
  chartName?: string
  namespace: string
  releaseName: string
  sourceIssue?: UpgradeInfo['sourceIssue']
  sourceError?: string
}

export function isValidHelmRepositoryURL(value: string) {
  try {
    const parsed = new URL(value.trim())
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

export function sourceStateLabel(recorded: boolean, configured: boolean, available: boolean) {
  if (!recorded) return 'Not recorded'
  if (!configured) return 'Recorded · not configured here'
  if (!available) return 'Recorded and configured · unavailable or version missing'
  return 'Recorded · configured · available'
}

export function getSourceIssueCopy(sourceIssue: UpgradeInfo['sourceIssue'], sourceError?: string) {
  switch (sourceIssue) {
    case 'repo_index_error': return { title: 'A Helm repo index failed', body: 'Fix or refresh the repository before associating it.' }
    case 'ambiguous_repository':
    case 'ambiguous_source': return { title: 'Multiple chart sources match', body: 'Radar will not guess. Select the source that originally supplied this release.' }
    case 'source_unavailable': return { title: 'Recorded source unavailable', body: 'The release provenance is preserved, but that source cannot currently be verified here.' }
    case 'untracked': return { title: 'Source not recorded', body: 'Select an exact configured match, or add the original classic repository or OCI prefix.' }
    default: return sourceError ? { title: 'Source unresolved', body: sourceError } : undefined
  }
}

export function TrackChartSourceDialog({ open, onClose, chartName, namespace, releaseName, sourceIssue, sourceError }: TrackChartSourceDialogProps) {
  const [ociPrefix, setOCIPrefix] = useState('')
  const [repositoryName, setRepositoryName] = useState('')
  const [repositoryURL, setRepositoryURL] = useState('')
  const { data: status, refetch: refetchStatus } = useHelmSourceStatus(namespace, releaseName, open)
  const { data: ociSources } = useHelmOCISources()
  const { data: clusterInfo } = useClusterInfo()
  const { data: capabilities } = useCapabilities()
  const addOCI = useAddOCISource()
  const removeOCI = useRemoveOCISource()
  const addRepository = useAddHelmRepository()
  const setSource = useSetHelmSource(namespace, releaseName)
  const issueCopy = getSourceIssueCopy(sourceIssue, sourceError)
  const recorded = status?.recorded
  const configManaged = capabilities?.configManagement === 'operator'

  const addRepositoryAndAssociate = (event: FormEvent) => {
    event.preventDefault()
    const name = repositoryName.trim()
    const url = repositoryURL.trim()
    if (!name || !isValidHelmRepositoryURL(url)) return
    addRepository.mutate({ name, url, namespace, releaseName }, { onSuccess: onClose })
  }

  const addOCIPrefix = async (event: FormEvent) => {
    event.preventDefault()
    if (!ociPrefix.trim().startsWith('oci://')) return
    await addOCI.mutateAsync(ociPrefix.trim())
    setOCIPrefix('')
    await refetchStatus()
  }

  return <DialogPortal open={open} onClose={onClose} className="max-w-2xl w-full">
    <div className="flex items-start gap-3 p-4 border-b border-theme-border">
      <div className="flex items-center justify-center w-10 h-10 rounded-full bg-theme-hover"><Link2 className="w-5 h-5" /></div>
      <div className="flex-1 min-w-0"><h3 className="text-lg font-semibold">Track chart source</h3><p className="text-sm text-theme-text-secondary mt-1">Associate this release with one verified classic repository or exact OCI chart source.</p></div>
      <button onClick={onClose} aria-label="Close" className="p-1 rounded hover:bg-theme-elevated"><X className="w-5 h-5" /></button>
    </div>
    <div className="p-4 space-y-4">
	  {configManaged && <OperatorManagedNotice helmValue="helm.repositories / helm.ociSources" />}
      {issueCopy && <div className="flex gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-sm"><AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" /><div><p className="font-medium">{issueCopy.title}</p><p className="text-theme-text-secondary">{issueCopy.body}</p></div></div>}
      <div className="rounded-lg border border-theme-border px-3 py-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Release source state</p>
        <p className="text-sm font-medium mt-1">{sourceStateLabel(Boolean(recorded), status?.configured ?? false, status?.available ?? false)}</p>
        {recorded && <p className="font-mono text-xs text-theme-text-secondary mt-1 break-all">{recorded.url ?? recorded.reference}</p>}
      </div>
      {status?.candidates && status.candidates.length > 0 && <div>
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-2">Verified exact matches</p>
        <div className="space-y-2">{status.candidates.map(candidate => <button key={`${candidate.type}:${candidate.url ?? candidate.reference}`} onClick={() => setSource.mutate(candidate, { onSuccess: onClose })} disabled={setSource.isPending} className="w-full text-left rounded-lg border border-theme-border px-3 py-2 hover:border-accent disabled:opacity-50"><span className="text-xs uppercase text-theme-text-tertiary">{candidate.type}</span><span className="block font-mono text-sm break-all">{candidate.url ?? candidate.reference}</span></button>)}</div>
        {status.candidates.length > 1 && <p className="text-xs text-amber-600 mt-2">Multiple sources publish this exact chart and version. Choose explicitly; Radar will not auto-associate one.</p>}
      </div>}
      <form onSubmit={addRepositoryAndAssociate} className="space-y-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Add classic repository and associate</p>
		<div className="grid grid-cols-1 md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.8fr)_auto] gap-2"><Input value={repositoryName} onChange={event => setRepositoryName(event.target.value)} placeholder="Repository name" disabled={configManaged} /><Input value={repositoryURL} onChange={event => setRepositoryURL(event.target.value)} placeholder="https://charts.example.com" disabled={configManaged} /><button type="submit" disabled={configManaged || !repositoryName.trim() || !isValidHelmRepositoryURL(repositoryURL) || addRepository.isPending} className="btn-brand inline-flex items-center justify-center gap-1 px-3 py-2"><Plus className="w-4 h-4" /> Add</button></div>
        <p className="text-xs text-theme-text-tertiary">Association succeeds only if the repository publishes {chartName || 'the installed chart'} at the exact installed version.</p>
      </form>
      <form onSubmit={addOCIPrefix} className="space-y-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Register OCI prefix</p>
		<div className="flex gap-2"><Input value={ociPrefix} onChange={event => setOCIPrefix(event.target.value)} placeholder="oci://registry.example.com/charts" disabled={configManaged} /><button type="submit" disabled={configManaged || !ociPrefix.trim().startsWith('oci://') || addOCI.isPending} className="btn-brand px-3 py-2">Add</button></div>
        <p className="text-xs text-theme-text-tertiary">After registration, select the verified exact match above. Credentials are reused from Helm and are never recorded in release provenance.</p>
      </form>
	  {ociSources && ociSources.length > 0 && <ul className="space-y-1">{ociSources.map(source => <li key={source} className="flex items-center justify-between gap-2 rounded-lg bg-theme-elevated px-3 py-2"><span className="font-mono text-sm truncate">{source}</span>{!configManaged && <button onClick={() => removeOCI.mutate(source)} disabled={removeOCI.isPending} aria-label={`Remove ${source}`} className="p-1 hover:text-red-400 disabled:opacity-50"><Trash2 className="w-4 h-4" /></button>}</li>)}</ul>}
      {clusterInfo?.inCluster && <p className="text-xs text-amber-600 flex items-start gap-2"><AlertTriangle className="w-4 h-4 shrink-0" />Private OCI sources require credentials available to the in-cluster Radar process.</p>}
    </div>
  </DialogPortal>
}
