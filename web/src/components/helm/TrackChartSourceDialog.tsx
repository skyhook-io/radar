import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { AlertTriangle, BadgeCheck, Link2, Plus, Search, Trash2, X } from 'lucide-react'
import { Input } from '@skyhook-io/k8s-ui'
import { useAddHelmRepository, useAddOCISource, useCapabilities, useCloudRole, useClusterInfo, useDiscoverArtifactHubSources, useHelmOCISources, useHelmRepositories, useHelmSourceStatus, useRecoverArtifactHubSource, useRemoveOCISource, useSetHelmSource } from '../../api/client'
import { OperatorManagedNotice } from '../settings/OperatorManagedNotice'
import type { ArtifactHubSourceCandidate, ChartSourceStatus, HelmRepository, UpgradeInfo } from '../../types'

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

// What the dialog says about the release's recorded source. "Not recorded" is
// a claim about the release, so it shows only once the read has succeeded.
export function sourceStatusLine(status: ChartSourceStatus | undefined, error: unknown) {
  if (status) return sourceStateLabel(Boolean(status.recorded), status.configured, status.available)
  if (error) return `Could not read this release's source: ${error instanceof Error ? error.message : String(error)}`
  return 'Checking the recorded source...'
}

// Short form of a repository URL for a button label: host and path, no scheme.
export function repositoryDisplayURL(url: string) {
  try {
    const parsed = new URL(url)
    return `${parsed.host}${parsed.pathname}`.replace(/\/+$/, '')
  } catch {
    return url
  }
}

// Mirrors normalizedRepositoryName in internal/helm/chart_sources.go. The
// server stores names in this form, so collisions are checked on it.
const MAX_REPOSITORY_NAME_LENGTH = 40
export function normalizedRepositoryName(name: string) {
  const base = name.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[-._]+|[-._]+$/g, '')
  return base.length > MAX_REPOSITORY_NAME_LENGTH ? base.slice(0, MAX_REPOSITORY_NAME_LENGTH).replace(/[-._]+$/, '') : base
}

