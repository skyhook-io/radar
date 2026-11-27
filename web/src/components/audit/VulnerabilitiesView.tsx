import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { WithTooltip } from '../ui/Tooltip'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { AlertTriangle, ArrowDownWideNarrow, CircleDashed, FileSpreadsheet, ExternalLink, Info, Search, ShieldAlert, ShieldCheck, X } from 'lucide-react'
import {
  Collapse,
  CollapseChevron,
  EmptyState,
  FreshnessControl,
  getRadarUpgradeRequirement,
  PageHeader,
  PaneLoader,
  RadarUpgradeAction,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  useDebouncedValue,
  useDisclosure,
  VULN_SEVERITY_TEXT,
  VULN_SEVERITY_BAR,
  DistributionBar,
  DistributionLegendChip,
  FilterPill,
  Input,
  RowActionMenu,
  type RowActionItem,
} from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { triggerDownload } from '@skyhook-io/k8s-ui/utils/download'
import { ApiError, useCapabilities, useRadarFeature } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import {
  fetchVulnerabilitiesCSV,
  hasVulnerabilityReports,
  vulnerabilitiesCSVFilename,
  JAVA4SHELL_CVE,
  useVulnerabilities,
  isKindUnknown,
  parseWorkloadParam,
  workloadParam,
  type VulnerabilitiesParams,
  type VulnerabilityExportView,
  type VulnerabilitiesResponse,
  type VulnerabilityCVE,
  type VulnerabilityFix,
  type VulnerabilityGap,
  type VulnerabilitySeverity,
  type VulnerabilityView,
  type VulnerabilityWorkload,
  type VulnerabilityWorkloadRef,
  type VulnerableImage,
} from '../../api/vulnerabilities'
import type { SelectedResource } from '../../types'
import { OwnerCell, OwnerProvider, ownerInfo, ownerTip, useOwners } from '../vulnerabilities/owner'
import { EndOfLifeBadge, OPTIONAL_FILTERS, OptionalHead, availableColumns, columnsFor, daysSince, isoDate, optionalColumn, useOptionalColumns, useOptionalFilters } from '../vulnerabilities/columns'
import { useConnection } from '../../context/ConnectionContext'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { ChecksViewTabs } from './ChecksViewTabs'
import { Java4ShellPanel } from './Java4ShellPanel'
import {
  SEVERITIES,
  SeverityBadge,
  SeverityBar,
  SeverityCounts,
  distinctNamespaces,
  totalFindings,
  FindingTotal,
  installedLabel,
  ImageName,
  FixVersion,
  ecosystemLabel,
  findingsCertainty,
  readsComplete,
  workloadsByReplicaSet,
  ClearedCount,
  OTHER_BUILD_TIP,
  imageParts,
  gapSentence,
  WorkloadSummary,
  ExposedMark,
  exposureLookup,
  routesFor,
  routeText,
  routesTip,
  ExposedCount,
  exposureUnknownIn,
  exposureGapSentence,
  uncheckedRoutesSentence,
  EXPOSURE,
  type ExposureLookup,
} from '../vulnerabilities/parts'

interface VulnerabilitiesViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

const VIEWS: { key: VulnerabilityView; label: string }[] = [
  { key: 'fix', label: 'By fix' },
  { key: 'image', label: 'By image' },
  { key: 'cve', label: 'By CVE' },
  { key: 'workload', label: 'By workload' },
]

const CHIP = 'min-h-8 rounded-full border px-3 text-xs font-medium transition-colors'
const CHIP_ON = 'selection-strong border-accent text-theme-text-primary'
const TRIVY_INSTALL_URL = 'https://aquasecurity.github.io/trivy-operator/latest/getting-started/installation/'

// Each table's columns, shared by its header, rows and expanded sub-rows so
// detail lines up under the column it belongs to. Short values get fixed
// tracks; the text columns take the rest.
// The image and CVE tables have optional columns (vulnerabilities/columns),
// so their tracks are built per render rather than fixed classes.
const gridOf = (tracks: (string | false | undefined)[]): React.CSSProperties => ({ gridTemplateColumns: tracks.filter(Boolean).join(' ') })
const trackOf = (view: VulnerabilityView, name: string, on: boolean | undefined) => on && optionalColumn(view, name)?.track
const imageGrid = (c: Record<string, boolean>) => gridOf(['minmax(220px,1.6fr)', trackOf('image', 'os', c.os), 'minmax(180px,1.2fr)', '88px', 'minmax(0,2fr)', trackOf('image', 'scanner', c.scanner), '72px'])
const cveGrid = (c: Record<string, boolean>) => gridOf(['minmax(260px,1.6fr)', '80px', '48px', trackOf('cve', 'published', c.published), trackOf('cve', 'age', c.age), 'minmax(200px,1.6fr)', '56px', '64px', '80px'])
const FIX_COLS = 'grid-cols-[minmax(200px,1.2fr)_minmax(220px,1.5fr)_120px_minmax(0,1.6fr)_64px_80px]'
// The route column shows only when the answer reports exposure.
const workloadGrid = (route: boolean, c: Record<string, boolean>) => gridOf(['minmax(200px,1.4fr)', trackOf('workload', 'owner', c.owner), 'minmax(180px,1.2fr)', route && 'minmax(180px,1.2fr)', route ? 'minmax(0,2fr)' : 'minmax(0,2.4fr)'])
const HEAD = 'grid gap-4 border-b border-theme-border py-2 pl-10 pr-3 text-[11px] font-semibold uppercase tracking-wider text-theme-text-tertiary'
const FOLD_ROW = 'grid grid-cols-[minmax(220px,1.6fr)_minmax(0,4fr)] items-baseline gap-4 py-1.5 pl-10 pr-3 text-xs'
const SUB = 'grid items-baseline gap-4 py-1.5 pl-10 pr-3 text-xs'

const toResource = (w: VulnerabilityWorkloadRef): SelectedResource => ({ kind: w.kind, group: w.group, namespace: w.namespace, name: w.name })

