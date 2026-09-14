import { useEffect, useRef, useState, type FormEvent } from 'react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { X, Plus, Trash2, Link2, AlertTriangle, Search } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmRepositories, useHelmOCISources, useAddOCISource, useRemoveOCISource, useClusterInfo, useHelmSourceCandidates, useSetHelmSource, useAddHelmRepository, useDiscoverArtifactHubSources } from '../../api/client'
import { Input } from '@skyhook-io/k8s-ui'
import type { ChartSourceCandidate, UpgradeInfo } from '../../types'

interface TrackChartSourceDialogProps {
  open: boolean
  onClose: () => void
  /** Chart name of the release this was opened from, for the example prompt. */
  chartName?: string
  namespace: string
  releaseName: string
  sourceIssue?: UpgradeInfo['sourceIssue']
  sourceError?: string
}

export const HELM_REPOSITORY_FORM_GRID_CLASS = 'grid grid-cols-1 md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.8fr)_auto] gap-2 items-end'
export const SOURCE_INPUT_BASE_CLASS = 'w-full min-w-0 px-3 py-2 rounded-lg border bg-theme-elevated text-sm text-theme-text-primary placeholder:text-theme-text-disabled shadow-sm transition-colors focus:outline-none focus:ring-2 focus:ring-accent focus:border-accent disabled:cursor-not-allowed disabled:opacity-50 disabled:bg-theme-surface'

