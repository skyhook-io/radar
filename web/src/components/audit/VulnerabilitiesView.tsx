import { useState } from 'react'
import { WithTooltip } from '../ui/Tooltip'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { AlertTriangle, CircleDashed, ExternalLink, Search, ShieldAlert, ShieldCheck, X } from 'lucide-react'
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
} from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { ApiError } from '../../api/client'
import {
  useVulnerabilities,
  isKindUnknown,
  parseWorkloadParam,
  workloadParam,
  type VulnerabilitiesResponse,
  type VulnerabilityCVE,
  type VulnerabilityFix,
  type VulnerabilitySeverity,
  type VulnerabilityView,
  type VulnerabilityWorkload,
  type VulnerabilityWorkloadRef,
  type VulnerableImage,
} from '../../api/vulnerabilities'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { ChecksViewTabs } from './ChecksViewTabs'
import {
  SEVERITIES,
  SeverityBadge,
  SeverityBar,
  SeverityCounts,
  distinctNamespaces,
  totalFindings,
  FindingTotal,
  ImageName,
  FixVersion,
  ecosystemLabel,
  findingsCertainty,
  readsComplete,
  workloadsByReplicaSet,
  OTHER_BUILD_TIP,
  imageParts,
  gapSentence,
  workloadSummary,
} from '../vulnerabilities/parts'

interface VulnerabilitiesViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

const VIEWS: { key: VulnerabilityView; label: string }[] = [
  { key: 'image', label: 'By image' },
  { key: 'fix', label: 'By fix' },
  { key: 'cve', label: 'By CVE' },
  { key: 'workload', label: 'By workload' },
]

const CHIP = 'min-h-8 rounded-full border px-3 text-xs font-medium transition-colors'
const CHIP_ON = 'selection-strong border-accent text-theme-text-primary'
const CHIP_OFF = 'border-theme-border text-theme-text-secondary hover:bg-theme-hover'

