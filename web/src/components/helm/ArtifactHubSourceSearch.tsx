import { useState, type ReactNode } from 'react'
import { Badge, Input } from '@skyhook-io/k8s-ui'
import { useArtifactHubSearch } from '../../api/client'
import type { ArtifactHubChart } from '../../types'
import { artifactHubPublisher, isArtifactHubOfficial } from '../../utils/artifacthub'

export type PublicChartSource = { type: 'repository'; url: string } | { type: 'oci'; url: string; prefix: string }

export function publicChartSourceURL(raw: string): string | undefined {
  try {
    const url = new URL(raw)
    if (
      !['http:', 'https:', 'oci:'].includes(url.protocol) ||
      !url.hostname ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    )
      return
    url.hostname = url.hostname.toLowerCase()
    return url.toString().replace(/\/+$/, '')
  } catch {
    return
  }
}

export function artifactHubChartSource(chart: ArtifactHubChart, chartName: string): PublicChartSource | undefined {
  if (chart.name !== chartName) return
  const address = publicChartSourceURL(chart.repository.url)
  if (!address) return
  if (!address.startsWith('oci://')) return { type: 'repository', url: address }
  if (address.endsWith(`/${chartName}`))
    return { type: 'oci', url: address, prefix: address.slice(0, -(chartName.length + 1)) }
}

interface ArtifactHubSourceSearchProps {
  chartName: string
  disabled: boolean
  isConfigured: (source: PublicChartSource) => boolean
  onAdd: (chart: ArtifactHubChart, source: PublicChartSource) => void
  onConfigured: (source: PublicChartSource) => void
  manualAction: ReactNode
}

const PAGE_SIZE = 60

function listingPriority(chart: ArtifactHubChart) {
  return Number(isArtifactHubOfficial(chart)) * 2 + Number(Boolean(chart.repository.verifiedPublisher))
}

function listingKey(chart: ArtifactHubChart) {
  return JSON.stringify([
    chart.packageId,
    chart.repository.organizationName,
    chart.repository.userAlias,
    chart.repository.name,
    chart.repository.organizationDisplayName,
    chart.repository.displayName,
    chart.version,
  ])
}