export function isValidHelmRepositoryURL(value: string) {
  try {
    const parsed = new URL(value.trim())
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

export function isValidOCIPrefix(value: string) {
  return value.trim().startsWith('oci://')
}

export function canAddHelmRepository(name: string, repositoryURL: string) {
  return name.trim() !== '' && isValidHelmRepositoryURL(repositoryURL)
}

export function uniqueSourceCandidate(candidates: ChartSourceCandidate[] | undefined) {
  return candidates?.length === 1 ? candidates[0] : undefined
}

export function ociPrefixForCandidate(reference: string, chartName?: string) {
  const normalized = reference.trim().replace(/\/+$/, '')
  const suffix = chartName?.trim() ? `/${chartName.trim()}` : ''
  return suffix && normalized.endsWith(suffix) ? normalized.slice(0, -suffix.length) : normalized
}

export function getSourceIssueCopy(sourceIssue: UpgradeInfo['sourceIssue'], sourceError?: string) {
  switch (sourceIssue) {
    case 'repo_index_error':
      return {
        title: 'A Helm repo index failed',
        body: 'Fix, refresh, or remove the broken Helm repo index. If this release came from OCI, add its registry prefix and Radar will check OCI before reporting that repo error.',
      }
    case 'ambiguous_repository':
      return {
        title: 'Multiple Helm repos match',
        body: 'Radar will not guess. Select the repository that originally supplied this release.',
      }
    case 'ambiguous_source':
      return {
        title: 'Multiple chart sources match',
        body: 'Select the exact classic repository or OCI source that originally supplied this release.',
      }
    case 'untracked':
      return {
        title: 'Source not tracked',
        body: 'Helm does not record the install source. Add or select the classic repository or OCI source that contains this chart.',
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
export function TrackChartSourceDialog({ open, onClose, chartName, namespace, releaseName, sourceIssue, sourceError }: TrackChartSourceDialogProps) {
  const [ociPrefix, setOCIPrefix] = useState('')
  const [repositoryName, setRepositoryName] = useState('')
  const [repositoryURL, setRepositoryURL] = useState('')
  const [repositoryError, setRepositoryError] = useState<string | null>(null)
  const [repositoryMessage, setRepositoryMessage] = useState<string | null>(null)
  const [repositorySubmitting, setRepositorySubmitting] = useState(false)
  const [ociError, setOCIError] = useState<string | null>(null)
  const [artifactHubCandidates, setArtifactHubCandidates] = useState<ChartSourceCandidate[] | null>(null)
  const [artifactHubError, setArtifactHubError] = useState<string | null>(null)
  const [selectedArtifactHubCandidate, setSelectedArtifactHubCandidate] = useState<ChartSourceCandidate | null>(null)
  const repositoryEdited = useRef(false)
  const repositoryInFlight = useRef(false)
  const artifactHubAbort = useRef<AbortController | null>(null)
  const { data: repositories } = useHelmRepositories()
  const { data: sources } = useHelmOCISources()
  const { data: clusterInfo } = useClusterInfo()
  const addSource = useAddOCISource()
  const removeSource = useRemoveOCISource()
  const addRepository = useAddHelmRepository()
  const discoverArtifactHub = useDiscoverArtifactHubSources(namespace, releaseName)
  const { data: candidates, refetch: refetchCandidates } = useHelmSourceCandidates(namespace, releaseName, open)
  const setSource = useSetHelmSource(namespace, releaseName)

  useEffect(() => {
    artifactHubAbort.current?.abort()
    artifactHubAbort.current = null
    repositoryEdited.current = false
    setRepositoryName('')
    setRepositoryURL('')
    setRepositoryError(null)
    setRepositoryMessage(null)
    setOCIError(null)
    setArtifactHubCandidates(null)
    setArtifactHubError(null)
    setSelectedArtifactHubCandidate(null)
  }, [namespace, releaseName])

  useEffect(() => {
    if (!open) {
      artifactHubAbort.current?.abort()
      artifactHubAbort.current = null
    }
    return () => artifactHubAbort.current?.abort()
  }, [open])

  useEffect(() => {
    if (!open || repositoryEdited.current) return
    const candidate = uniqueSourceCandidate(candidates)
    if (candidate?.type !== 'repository') return
    const repository = repositories?.find(repo => repo.name === candidate.reference)
    const repositoryURL = candidate.url ?? repository?.url
    if (repositoryURL) {
      setRepositoryName(candidate.reference)
      setRepositoryURL(repositoryURL)
    }
  }, [open, candidates, repositories, namespace, releaseName])

  // In-cluster Radar has no `helm registry login` store (the pod's HELM_CONFIG_HOME
  // points at an empty /tmp), so private registries can't authenticate — only
  // public charts can be tracked. Be honest about it rather than silently failing.
  const inCluster = clusterInfo?.inCluster ?? false

  const trimmedOCIPrefix = ociPrefix.trim()
  const invalidOCI = trimmedOCIPrefix !== '' && !isValidOCIPrefix(trimmedOCIPrefix)
  const trimmedRepositoryName = repositoryName.trim()
  const trimmedRepositoryURL = repositoryURL.trim()
  const invalidRepositoryURL = trimmedRepositoryURL !== '' && !isValidHelmRepositoryURL(trimmedRepositoryURL)
  const canAddRepository = canAddHelmRepository(trimmedRepositoryName, trimmedRepositoryURL)
  const normalizedInput = trimmedOCIPrefix.replace(/\/+$/, '')
  const normalizedChartName = chartName?.trim().replace(/^\/+|\/+$/g, '') ?? ''
  const probedRef = normalizedInput && normalizedChartName ? `${normalizedInput}/${normalizedChartName}` : ''
  const looksLikeFullChartRef = Boolean(normalizedInput && normalizedChartName && normalizedInput.endsWith(`/${normalizedChartName}`))
  const sourceIssueCopy = getSourceIssueCopy(sourceIssue, sourceError)

  const handleAddOCI = (event?: FormEvent) => {
    event?.preventDefault()
    if (!trimmedOCIPrefix || invalidOCI) return
    void (async () => {
      setOCIError(null)
      try {
        await addSource.mutateAsync(trimmedOCIPrefix)
        if (selectedArtifactHubCandidate?.type === 'oci') {
          await setSource.mutateAsync(selectedArtifactHubCandidate)
          onClose()
        } else {
          setOCIPrefix('')
        }
      } catch (error) {
        setOCIError(error instanceof Error ? error.message : String(error))
      }
    })()
  }

  const selectArtifactHubCandidate = (candidate: ChartSourceCandidate) => {
    setSelectedArtifactHubCandidate(candidate)
    if (candidate.type === 'repository' && candidate.url) {
      repositoryEdited.current = false
      setRepositoryName(candidate.reference)
      setRepositoryURL(candidate.url)
    } else if (candidate.type === 'oci') {
      setOCIPrefix(ociPrefixForCandidate(candidate.reference, chartName))
    }
  }

  const handleArtifactHubDiscovery = async () => {
	artifactHubAbort.current?.abort()
	const controller = new AbortController()
	artifactHubAbort.current = controller
    setArtifactHubError(null)
    setArtifactHubCandidates(null)
    setSelectedArtifactHubCandidate(null)
    try {
	  const found = await discoverArtifactHub.mutateAsync(controller.signal)
	  if (controller.signal.aborted) return
      setArtifactHubCandidates(found)
      const unique = uniqueSourceCandidate(found)
      if (unique) selectArtifactHubCandidate(unique)
    } catch (error) {
	  if (controller.signal.aborted) return
      setArtifactHubError(error instanceof Error ? error.message : String(error))
	} finally {
	  if (artifactHubAbort.current === controller) artifactHubAbort.current = null
    }
  }

  const handleClose = () => {
	artifactHubAbort.current?.abort()
	artifactHubAbort.current = null
	onClose()
	}

  const handleAddRepository = async (event?: FormEvent) => {
    event?.preventDefault()
    if (repositoryInFlight.current) return
    if (!canAddRepository) {
      setRepositoryError('Enter a repository name and a valid HTTP/HTTPS URL.')
      return
    }
    repositoryInFlight.current = true
    setRepositorySubmitting(true)
    setRepositoryError(null)
    setRepositoryMessage(null)
    try {
      const result = await addRepository.mutateAsync({
        name: trimmedRepositoryName,
        url: trimmedRepositoryURL,
        namespace,
        releaseName,
      })
      if (result.associated) {
        onClose()
        return
      }
      await refetchCandidates()
      setRepositoryMessage(result.candidates?.length
        ? 'Repository added. Select the original chart source above to finish recovery.'
        : 'Repository added, but no exact chart/version match was found. Check the repository or refresh its index.')
    } catch (error) {
      setRepositoryError(error instanceof Error ? error.message : String(error))
    } finally {
      repositoryInFlight.current = false
      setRepositorySubmitting(false)
    }
  }

  const handleSelect = (candidate: NonNullable<typeof candidates>[number]) => {
    setSource.mutate(candidate, { onSuccess: onClose })
  }

  return (
    <DialogPortal open={open} onClose={handleClose} className="max-w-2xl w-full overflow-x-hidden">
      <div className="flex items-start gap-3 p-4 border-b border-theme-border">
        <div className="flex items-center justify-center w-10 h-10 rounded-full shrink-0 bg-theme-hover">
          <Link2 className="w-5 h-5 text-theme-text-secondary" />
        </div>
        <div className="flex-1 min-w-0">
          <h3 className="text-lg font-semibold text-theme-text-primary">Track chart source</h3>
          <p className="text-sm text-theme-text-secondary mt-1">
            Associate this release with the exact Helm repository or OCI chart source.
          </p>
        </div>
        <button
          onClick={handleClose}
          aria-label="Close"
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

        {candidates && candidates.length > 0 && (
          <div>
            <p className="text-xs font-medium text-theme-text-tertiary uppercase tracking-wide mb-2">Matching exact sources</p>
            <ul className="space-y-1">
              {candidates.map((candidate) => (
                <li key={`${candidate.type}:${candidate.reference}`} className="flex items-center justify-between gap-2 px-3 py-2 bg-theme-elevated rounded-lg">
                  <span className="text-sm text-theme-text-primary font-mono truncate">{candidate.type === 'repository' ? `Helm repository: ${candidate.reference}` : candidate.reference}</span>
                  <button onClick={() => handleSelect(candidate)} disabled={setSource.isPending} className="btn-brand px-2 py-1 text-xs disabled:opacity-50">Select</button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {sourceIssue === 'untracked' && candidates?.length === 0 && (
          <div className="rounded-lg border border-theme-border bg-theme-surface/40 p-3 space-y-2" data-testid="artifacthub-recovery">
            <div>
              <h4 className="text-sm font-semibold text-theme-text-primary">Optional ArtifactHub discovery</h4>
              <p className="text-xs text-theme-text-tertiary">Search using only the installed chart name. Radar independently verifies the exact installed version and complete chart package before showing a result.</p>
            </div>
            <button type="button" onClick={handleArtifactHubDiscovery} disabled={discoverArtifactHub.isPending} className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border-light bg-theme-elevated px-3 py-2 text-sm text-theme-text-primary hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:opacity-50">
              <Search className="w-4 h-4" />
              {discoverArtifactHub.isPending ? 'Searching and verifying…' : 'Search ArtifactHub for possible source'}
            </button>
            {artifactHubError && <p role="alert" className="text-xs text-red-500 break-words">{artifactHubError}</p>}
            {artifactHubCandidates && artifactHubCandidates.length === 0 && <p role="status" className="text-xs text-theme-text-secondary">No verified source found</p>}
            {artifactHubCandidates && artifactHubCandidates.length > 0 && (
              <div className="space-y-2">
                <p className="text-xs font-medium text-theme-text-secondary">{artifactHubCandidates.length === 1 ? 'Possible source found via ArtifactHub' : 'Possible sources found via ArtifactHub — choose one'}</p>
                <ul className="space-y-1">
                  {artifactHubCandidates.map(candidate => (
                    <li key={`artifacthub:${candidate.type}:${candidate.reference}:${candidate.url ?? ''}`} className="flex min-w-0 items-center justify-between gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2">
                      <span className="min-w-0 break-all font-mono text-xs text-theme-text-primary">{candidate.type === 'repository' ? `Helm repository: ${candidate.reference} — ${candidate.url}` : `OCI: ${candidate.reference}`}</span>
                      {artifactHubCandidates.length > 1 && <button type="button" onClick={() => selectArtifactHubCandidate(candidate)} className="btn-brand shrink-0 px-2 py-1 text-xs">Use this source</button>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}

        <form onSubmit={handleAddRepository} className="rounded-lg border border-theme-border p-3 space-y-3" data-testid="helm-repository-form">
          <div>
            <h4 className="text-sm font-semibold text-theme-text-primary">Helm repository</h4>
            <p className="text-xs text-theme-text-tertiary">Add an HTTP/HTTPS chart repository.</p>
          </div>
          <div className={HELM_REPOSITORY_FORM_GRID_CLASS}>
            <label className="block min-w-0">
              <span className="block text-xs font-medium text-theme-text-secondary mb-1">Name</span>
              <Input className={clsx(SOURCE_INPUT_BASE_CLASS, 'border-theme-border-light')} disabled={repositorySubmitting} value={repositoryName} onChange={(e) => { repositoryEdited.current = true; setSelectedArtifactHubCandidate(null); setRepositoryName(e.target.value) }} placeholder="Repository name" />
            </label>
            <label className="block min-w-0">
              <span className="block text-xs font-medium text-theme-text-secondary mb-1">Repository URL</span>
              <Input className={clsx(SOURCE_INPUT_BASE_CLASS, invalidRepositoryURL ? 'border-red-500/60 focus:ring-red-500 focus:border-red-500' : 'border-theme-border-light')} disabled={repositorySubmitting} value={repositoryURL} onChange={(e) => { repositoryEdited.current = true; setSelectedArtifactHubCandidate(null); setRepositoryURL(e.target.value) }} placeholder="https://charts.example.org/" aria-invalid={invalidRepositoryURL ? true : undefined} aria-describedby={invalidRepositoryURL ? 'repository-url-error' : undefined} />
              {invalidRepositoryURL && <span id="repository-url-error" className="mt-1 block text-xs text-red-500">Must be an http:// or https:// Helm repository URL.</span>}
            </label>
            <button type="submit" disabled={!canAddRepository || repositorySubmitting || addRepository.isPending || setSource.isPending} className="btn-brand w-full md:w-auto px-3 py-2 text-sm whitespace-nowrap disabled:opacity-50 disabled:pointer-events-none">{repositorySubmitting ? 'Adding repository…' : selectedArtifactHubCandidate?.type === 'repository' ? 'Add and use repository' : 'Add repository'}</button>
          </div>
          {repositoryError && <p role="alert" className="text-xs text-red-500 break-words">{repositoryError}</p>}
          {repositoryMessage && <p role="status" className="text-xs text-theme-text-secondary">{repositoryMessage}</p>}
          <p className="mt-1 text-xs text-theme-text-tertiary">Credentials remain in Helm&apos;s repository configuration; Radar stores no repository credentials.</p>
        </form>

        <form onSubmit={handleAddOCI} className="rounded-lg border border-theme-border p-3 space-y-3" data-testid="oci-source-form">
          <div>
            <h4 className="text-sm font-semibold text-theme-text-primary">OCI registry</h4>
            <p className="text-xs text-theme-text-tertiary">Register an OCI prefix. HTTP Helm repository URLs belong in the form above.</p>
          </div>
          <label htmlFor="oci-source-prefix" className="block text-xs font-medium text-theme-text-secondary">Prefix</label>
          <div className="flex flex-col sm:flex-row gap-2 min-w-0">
            <Input
              id="oci-source-prefix"
              value={ociPrefix}
              onChange={(e) => { setSelectedArtifactHubCandidate(null); setOCIPrefix(e.target.value) }}
              disabled={addSource.isPending || setSource.isPending}
              placeholder="oci://ghcr.io/example"
              aria-invalid={invalidOCI ? true : undefined}
              aria-describedby={invalidOCI ? 'oci-prefix-error' : undefined}
              className={clsx(
                SOURCE_INPUT_BASE_CLASS,
                'flex-1',
                invalidOCI ? 'border-red-500/60 focus:ring-red-500 focus:border-red-500' : 'border-theme-border-light',
              )}
            />
            <button
              type="submit"
              disabled={!trimmedOCIPrefix || invalidOCI || addSource.isPending}
              className="btn-brand w-full sm:w-auto px-3 py-2 text-sm inline-flex justify-center items-center gap-1 whitespace-nowrap disabled:opacity-50 disabled:pointer-events-none"
            >
              <Plus className="w-4 h-4" />
              {selectedArtifactHubCandidate?.type === 'oci' ? 'Add and use OCI source' : 'Add OCI source'}
            </button>
          </div>
          {invalidOCI && <p id="oci-prefix-error" className="text-xs text-red-500">Must be an oci:// reference.</p>}
          {ociError && <p role="alert" className="text-xs text-red-500 break-words">{ociError}</p>}
          <p className="mt-1 text-xs text-theme-text-tertiary">
            {looksLikeFullChartRef
                ? `This looks like a full chart ref. Radar would probe "${probedRef}"; remove the final "/${normalizedChartName}" if "${normalizedChartName}" is the chart name.`
              : chartName
                ? `Radar will probe "${probedRef || `<prefix>/${normalizedChartName}`}".`
                : 'Radar probes <prefix>/<chartName> for each untracked release.'}
          </p>
        </form>

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
