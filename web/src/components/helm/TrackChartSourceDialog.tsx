import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { Trash2, X } from 'lucide-react'
import { Input } from '@skyhook-io/k8s-ui'
import {
  useAddHelmRepository,
  useAddOCISource,
  useCapabilities,
  useCloudRole,
  useClusterInfo,
  useHelmOCISources,
  useHelmRepositories,
  useHelmSourceStatus,
  useRemoveOCISource,
  useSetHelmSource,
  useRadarFeature,
} from '../../api/client'
import { OperatorManagedNotice } from '../settings/OperatorManagedNotice'
import { ArtifactHubSourceSearch, publicChartSourceURL } from './ArtifactHubSourceSearch'
import type {
  ChartSourceCandidate,
  ChartSourceStatus,
  HelmRepository,
  UpgradeInfo,
  SourceUpgradeSelection,
} from '../../types'

interface TrackChartSourceDialogProps {
  operationError?: string | null
  onSelectUpgradeSource?: (selection: SourceUpgradeSelection) => void
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
  if (!available) return 'Recorded and configured · source unavailable'
  return 'Recorded · configured · available'
}

// What the dialog says about the release's recorded source. "Not recorded" is
// a claim about the release, so it shows only once the read has succeeded.
export function sourceStatusLine(status: ChartSourceStatus | undefined, error: unknown) {
  if (status?.changing) return 'A Helm operation is in progress'
  if (status?.sourceChangeRequired) return 'Source choice required after a failed chart change'
  if (status?.confirmed === false && status.recorded) return 'Last recorded source - confirm before upgrading'
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
  const base = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[-._]+|[-._]+$/g, '')
  return base.length > MAX_REPOSITORY_NAME_LENGTH
    ? base.slice(0, MAX_REPOSITORY_NAME_LENGTH).replace(/[-._]+$/, '')
    : base
}

// Name for re-adding the recorded repository on this machine. Reuses the
// recorded alias when it is free here; otherwise the first free "<alias>-N",
// so an alias that points at a different repository is never reused. The
// server refuses a taken name, which covers a repository added meanwhile.
// An empty result lets the server pick a name from the URL.
export function recordedRepositoryName(alias: string, repositories: HelmRepository[]) {
  const base = normalizedRepositoryName(alias)
  if (!base) return ''
  const taken = new Set(repositories.map((repository) => normalizedRepositoryName(repository.name)))
  if (!taken.has(base)) return base
  for (let n = 2; ; n++) {
    const suffix = `-${n}`
    const name = base.slice(0, MAX_REPOSITORY_NAME_LENGTH - suffix.length).replace(/[-._]+$/, '') + suffix
    if (!taken.has(name)) return name
  }
}

export function getSourceIssueCopy(sourceIssue: UpgradeInfo['sourceIssue'], sourceError?: string) {
  switch (sourceIssue) {
    case 'release_changing':
      return {
        title: 'Helm operation in progress',
        body: 'Wait for the current operation to finish, then refresh the source state.',
      }
    case 'source_confirmation_required':
      return {
        title: 'Confirm the recorded source',
        body: 'Verify the original source, or select a source for the next upgrade.',
      }
    case 'source_change_required':
      return {
        title: 'Source choice required',
        body: 'The previous chart change failed. Select a source and preview again before retrying.',
      }
    case 'repo_index_error':
      return { title: 'A Helm repo index failed', body: 'Fix or refresh the repository before associating it.' }
    case 'ambiguous_repository':
    case 'ambiguous_source':
      return {
        title: 'Chart source unresolved',
        body: 'Select the original source, or choose a source for the next upgrade.',
      }
    case 'source_unavailable':
      return {
        title: 'Recorded source unavailable',
        body: 'The release provenance is preserved, but that source cannot currently be verified here.',
      }
    case 'untracked':
      return {
        title: 'Source not recorded',
        body: 'Choose a source for the next upgrade, or record the original source.',
      }
    default:
      return sourceError ? { title: 'Source unresolved', body: sourceError } : undefined
  }
}