export function ArtifactHubSourceSearch({
  chartName,
  disabled,
  isConfigured,
  onAdd,
  onConfigured,
  manualAction,
}: ArtifactHubSourceSearchProps) {
  const [started, setStarted] = useState(false)
  const [offset, setOffset] = useState(0)
  const [selectedURL, setSelectedURL] = useState('')
  const [filter, setFilter] = useState('')
  const search = useArtifactHubSearch(chartName, { offset, limit: PAGE_SIZE }, started)
  const listings = new Map<string, { chart: ArtifactHubChart; source: PublicChartSource }>()
  for (const chart of search.data?.charts ?? []) {
    const source = artifactHubChartSource(chart, chartName)
    if (!source) continue
    const current = listings.get(source.url)
    if (
      !current ||
      listingPriority(chart) > listingPriority(current.chart) ||
      (listingPriority(chart) === listingPriority(current.chart) && listingKey(chart) < listingKey(current.chart))
    ) {
      listings.set(source.url, { chart, source })
    }
  }
  const matches = [...listings.values()]

  const selected = matches.find((item) => item.source.url === selectedURL)
  const filtered = matches.filter(({ chart, source }) =>
    `${chart.repository.name} ${chart.repository.displayName ?? ''} ${artifactHubPublisher(chart.repository)} ${chart.repository.organizationName ?? ''} ${chart.repository.userAlias ?? ''} ${source.url}`
      .toLowerCase()
      .includes(filter.trim().toLowerCase()),
  )
  const changePage = (nextOffset: number) => {
    setOffset(nextOffset)
    setSelectedURL('')
    setFilter('')
  }

  return (
    <section className="flex flex-col flex-1 min-h-0" aria-label="Public chart sources">
      {!started ? (
        <div className="flex-1 flex flex-col items-center justify-center gap-3 p-6 text-center">
          <p className="text-sm text-theme-text-secondary">
            Find public repositories for <span className="font-medium text-theme-text-primary">{chartName}</span>.
          </p>
          <button type="button" onClick={() => setStarted(true)} className="btn-brand px-3 py-2 text-sm">
            Search ArtifactHub
          </button>
          <p className="text-xs text-theme-text-tertiary">Only the chart name is sent.</p>
        </div>
      ) : (
        <>
          <div className="shrink-0 px-4 py-3 border-b border-theme-border space-y-2" aria-live="polite">
            <p className="text-xs text-theme-text-secondary">
              Matching names and ArtifactHub badges do not prove where this release was installed from.
            </p>
            {matches.length > 6 && (
              <Input
                aria-label="Filter public sources on this page"
                value={filter}
                onChange={(event) => {
                  setFilter(event.target.value)
                  setSelectedURL('')
                }}
                placeholder="Filter this page by publisher or address"
                className="w-full"
              />
            )}
            {search.isFetching && <p className="text-xs text-theme-text-secondary">Searching ArtifactHub...</p>}
            {search.error && (
              <div role="alert" className="text-sm text-theme-text-secondary">
                <p>Could not search ArtifactHub. You can still add a source manually.</p>
                <button
                  type="button"
                  onClick={() => void search.refetch()}
                  disabled={search.isFetching}
                  className="text-accent hover:underline disabled:opacity-50"
                >
                  Retry search
                </button>
              </div>
            )}
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto" aria-label="Public source results">
            {!search.isFetching && !search.error && search.data && filtered.length === 0 && (
              <p className="p-4 text-sm text-theme-text-secondary">
                {filter
                  ? 'No matching sources on this page.'
                  : 'No exact chart-name matches on this page. Try another page or add a source manually.'}
              </p>
            )}
            {filtered.map(({ chart, source }) => (
              <button
                key={source.url}
                type="button"
                aria-label={`Choose source: ${source.url}`}
                aria-pressed={selectedURL === source.url}
                onClick={() => setSelectedURL(source.url)}
                className={`w-full text-left px-4 py-3 border-b border-theme-border hover:bg-theme-hover ${selectedURL === source.url ? 'bg-theme-active' : ''}`}
              >
                <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-sm font-medium">{chart.repository.displayName || chart.repository.name}</span>
                  <span className="text-xs text-theme-text-tertiary">
                    {artifactHubPublisher(chart.repository) !==
                      (chart.repository.displayName || chart.repository.name) && artifactHubPublisher(chart.repository)}
                  </span>
                  {isConfigured(source) && (
                    <Badge tone="note" size="sm">
                      Configured
                    </Badge>
                  )}
                  {isArtifactHubOfficial(chart) && (
                    <Badge tone="note" size="sm">
                      {chart.official ? 'ArtifactHub official package' : 'ArtifactHub official repository'}
                    </Badge>
                  )}
                  {chart.repository.verifiedPublisher && (
                    <Badge tone="note" size="sm">
                      ArtifactHub verified publisher
                    </Badge>
                  )}
                </span>
                <span className="block font-mono text-xs text-theme-text-secondary break-all mt-1">{source.url}</span>
              </button>
            ))}
          </div>
          {search.data && (
            <div className="shrink-0 flex items-center justify-between gap-3 px-4 py-2 border-t border-theme-border text-xs">
              <button
                type="button"
                onClick={() => changePage(offset - PAGE_SIZE)}
                disabled={offset === 0 || search.isFetching}
                className="text-accent hover:underline disabled:opacity-50"
              >
                Previous results
              </button>
              <span className="text-theme-text-tertiary">Search page {offset / PAGE_SIZE + 1}</span>
              <button
                type="button"
                onClick={() => changePage(offset + PAGE_SIZE)}
                disabled={search.data.charts.length < PAGE_SIZE || search.isFetching}
                className="text-accent hover:underline disabled:opacity-50"
              >
                Next results
              </button>
            </div>
          )}
        </>
      )}
      <div className="shrink-0 flex items-center justify-between gap-3 px-4 py-3 border-t border-theme-border">
        {manualAction}
        {started && (
          <button
            type="button"
            disabled={
              !selected || search.isFetching || Boolean(search.error) || (!isConfigured(selected.source) && disabled)
            }
            onClick={() => {
              if (!selected) return
              if (isConfigured(selected.source)) onConfigured(selected.source)
              else onAdd(selected.chart, selected.source)
            }}
            className="btn-brand px-3 py-2 text-sm disabled:opacity-50"
          >
            {selected && isConfigured(selected.source)
              ? 'Show in My Repos'
              : selected?.source.type === 'oci'
                ? 'Register OCI source'
                : 'Add repository'}
          </button>
        )}
      </div>
    </section>
  )
}