// Each table's columns, shared by its header, rows and expanded sub-rows so
// detail lines up under the column it belongs to. Short values get fixed
// tracks; the text columns take the rest.
const IMAGE_COLS = 'grid-cols-[minmax(220px,1.6fr)_minmax(180px,1.2fr)_88px_minmax(0,2fr)_72px]'
const FIX_COLS = 'grid-cols-[minmax(200px,1.2fr)_minmax(220px,1.5fr)_120px_minmax(0,1.6fr)_64px_80px]'
const CVE_COLS = 'grid-cols-[minmax(260px,1.6fr)_80px_48px_minmax(200px,1.6fr)_56px_64px_80px]'
const WORKLOAD_COLS = 'grid-cols-[minmax(200px,1.4fr)_minmax(180px,1.2fr)_minmax(0,2.4fr)]'
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
  const view = (VIEWS.find((v) => v.key === params.get('by'))?.key ?? 'image') as VulnerabilityView
  const severities = (params.get('severity') ?? '').split(',').filter((s): s is VulnerabilitySeverity => SEVERITIES.some((x) => x.key === s))
  const fixableOnly = params.get('fixable') === '1'
  const queryDraft = params.get('q') ?? ''
  const workload = parseWorkloadParam(params.get('workload'))
  const query = useDebouncedValue(queryDraft.trim(), 300)
  const { connection } = useConnection()

  // Typing and chip toggles replace the entry; a new view or a drill-down
  // pushes one, so Back returns to the list it came from.
  const update = (mutate: (p: URLSearchParams) => void, push = false) => {
    const next = new URLSearchParams(params)
    mutate(next)
    setParams(next, { replace: !push })
  }
  const { data, isLoading, isFetching, isPlaceholderData, error, dataUpdatedAt, refetch } = useVulnerabilities({ view, namespaces, severities, fixableOnly, query, workload })

  const toggleSeverity = (s: VulnerabilitySeverity) => update((p) => {
    const next = severities.includes(s) ? severities.filter((x) => x !== s) : [...severities, s]
    if (next.length > 0) p.set('severity', next.join(','))
    else p.delete('severity')
  })

  if (isLoading) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4">
        <ChecksViewTabs />
        <PaneLoader label="Reading vulnerability reports…" className="flex-1" />
      </div>
    )
  }
  if (!data) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4">
        <ChecksViewTabs />
        <div className="flex flex-1 items-center justify-center"><VulnerabilitiesError error={error} /></div>
      </div>
    )
  }

  // By fix lists only fixable findings, and hides the chip that says so.
  const filtered = severities.length > 0 || (fixableOnly && view !== 'fix') || query !== '' || !!workload
  // Narrowed only to a workload whose images are all clean or unscanned:
  // the folds below list them, so "nothing matches" would mislead.
  const workloadFolds = !!workload && (view === 'image' || view === 'workload') && severities.length === 0 && !fixableOnly && query === '' &&
    ((data.unscanned ?? []).length > 0 || (data.clean ?? []).length > 0 || (data.cleanWorkloads ?? []).length > 0)
  const rows = view === 'cve' ? data.cves ?? [] : view === 'workload' ? data.workloads ?? [] : view === 'fix' ? data.fixes ?? [] : data.images ?? []

  return (
    <div aria-busy={isFetching} className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <ChecksViewTabs />
      <PageHeader
        icon={ShieldAlert}
        title="Vulnerabilities"
        description="Known CVEs in the images that run now, from Trivy Operator's reports."
        actions={<FreshnessControl mode="auto" dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} onRefresh={() => refetch()} connectionState={connection.state} />}
      />

      <div className={clsx('flex flex-col gap-4 transition-opacity', isPlaceholderData && 'opacity-60')}>
        <SummaryTiles data={data} />
        <CoverageNotes data={data} filtered={filtered} />
        {error && <p className="text-xs text-warning-text">Couldn't refresh: {error instanceof Error ? error.message : 'request failed'}. Showing the last answer.</p>}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex min-h-9 max-w-sm flex-1 basis-64 items-center gap-2 rounded-md border border-theme-border bg-theme-elevated px-2.5">
          <Search className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
          <span className="sr-only">Search</span>
          <input
            type="search"
            value={queryDraft}
            onChange={(e) => update((p) => (e.target.value ? p.set('q', e.target.value) : p.delete('q')))}
            placeholder="Image, CVE, package or workload"
            className="w-full bg-transparent text-sm text-theme-text-primary outline-none placeholder:text-theme-text-tertiary"
          />
        </label>
        {workload && (
          <span className={clsx(CHIP, CHIP_ON, 'inline-flex items-center gap-1.5 pr-1.5')}>
            {workload.name}
            <span className="font-normal text-theme-text-tertiary">{workload.kind} · {workload.namespace}</span>
            <button type="button" aria-label="Show every workload" onClick={() => update((p) => p.delete('workload'))} className="rounded-full p-0.5 hover:bg-theme-hover">
              <X className="h-3 w-3" aria-hidden />
            </button>
          </span>
        )}
        <div role="group" aria-label="Severity" className="flex flex-wrap gap-1.5">
          {SEVERITIES.map((s) => {
            const on = severities.includes(s.key)
            return (
              <button
                key={s.key}
                type="button"
                aria-pressed={on}
                onClick={() => toggleSeverity(s.key)}
                className={clsx(CHIP, on ? CHIP_ON : CHIP_OFF)}
              >
                {s.label}
              </button>
            )
          })}
        </div>
        {view !== 'fix' && (
          <>
            <span aria-hidden className="h-5 w-px bg-theme-border" />
            <button
              type="button"
              aria-pressed={fixableOnly}
              onClick={() => update((p) => (fixableOnly ? p.delete('fixable') : p.set('fixable', '1')))}
              className={clsx(CHIP, fixableOnly ? CHIP_ON : CHIP_OFF)}
            >
              Has a fix
            </button>
          </>
        )}
        <div role="group" aria-label="Group by" className="ml-auto flex rounded-md border border-theme-border bg-theme-elevated p-0.5">
          {VIEWS.map((v) => (
            <button
              key={v.key}
              type="button"
              aria-pressed={view === v.key}
              onClick={() => update((p) => (v.key === 'image' ? p.delete('by') : p.set('by', v.key)), true)}
              className={clsx('min-h-8 rounded px-3 text-xs font-medium transition-colors', view === v.key ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary')}
            >
              {v.label}
            </button>
          ))}
        </div>
      </div>

      {rows.length === 0 ? (
        <EmptyState
          tone="neutral"
          icon={ShieldAlert}
          headline={emptyHeadline(view, filtered, data, workloadFolds ? workload : null)}
          body={filtered && !workloadFolds ? 'Clear a filter to see more.' : data.summary.imagesScanned === 0 && (data.unscanned ?? []).length > 0 ? 'No report covers a running image yet. Trivy Operator scans workloads as they appear; if this persists, check that it is running and that its target namespaces include these.' : undefined}
        />
      ) : view === 'fix' ? (
        <FixTable fixes={data.fixes ?? []} onOpen={(w) => onNavigateToResource(toResource(w))} onShowCVE={(id) => update((p) => { p.set('by', 'cve'); p.set('q', id) }, true)} />
      ) : view === 'cve' ? (
        <CVETable cves={data.cves ?? []} onOpen={(w) => onNavigateToResource(toResource(w))} />
      ) : view === 'workload' ? (
        <WorkloadTable
          workloads={data.workloads ?? []}
          severities={severities}
          query={query}
          namespaces={namespaces}
          onOpen={(w) => onNavigateToResource(toResource(w))}
          onShow={(w, by) => update((p) => { p.set('by', by); p.set('workload', workloadParam(w)); p.delete('q') }, true)}
        />
      ) : (
        <ImageTable
          images={data.images ?? []}
          onOpen={(w) => onNavigateToResource(toResource(w))}
          onOpenReport={(img) => onNavigateToResource({ kind: img.report.kind, group: img.report.group, namespace: img.report.namespace ?? '', name: img.report.name })}
        />
      )}

      {(data.coverage.rowsOmitted ?? 0) > 0 && (
        <p className="text-xs text-theme-text-tertiary">{data.coverage.rowsOmitted} more rows not shown. Narrow with a filter or the namespace picker.</p>
      )}

      {!severities.length && !fixableOnly && (view === 'image' || view === 'workload') && (
        <CleanList data={data} view={view} onOpen={(w) => onNavigateToResource(toResource(w))} />
      )}
      <UnscannedList data={data} onOpen={(w) => onNavigateToResource(toResource(w))} />
    </div>
  )
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
  if (s.imagesScanned === 0) return 'No scanned images run in this scope'
  if (view === 'fix' && totalFindings(s.counts) > 0) return `None of the ${totalFindings(s.counts)} reported findings has a fixed release yet`
  return `No CVEs reported for the ${s.imagesScanned} scanned ${s.imagesScanned === 1 ? 'image' : 'images'} that run`
}

function VulnerabilitiesError({ error }: { error: unknown }) {
  const upgrade = getRadarUpgradeRequirement(error)
  if (upgrade) {
    return <EmptyState tone="neutral" icon={ShieldAlert} headline={radarUpgradeHeadline('Vulnerability views')} body={radarUpgradeDetail(upgrade)} action={<RadarUpgradeAction requirement={upgrade} />} />
  }
  // A cluster without the report kind: nothing failed, there is no scanner.
  if (isKindUnknown(error)) {
    return (
      <EmptyState
        tone="neutral"
        icon={ShieldAlert}
        headline="No vulnerability reports in this cluster"
        body="Radar doesn't scan images itself. It reads the VulnerabilityReports Trivy Operator writes, and this cluster doesn't serve that kind."
        action={<a href="https://aquasecurity.github.io/trivy-operator/latest/getting-started/installation/" target="_blank" rel="noreferrer" className="btn-brand px-3 py-1.5 text-xs font-medium">How to install Trivy Operator</a>}
      />
    )
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

function SummaryTiles({ data }: { data: VulnerabilitiesResponse }) {
  const s = data.summary
  const certainty = findingsCertainty(data)
  const nothing = certainty === 'unknown'
  const tiles = [
    { label: 'Running images scanned', value: s.imagesScanned, certainty: 'exact' as const, sub: s.imagesUnscanned > 0 ? `${s.imagesUnscanned} not scanned yet` : readsComplete(data.coverage) ? 'every running image has a report' : 'none unscanned where Radar could read', tone: '' },
    { label: 'Images with Critical CVEs', value: s.imagesWithCritical, certainty, sub: nothing ? 'nothing scanned yet' : `of ${s.imagesScanned} scanned`, tone: s.imagesWithCritical > 0 ? VULN_SEVERITY_TEXT.CRITICAL : '' },
    { label: 'Critical and High CVEs with a fix', value: s.fixableCriticalHighCves, certainty, sub: nothing ? 'nothing scanned yet' : 'a fixed release is reported', tone: s.fixableCriticalHighCves > 0 ? VULN_SEVERITY_TEXT.HIGH : '' },
    { label: 'Workloads with Critical or High', value: s.workloadsWithCriticalOrHigh, certainty, sub: workloadsByReplicaSet(data.coverage) ? `of ${s.workloads}; a Deployment may count once per ReplicaSet` : `of ${s.workloads} running or scheduled`, tone: '' },
  ]
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      {tiles.map((t) => (
        <div key={t.label} className="card-inner flex min-w-0 flex-col gap-0.5">
          <span className="text-xs text-theme-text-tertiary">{t.label}</span>
          <FindingTotal value={t.value} certainty={t.certainty} className={clsx('text-2xl font-medium text-theme-text-primary', t.tone)} />
          <span className="text-xs text-theme-text-tertiary">{t.sub}</span>
        </div>
      ))}
    </div>
  )
}

// filtered: the totals ignore the filters, which a filtered list beside them
// can make look like a contradiction.
function CoverageNotes({ data, filtered }: { data: VulnerabilitiesResponse; filtered: boolean }) {
  const gaps = gapSentence(data.coverage.gaps)
  const scan = data.summary.newestScan
    ? data.summary.oldestScan && data.summary.oldestScan !== data.summary.newestScan
      ? `Scans from ${formatRelativeAgeTime(data.summary.oldestScan)} to ${formatRelativeAgeTime(data.summary.newestScan)}`
      : `Scanned ${formatRelativeAgeTime(data.summary.newestScan)}`
    : null
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      {filtered && <p className="text-theme-text-secondary">The totals above cover everything in this scope; the filters narrow only the list below.</p>}
      <p className="text-theme-text-tertiary">
        From Trivy Operator's VulnerabilityReports{scan ? ` · ${scan}` : ''}. A report lists what was known when the image was scanned, after the scanner's own filters.
        {" A CVE counts once per package version in an image, so totals can be lower than the summaries in Trivy's own reports."}
        {data.coverage.inactiveReports > 0 && ` ${data.coverage.inactiveReports} ${data.coverage.inactiveReports === 1 ? 'report is' : 'reports are'} about workloads or builds that don't run now (older revisions or image builds, scaled to zero or removed) and ${data.coverage.inactiveReports === 1 ? "isn't" : "aren't"} counted.`}
      </p>
      {gaps && <p className="text-warning-text">{gaps}</p>}
      {data.coverage.truncated && <p className="text-warning-text">This request reached its findings limit, so some images' findings weren't read and are marked unread. Narrow with the namespace picker.</p>}
    </div>
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

function WorkloadLinks({ workloads, onOpen, inline = false }: { workloads: VulnerabilityWorkloadRef[]; onOpen: (w: VulnerabilityWorkloadRef) => void; inline?: boolean }) {
  return (
    <ul className={clsx('flex min-w-0', inline ? 'flex-wrap gap-x-4 gap-y-0.5' : 'flex-col gap-0.5')}>
      {workloads.map((w) => (
        <li key={`${w.namespace}/${w.kind}/${w.name}/${w.container ?? ''}`} className="min-w-0">
          <button type="button" onClick={() => onOpen(w)} className="group flex min-w-0 items-baseline gap-1.5 text-left text-xs">
            <span className="truncate text-accent-text group-hover:underline">{w.name}</span>
            <span className="shrink-0 text-theme-text-tertiary">{w.kind} · {w.namespace}{w.container ? ` · ${w.container}` : ''}</span>
          </button>
        </li>
      ))}
    </ul>
  )
}

function NumHead({ children }: { children: React.ReactNode }) {
  return <span className="text-right">{children}</span>
}

function Num({ children, strong = false }: { children: React.ReactNode; strong?: boolean }) {
  return <span className={clsx('text-right font-mono text-xs tabular-nums', strong ? 'font-medium text-theme-text-primary' : 'text-theme-text-secondary')}>{children}</span>
}

function ImageTable({ images, onOpen, onOpenReport }: { images: VulnerableImage[]; onOpen: (w: VulnerabilityWorkloadRef) => void; onOpenReport: (img: VulnerableImage) => void }) {
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div className="min-w-[860px]">
        <div className={clsx(HEAD, IMAGE_COLS)}>
          <span>Image</span><span>Findings</span><NumHead>With a fix</NumHead><span>Running in</span><NumHead>Scanned</NumHead>
        </div>
        {images.map((img) => {
          const total = img.counts.critical + img.counts.high + img.counts.medium + img.counts.low + img.counts.unknown
          const fixable = img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown
          return (
            <Disclosure
              key={img.id}
              flush
              summary={
                <div className={clsx('grid flex-1 items-center gap-4 text-sm', IMAGE_COLS)}>
                  <span className="flex min-w-0 flex-col">
                    <ImageName image={img.image} className="text-theme-text-primary" />
                    <span className="truncate font-mono text-xs text-theme-text-tertiary">{[imageParts(img.image).path, img.os, img.digest ? img.digest.replace('sha256:', '').slice(0, 12) : null].filter(Boolean).join(' · ')}</span>
                  </span>
                  {img.findingsUnread ? <span className="text-xs text-warning-text">Findings not read</span> : <SeverityBar counts={img.counts} className="max-w-[280px]" />}
                  <WithTooltip tip={img.findingsUnread ? undefined : `${fixable} of ${total} findings have a fixed release`}>
                    <Num>{img.findingsUnread ? '—' : <>{fixable}<span className="text-theme-text-tertiary"> of {total}</span></>}</Num>
                  </WithTooltip>
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate text-theme-text-secondary">{workloadSummary(img.workloads)}{img.workloadsTotal > img.workloads.length ? ` and ${img.workloadsTotal - img.workloads.length} more` : ''}</span>
                    <span className="text-xs text-theme-text-tertiary">{namespaceCount(img)}</span>
                  </span>
                  <span className="text-right text-xs text-theme-text-tertiary">{formatRelativeAgeTime(img.scannedAt)}</span>
                </div>
              }
            >
              <div className={clsx('grid gap-4 py-1.5 pl-10 pr-3', IMAGE_COLS)}>
                <div className="col-span-3 flex min-w-0 flex-col gap-1.5">
                  <span className="text-xs font-medium text-theme-text-secondary">
                    {img.matching === img.findingsTotal ? 'Findings, worst first' : `${img.matching} of ${img.findingsTotal} findings match`}
                  </span>
                  <FindingsList findings={img.findings} total={img.findingsTotal} unread={img.findingsUnread} />
                  <span className="flex gap-3 text-xs">
                    {img.matching > img.findings.length && <span className="text-theme-text-tertiary">{img.matching - img.findings.length} more in the report.</span>}
                    <button type="button" onClick={() => onOpenReport(img)} className="text-accent-text hover:underline">Open the scan report</button>
                  </span>
                </div>
                <div className="col-span-2 flex min-w-0 flex-col gap-1.5">
                  <span className="text-xs font-medium text-theme-text-secondary">{img.workloadsTotal} {img.workloadsTotal === 1 ? 'container' : 'containers'}{img.workloadsTotal > img.workloads.length ? ` (first ${img.workloads.length})` : ''}</span>
                  <WorkloadLinks workloads={img.workloads} onOpen={onOpen} />
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

function FindingsList({ findings, total, unread }: { findings: VulnerableImage['findings']; total: number; unread?: boolean }) {
  if (unread) {
    return <span className="text-xs text-warning-text">Not read: this request reached its findings limit before this image. Narrow with the namespace picker.</span>
  }
  if (findings.length === 0) {
    return <span className="text-xs text-theme-text-tertiary">{total === 0 ? 'No CVEs reported for this image.' : 'No findings match the filters.'}</span>
  }
  return (
    <div className="flex flex-col divide-y divide-theme-border border-y border-theme-border">
      {findings.map((f, i) => (
        <div key={`${f.id}/${f.package}/${f.installedVersion}/${i}`} className="grid grid-cols-[136px_72px_minmax(0,0.8fr)_minmax(0,1.4fr)] items-center gap-3 py-1 text-xs">
          {f.link ? (
            <WithTooltip tip={f.title}><a href={f.link} target="_blank" rel="noreferrer" className="truncate font-mono text-accent-text hover:underline">{f.id}</a></WithTooltip>
          ) : (
            <WithTooltip tip={f.title}><span className="truncate font-mono text-theme-text-primary">{f.id}</span></WithTooltip>
          )}
          <SeverityBadge severity={f.severity} fromScore={f.severityFromScore} score={f.score} />
          <span className="truncate font-mono text-theme-text-secondary">{f.package}</span>
          <FixVersion installed={f.installedVersion} fixed={f.fixedVersion} target={f.fixTarget} />
        </div>
      ))}
    </div>
  )
}

function FixTable({ fixes, onOpen, onShowCVE }: { fixes: VulnerabilityFix[]; onOpen: (w: VulnerabilityWorkloadRef) => void; onShowCVE: (id: string) => void }) {
  return (
    <div className="flex shrink-0 flex-col gap-2">
      <p className="text-xs text-theme-text-tertiary">
        One row per package update, with every CVE it clears: the lowest release that fixes them all, compared with the package's own versioning. Updating a package means rebuilding the image that holds it; for an image you don't build, look for a newer release of the image.
      </p>
      <div className="overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
        <div className="min-w-[960px]">
          <div className={clsx(HEAD, FIX_COLS)}>
            <span>Package</span><span>Upgrade</span><span>Clears</span><span>Running in</span><NumHead>Images</NumHead><NumHead>Workloads</NumHead>
          </div>
          {fixes.map((f) => {
            const runners = f.affected.flatMap((a) => a.workloads)
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
                      <span className="min-w-0 truncate font-mono text-xs">
                        <span className="text-theme-text-tertiary">{f.from.join(', ')} → </span>
                        {f.to
                          ? <span className={SEVERITY_TEXT.success}>{f.to}</span>
                          : <span className="text-theme-text-secondary">fixed in {(f.fixedVersions ?? []).join(', ')}</span>}
                      </span>
                    </WithTooltip>
                    <SeverityCounts counts={f.counts} />
                    <span className="truncate text-theme-text-secondary">{workloadSummary(runners)}</span>
                    <Num>{f.images}</Num>
                    <Num strong>{f.workloads}</Num>
                  </div>
                }
              >
                <div className={clsx(SUB, FIX_COLS)}>
                  <span className="text-theme-text-tertiary">{f.cvesTotal} {f.cvesTotal === 1 ? 'CVE' : 'CVEs'} cleared</span>
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
                    <WorkloadLinks workloads={a.workloads} onOpen={onOpen} inline />
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

function CVETable({ cves, onOpen }: { cves: VulnerabilityCVE[]; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  return (
    <div className="flex shrink-0 flex-col gap-2">
      <p className="text-xs text-theme-text-tertiary">Worst first, then CVEs with a fix, then the ones the most running workloads carry: one base-image bump can clear a CVE everywhere it runs.</p>
      <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
        <div className="min-w-[960px]">
          <div className={clsx(HEAD, CVE_COLS)}>
            <span>CVE</span><span>Severity</span><NumHead>Score</NumHead><span>Package</span><span>Fix</span><NumHead>Images</NumHead><NumHead>Workloads</NumHead>
          </div>
          {cves.map((c) => (
            <Disclosure
              key={c.id}
              flush
              summary={
                <div className={clsx('grid flex-1 items-center gap-4 text-sm', CVE_COLS)}>
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-mono text-theme-text-primary">{c.id}</span>
                    {c.title && <span className="truncate text-xs text-theme-text-tertiary">{c.title}</span>}
                  </span>
                  <SeverityBadge severity={c.severity} />
                  <Num>{c.score?.toFixed(1) ?? '—'}</Num>
                  <span className="truncate font-mono text-xs text-theme-text-secondary">{c.packages.join(', ')}</span>
                  <CVEFix cve={c} />
                  <Num>{c.images}</Num>
                  <Num strong>{c.workloads}</Num>
                </div>
              }
            >
              {c.affected.map((a) => (
                <div key={a.imageId} className={clsx(SUB, CVE_COLS)}>
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <ImageName image={a.image} className="text-theme-text-primary" />
                    <WorkloadLinks workloads={a.workloads} onOpen={onOpen} inline />
                  </span>
                  <span className="col-span-2" />
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
          ))}
        </div>
      </div>
    </div>
  )
}

function WorkloadTable({ workloads, severities, query, namespaces, onOpen, onShow }: {
  workloads: VulnerabilityWorkload[]
  severities: VulnerabilitySeverity[]
  query: string
  /** The table's namespace scope, so an expanded row reads what the row did. */
  namespaces: string[]
  onOpen: (w: VulnerabilityWorkloadRef) => void
  onShow: (w: VulnerabilityWorkloadRef, by: VulnerabilityView) => void
}) {
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div className="min-w-[760px]">
        <div className={clsx(HEAD, WORKLOAD_COLS)}>
          <span>Workload</span><span>Findings across its images</span><span>Containers</span>
        </div>
        {workloads.map((w) => (
          <Disclosure
            key={`${w.namespace}/${w.kind}/${w.name}`}
            flush
            summary={
              <div className={clsx('grid flex-1 items-start gap-4 text-sm', WORKLOAD_COLS)}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-theme-text-primary">{w.name}</span>
                  <span className="text-xs text-theme-text-tertiary">{w.kind} · {w.namespace}</span>
                </span>
                <WorkloadFindings workload={w} />
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
            {(open) => <WorkloadUpgrades workload={w} active={open} severities={severities} query={query} namespaces={namespaces} onOpen={onOpen} onShow={onShow} />}
          </Disclosure>
        ))}
      </div>
    </div>
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
function WorkloadUpgrades({ workload: w, active, severities, query, namespaces, onOpen, onShow }: {
  workload: VulnerabilityWorkload
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
      {total > 0 && <button type="button" onClick={() => onShow(ref, 'image')} className="text-accent-text hover:underline">{filtered ? 'Its findings' : `All ${total} findings`}</button>}
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
      {fixes.length} {fixes.length === 1 ? 'upgrade clears' : 'upgrades clear'} {fixable} of its {total} findings
      {total > fixable && <span className="text-theme-text-tertiary">; {total - fixable} have no fix yet</span>}
      {unknown > 0 && <span className="text-warning-text">. {unknown} {unknown === 1 ? "container's" : "containers'"} findings weren't read</span>}
    </span>
  )
  return (
    <>
      <div className={clsx(SUB, WORKLOAD_COLS)}>
        <span className="col-span-3">{lead}</span>
      </div>
      {fixes.slice(0, MAX_WORKLOAD_UPGRADES).map((f) => (
        <div key={`${f.packageType ?? ''}/${f.package}/${f.to ?? (f.fixedVersions ?? []).join('|')}`} className={clsx(SUB, WORKLOAD_COLS)}>
          <span className="flex min-w-0 items-baseline gap-2">
            <span className="truncate font-mono text-theme-text-primary">{f.package}</span>
            {f.packageType && <span className="truncate text-theme-text-tertiary">{ecosystemLabel(f.packageType)}</span>}
          </span>
          <span className="grid min-w-0 grid-cols-[64px_minmax(0,1fr)] items-baseline gap-2">
            <SeverityCounts counts={f.counts} />
            {f.to
              ? <FixVersion installed={f.from.join(', ')} target={f.to} />
              : <span className="truncate font-mono text-theme-text-secondary">fixed in {(f.fixedVersions ?? []).join(', ')}</span>}
          </span>
          <span className="flex min-w-0 flex-wrap gap-x-3">
            {several && f.affected.map((a) => <ImageName key={a.imageId} image={a.image} className="text-theme-text-tertiary" />)}
          </span>
        </div>
      ))}
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
                <WorkloadLinks workloads={u.workloads} onOpen={onOpen} inline />
                {u.workloadsTotal > u.workloads.length && <span className="text-theme-text-tertiary">and {u.workloadsTotal - u.workloads.length} more</span>}
              </span>
            </li>
          ))}
        </ul>
      </Disclosure>
    </div>
  )
}