// Name for re-adding the recorded repository on this machine. Reuses the
// recorded alias when it is free here; otherwise the first free "<alias>-N",
// so an alias that points at a different repository is never reused. The
// server refuses a taken name, which covers a repository added meanwhile.
// An empty result lets the server pick a name from the URL.
export function recordedRepositoryName(alias: string, repositories: HelmRepository[]) {
  const base = normalizedRepositoryName(alias)
  if (!base) return ''
  const taken = new Set(repositories.map(repository => normalizedRepositoryName(repository.name)))
  if (!taken.has(base)) return base
  for (let n = 2; ; n++) {
    const suffix = `-${n}`
    const name = base.slice(0, MAX_REPOSITORY_NAME_LENGTH - suffix.length).replace(/[-._]+$/, '') + suffix
    if (!taken.has(name)) return name
  }
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
  const [artifactHubCandidates, setArtifactHubCandidates] = useState<ArtifactHubSourceCandidate[] | null>(null)
  const [artifactHubError, setArtifactHubError] = useState<string | null>(null)
  const artifactHubAbort = useRef<AbortController | null>(null)
  const { data: status, error: statusError, refetch: refetchStatus } = useHelmSourceStatus(namespace, releaseName, open)
  const { data: ociSources } = useHelmOCISources()
  const { data: clusterInfo } = useClusterInfo()
  const { data: capabilities } = useCapabilities()
  const addOCI = useAddOCISource()
  const removeOCI = useRemoveOCISource()
  const addRepository = useAddHelmRepository()
  const setSource = useSetHelmSource(namespace, releaseName)
  const discoverArtifactHub = useDiscoverArtifactHubSources(namespace, releaseName)
  const recoverArtifactHub = useRecoverArtifactHubSource(namespace, releaseName)
  const issueCopy = getSourceIssueCopy(sourceIssue, sourceError)
  const recorded = status?.recorded
  const configManaged = capabilities?.configManagement === 'operator'
  // Chart sources are shared configuration that every user's upgrades resolve
  // against, so in Radar Cloud only owners change them (the server enforces it).
  const ownerOnly = !useCloudRole().canAtLeast('owner')
  const canEditSources = capabilities != null && !configManaged && !ownerOnly
  const titleId = useId()
  // The release says which repository it came from, but this machine has no
  // repository with that URL: offer to add exactly that one.
  const missingRecordedRepository = status && !status.configured && recorded?.type === 'repository' && recorded.url ? recorded : undefined
  const { data: repositories, isLoading: repositoriesLoading } = useHelmRepositories()
  const recordedName = missingRecordedRepository ? recordedRepositoryName(missingRecordedRepository.reference, repositories ?? []) : undefined
  const artifactHubEligible = Boolean(status && !status.available && !status.candidates?.length)

  useEffect(() => {
    artifactHubAbort.current?.abort()
    artifactHubAbort.current = null
    setArtifactHubCandidates(null)
    setArtifactHubError(null)
  }, [namespace, releaseName, open])

  const searchArtifactHub = async () => {
    artifactHubAbort.current?.abort()
    const controller = new AbortController()
    artifactHubAbort.current = controller
    setArtifactHubCandidates(null)
    setArtifactHubError(null)
    try {
      const candidates = await discoverArtifactHub.mutateAsync(controller.signal)
      if (!controller.signal.aborted) setArtifactHubCandidates(candidates)
    } catch (error) {
      if (!controller.signal.aborted) setArtifactHubError(error instanceof Error ? error.message : String(error))
    } finally {
      if (artifactHubAbort.current === controller) artifactHubAbort.current = null
    }
  }

  const recoverArtifactHubCandidate = (candidate: ArtifactHubSourceCandidate) => {
    setArtifactHubError(null)
    recoverArtifactHub.mutate(candidate, {
      onSuccess: onClose,
      onError: error => setArtifactHubError(error instanceof Error ? error.message : String(error)),
    })
  }

  const addRecordedRepository = () => {
    if (!missingRecordedRepository || recordedName === undefined) return
    addRepository.mutate({ name: recordedName, url: missingRecordedRepository.url, namespace, releaseName }, { onSuccess: onClose })
  }

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

  return <DialogPortal ariaLabelledBy={titleId} open={open} onClose={onClose} className="max-w-2xl w-full">
    <div className="flex items-start gap-3 p-4 border-b border-theme-border">
      <div className="flex items-center justify-center w-10 h-10 rounded-full bg-theme-hover"><Link2 className="w-5 h-5" /></div>
      <div className="flex-1 min-w-0"><h3 id={titleId} className="text-lg font-semibold">Track chart source</h3><p className="text-sm text-theme-text-secondary mt-1">Tell Radar which repository or OCI source this release was installed from. Upgrades then come only from that source.</p></div>
      <button onClick={onClose} aria-label="Close" className="p-1 rounded hover:bg-theme-elevated"><X className="w-5 h-5" /></button>
    </div>
    <div className="p-4 space-y-4">
	  {configManaged && <OperatorManagedNotice helmValue="helm.ociSources" />}
      {!configManaged && ownerOnly && <p className="text-sm text-theme-text-secondary">Only organization owners can add or remove chart sources.</p>}
      {issueCopy && <div className="flex gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-sm"><AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" /><div><p className="font-medium">{issueCopy.title}</p><p className="text-theme-text-secondary">{issueCopy.body}</p></div></div>}
      <div className="rounded-lg border border-theme-border px-3 py-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Release source state</p>
        <p className="text-sm font-medium mt-1 break-all">{sourceStatusLine(status, statusError)}</p>
        {!status && statusError && <button type="button" onClick={() => refetchStatus()} className="mt-2 px-3 py-1.5 text-sm text-theme-text-primary border border-theme-border rounded-lg hover:bg-theme-elevated transition-colors">Retry</button>}
        {recorded && <p className="font-mono text-xs text-theme-text-secondary mt-1 break-all">{recorded.url ?? recorded.reference}</p>}
      </div>
      {missingRecordedRepository && recordedName !== undefined && <div className="space-y-1">
        <button type="button" onClick={addRecordedRepository} disabled={!canEditSources || repositoriesLoading || addRepository.isPending} className="btn-brand inline-flex items-center gap-1 px-3 py-2 max-w-full"><Plus className="w-4 h-4 shrink-0" /><span className="truncate">Add {repositoryDisplayURL(missingRecordedRepository.url)} and use it</span></button>
        <p className="text-xs text-theme-text-tertiary">{!recordedName
          ? 'Radar picks a name for it in Helm.'
          : recordedName === missingRecordedRepository.reference
            ? <>Saved in Helm as <span className="font-mono">{recordedName}</span>, the name it was recorded under.</>
            : recordedName === normalizedRepositoryName(missingRecordedRepository.reference)
              ? <>Saved in Helm as <span className="font-mono">{recordedName}</span>.</>
              : <>Saved in Helm as <span className="font-mono">{recordedName}</span>. The name <span className="font-mono">{missingRecordedRepository.reference}</span> already points to a different repository here.</>}</p>
      </div>}
      {status?.candidates && status.candidates.length > 0 && <div>
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-2">Configured sources that publish {chartName || 'this chart'} at the installed version</p>
        <div className="space-y-2">{status.candidates.map(candidate => <button key={`${candidate.type}:${candidate.url ?? candidate.reference}`} onClick={() => setSource.mutate(candidate, { onSuccess: onClose })} disabled={setSource.isPending} className="w-full text-left rounded-lg border border-theme-border px-3 py-2 hover:border-accent disabled:opacity-50"><span className="text-xs uppercase text-theme-text-tertiary">{candidate.type}</span><span className="block font-mono text-sm break-all">{candidate.url ?? candidate.reference}</span></button>)}</div>
        {status.candidates.length > 1 && <p className="text-xs text-amber-600 mt-2">Multiple sources publish this exact chart and version. Choose explicitly; Radar will not auto-associate one.</p>}
      </div>}
      {artifactHubEligible && <div className="rounded-lg border border-theme-border bg-theme-surface/40 p-3 space-y-3" data-testid="artifacthub-recovery">
        <div>
          <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Optional ArtifactHub recovery</p>
          <p className="text-xs text-theme-text-secondary mt-1">Search sends only the installed chart name. Results are discovery metadata; Radar downloads and verifies only the source you explicitly choose.</p>
        </div>
        <button type="button" onClick={() => void searchArtifactHub()} disabled={!canEditSources || discoverArtifactHub.isPending || recoverArtifactHub.isPending} className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border-light bg-theme-elevated px-3 py-2 text-sm text-theme-text-primary hover:bg-theme-hover disabled:opacity-50">
          <Search className="w-4 h-4" />
          {discoverArtifactHub.isPending ? 'Searching ArtifactHub…' : 'Search ArtifactHub'}
        </button>
        {artifactHubError && <p role="alert" className="text-xs text-red-500 break-words">{artifactHubError}</p>}
        {artifactHubCandidates?.length === 0 && <p role="status" className="text-xs text-theme-text-secondary">No exact-name ArtifactHub candidates found.</p>}
        {artifactHubCandidates && artifactHubCandidates.length > 0 && <div className="space-y-2">
          <p className="text-xs font-medium text-theme-text-secondary">Select one candidate to verify against the complete installed chart.</p>
          <ul className="space-y-2">{artifactHubCandidates.map(candidate => <li key={`${candidate.type}:${candidate.url ?? candidate.reference}`} className="rounded-lg border border-theme-border bg-theme-elevated px-3 py-2">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5 text-xs">
                  <span className="font-medium text-theme-text-primary">{candidate.publisher}</span>
                  <span className="text-theme-text-tertiary">· {candidate.repositoryName}</span>
                  {candidate.official && <span className="inline-flex items-center gap-1 rounded bg-blue-500/10 px-1.5 py-0.5 text-blue-500"><BadgeCheck className="w-3 h-3" />Official</span>}
                  {candidate.verifiedPublisher && <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 text-emerald-500"><BadgeCheck className="w-3 h-3" />Verified publisher</span>}
                </div>
                <p className="font-mono text-xs text-theme-text-secondary break-all mt-1">{candidate.url ?? candidate.reference}</p>
              </div>
              <button type="button" onClick={() => recoverArtifactHubCandidate(candidate)} disabled={!canEditSources || recoverArtifactHub.isPending} className="btn-brand shrink-0 px-2 py-1 text-xs disabled:opacity-50">{recoverArtifactHub.isPending ? 'Verifying…' : 'Verify and use'}</button>
            </div>
          </li>)}</ul>
          {artifactHubCandidates.length > 1 && <p className="text-xs text-amber-600">Multiple publishers use this chart name. Radar will not guess or verify them automatically.</p>}
        </div>}
      </div>}
      <form onSubmit={addRepositoryAndAssociate} className="space-y-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Add classic repository and associate</p>
		<div className="grid grid-cols-1 md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.8fr)_auto] gap-2"><Input value={repositoryName} onChange={event => setRepositoryName(event.target.value)} placeholder="Repository name" disabled={!canEditSources} /><Input value={repositoryURL} onChange={event => setRepositoryURL(event.target.value)} placeholder="https://charts.example.com" disabled={!canEditSources} /><button type="submit" disabled={!canEditSources || !repositoryName.trim() || !isValidHelmRepositoryURL(repositoryURL) || addRepository.isPending} className="btn-brand inline-flex items-center justify-center gap-1 px-3 py-2"><Plus className="w-4 h-4" /> Add</button></div>
        <p className="text-xs text-theme-text-tertiary">Association succeeds only if the repository publishes {chartName || 'the installed chart'} at the exact installed version.</p>
      </form>
      <form onSubmit={addOCIPrefix} className="space-y-2">
        <p className="text-xs uppercase tracking-wide text-theme-text-tertiary">Register OCI prefix</p>
		<div className="flex gap-2"><Input value={ociPrefix} onChange={event => setOCIPrefix(event.target.value)} placeholder="oci://registry.example.com/charts" disabled={!canEditSources} /><button type="submit" disabled={!canEditSources || !ociPrefix.trim().startsWith('oci://') || addOCI.isPending} className="btn-brand px-3 py-2">Add</button></div>
        <p className="text-xs text-theme-text-tertiary">After registration, select it in the list above. Credentials are reused from Helm and are never recorded in release provenance.</p>
      </form>
	  {ociSources && ociSources.length > 0 && <ul className="space-y-1">{ociSources.map(source => <li key={source} className="flex items-center justify-between gap-2 rounded-lg bg-theme-elevated px-3 py-2"><span className="font-mono text-sm truncate">{source}</span>{canEditSources && <button onClick={() => removeOCI.mutate(source)} disabled={removeOCI.isPending} aria-label={`Remove ${source}`} className="p-1 hover:text-red-400 disabled:opacity-50"><Trash2 className="w-4 h-4" /></button>}</li>)}</ul>}
      {clusterInfo?.inCluster && <p className="text-xs text-amber-600 flex items-start gap-2"><AlertTriangle className="w-4 h-4 shrink-0" />Private OCI sources require credentials available to the in-cluster Radar process.</p>}
    </div>
  </DialogPortal>
}