// The Checks › Vulnerabilities tab: scanner findings for the images that run
// now, from /api/vulnerabilities. Filters live in the URL so a filtered view
// is a shareable link; they run server-side, and the summary tiles always
// describe the unfiltered scope.
export function VulnerabilitiesView({ namespaces, onNavigateToResource }: VulnerabilitiesViewProps) {
  const [params, setParams] = useSearchParams()
  const view = (VIEWS.find((v) => v.key === params.get('by'))?.key ?? 'fix') as VulnerabilityView
  const severities = (params.get('severity') ?? '').split(',').filter((s): s is VulnerabilitySeverity => LEGEND.some((x) => x.key === s))
  const fixableOnly = params.get('fixable') === '1'
  const exposedOnly = params.get('exposed') === '1'
  const eolOnly = params.get('eol') === '1'
  const optional = useOptionalColumns()
  const optionalFilters = useOptionalFilters()
  const queryDraft = params.get('q') ?? ''
  const workload = parseWorkloadParam(params.get('workload'))
  const query = useDebouncedValue(queryDraft.trim(), 300)
  const { connection } = useConnection()
  const { data: capabilities } = useCapabilities()
  const { data: resources } = useAPIResources()
  const java4ShellSupported = capabilities?.features?.runtimeAdvisoryChecks === true
  const reportsAbsent = java4ShellSupported && resources !== undefined && !hasVulnerabilityReports(resources)

  // Typing and chip toggles replace the entry; a new view or a drill-down
  // pushes one, so Back returns to the list it came from.
  const update = (mutate: (p: URLSearchParams) => void, push = false) => {
    const next = new URLSearchParams(params)
    mutate(next)
    setParams(next, { replace: !push })
  }
  const tableParams: VulnerabilitiesParams = { view, namespaces, severities, fixableOnly, exposed: exposedOnly, osEndOfLife: eolOnly, query, workload }
  const { data, isLoading, isFetching, isPlaceholderData, error, dataUpdatedAt, refetch } = useVulnerabilities(tableParams, !reportsAbsent)
  const hasReports = !reportsAbsent && (hasVulnerabilityReports(resources) || !!data)
  const trivySearch = new URLSearchParams({ by: 'cve', q: JAVA4SHELL_CVE })
  for (const key of ['namespace', 'namespaces']) {
    const value = params.get(key)
    if (value) trivySearch.set(key, value)
  }
  const advisory = java4ShellSupported && (
    <Java4ShellPanel
      namespaces={namespaces}
      context={connection.context}
      connected={connection.state === 'connected'}
      trivySearch={hasReports ? trivySearch.toString() : undefined}
      onNavigateToResource={onNavigateToResource}
    />
  )

  const toggleSeverity = (s: VulnerabilitySeverity) => update((p) => {
    const next = severities.includes(s) ? severities.filter((x) => x !== s) : [...severities, s]
    if (next.length > 0) p.set('severity', next.join(','))
    else p.delete('severity')
  })

  const header = <PageHeader icon={ShieldAlert} title="Vulnerabilities" description={PAGE_DESCRIPTION} />
  if (isLoading && !reportsAbsent) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
        <ChecksViewTabs />
        {header}
        {advisory}
        <PaneLoader label="Reading vulnerability reports…" className="flex-1" />
      </div>
    )
  }
  if (!data || reportsAbsent) {
    // No report kind: nothing failed, the cluster has no scanner yet.
    const noScanner = reportsAbsent || isKindUnknown(error)
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
        <ChecksViewTabs />
        {header}
        {advisory}
        {noScanner ? <NoScannerState /> : (
          <div className="flex flex-1 items-center justify-center"><VulnerabilitiesError error={error} /></div>
        )}
      </div>
    )
  }

  // By fix lists only fixable findings, and hides the chip that says so.
  // An older Radar ignores exposed=1 and reports no exposure, and ignores
  // eol=1 and sends no end-of-life count.
  const exposure = data.exposure
  const eolCount = data.summary.imagesOsEndOfLife
  // An optional chip: shown when this browser shows it and there is something
  // to filter to, and always while a link sets it.
  const eolChip = eolCount !== undefined && (eolOnly || (optionalFilters.shown('eol') && eolCount > 0))
  // Optional filters this Radar can apply: an older one sends no end-of-life count.
  const filterAvailable = (key: string) => key !== 'eol' || eolCount !== undefined
  const filtered = severities.length > 0 || (fixableOnly && view !== 'fix') || (exposedOnly && !!exposure) || (eolOnly && eolCount !== undefined) || query !== '' || !!workload
  // Narrowed only to a workload whose images are all clean or unscanned:
  // the folds below list them, so "nothing matches" would mislead.
  const workloadFolds = !!workload && (view === 'image' || view === 'workload') && severities.length === 0 && !fixableOnly && query === '' &&
    ((data.unscanned ?? []).length > 0 || (data.clean ?? []).length > 0 || (data.cleanWorkloads ?? []).length > 0)
  const rows = view === 'cve' ? data.cves ?? [] : view === 'workload' ? data.workloads ?? [] : view === 'fix' ? data.fixes ?? [] : data.images ?? []

  // With nothing scanned or readable there are no counts to show or filter.
  const known = findingsCertainty(data) !== 'unknown'
  // A new view or filter starts again from the first page of rows.
  // Every filter lives in the URL; the view and the namespace scope aren't filters.
  const clearFilters = () => update((p) => { for (const key of [...p.keys()]) if (!KEEP_ON_CLEAR.has(key)) p.delete(key) }, true)
  const pageKey = JSON.stringify([view, namespaces, severities, fixableOnly, exposedOnly, eolOnly, query, workload])
  // Owners were read and none of these workloads has one: a column of blanks
  // would look broken, so the menu item says so instead.
  const ownerNone = !!data.ownership && !data.ownership.unavailable && (data.owners ?? []).length === 0
  const fixChip = known && view !== 'fix'
  return (
    <div aria-busy={isFetching} className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <ChecksViewTabs />
      <PageHeader
        icon={ShieldAlert}
        title="Vulnerabilities"
        description={PAGE_DESCRIPTION}
        actions={
          <div className="flex items-center gap-2">
            <FreshnessControl mode="auto" dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} onRefresh={() => refetch()} connectionState={connection.state} />
            <PageMenu
              params={tableParams}
              cluster={connection.clusterName || connection.context}
              columns={data.view === view ? availableColumns(view, data).map((c) => ({ key: c.key, label: c.label, shown: optional.shown.has(c.key), ...(c.key === 'workload.owner' && ownerNone ? NO_OWNER : {}) })) : []}
              onToggleColumn={optional.toggle}
              filters={OPTIONAL_FILTERS.filter((f) => filterAvailable(f.key)).map((f) => ({ key: f.key, label: f.label, shown: optionalFilters.shown(f.key) }))}
              onToggleFilter={optionalFilters.toggle}
            />
          </div>
        }
      />
      {advisory}

      {/* Triage header, as on Best practices: the headline, the severity
          distribution, and its legend as the severity filters. */}
      <div className={clsx('flex flex-col gap-3.5 rounded-2xl border border-theme-border bg-theme-surface p-4 shadow-theme-sm transition-opacity', isPlaceholderData && 'opacity-60')}>
        <div className="flex flex-wrap items-end justify-between gap-3">
          <SummaryHeadline data={data} />
          <div className="relative">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-theme-text-tertiary" aria-hidden />
            <Input
              type="search"
              aria-label="Search"
              value={queryDraft}
              onChange={(e) => update((p) => (e.target.value ? p.set('q', e.target.value) : p.delete('q')))}
              placeholder="Image, CVE, package or workload"
              className="w-72 rounded-lg border border-theme-border-light bg-theme-base py-1.5 pl-9 pr-3 text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-[var(--color-radar-accent,var(--accent))]"
            />
          </div>
        </div>

        {known && <DistributionBar
          ariaLabel="Findings by severity"
          segments={LEGEND.map((s) => ({ key: s.key, count: data.summary.counts[s.key], fillClass: VULN_SEVERITY_BAR[s.key.toUpperCase()] }))}
        />}

        <div className="flex flex-wrap items-center gap-1.5">
          {known && LEGEND.map((s) => (
            <DistributionLegendChip
              key={s.key}
              label={s.label}
              count={data.summary.counts[s.key]}
              fillClass={VULN_SEVERITY_BAR[s.key.toUpperCase()]}
              textClass={VULN_SEVERITY_TEXT[s.key.toUpperCase()]}
              active={severities.includes(s.key)}
              onClick={() => toggleSeverity(s.key)}
            />
          ))}
          {known && (fixChip || exposure || eolChip) && <span aria-hidden className="mx-1.5 h-5 w-px bg-theme-border" />}
          {fixChip && <FilterPill label="Fix available" tooltip="Only findings the scanner lists a fixed release for" active={fixableOnly} onClick={() => update((p) => (fixableOnly ? p.delete('fixable') : p.set('fixable', '1')))} />}
          {exposure && (
            <FilterPill
              label={EXPOSURE.chip}
              icon={EXPOSURE.icon}
              tooltip={EXPOSURE.chipTip}
              active={exposedOnly}
              onClick={() => update((p) => (exposedOnly ? p.delete('exposed') : p.set('exposed', '1')))}
            />
          )}
          {eolChip && (
            <FilterPill
              label="OS end of life"
              count={eolCount}
              tooltip="Only images whose OS release Trivy reports as end of life. Images not scanned yet stay listed: their OS is unknown."
              active={eolOnly}
              onClick={() => update((p) => (eolOnly ? p.delete('eol') : p.set('eol', '1')))}
            />
          )}
          {workload && (
            <>
              <span aria-hidden className="mx-1.5 h-5 w-px bg-theme-border" />
              <span className={clsx(CHIP, CHIP_ON, 'inline-flex items-center gap-1.5 pr-1.5')}>
                {workload.name}
                <span className="font-normal text-theme-text-tertiary">{workload.kind} · {workload.namespace}</span>
                <button type="button" aria-label="Show every workload" onClick={() => update((p) => p.delete('workload'))} className="rounded-full p-0.5 hover:bg-theme-hover">
                  <X className="h-3 w-3" aria-hidden />
                </button>
              </span>
            </>
          )}
          <div role="group" aria-label="Group by" className="ml-auto flex rounded-md border border-theme-border bg-theme-elevated p-0.5">
            {VIEWS.map((v) => (
              <button
                key={v.key}
                type="button"
                aria-pressed={view === v.key}
                onClick={() => update((p) => (v.key === 'fix' ? p.delete('by') : p.set('by', v.key)), true)}
                className={clsx('min-h-7 rounded px-3 text-xs font-medium transition-colors', view === v.key ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary')}
              >
                {v.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="flex flex-col gap-1.5">
        <CoverageNotes
          data={data}
          view={view}
          matches={filtered && data.view === view ? {
            count: rows.length + (data.coverage.rowsOmitted ?? 0),
            // Every filter lives in the URL; the view and the namespace scope aren't filters.
            onClear: clearFilters,
          } : undefined}
        />
        {error && <p className="text-xs text-warning-text">Couldn't refresh: {error instanceof Error ? error.message : 'request failed'}. Showing the last answer.</p>}
      </div>

      {/* A view switched to for the first time shows the last answer's
          header while its own rows load. */}
      {data.view !== view ? (
        <PaneLoader label="Reading vulnerability reports…" className="min-h-48" />
      ) : (
        <OwnerProvider info={ownerInfo(data)}>
        {rows.length === 0 ? (
          <EmptyState
            tone="neutral"
            icon={ShieldAlert}
            headline={emptyHeadline(view, filtered, data, workloadFolds ? workload : null)}
            action={filtered && !workloadFolds ? <button type="button" onClick={clearFilters} className="btn-secondary px-3 py-1.5 text-xs font-medium">Clear filters</button> : undefined}
            body={filtered && !workloadFolds ? undefined : data.summary.imagesScanned === 0 && data.summary.imagesUnscanned > 0 ? 'Trivy Operator scans each workload shortly after it starts. If nothing appears within a few minutes, check that it is running and that its target namespaces include these. The images waiting for a report are listed below.' : undefined}
          />
        ) : view === 'fix' ? (
          <Paged key={pageKey} rows={data.fixes ?? []}>{(rows) => <FixTable fixes={rows} onOpen={(w) => onNavigateToResource(toResource(w))} onShowCVE={(id) => update((p) => { p.set('by', 'cve'); p.set('q', id) }, true)} />}</Paged>
        ) : view === 'cve' ? (
          <Paged key={pageKey} rows={data.cves ?? []}>{(rows) => <CVETable cves={rows} columns={columnsFor('cve', optional.shown, data)} onOpen={(w) => onNavigateToResource(toResource(w))} />}</Paged>
        ) : view === 'workload' ? (
          <Paged key={pageKey} rows={data.workloads ?? []}>{(rows) => <WorkloadTable
            workloads={rows}
            columns={{ ...columnsFor('workload', optional.shown, data), ...(ownerNone && { owner: false }) }}
            exposure={exposure}
            severities={severities}
            query={query}
            namespaces={namespaces}
            onOpen={(w) => onNavigateToResource(toResource(w))}
            onShow={(w, by) => update((p) => { p.set('by', by); p.set('workload', workloadParam(w)); p.delete('q') }, true)}
          />}</Paged>
        ) : (
          <Paged key={pageKey} rows={data.images ?? []}>{(rows) => <ImageTable
            images={rows}
            columns={columnsFor('image', optional.shown, data)}
            onOpen={(w) => onNavigateToResource(toResource(w))}
            onOpenReport={(img) => onNavigateToResource({ kind: img.report.kind, group: img.report.group, namespace: img.report.namespace ?? '', name: img.report.name })}
          />}</Paged>
        )}

        {(data.coverage.rowsOmitted ?? 0) > 0 && (
          <p className="text-xs text-theme-text-tertiary">{data.coverage.rowsOmitted} more rows not shown. Narrow with a filter or the namespace picker.</p>
        )}

        {!severities.length && !fixableOnly && (view === 'image' || view === 'workload') && (
          <CleanList data={data} view={view} onOpen={(w) => onNavigateToResource(toResource(w))} />
        )}
        <UnscannedList data={data} onOpen={(w) => onNavigateToResource(toResource(w))} />
        </OwnerProvider>
      )}
    </div>
  )
}

const VIEW_NAMES: Partial<Record<VulnerabilityView, string>> = { fix: 'By fix', image: 'By image', cve: 'By CVE', workload: 'By workload' }

// The page's overflow menu: CSV of the view on screen with its filters, or
// of every finding (one row per CVE, image and workload, the shape a POA&M
// or a ticket import wants). Each lists every row and every value the table
// shortens. Below them, a checkbox for each of the view's optional columns,
// then one for each optional filter chip, each group after a divider.
function PageMenu({ params, cluster, columns: columnChoices, onToggleColumn, filters, onToggleFilter }: {
  params: VulnerabilitiesParams
  cluster: string
  /** The view's optional columns this answer has data for. */
  columns: { key: string; label: string; shown: boolean; hint?: string; tooltip?: string }[]
  onToggleColumn: (key: string) => void
  filters: { key: string; label: string; shown: boolean }[]
  onToggleFilter: (key: string) => void
}) {
  const { support, guard } = useRadarFeature('vulnerabilitiesExport')
  const desktopDownload = useDesktopDownload()
  const download = useMutation({
    // The file is named for the view and cluster the request was for.
    mutationFn: ({ params, as }: { params: VulnerabilitiesParams; as: VulnerabilityExportView; cluster: string }) => guard(() => fetchVulnerabilitiesCSV(params, as)),
    onSuccess: (csv, { as, cluster }) => triggerDownload(csv, 'text/csv;charset=utf-8', vulnerabilitiesCSVFilename(as, cluster), desktopDownload),
    meta: { errorMessage: 'Could not download the CSV' },
  })
  const pending = download.isPending ? download.variables?.as : undefined
  const downloads: RowActionItem[] = support !== 'supported' ? [] : [
    { key: 'view', label: `Download ${VIEW_NAMES[params.view] ?? 'this view'} as CSV`, icon: FileSpreadsheet, pending: pending === params.view, disabled: !!pending, onClick: () => download.mutate({ params, as: params.view, cluster }) },
    { key: 'findings', label: 'Download all findings as CSV', icon: FileSpreadsheet, pending: pending === 'findings', disabled: !!pending, onClick: () => download.mutate({ params, as: 'findings', cluster }) },
  ]
  const columns: RowActionItem[] = columnChoices.map((c) => ({
    key: c.key, label: `Show ${c.label}`, hint: c.hint, tooltip: c.tooltip, checked: c.shown, onClick: () => onToggleColumn(c.key),
  }))
  const filterItems: RowActionItem[] = filters.map((f) => ({
    key: `filter.${f.key}`, label: `Show ${f.label} filter`, checked: f.shown, onClick: () => onToggleFilter(f.key),
  }))
  // Each group after the first opens with a divider.
  const items = [downloads, columns, filterItems].filter((g) => g.length > 0)
    .flatMap((g, i) => g.map((item, j) => ({ ...item, divider: i > 0 && j === 0 })))
  return <RowActionMenu compact={false} ariaLabel="More actions" items={items} />
}

function emptyHeadline(view: VulnerabilityView, filtered: boolean, data: VulnerabilitiesResponse, workload: { name: string } | null): string {
  // Narrowed to one workload, the folds below say what it runs.
  if (workload && (data.unscanned ?? []).length > 0) {
    const scanned = (data.clean ?? []).length > 0 || (data.cleanWorkloads ?? []).length > 0
    return scanned ? `No reported CVEs for ${workload.name}, and not every image it runs is scanned yet` : `No report yet for the images ${workload.name} runs`
  }
  if (workload && ((data.clean ?? []).length > 0 || (data.cleanWorkloads ?? []).length > 0)) return `No CVEs reported for ${workload.name}`
  if (filtered) return 'Nothing matches these filters'
  const s = data.summary
  if (!readsComplete(data.coverage) && totalFindings(s.counts) === 0 && s.imagesClean === 0) return 'No findings could be read in this scope'
  if (s.imagesScanned === 0) return s.imagesUnscanned > 0 ? "Trivy hasn't scanned the images in this scope yet" : 'No workloads run in this scope'
  if (view === 'fix' && totalFindings(s.counts) > 0) return `None of the ${totalFindings(s.counts)} reported findings has a fixed release yet`
  return `No CVEs reported for the ${s.imagesScanned} scanned ${s.imagesScanned === 1 ? 'image' : 'images'} that run`
}

const NO_OWNER = {
  hint: 'none found',
  tooltip: 'Radar found no owner for these workloads. It looks for a Helm release, an Argo CD or Flux app, or a team label on the workload or its namespace.',
}

const PAGE_DESCRIPTION = "Known CVEs in the images that run now, from Trivy Operator's reports."

// A cluster without Trivy Operator: what the page is for, and the one step
// that fills it. The Java4Shell check above it works without Trivy.
function NoScannerState() {
  return (
    <section aria-label="Trivy Operator not installed" className="flex flex-col gap-4 rounded-2xl border border-theme-border bg-theme-surface p-6 shadow-theme-sm">
      <div className="flex items-start gap-3">
        <ShieldAlert className="mt-0.5 h-5 w-5 shrink-0 text-theme-text-tertiary" aria-hidden />
        <div className="flex flex-col gap-1">
          <h2 className="text-base font-semibold text-theme-text-primary">Trivy Operator isn't installed in this cluster</h2>
          <p className="max-w-2xl text-sm text-theme-text-secondary">
            Radar reads the vulnerability reports Trivy Operator writes for each running image. With it installed, this page shows:
          </p>
        </div>
      </div>
      <ul className="grid max-w-3xl list-disc gap-x-8 gap-y-1.5 pl-12 text-sm text-theme-text-secondary sm:grid-cols-2">
        <li>The package upgrades that clear the most Critical and High CVEs</li>
        <li>Every image, CVE and workload, worst first</li>
        <li>Which affected workloads have a route from outside the cluster</li>
        <li>CSV downloads for a compliance tracker or a ticket import</li>
      </ul>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pl-8">
        <a href={TRIVY_INSTALL_URL} target="_blank" rel="noreferrer" className="btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium">
          Install Trivy Operator <ExternalLink className="h-3.5 w-3.5" aria-hidden />
        </a>
        <span className="text-xs text-theme-text-tertiary">Findings appear here once Trivy has scanned your workloads.</span>
      </div>
    </section>
  )
}

function VulnerabilitiesError({ error }: { error: unknown }) {
  const upgrade = getRadarUpgradeRequirement(error)
  if (upgrade) {
    return <EmptyState tone="neutral" icon={ShieldAlert} headline={radarUpgradeHeadline('Vulnerability views')} body={radarUpgradeDetail(upgrade)} action={<RadarUpgradeAction requirement={upgrade} />} />
  }
  return (
    <EmptyState
      tone="neutral"
      icon={AlertTriangle}
      headline="Couldn't read vulnerability reports"
      body={error instanceof ApiError ? error.message : 'Failed to read vulnerability reports.'}
    />
  )
}

// Severities in the legend, unrated last: each is also a filter.
const LEGEND: { key: VulnerabilitySeverity; label: string }[] = [...SEVERITIES.map(({ key, label }) => ({ key, label })), { key: 'unknown', label: 'Unrated' }]

// The headline: images with Critical CVEs, then the other totals in a line.
// Every total is a lower bound while anything is unscanned or unread.
function SummaryHeadline({ data }: { data: VulnerabilitiesResponse }) {
  const s = data.summary
  const certainty = findingsCertainty(data)
  if (certainty === 'unknown') {
    // Unknown because reports couldn't be read is not the same as unscanned;
    // the coverage notes below say what couldn't be read.
    const unread = data.coverage.gaps.length > 0
    return <span className="text-sm text-theme-text-secondary">{unread ? 'Findings unknown' : 'No running image is scanned yet'}{!unread && s.imagesUnscanned > 0 ? ` · ${s.imagesUnscanned} not scanned` : ''}</span>
  }
  const bound = certainty === 'lower_bound' ? '≥ ' : ''
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <div className="flex flex-wrap items-baseline gap-x-2">
        <FindingTotal value={s.imagesWithCritical} certainty={certainty} className="text-2xl font-semibold text-theme-text-primary" />
        <span className="text-sm text-theme-text-secondary">
          {s.imagesWithCritical === 1 ? 'image' : 'images'} with Critical CVEs
          <span className="text-theme-text-tertiary">{` · of ${s.imagesScanned} scanned${s.imagesUnscanned > 0 ? `, ${s.imagesUnscanned} not scanned yet` : ''}`}</span>
        </span>
      </div>
      <span className="text-xs text-theme-text-tertiary">
        {`${bound}${s.fixableCriticalHighCves} Critical and High CVEs with a fix · ${bound}${s.workloadsWithCriticalOrHigh} of ${s.workloads} workloads with Critical or High`}
        {workloadsByReplicaSet(data.coverage) && ' (a Deployment may count once per ReplicaSet)'}
      </span>
    </div>
  )
}

// How each view orders its rows, for the sort hint's tooltip.
function sortRule(view: VulnerabilityView): string {
  const tiebreak = view === 'workload' ? '' : ' Then the most exposed workloads, then the most workloads.'
  return `Most severe first. Within each severity, exposed rows come first, then those whose exposure is unknown.${tiebreak}`
}

// URL keys that clearing the filters keeps: the view and the namespace scope.
const KEEP_ON_CLEAR = new Set(['by', 'namespace', 'namespaces'])

const ROW_NOUNS: Partial<Record<VulnerabilityView, [string, string]>> = {
  fix: ['fix', 'fixes'], image: ['image', 'images'], cve: ['CVE', 'CVEs'], workload: ['workload', 'workloads'],
}

function CoverageNotes({ data, view, matches }: {
  data: VulnerabilitiesResponse
  view: VulnerabilityView
  /** Set while filters narrow the table: how many rows match, and how to clear them. */
  matches?: { count: number; onClear: () => void }
}) {
  const [one, many] = ROW_NOUNS[view] ?? ['row', 'rows']
  const gaps = gapSentence(data.coverage.gaps)
  const exposureNotes = [exposureGapSentence(data.exposure?.gaps), uncheckedRoutesSentence(data.exposure?.unchecked)].filter(Boolean).join(' ')
  const unscannedExposed = (data.unscanned ?? []).reduce((n, u) => n + (u.exposedTotal ?? 0), 0)
  const scan = data.summary.newestScan
    ? data.summary.oldestScan && data.summary.oldestScan !== data.summary.newestScan
      ? `Scans from ${formatRelativeAgeTime(data.summary.oldestScan)} to ${formatRelativeAgeTime(data.summary.newestScan)}`
      : `Scanned ${formatRelativeAgeTime(data.summary.newestScan)}`
    : null
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-theme-text-tertiary">
        {scan}
        {matches && (
          <>
            <span aria-hidden>·</span>
            <span className="text-theme-text-secondary">{matches.count.toLocaleString('en-US')} {matches.count === 1 ? one : many} match</span>
            <button type="button" onClick={matches.onClear} className="text-accent-text hover:underline">Clear filters</button>
          </>
        )}
        {/* For someone comparing these numbers with Trivy's own: why they can differ.
            With nothing scanned there are no numbers or order to explain. */}
        {data.summary.imagesScanned > 0 && <WithTooltip tip={[
          "Reports list what was known when each image was scanned, after Trivy's own filters.",
          'A CVE counts once per package version in an image, and findings Trivy left unrated are rated from their CVSS score, so totals can differ from the summaries in Trivy\'s reports.',
          data.coverage.inactiveReports > 0 ? `${data.coverage.inactiveReports} ${data.coverage.inactiveReports === 1 ? 'report is' : 'reports are'} about older revisions or builds that don't run now, and ${data.coverage.inactiveReports === 1 ? "isn't" : "aren't"} counted.` : '',
        ].filter(Boolean).join(' ')}>
          <Info className="h-3.5 w-3.5" aria-label="How these numbers relate to Trivy's" />
        </WithTooltip>}
        {data.exposure && data.summary.imagesScanned > 0 && (
          <WithTooltip tip={sortRule(view)}>
            <span className="inline-flex items-center gap-1 rounded px-1 hover:text-theme-text-secondary" tabIndex={0} aria-label={`Sort: ${sortRule(view)}`}>
              <ArrowDownWideNarrow className="h-3.5 w-3.5" aria-hidden />
              Severity, then exposure
            </span>
          </WithTooltip>
        )}
        {exposureNotes && (
          <WithTooltip tip={exposureNotes}>
            <span className="inline-flex items-center gap-1 text-warning-text" tabIndex={0} aria-label={exposureNotes}>
              <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
              Exposure partly unknown
            </span>
          </WithTooltip>
        )}
        {unscannedExposed > 0 && (
          <WithTooltip tip="Exposed workloads whose images Trivy hasn't scanned yet: their findings are unknown. They're listed under the table.">
            <span className="inline-flex items-center gap-1 text-warning-text" tabIndex={0}>
              <CircleDashed className="h-3.5 w-3.5" aria-hidden />
              {unscannedExposed} exposed {unscannedExposed === 1 ? 'workload' : 'workloads'} not scanned
            </span>
          </WithTooltip>
        )}
      </p>
      {gaps && <p className="text-warning-text">{gaps}</p>}
      {data.coverage.truncated && <p className="text-warning-text">This request reached its findings limit, so some images' findings weren't read and are marked unread. Narrow with the namespace picker.</p>}
    </div>
  )
}

// Rows drawn at a time: a pivot can return 2,000, and drawing them all makes
// switching views slow. The worst rows come first.
const PAGE_ROWS = 100

function Paged<T>({ rows, children }: { rows: T[]; children: (rows: T[]) => React.ReactNode }) {
  const [shown, setShown] = useState(PAGE_ROWS)
  const rest = rows.length - shown
  return (
    <>
      {children(rows.slice(0, shown))}
      {rest > 0 && (
        <button type="button" onClick={() => setShown(shown + PAGE_ROWS)} className="self-start text-xs text-accent-text hover:underline">
          Show {Math.min(PAGE_ROWS, rest)} more ({rest} not shown)
        </button>
      )}
    </>
  )
}

function Disclosure({ summary, children, flush = false }: { summary: React.ReactNode; children: React.ReactNode | ((open: boolean) => React.ReactNode); flush?: boolean }) {
  const [open, setOpen] = useState(false)
  const { panelId, buttonProps } = useDisclosure(open)
  return (
    <div className="border-b border-theme-border last:border-0">
      <button {...buttonProps} type="button" onClick={() => setOpen(!open)} className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-theme-hover">
        <CollapseChevron open={open} className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
        {summary}
      </button>
      <Collapse open={open} id={panelId} mountLazily>
        <div className={clsx('border-t border-theme-border bg-theme-base', flush ? 'py-2' : 'px-3 py-3 pl-10')}>{typeof children === 'function' ? children(open) : children}</div>
      </Collapse>
    </div>
  )
}

function WorkloadLinks({ workloads, onOpen, inline = false, exposure }: { workloads: VulnerabilityWorkloadRef[]; onOpen: (w: VulnerabilityWorkloadRef) => void; inline?: boolean; exposure?: ExposureLookup }) {
  const owners = useOwners()
  return (
    <ul className={clsx('flex min-w-0', inline ? 'flex-wrap gap-x-4 gap-y-0.5' : 'flex-col gap-0.5')}>
      {workloads.map((w) => {
        const exposed = routesFor(exposure, w)
        return (
        <li key={`${w.namespace}/${w.kind}/${w.name}/${w.container ?? ''}`} className="flex min-w-0 items-baseline gap-1">
          {exposed && <ExposedMark exposed={exposed} />}
          <WithTooltip tip={ownerTip(owners?.find(w))}>
            <button type="button" onClick={() => onOpen(w)} className="group flex min-w-0 items-baseline gap-1.5 text-left text-xs">
              <span className="truncate text-accent-text group-hover:underline">{w.name}</span>
              <span className="shrink-0 text-theme-text-tertiary">{w.kind} · {w.namespace}{w.container ? ` · ${w.container}` : ''}</span>
            </button>
          </WithTooltip>
        </li>
        )
      })}
    </ul>
  )
}

function NumHead({ children }: { children: React.ReactNode }) {
  return <span className="text-right">{children}</span>
}

function Num({ children, strong = false }: { children: React.ReactNode; strong?: boolean }) {
  return <span className={clsx('text-right font-mono text-xs tabular-nums', strong ? 'font-medium text-theme-text-primary' : 'text-theme-text-secondary')}>{children}</span>
}

function ImageTable({ images, columns, onOpen, onOpenReport }: { images: VulnerableImage[]; columns: Record<string, boolean>; onOpen: (w: VulnerabilityWorkloadRef) => void; onOpenReport: (img: VulnerableImage) => void }) {
  const grid = imageGrid(columns)
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div style={{ minWidth: 860 + (columns.os ? 156 : 0) + (columns.scanner ? 128 : 0) }}>
        <div className={HEAD} style={grid}>
          <span>Image</span>{columns.os && <OptionalHead column={optionalColumn('image', 'os')} />}<span>Findings</span><NumHead>With a fix</NumHead><span>Running in</span>{columns.scanner && <OptionalHead column={optionalColumn('image', 'scanner')} />}<NumHead>Scanned</NumHead>
        </div>
        {images.map((img) => {
          const exposure = exposureLookup(img.exposed)
          const total = img.counts.critical + img.counts.high + img.counts.medium + img.counts.low + img.counts.unknown
          const fixable = img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown
          return (
            <Disclosure
              key={img.id}
              flush
              summary={
                <div className="grid flex-1 items-center gap-4 text-sm" style={grid}>
                  <span className="flex min-w-0 flex-col">
                    <ImageName image={img.image} className="text-theme-text-primary" />
                    {/* With the OS column on, the OS and its badge move there. */}
                    <span className="flex min-w-0 items-center gap-1.5">
                      <span className="truncate font-mono text-xs text-theme-text-tertiary">{[imageParts(img.image).path, !columns.os && img.os].filter(Boolean).join(' · ')}</span>
                      {!columns.os && img.osEndOfLife && <EndOfLifeBadge />}
                    </span>
                  </span>
                  {columns.os && (
                    <span className="flex min-w-0 flex-col items-start gap-0.5">
                      <span className="truncate font-mono text-xs text-theme-text-secondary">{img.os || '—'}</span>
                      {img.osEndOfLife && <EndOfLifeBadge />}
                    </span>
                  )}
                  {img.findingsUnread ? <span className="text-xs text-warning-text">Findings not read</span> : <SeverityBar counts={img.counts} className="max-w-[280px]" />}
                  <WithTooltip tip={img.findingsUnread ? undefined : `${fixable} of ${total} findings have a fixed release`}>
                    <Num>{img.findingsUnread ? '—' : <>{fixable}<span className="text-theme-text-tertiary"> of {total}</span></>}</Num>
                  </WithTooltip>
                  <span className="flex min-w-0 flex-col">
                    <WorkloadSummary workloads={img.workloads} exposure={exposure} suffix={img.workloadsTotal > img.workloads.length ? ` and ${img.workloadsTotal - img.workloads.length} more` : ''} />
                    <span className="flex flex-wrap items-center gap-x-2 text-xs text-theme-text-tertiary">
                      {namespaceCount(img)}
                      <ExposedCount exposed={img.exposed} total={img.exposedTotal} unknown={img.exposureUnknown} />
                    </span>
                  </span>
                  {columns.scanner && <span className="truncate text-xs text-theme-text-secondary">{img.scanner ? [img.scanner.name, img.scanner.version].filter(Boolean).join(' ') : '—'}</span>}
                  <span className="text-right text-xs text-theme-text-tertiary">{formatRelativeAgeTime(img.scannedAt)}</span>
                </div>
              }
            >
              <div className="grid gap-4 py-1.5 pl-10 pr-3" style={grid}>
                <div className="flex min-w-0 flex-col gap-1.5" style={{ gridColumn: `span ${columns.os ? 4 : 3}` }}>
                  <span className="text-xs font-medium text-theme-text-secondary">
                    {img.matching === img.findingsTotal ? 'Findings, worst first' : `${img.matching} of ${img.findingsTotal} findings match`}
                  </span>
                  <FindingsList findings={img.findings} total={img.findingsTotal} unread={img.findingsUnread} packageUrl={columns.packageUrl} />
                  <span className="flex gap-3 text-xs">
                    {img.matching > img.findings.length && <span className="text-theme-text-tertiary">{img.matching - img.findings.length} more in the report.</span>}
                    <button type="button" onClick={() => onOpenReport(img)} className="text-accent-text hover:underline">Open the scan report</button>
                  </span>
                </div>
                <div className="flex min-w-0 flex-col gap-1.5" style={{ gridColumn: `span ${columns.scanner ? 3 : 2}` }}>
                  <span className="text-xs font-medium text-theme-text-secondary">{img.workloadsTotal} {img.workloadsTotal === 1 ? 'container' : 'containers'}{img.workloadsTotal > img.workloads.length ? ` (first ${img.workloads.length})` : ''}</span>
                  <WorkloadLinks workloads={img.workloads} onOpen={onOpen} exposure={exposure} />
                </div>
              </div>
            </Disclosure>
          )
        })}
      </div>
    </div>
  )
}

// The listed workloads are capped, so past the cap their namespaces are a floor.
function namespaceCount(img: VulnerableImage): string {
  const n = distinctNamespaces(img.workloads)
  const partial = img.workloadsTotal > img.workloads.length
  return `${partial ? 'at least ' : ''}${n} ${n === 1 ? 'namespace' : 'namespaces'}`
}

function FindingsList({ findings, total, unread, packageUrl }: { findings: VulnerableImage['findings']; total: number; unread?: boolean; packageUrl?: boolean }) {
  if (unread) {
    return <span className="text-xs text-warning-text">Not read: this request reached its findings limit before this image. Narrow with the namespace picker.</span>
  }
  if (findings.length === 0) {
    return <span className="text-xs text-theme-text-tertiary">{total === 0 ? 'No CVEs reported for this image.' : 'No findings match the filters.'}</span>
  }
  return (
    <div className="flex flex-col divide-y divide-theme-border border-y border-theme-border">
      {packageUrl && (
        <div className={clsx('grid items-center gap-3 py-1 text-[11px] font-semibold uppercase tracking-wider text-theme-text-tertiary', FINDING_COLS_PURL)}>
          <span>CVE</span><span>Severity</span><span>Package</span><span>Fix</span><OptionalHead column={optionalColumn('image', 'packageUrl')} />
        </div>
      )}
      {findings.map((f, i) => (
        <div key={`${f.id}/${f.package}/${f.installedVersion}/${i}`} className={clsx('grid items-center gap-3 py-1 text-xs', packageUrl ? FINDING_COLS_PURL : FINDING_COLS)}>
          {f.link ? (
            <WithTooltip tip={f.title}><a href={f.link} target="_blank" rel="noreferrer" className="truncate font-mono text-accent-text hover:underline">{f.id}</a></WithTooltip>
          ) : (
            <WithTooltip tip={f.title}><span className="truncate font-mono text-theme-text-primary">{f.id}</span></WithTooltip>
          )}
          <SeverityBadge severity={f.severity} fromScore={f.severityFromScore} score={f.score} />
          <span className="truncate font-mono text-theme-text-secondary">{f.package}</span>
          <FixVersion installed={f.installedVersion} fixed={f.fixedVersion} target={f.fixTarget} />
          {packageUrl && (
            <WithTooltip tip={f.packageUrl}>
              <span className="truncate font-mono text-theme-text-tertiary">{f.packageUrl || '—'}</span>
            </WithTooltip>
          )}
        </div>
      ))}
    </div>
  )
}

const FINDING_COLS = 'grid-cols-[136px_72px_minmax(0,0.8fr)_minmax(0,1.4fr)]'
const FINDING_COLS_PURL = 'grid-cols-[136px_72px_minmax(0,0.8fr)_minmax(0,1.4fr)_minmax(0,1fr)]'

function FixTable({ fixes, onOpen, onShowCVE }: { fixes: VulnerabilityFix[]; onOpen: (w: VulnerabilityWorkloadRef) => void; onShowCVE: (id: string) => void }) {
  return (
    <div className="flex shrink-0 flex-col gap-2">
      <div className="overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
        <div className="min-w-[960px]">
          <div className={clsx(HEAD, FIX_COLS)}>
            <span>Package</span><WithTooltip tip="Upgrading a package means rebuilding the image that holds it."><span className="self-start underline decoration-dotted underline-offset-2">Upgrade</span></WithTooltip><span>Clears</span><span>Running in</span><NumHead>Images</NumHead><NumHead>Workloads</NumHead>
          </div>
          {fixes.map((f) => {
            const runners = f.affected.flatMap((a) => a.workloads)
            const exposure = exposureLookup(f.exposed)
            const several = f.from.length > 1
            return (
              <Disclosure
                key={`${f.packageType ?? ''}/${f.package}/${f.to ?? (f.fixedVersions ?? []).join('|')}`}
                flush
                summary={
                  <div className={clsx('grid flex-1 items-center gap-4 text-sm', FIX_COLS)}>
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate font-mono text-theme-text-primary">{f.package}</span>
                      {f.packageType && <span className="truncate text-xs text-theme-text-tertiary">{ecosystemLabel(f.packageType)}</span>}
                    </span>
                    <WithTooltip tip={f.to ? `${f.from.join(', ')} → ${f.to}` : "Radar can't pick one release that fixes every CVE here: it can't order this package's versions, or no listed release is above the installed one. These are the fixed releases the scanner lists."}>
                      {/* The installed side gives way first: the target is what to act on. */}
                      <span className="flex min-w-0 items-baseline font-mono text-xs">
                        <span className="min-w-0 shrink-[100] truncate text-theme-text-tertiary">{installedLabel(f.from)}</span>
                        <span className="shrink-0 whitespace-pre text-theme-text-tertiary">{f.to ? ' → ' : ' · '}</span>
                        {f.to
                          ? <span className={clsx('min-w-0 truncate', SEVERITY_TEXT.success)}>{f.to}</span>
                          : <span className="min-w-0 truncate text-theme-text-secondary">fixed in {(f.fixedVersions ?? []).join(', ')}</span>}
                      </span>
                    </WithTooltip>
                    <SeverityCounts counts={f.counts} />
                    <WorkloadSummary workloads={runners} exposure={exposure} />
                    <Num>{f.images}</Num>
                    <span className="flex flex-col items-end">
                      <Num strong>{f.workloads}</Num>
                      <ExposedCount exposed={f.exposed} total={f.exposedTotal} workloads={f.workloads} unknown={f.exposureUnknown} />
                    </span>
                  </div>
                }
              >
                <div className={clsx(SUB, FIX_COLS)}>
                  <span className="text-theme-text-tertiary">{f.cvesTotal} {f.cvesTotal === 1 ? 'CVE' : 'CVEs'}{f.to ? ' cleared' : ''}</span>
                  <span className="col-span-5 flex flex-wrap gap-1.5">
                    {f.cves.map((id) => (
                      <button key={id} type="button" onClick={() => onShowCVE(id)} className="badge-sm bg-theme-elevated font-mono text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary">{id}</button>
                    ))}
                    {f.cvesTotal > f.cves.length && <span className="text-theme-text-tertiary">and {f.cvesTotal - f.cves.length} more</span>}
                  </span>
                </div>
                {f.affected.map((a) => (
                  <div key={a.imageId} className={clsx(SUB, FIX_COLS)}>
                    <ImageName image={a.image} className="text-theme-text-primary" />
                    <span className="truncate font-mono text-theme-text-tertiary">{several ? a.installedVersions.join(', ') : ''}</span>
                    <span />
                    <WorkloadLinks workloads={a.workloads} onOpen={onOpen} inline exposure={exposure} />
                    <span />
                    <Num>{a.workloadsTotal}</Num>
                  </div>
                ))}
                {f.images > f.affected.length && <p className={clsx(SUB, 'text-theme-text-tertiary')}>{f.images - f.affected.length} more images.</p>}
              </Disclosure>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function CVETable({ cves, columns, onOpen }: { cves: VulnerabilityCVE[]; columns: Record<string, boolean>; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  const grid = cveGrid(columns)
  const extra = (columns.published ? 1 : 0) + (columns.age ? 1 : 0)
  const now = Date.now()
  return (
    <div className="flex shrink-0 flex-col gap-2">
      <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
        <div style={{ minWidth: 960 + (columns.published ? 108 : 0) + (columns.age ? 92 : 0) }}>
          <div className={HEAD} style={grid}>
            <span>CVE</span><span>Severity</span><NumHead>Score</NumHead>
            {columns.published && <OptionalHead column={optionalColumn('cve', 'published')} />}
            {columns.age && <OptionalHead column={optionalColumn('cve', 'age')} align="right" />}
            <span>Package</span><span>Fix</span><NumHead>Images</NumHead><NumHead>Workloads</NumHead>
          </div>
          {cves.map((c) => {
            const exposure = exposureLookup(c.exposed)
            return (
            <Disclosure
              key={c.id}
              flush
              summary={
                <div className="grid flex-1 items-center gap-4 text-sm" style={grid}>
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-mono text-theme-text-primary">{c.id}</span>
                    {c.title && <span className="truncate text-xs text-theme-text-tertiary">{c.title}</span>}
                  </span>
                  <SeverityBadge severity={c.severity} />
                  <Num>{c.score?.toFixed(1) ?? '—'}</Num>
                  {columns.published && <PublishedDate at={c.publishedAt} now={now} />}
                  {columns.age && <Num>{c.publishedAt ? `${(daysSince(c.publishedAt, now) ?? 0).toLocaleString('en-US')} d` : '—'}</Num>}
                  <span className="truncate font-mono text-xs text-theme-text-secondary">{c.packages.join(', ')}</span>
                  <CVEFix cve={c} />
                  <Num>{c.images}</Num>
                  <span className="flex flex-col items-end">
                    <Num strong>{c.workloads}</Num>
                    <ExposedCount exposed={c.exposed} total={c.exposedTotal} workloads={c.workloads} unknown={c.exposureUnknown} />
                  </span>
                </div>
              }
            >
              {c.affected.map((a) => (
                <div key={a.imageId} className={SUB} style={grid}>
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <ImageName image={a.image} className="text-theme-text-primary" />
                    <WorkloadLinks workloads={a.workloads} onOpen={onOpen} inline exposure={exposure} />
                  </span>
                  <span style={{ gridColumn: `span ${2 + extra}` }} />
                  <span className="col-span-2 flex min-w-0 flex-col">
                    {a.packages.map((p) => (
                      <span key={`${p.package}/${p.installedVersion}`} className="flex min-w-0 gap-1.5">
                        <span className="shrink-0 font-mono text-theme-text-secondary">{p.package}</span>
                        <FixVersion installed={p.installedVersion} fixed={p.fixedVersion} target={p.fixTarget} />
                      </span>
                    ))}
                  </span>
                  <span />
                  <Num>{a.workloadsTotal}</Num>
                </div>
              ))}
              <div className={clsx(SUB, 'flex gap-4')}>
                {c.images > c.affected.length && <span className="text-theme-text-tertiary">{c.images - c.affected.length} more images.</span>}
                {c.link && <a href={c.link} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent-text hover:underline">Advisory <ExternalLink className="h-3 w-3" aria-hidden /></a>}
              </div>
            </Disclosure>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function WorkloadTable({ workloads, columns, exposure, severities, query, namespaces, onOpen, onShow }: {
  workloads: VulnerabilityWorkload[]
  /** Its optional columns that are on. */
  columns: Record<string, boolean>
  /** Absent from an older Radar: the route column is left out, not shown empty. */
  exposure: VulnerabilitiesResponse['exposure']
  severities: VulnerabilitySeverity[]
  query: string
  /** The table's namespace scope, so an expanded row reads what the row did. */
  namespaces: string[]
  onOpen: (w: VulnerabilityWorkloadRef) => void
  onShow: (w: VulnerabilityWorkloadRef, by: VulnerabilityView) => void
}) {
  const grid = workloadGrid(!!exposure, columns)
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div style={{ minWidth: (exposure ? 920 : 760) + (columns.owner ? 176 : 0) }}>
        <div className={HEAD} style={grid}>
          <span>Workload</span>{columns.owner && <OptionalHead column={optionalColumn('workload', 'owner')} />}<span>Findings across its images</span>{exposure && <span>{EXPOSURE.column}</span>}<span>Containers</span>
        </div>
        {workloads.map((w) => (
          <Disclosure
            key={`${w.namespace}/${w.kind}/${w.name}`}
            flush
            summary={
              <div className="grid flex-1 items-start gap-4 text-sm" style={grid}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-theme-text-primary">{w.name}</span>
                  <span className="text-xs text-theme-text-tertiary">{w.kind} · {w.namespace}</span>
                </span>
                {columns.owner && <OwnerCell workload={w} />}
                <WorkloadFindings workload={w} />
                {exposure && <WorkloadRoute workload={w} gaps={exposure.gaps} />}
                <ul className="flex min-w-0 flex-col gap-1">
                  {w.containers.map((c) => (
                    <li key={`${c.name}/${c.image}`} className="flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs">
                      <span className="shrink-0 text-theme-text-secondary">{c.name}</span>
                      <ImageName image={c.image} className="text-theme-text-tertiary" />
                      {c.scanned && c.counts ? <SeverityCounts counts={c.counts} /> : c.findingsUnread ? <span className="text-warning-text">findings not read</span> : <span className="text-warning-text">not scanned yet</span>}
                    </li>
                  ))}
                </ul>
              </div>
            }
          >
            {(open) => <WorkloadUpgrades workload={w} grid={grid} ownerColumn={!!columns.owner} routeColumn={!!exposure} active={open} severities={severities} query={query} namespaces={namespaces} onOpen={onOpen} onShow={onShow} />}
          </Disclosure>
        ))}
      </div>
    </div>
  )
}

// The workload's routes from outside the cluster; empty when none was seen,
// Unknown where Radar couldn't read a route kind in its namespace.
function WorkloadRoute({ workload: w, gaps }: { workload: VulnerabilityWorkload; gaps?: VulnerabilityGap[] }) {
  const routes = w.routes ?? []
  const total = w.routesTotal ?? routes.length
  if (routes.length > 0) {
    return (
      <WithTooltip tip={routesTip(routes, total)}>
        <span className="flex min-w-0 items-center gap-1 truncate text-xs text-theme-text-secondary">
          <ExposedMark tip={routesTip(routes, total)} />
          {routeText(routes[0])}{total > 1 && <span className="text-theme-text-tertiary"> and {total - 1} more</span>}
        </span>
      </WithTooltip>
    )
  }
  if (w.exposureUnknown || exposureUnknownIn(gaps, w.namespace)) {
    return (
      <WithTooltip tip={EXPOSURE.unknownTip}>
        <span className="text-xs text-theme-text-tertiary">{EXPOSURE.unknown}</span>
      </WithTooltip>
    )
  }
  return <span />
}

// The CVE's publication date; its age, which is not how long it has been
// here, is in the tooltip.
function PublishedDate({ at, now }: { at?: string; now: number }) {
  const days = at ? daysSince(at, now) : undefined
  return (
    <WithTooltip tip={days === undefined ? undefined : `Published ${days.toLocaleString()} ${days === 1 ? 'day' : 'days'} ago. That is the CVE's age, not how long it has been in this cluster.`}>
      <span className="font-mono text-xs tabular-nums text-theme-text-secondary">{at ? isoDate(at) : '—'}</span>
    </WithTooltip>
  )
}

// Whether an upgrade clears the CVE: in every image, some, or none.
function CVEFix({ cve: c }: { cve: VulnerabilityCVE }) {
  const all = c.fixableImages >= c.images
  const text = c.fixableImages === 0 ? 'No' : all ? 'Yes' : `${c.fixableImages} of ${c.images}`
  return (
    <WithTooltip tip={c.fixableImages > 0 && !all ? `A fixed release is reported in ${c.fixableImages} of the ${c.images} images` : undefined}>
      <span className={clsx('text-xs', c.fixableImages === 0 ? 'text-theme-text-tertiary' : all ? SEVERITY_TEXT.success : 'text-theme-text-secondary')}>
        {text}
      </span>
    </WithTooltip>
  )
}

const MAX_WORKLOAD_UPGRADES = 8

// What a workload's owner changes: the By fix rows narrowed to this
// workload, read when the row opens.
function WorkloadUpgrades({ workload: w, grid, ownerColumn, routeColumn, active, severities, query, namespaces, onOpen, onShow }: {
  workload: VulnerabilityWorkload
  /** The table's columns; sub-rows keep a cell for the owner column too. */
  grid: React.CSSProperties
  ownerColumn: boolean
  /** The table shows the route column; sub-rows keep a cell for it so detail lines up. */
  routeColumn: boolean
  /** False once the row closes: its subtree stays mounted, its polling stops. */
  active: boolean
  severities: VulnerabilitySeverity[]
  query: string
  /** The table's namespace scope, so an expanded row reads what the row did. */
  namespaces: string[]
  onOpen: (w: VulnerabilityWorkloadRef) => void
  onShow: (w: VulnerabilityWorkloadRef, by: VulnerabilityView) => void
}) {
  const ref = { kind: w.kind, group: w.group, namespace: w.namespace, name: w.name }
  const ownerLine = ownerTip(useOwners()?.find(ref))
  const routeCell = routeColumn && <span />
  const ownerCell = ownerColumn && <span />
  // The row's own scope: the newest report for an image wins across every
  // namespace read, so a narrower read could pick an older one.
  const { data, isLoading, error } = useVulnerabilities({ view: 'fix', namespaces, severities, query, workload: ref }, active)
  const total = totalFindings(w.counts)
  const fixable = totalFindings(w.fixable)
  const fixes = data?.fixes ?? []
  const several = w.containers.length > 1
  const filtered = severities.length > 0 || query !== ''
  // Containers whose findings weren't read: their upgrades are unknown.
  const unknown = w.containers.filter((c) => !c.scanned || c.findingsUnread).length
  const actions = (
    <span className="flex flex-wrap gap-x-4">
      <button type="button" onClick={() => onOpen(ref)} className="text-accent-text hover:underline">Open {w.name}</button>
      {total > 0 && <button type="button" onClick={() => onShow(ref, 'cve')} className="text-accent-text hover:underline">{filtered ? 'Its CVEs' : 'All its CVEs'}</button>}
      {fixes.length > MAX_WORKLOAD_UPGRADES && <button type="button" onClick={() => onShow(ref, 'fix')} className="text-accent-text hover:underline">All {fixes.length} upgrades</button>}
    </span>
  )
  let lead: React.ReactNode
  if (isLoading) lead = <span className="text-theme-text-tertiary">Reading its upgrades…</span>
  else if (error) lead = <span className="text-warning-text">Couldn't read its upgrades: {error instanceof Error ? error.message : 'request failed'}.</span>
  else if (total === 0 && unknown > 0) lead = <span className="text-warning-text">{unknown === w.containers.length ? "No findings were read for its containers: not scanned yet, or too many to read." : 'Its other containers have no reported CVEs; the rest weren\'t read.'}</span>
  // The workload's counts cover its whole images; under a filter only the
  // upgrade rows follow it, so the sentence leaves the counts out.
  else if (filtered) lead = <span className="text-theme-text-secondary">{fixes.length === 0 ? 'None of the findings that match has a fixed release yet.' : `${fixes.length} ${fixes.length === 1 ? 'upgrade' : 'upgrades'} for the findings that match`}</span>
  else if (fixes.length === 0) lead = <span className="text-theme-text-tertiary">None of its {total} findings has a fixed release yet.</span>
  else lead = (
    <span className="text-theme-text-secondary">
      {/* Without a confirmed version for every upgrade, they don't promise to clear. */}
      {fixes.length} {fixes.every((f) => f.to) ? (fixes.length === 1 ? 'upgrade clears' : 'upgrades clear') : (fixes.length === 1 ? 'upgrade listed for' : 'upgrades listed for')} {fixable} of its {total} findings
      {total > fixable && <span className="text-theme-text-tertiary">; {total - fixable} have no fix yet</span>}
      {unknown > 0 && <span className="text-warning-text">. {unknown} {unknown === 1 ? "container's" : "containers'"} findings weren't read</span>}
    </span>
  )
  return (
    <>
      <div className={SUB} style={grid}>
        <span className="col-span-full">{lead}</span>
      </div>
      {fixes.length > 0 && (
        <div className={clsx(SUB, 'text-theme-text-tertiary')} style={grid}>
          <span />
          {ownerCell}
          <span className="grid min-w-0 grid-cols-[1.75rem_2.25rem_minmax(0,1fr)] gap-2">
            <span className="text-right">C</span><span className="text-right">H</span><span>Upgrade</span>
          </span>
          {routeCell}
          <span />
        </div>
      )}
      {fixes.slice(0, MAX_WORKLOAD_UPGRADES).map((f) => (
        <div key={`${f.packageType ?? ''}/${f.package}/${f.to ?? (f.fixedVersions ?? []).join('|')}`} className={SUB} style={grid}>
          <span className="flex min-w-0 items-baseline gap-2">
            <span className="truncate font-mono text-theme-text-primary">{f.package}</span>
            {f.packageType && <span className="truncate text-theme-text-tertiary">{ecosystemLabel(f.packageType)}</span>}
          </span>
          {ownerCell}
          {/* Critical and High cleared, in fixed columns so rows line up. */}
          <span className="grid min-w-0 grid-cols-[1.75rem_2.25rem_minmax(0,1fr)] items-baseline gap-2">
            <ClearedCount count={f.counts.critical} severity="critical" />
            <ClearedCount count={f.counts.high} severity="high" />
            {f.to
              ? <WithTooltip tip={`${f.from.join(', ')} → ${f.to}`}><FixVersion installed={installedLabel(f.from)} target={f.to} /></WithTooltip>
              : <span className="truncate font-mono text-theme-text-secondary">fixed in {(f.fixedVersions ?? []).join(', ')}</span>}
          </span>
          {routeCell}
          <span className="flex min-w-0 flex-wrap gap-x-3">
            {several && f.affected.map((a) => <ImageName key={a.imageId} image={a.image} className="text-theme-text-tertiary" />)}
          </span>
        </div>
      ))}
      {ownerLine && <div className={clsx(SUB, 'flex text-theme-text-secondary')}>{ownerLine}</div>}
      <div className={clsx(SUB, 'flex')}>{actions}</div>
    </>
  )
}

// A workload's counts cover only its scanned containers: with none scanned
// they are unknown, never "no reported CVEs".
function WorkloadFindings({ workload }: { workload: VulnerabilityWorkload }) {
  const unscanned = workload.containers.filter((c) => !c.scanned).length
  const unread = workload.containers.filter((c) => c.scanned && c.findingsUnread).length
  if (unscanned + unread === workload.containers.length) {
    return <span className="text-xs text-warning-text">{unread === 0 ? 'Not scanned yet' : 'Findings not read'}</span>
  }
  const containers = (n: number) => `${n} ${n === 1 ? 'container' : 'containers'}`
  const notCounted = [
    unscanned > 0 && `${containers(unscanned)} not scanned yet`,
    unread > 0 && `${containers(unread)} with findings not read`,
  ].filter(Boolean).join(', ')
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <SeverityBar counts={workload.counts} className="max-w-[280px]" />
      {notCounted && <span className="text-xs text-warning-text">Not counted: {notCounted}</span>}
    </div>
  )
}

// Scanned and clean, folded away so the rows are the ones that need work.
function CleanList({ data, view, onOpen }: { data: VulnerabilitiesResponse; view: VulnerabilityView; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  const images = view === 'image' ? data.clean ?? [] : []
  const workloads = view === 'workload' ? data.cleanWorkloads ?? [] : []
  if (images.length + workloads.length === 0) return null
  const count = images.length + workloads.length + (data.coverage.cleanOmitted ?? 0)
  return (
    <div className="shrink-0 rounded-lg border border-theme-border bg-theme-surface">
      <Disclosure
        flush
        summary={
          <span className="flex items-center gap-2 text-sm text-theme-text-secondary">
            <ShieldCheck className={clsx('h-4 w-4', SEVERITY_TEXT.success)} aria-hidden />
            {view === 'image'
              ? `${count} scanned ${count === 1 ? 'image has' : 'images have'} no reported CVEs`
              : `${count} ${count === 1 ? 'workload runs' : 'workloads run'} only images with no reported CVEs`}
          </span>
        }
      >
        {view === 'image' ? (
          <ul className="flex flex-col">
            {images.map((c) => (
              <li key={c.id} className={FOLD_ROW}>
                <span className="flex min-w-0 items-baseline gap-2">
                  <ImageName image={c.image} className="text-theme-text-primary" />
                  {c.osEndOfLife && <EndOfLifeBadge />}
                  <span className="shrink-0 text-theme-text-tertiary">scanned {formatRelativeAgeTime(c.scannedAt)}</span>
                </span>
                <span className="flex min-w-0 flex-wrap items-baseline gap-x-4">
                  <WorkloadLinks workloads={c.workloads} onOpen={onOpen} inline />
                  {c.workloadsTotal > c.workloads.length && <span className="text-theme-text-tertiary">and {c.workloadsTotal - c.workloads.length} more</span>}
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <div className="py-1.5 pl-10 pr-3"><WorkloadLinks workloads={workloads} onOpen={onOpen} inline /></div>
        )}
      </Disclosure>
    </div>
  )
}

// The rows follow a search or workload filter, so they're counted here, not
// by the summary's unfiltered total.
function UnscannedList({ data, onOpen }: { data: VulnerabilitiesResponse; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  const unscanned = data.unscanned ?? []
  if (unscanned.length === 0) return null
  const count = unscanned.length + (data.coverage.unscannedOmitted ?? 0)
  return (
    <div className="shrink-0 rounded-lg border border-theme-border bg-theme-surface">
      <Disclosure
        flush
        summary={
          <span className="flex items-center gap-2 text-sm text-theme-text-secondary">
            <CircleDashed className="h-4 w-4 text-theme-text-tertiary" aria-hidden />
            {count} running {count === 1 ? 'image has' : 'images have'} no report yet: unknown, not clean
          </span>
        }
      >
        <ul className="flex flex-col">
          {unscanned.map((u) => (
            <li key={u.image} className={FOLD_ROW}>
              <span className="flex min-w-0 flex-col">
                <ImageName image={u.image} className="text-theme-text-primary" />
                {u.otherBuildScanned && (
                  <WithTooltip tip={OTHER_BUILD_TIP}>
                    <span className="self-start text-xs text-theme-text-tertiary underline decoration-dotted underline-offset-2">Trivy scanned another build of this tag</span>
                  </WithTooltip>
                )}
              </span>
              <span className="flex min-w-0 flex-wrap items-baseline gap-x-4">
                <WorkloadLinks workloads={u.workloads} onOpen={onOpen} inline exposure={exposureLookup(u.exposed)} />
                {u.workloadsTotal > u.workloads.length && <span className="text-theme-text-tertiary">and {u.workloadsTotal - u.workloads.length} more</span>}
              </span>
            </li>
          ))}
        </ul>
      </Disclosure>
    </div>
  )
}