export function TrackChartSourceDialog(props: TrackChartSourceDialogProps) {
  return <ChartSourcePicker key={`${props.namespace}/${props.releaseName}`} {...props} />
}

function sourceKey(source: ChartSourceCandidate) {
  return `${source.type}:${publicChartSourceURL(source.url ?? source.reference) ?? source.reference}`
}

function ChartSourcePicker({
  operationError,
  onSelectUpgradeSource,
  open,
  onClose,
  chartName,
  namespace,
  releaseName,
  sourceIssue,
  sourceError,
}: TrackChartSourceDialogProps) {
  const sourceFeature = useRadarFeature('helmSourceIdentity')
  const [view, setView] = useState<'added' | 'public' | 'manual'>('added')
  const [manualType, setManualType] = useState<'repository' | 'oci'>('repository')
  const [selectedKey, setSelectedKey] = useState('')
  const [filter, setFilter] = useState('')
  const [ociPrefix, setOCIPrefix] = useState('')
  const [repositoryName, setRepositoryName] = useState('')
  const [repositoryURL, setRepositoryURL] = useState('')
  const active = useRef(open)
  const generation = useRef(0)
  useEffect(() => {
    active.current = open
    return () => {
      active.current = false
      generation.current += 1
    }
  }, [open])
  const currentSession = () => {
    const startedIn = generation.current
    return () => active.current && generation.current === startedIn
  }
  const {
    data: status,
    error: statusError,
    refetch: refetchStatus,
    isFetching: statusFetching,
  } = useHelmSourceStatus(namespace, releaseName, open)
  const { data: ociSources } = useHelmOCISources()
  const { data: clusterInfo } = useClusterInfo()
  const { data: capabilities } = useCapabilities()
  const { data: repositories, isLoading: repositoriesLoading } = useHelmRepositories()
  const addOCI = useAddOCISource()
  const removeOCI = useRemoveOCISource()
  const addRepository = useAddHelmRepository()
  const setSource = useSetHelmSource(namespace, releaseName)
  const ownerOnly = !useCloudRole().canAtLeast('owner')
  const configManaged = capabilities?.configManagement === 'operator'
  const canEditSources = capabilities != null && !configManaged && !ownerOnly
  const pending = addRepository.isPending || addOCI.isPending
  const sourceBusy = Boolean(statusFetching || !status || status.changing || setSource.isPending || pending)
  const configBusy = !canEditSources || repositoriesLoading || pending
  const titleId = useId()
  const formId = useId()
  const panelId = useId()
  const locations: ('added' | 'public')[] =
    chartName && sourceFeature.support === 'supported' ? ['added', 'public'] : ['added']
  const issueCopy = getSourceIssueCopy(sourceIssue, sourceError)
  const recorded = status?.recorded
  const missingRecorded =
    status && !status.configured && recorded?.type === 'repository' && recorded.url ? recorded : undefined
  const recordedName = missingRecorded ? recordedRepositoryName(missingRecorded.reference, repositories ?? []) : ''
  const sources = [...(status?.suggestions ?? []).map((item) => item.source), ...(status?.candidates ?? [])]
  if (recorded) sources.unshift(recorded)
  const seen = new Set<string>()
  const choices = sources.filter((source) => {
    const key = sourceKey(source)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
  const selected = choices.find((source) => sourceKey(source) === selectedKey)
  const visible = choices.filter((source) =>
    `${source.reference} ${source.url ?? ''}`.toLowerCase().includes(filter.trim().toLowerCase()),
  )
  const selectedMissing = selected && missingRecorded && sourceKey(selected) === sourceKey(missingRecorded)
  const canVerify =
    selected &&
    !status?.confirmed &&
    !status?.sourceChangeRequired &&
    (selected.type === 'oci' || status?.candidates?.some((source) => sourceKey(source) === selectedKey))

  const showAdded = (source?: ChartSourceCandidate) => {
    if (!active.current) return
    setView('added')
    setFilter('')
    setSelectedKey(source ? sourceKey(source) : '')
    void refetchStatus()
  }
  const addRecorded = () => {
    if (!missingRecorded) return
    const isCurrent = currentSession()
    const associate = status?.confirmed !== false
    addRepository.mutate(
      { name: recordedName, url: missingRecorded.url, ...(associate ? { namespace, releaseName } : {}) },
      {
        onSuccess: () => {
          if (!isCurrent()) return
          if (associate) onClose()
          else showAdded(missingRecorded)
        },
      },
    )
  }
  const submitManual = (event: FormEvent) => {
    event.preventDefault()
    const isCurrent = currentSession()
    if (manualType === 'oci') {
      if (!ociPrefix.trim().startsWith('oci://')) return
      const prefix = ociPrefix.trim().replace(/\/+$/, '')
      addOCI.mutate(prefix, {
        onSuccess: () => {
          if (!isCurrent()) return
          setOCIPrefix('')
          showAdded(chartName ? { type: 'oci', reference: `${prefix}/${chartName}` } : undefined)
        },
      })
    } else {
      const url = repositoryURL.trim()
      const name = repositoryName.trim()
      if (!name || !isValidHelmRepositoryURL(url)) return
      addRepository.mutate(
        { name, url },
        {
          onSuccess: (result) => {
            if (!isCurrent()) return
            setRepositoryName('')
            setRepositoryURL('')
            showAdded({ type: 'repository', reference: result.name, url })
          },
        },
      )
    }
  }
  const manualAction = (
    <button
      type="button"
      onClick={() => setView(view === 'manual' ? 'added' : 'manual')}
      className="text-sm text-accent hover:underline"
    >
      {view === 'manual' ? 'Back to sources' : 'Add manually'}
    </button>
  )

  return (
    <DialogPortal
      ariaLabelledBy={titleId}
      open={open}
      onClose={onClose}
      className="max-w-xl w-full max-h-[85vh] h-[36rem] flex flex-col overflow-hidden"
    >
      <header className="shrink-0 px-4 pt-4 pb-3">
        <div className="flex items-center justify-between gap-3">
          <h3 id={titleId} className="text-base font-semibold">
            {view === 'manual' ? 'Add chart source' : 'Choose chart source'}
          </h3>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="p-1 rounded text-theme-text-secondary hover:bg-theme-hover"
          >
            <X className="w-5 h-5" />
          </button>
        </div>
        <p className="mt-2 text-xs text-theme-text-tertiary">
          {chartName && (
            <>
              <span className="font-medium">{chartName}</span> ·{' '}
            </>
          )}
          {sourceStatusLine(status, statusError)}
        </p>
        {!status && statusError && (
          <button
            type="button"
            onClick={() => void refetchStatus()}
            className="text-sm text-accent hover:underline mt-1"
          >
            Retry
          </button>
        )}
        {status?.changing && (
          <button
            type="button"
            onClick={() => void refetchStatus()}
            className="text-sm text-accent hover:underline mt-1"
          >
            Refresh source state
          </button>
        )}
        {issueCopy &&
          sourceIssue !== 'untracked' &&
          sourceIssue !== 'source_unavailable' &&
          sourceIssue !== 'source_confirmation_required' && (
            <p className="text-xs text-theme-text-secondary mt-2">{issueCopy.body}</p>
          )}
        {operationError && (
          <p role="alert" className="text-sm text-warning-text mt-2">
            {operationError}
          </p>
        )}
      </header>
      {configManaged && (
        <div className="shrink-0 px-4 pb-2">
          <OperatorManagedNotice helmValue="helm.ociSources" />
        </div>
      )}
      {!configManaged && ownerOnly && (
        <p className="shrink-0 px-4 pb-2 text-xs text-theme-text-secondary">
          Only organization owners can add or remove chart sources.
        </p>
      )}
      {view !== 'manual' && (
        <div
          role="tablist"
          aria-label="Chart source location"
          className="shrink-0 flex gap-5 px-4 border-b border-theme-border"
        >
          {locations.map((tab, index) => (
            <button
              key={tab}
              id={`${panelId}-${tab}`}
              type="button"
              role="tab"
              aria-controls={panelId}
              aria-selected={view === tab}
              tabIndex={view === tab ? 0 : -1}
              onKeyDown={(event) => {
                const next =
                  event.key === 'Home'
                    ? 0
                    : event.key === 'End'
                      ? locations.length - 1
                      : event.key === 'ArrowRight'
                        ? (index + 1) % locations.length
                        : event.key === 'ArrowLeft'
                          ? (index + locations.length - 1) % locations.length
                          : undefined
                if (next === undefined) return
                event.preventDefault()
                setView(locations[next])
                setFilter('')
                document.getElementById(`${panelId}-${locations[next]}`)?.focus()
              }}
              onClick={() => {
                setView(tab === 'public' ? 'public' : 'added')
                setFilter('')
              }}
              className={`py-2 text-sm border-b-2 ${view === tab ? 'border-accent text-theme-text-primary' : 'border-transparent text-theme-text-secondary'}`}
            >
              {tab === 'added' ? 'My Repos' : 'ArtifactHub'}
            </button>
          ))}
        </div>
      )}
      <div
        id={panelId}
        role={view === 'manual' ? undefined : 'tabpanel'}
        aria-labelledby={view === 'manual' ? undefined : `${panelId}-${view}`}
        className="flex flex-col flex-1 min-h-0"
      >
        {view === 'public' && open && chartName ? (
          <ArtifactHubSourceSearch
            chartName={chartName}
            disabled={configBusy || sourceBusy}
            manualAction={manualAction}
            isConfigured={(source) =>
              source.type === 'oci'
                ? (ociSources ?? []).some((prefix) => publicChartSourceURL(prefix) === source.prefix)
                : (repositories ?? []).some((repository) => publicChartSourceURL(repository.url) === source.url)
            }
            onConfigured={(source) =>
              showAdded(
                source.type === 'oci'
                  ? { type: 'oci', reference: source.url }
                  : { type: 'repository', reference: '', url: source.url },
              )
            }
            onAdd={(chart, source) => {
              const isCurrent = currentSession()
              if (source.type === 'oci')
                addOCI.mutate(source.prefix, {
                  onSuccess: () => {
                    if (isCurrent()) showAdded({ type: 'oci', reference: source.url })
                  },
                })
              else
                addRepository.mutate(
                  { name: recordedRepositoryName(chart.repository.name, repositories ?? []), url: source.url },
                  {
                    onSuccess: (result) => {
                      if (isCurrent()) showAdded({ type: 'repository', reference: result.name, url: source.url })
                    },
                  },
                )
            }}
          />
        ) : view === 'manual' ? (
          <>
            <div className="flex-1 min-h-0 overflow-y-auto p-4 space-y-4" aria-label="Manual source setup">
              <div className="flex gap-3" role="group" aria-label="Source type">
                <button
                  type="button"
                  onClick={() => setManualType('repository')}
                  aria-pressed={manualType === 'repository'}
                  className={`px-3 py-1.5 text-sm rounded-md ${manualType === 'repository' ? 'bg-theme-active' : 'text-theme-text-secondary'}`}
                >
                  Helm repository
                </button>
                <button
                  type="button"
                  onClick={() => setManualType('oci')}
                  aria-pressed={manualType === 'oci'}
                  className={`px-3 py-1.5 text-sm rounded-md ${manualType === 'oci' ? 'bg-theme-active' : 'text-theme-text-secondary'}`}
                >
                  OCI registry
                </button>
              </div>
              <form id={formId} onSubmit={submitManual} className="space-y-3">
                {manualType === 'repository' ? (
                  <>
                    <label className="block text-sm text-theme-text-secondary">
                      Repository name
                      <Input
                        value={repositoryName}
                        onChange={(event) => setRepositoryName(event.target.value)}
                        placeholder="Repository name"
                        disabled={configBusy}
                        className="mt-1 w-full"
                      />
                    </label>
                    <label className="block text-sm text-theme-text-secondary">
                      Repository address
                      <Input
                        value={repositoryURL}
                        onChange={(event) => setRepositoryURL(event.target.value)}
                        placeholder="https://charts.example.com"
                        disabled={configBusy}
                        className="mt-1 w-full"
                      />
                    </label>
                  </>
                ) : (
                  <label className="block text-sm text-theme-text-secondary">
                    OCI prefix
                    <Input
                      value={ociPrefix}
                      onChange={(event) => setOCIPrefix(event.target.value)}
                      placeholder="oci://registry.example.com/charts"
                      disabled={configBusy}
                      className="mt-1 w-full"
                    />
                  </label>
                )}
                <p className="text-xs text-theme-text-tertiary">
                After adding a source, choose it for an upgrade or check and record it for this release.
                </p>
              </form>
              {manualType === 'oci' && ociSources && ociSources.length > 0 && (
                <div className="space-y-2">
                  <p className="text-xs text-theme-text-tertiary">Registered OCI prefixes</p>
                  <ul className="divide-y divide-theme-border">
                    {ociSources.map((source) => (
                      <li key={source} className="flex items-center justify-between gap-2 py-2">
                        <span className="font-mono text-xs break-all">{source}</span>
                        {canEditSources && (
                          <button
                            type="button"
                            onClick={() => removeOCI.mutate(source)}
                            disabled={removeOCI.isPending}
                            aria-label={`Remove ${source}`}
                            className="p-1 shrink-0 text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50"
                          >
                            <Trash2 className="w-4 h-4" />
                          </button>
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {manualType === 'oci' && clusterInfo?.inCluster && (
                <p className="text-xs text-warning-text">
                  Private OCI sources need credentials available to the in-cluster Radar process.
                </p>
              )}
            </div>
            <footer className="shrink-0 flex items-center justify-between gap-3 px-4 py-3 border-t border-theme-border">
              {manualAction}
              <button
                type="submit"
                form={formId}
                disabled={
                  configBusy ||
                  (manualType === 'repository'
                    ? !repositoryName.trim() || !isValidHelmRepositoryURL(repositoryURL)
                    : !ociPrefix.trim().startsWith('oci://'))
                }
                className="btn-brand px-3 py-2 text-sm"
              >
                {manualType === 'repository' ? 'Add repository' : 'Register OCI source'}
              </button>
            </footer>
          </>
        ) : (
          <>
            {choices.length > 6 && (
              <div className="shrink-0 px-4 py-3 border-b border-theme-border">
                <Input
                  aria-label="Filter configured sources"
                  placeholder="Filter by name or address"
                  value={filter}
                  onChange={(event) => {
                    setFilter(event.target.value)
                    setSelectedKey('')
                  }}
                  className="w-full"
                />
              </div>
            )}
            <div className="flex-1 min-h-0 overflow-y-auto" aria-label="Configured chart sources">
              {visible.length === 0 ? (
                <div className="min-h-40 h-full flex flex-col items-center justify-center gap-3 p-4 text-center">
                  <p className="text-sm text-theme-text-secondary">
                    {filter ? 'No matching sources.' : 'No configured sources found.'}
                  </p>
                  {!filter && chartName && sourceFeature.support === 'supported' && (
                    <button type="button" onClick={() => setView('public')} className="btn-brand px-3 py-2 text-sm">
                      Find a public source
                    </button>
                  )}
                </div>
              ) : (
                visible.map((source) => {
                  const suggestion = status?.suggestions?.find((item) => sourceKey(item.source) === sourceKey(source))
                  const isRecorded = recorded && sourceKey(recorded) === sourceKey(source)
                  return (
                    <button
                      key={sourceKey(source)}
                      type="button"
                      onClick={() => setSelectedKey(sourceKey(source))}
                      aria-pressed={selectedKey === sourceKey(source)}
                      aria-label={`Choose configured source: ${source.url ?? source.reference}`}
                      className={`w-full text-left px-4 py-3 border-b border-theme-border hover:bg-theme-hover ${selectedKey === sourceKey(source) ? 'bg-theme-active' : ''}`}
                    >
                      <span className="block text-sm font-medium">
                        {source.type === 'repository' ? source.reference : 'OCI chart'}
                        <span className="ml-2 font-normal text-xs text-theme-text-tertiary">
                          {isRecorded
                            ? status?.confirmed
                              ? 'Current source'
                              : 'Last recorded source'
                            : source.type === 'oci'
                              ? 'Registered source'
                              : 'Configured repository'}
                        </span>
                      </span>
                      <span className="block font-mono text-xs text-theme-text-secondary break-all mt-1">
                        {source.url ?? source.reference}
                      </span>
                      {((isRecorded && !status?.configured) || suggestion?.state === 'index_unavailable') && (
                        <span className="block text-xs text-warning-text mt-1">
                          {isRecorded && !status?.configured
                            ? 'Not configured here'
                            : 'Refresh its index in Helm Catalog'}
                        </span>
                      )}
                    </button>
                  )
                })
              )}
            </div>
            <footer className="shrink-0 border-t border-theme-border px-4 py-3 space-y-2">
              {selectedMissing && (
                <p className="text-xs text-theme-text-tertiary">
                  {recordedName ? (
                    <>
                      Saved in Helm as <span className="font-mono">{recordedName}</span>.
                      {recordedName !== missingRecorded.reference && (
                        <>
                          {' '}
                          The name <span className="font-mono">{missingRecorded.reference}</span> is already taken or
                          needs normalization.
                        </>
                      )}
                    </>
                  ) : (
                    'Radar picks a repository name from the address.'
                  )}
                </p>
              )}
              {selected && !selectedMissing && (
                <p className="text-xs text-theme-text-tertiary">
                  {canVerify
                    ? 'Radar compares the chart information Helm saved. Matching copies can come from more than one publisher.'
                    : 'Review the chart version, settings and resource changes before applying.'}
                </p>
              )}
              <div className="flex flex-wrap items-center justify-between gap-3">
                {manualAction}
                <div className="flex flex-wrap items-center gap-3">
                  {canVerify && !selectedMissing && (
                    <button
                      type="button"
                      onClick={() => {
                        const isCurrent = currentSession()
                        if (selected)
                          setSource.mutate(selected, {
                            onSuccess: () => {
                              if (isCurrent()) onClose()
                            },
                          })
                      }}
                      disabled={sourceBusy}
                      className="text-sm text-accent hover:underline disabled:opacity-50"
                    >
                      Check and record source
                    </button>
                  )}
                  {selectedMissing ? (
                    <button
                      type="button"
                      onClick={addRecorded}
                      disabled={configBusy || sourceBusy}
                      className="btn-brand px-3 py-2 text-sm"
                    >
                      {status?.confirmed !== false ? 'Add repository and use it' : 'Add repository'}
                    </button>
                  ) : sourceFeature.support === 'supported' && onSelectUpgradeSource && status?.revision ? (
                    <button
                      type="button"
                      disabled={!selected || sourceBusy}
                      onClick={() => {
                        if (selected && status.revision)
                          onSelectUpgradeSource({
                            source: selected,
                            expectedRevision: status.revision,
                            expectedSource: status.recorded ?? null,
                          })
                      }}
                      className="btn-brand px-3 py-2 text-sm"
                    >
                      Select for upgrade
                    </button>
                  ) : null}
                </div>
              </div>
            </footer>
          </>
        )}
      </div>
    </DialogPortal>
  )
}
