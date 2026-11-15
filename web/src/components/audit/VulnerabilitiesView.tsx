import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { AlertTriangle, CircleDashed, ExternalLink, Search, ShieldAlert } from 'lucide-react'
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
} from '@skyhook-io/k8s-ui'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { ApiError } from '../../api/client'
import {
  useVulnerabilities,
  type VulnerabilitiesResponse,
  type VulnerabilityCVE,
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
  FindingTotal,
  findingsCertainty,
  gapSentence,
  workloadLabel,
  workloadSummary,
} from '../vulnerabilities/parts'

interface VulnerabilitiesViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

const VIEWS: { key: VulnerabilityView; label: string }[] = [
  { key: 'image', label: 'By image' },
  { key: 'cve', label: 'By CVE' },
  { key: 'workload', label: 'By workload' },
]

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
  const query = useDebouncedValue(queryDraft.trim(), 300)
  const { connection } = useConnection()

  const update = (mutate: (p: URLSearchParams) => void) => {
    const next = new URLSearchParams(params)
    mutate(next)
    setParams(next, { replace: true })
  }
  const { data, isLoading, isFetching, error, dataUpdatedAt, refetch } = useVulnerabilities({ view, namespaces, severities, fixableOnly, query })

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
  if (error || !data) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4">
        <ChecksViewTabs />
        <div className="flex flex-1 items-center justify-center"><VulnerabilitiesError error={error} /></div>
      </div>
    )
  }

  const filtered = severities.length > 0 || fixableOnly || query !== ''
  const rows = view === 'cve' ? data.cves ?? [] : view === 'workload' ? data.workloads ?? [] : data.images ?? []

  return (
    <div aria-busy={isFetching} className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <ChecksViewTabs />
      <PageHeader
        icon={ShieldAlert}
        title="Vulnerabilities"
        description="Known CVEs in the images that run now, from Trivy Operator's reports."
        actions={<FreshnessControl mode="auto" dataUpdatedAt={dataUpdatedAt} onRefresh={() => refetch()} connectionState={connection.state} />}
      />

      <SummaryTiles data={data} />
      <CoverageNotes data={data} />

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
        <div role="group" aria-label="Severity" className="flex flex-wrap gap-1.5">
          {SEVERITIES.map((s) => {
            const on = severities.includes(s.key)
            return (
              <button
                key={s.key}
                type="button"
                aria-pressed={on}
                onClick={() => toggleSeverity(s.key)}
                className={clsx('min-h-8 rounded-full border px-3 text-xs font-medium transition-colors', on ? 'selection-strong border-accent text-theme-text-primary' : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover')}
              >
                {s.label}
              </button>
            )
          })}
        </div>
        <label className="flex min-h-9 items-center gap-1.5 text-sm text-theme-text-secondary">
          <input type="checkbox" checked={fixableOnly} onChange={(e) => update((p) => (e.target.checked ? p.set('fixable', '1') : p.delete('fixable')))} className="h-4 w-4 accent-[var(--accent)]" />
          Has a fix
        </label>
        <div role="group" aria-label="Group by" className="ml-auto flex rounded-md border border-theme-border bg-theme-elevated p-0.5">
          {VIEWS.map((v) => (
            <button
              key={v.key}
              type="button"
              aria-pressed={view === v.key}
              onClick={() => update((p) => (v.key === 'image' ? p.delete('by') : p.set('by', v.key)))}
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
          headline={filtered ? 'Nothing matches these filters' : data.summary.imagesScanned === 0 ? 'No scanned images run in this scope' : 'No known CVEs in the images that run'}
          body={filtered ? 'Clear a filter to see more.' : data.summary.imagesScanned === 0 && data.unscanned.length > 0 ? 'No report covers a running image yet. Trivy Operator scans workloads as they appear; if this persists, check that it is running and that its target namespaces include these.' : undefined}
        />
      ) : view === 'cve' ? (
        <CVETable cves={data.cves ?? []} onOpen={(w) => onNavigateToResource(toResource(w))} />
      ) : view === 'workload' ? (
        <WorkloadTable workloads={data.workloads ?? []} onOpen={(w) => onNavigateToResource(toResource(w))} />
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

      <UnscannedList data={data} onOpen={(w) => onNavigateToResource(toResource(w))} />
    </div>
  )
}

function VulnerabilitiesError({ error }: { error: unknown }) {
  const upgrade = getRadarUpgradeRequirement(error)
  if (upgrade) {
    return <EmptyState tone="neutral" icon={ShieldAlert} headline={radarUpgradeHeadline('Vulnerability views')} body={radarUpgradeDetail(upgrade)} action={<RadarUpgradeAction requirement={upgrade} />} />
  }
  // A cluster without the report kind: nothing failed, there is no scanner.
  if (error instanceof ApiError && error.status === 404) {
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
    { label: 'Running images scanned', value: s.imagesScanned, certainty: 'exact' as const, sub: s.imagesUnscanned > 0 ? `${s.imagesUnscanned} not scanned yet` : 'every running image has a report', tone: '' },
    { label: 'Images with Critical CVEs', value: s.imagesWithCritical, certainty, sub: nothing ? 'nothing scanned yet' : `of ${s.imagesScanned} scanned`, tone: s.imagesWithCritical > 0 ? 'text-red-700 dark:text-red-400' : '' },
    { label: 'Critical and High CVEs with a fix', value: s.fixableCriticalHighCves, certainty, sub: nothing ? 'nothing scanned yet' : 'fixed in at least one image', tone: s.fixableCriticalHighCves > 0 ? 'text-orange-700 dark:text-orange-400' : '' },
    { label: 'Workloads with Critical or High', value: s.workloadsAffected, certainty, sub: `of ${s.workloads} running or scheduled`, tone: '' },
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

function CoverageNotes({ data }: { data: VulnerabilitiesResponse }) {
  const gaps = gapSentence(data.coverage.gaps)
  const scan = data.summary.newestScan
    ? data.summary.oldestScan && data.summary.oldestScan !== data.summary.newestScan
      ? `Scans from ${formatRelativeAgeTime(data.summary.oldestScan)} to ${formatRelativeAgeTime(data.summary.newestScan)}`
      : `Scanned ${formatRelativeAgeTime(data.summary.newestScan)}`
    : null
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      <p className="text-theme-text-tertiary">
        From Trivy Operator's VulnerabilityReports{scan ? ` · ${scan}` : ''}. A report reflects what was known when the image was scanned.
        {data.coverage.inactiveReports > 0 && ` ${data.coverage.inactiveReports} ${data.coverage.inactiveReports === 1 ? 'report is' : 'reports are'} about older revisions or removed workloads and isn't counted.`}
      </p>
      {gaps && <p className="text-warning-text">{gaps}</p>}
      {data.coverage.truncated && <p className="text-warning-text">Some images have too many findings to read in one request and are marked unread. Narrow with the namespace picker.</p>}
    </div>
  )
}

function Disclosure({ summary, children, open: initiallyOpen = false }: { summary: React.ReactNode; children: React.ReactNode; open?: boolean }) {
  const [open, setOpen] = useState(initiallyOpen)
  const { panelId, buttonProps } = useDisclosure(open)
  return (
    <div className="border-b border-theme-border last:border-0">
      <button {...buttonProps} type="button" onClick={() => setOpen(!open)} className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-theme-hover">
        <CollapseChevron open={open} className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
        {summary}
      </button>
      <Collapse open={open} id={panelId} mountLazily>
        <div className="px-3 pb-3 pl-10">{children}</div>
      </Collapse>
    </div>
  )
}

function WorkloadLinks({ workloads, onOpen }: { workloads: VulnerabilityWorkloadRef[]; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  return (
    <ul className="flex flex-col gap-1">
      {workloads.map((w) => (
        <li key={`${w.namespace}/${w.kind}/${w.name}/${w.container ?? ''}`}>
          <button type="button" onClick={() => onOpen(w)} className="flex min-h-8 w-full items-center gap-2 rounded-md border border-theme-border bg-theme-surface px-2 text-left text-xs hover:bg-theme-hover">
            <span className="badge-sm bg-theme-elevated text-theme-text-secondary">{w.kind}</span>
            <span className="text-theme-text-primary">{w.name}</span>
            <span className="text-theme-text-tertiary">{w.namespace}{w.container ? ` · container ${w.container}` : ''}</span>
          </button>
        </li>
      ))}
    </ul>
  )
}

function ImageTable({ images, onOpen, onOpenReport }: { images: VulnerableImage[]; onOpen: (w: VulnerabilityWorkloadRef) => void; onOpenReport: (img: VulnerableImage) => void }) {
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div className="min-w-[860px]">
        <div className="grid grid-cols-[minmax(0,2.4fr)_minmax(0,2fr)_80px_minmax(0,2fr)_100px] gap-4 border-b border-theme-border py-2 pl-10 pr-3 text-[11px] font-semibold uppercase tracking-wider text-theme-text-tertiary">
          <span>Image</span><span>Findings</span><span>With a fix</span><span>Running in</span><span>Scanned</span>
        </div>
        {images.map((img) => (
          <Disclosure
            key={img.id}
            summary={
              <div className="grid flex-1 grid-cols-[minmax(0,2.4fr)_minmax(0,2fr)_80px_minmax(0,2fr)_100px] items-center gap-4 text-sm">
                <span className="flex min-w-0 flex-col">
                  <span className="truncate font-mono text-theme-text-primary" title={img.image}>{img.image}</span>
                  <span className="truncate font-mono text-xs text-theme-text-tertiary">{img.digest ? `${img.digest.slice(0, 19)}…` : 'no digest recorded'}{img.os ? ` · ${img.os}` : ''}</span>
                </span>
                {img.findingsUnread ? <span className="text-xs text-warning-text">Too many findings to read here</span> : <SeverityBar counts={img.counts} />}
                <span className="font-mono text-xs text-theme-text-secondary">{img.findingsUnread ? '—' : img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown}</span>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-theme-text-secondary">{workloadSummary(img.workloads)}{img.workloadsTotal > img.workloads.length ? ` and ${img.workloadsTotal - img.workloads.length} more` : ''}</span>
                  <span className="text-xs text-theme-text-tertiary">{namespaceCount(img)}</span>
                </span>
                <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(img.scannedAt)}</span>
              </div>
            }
          >
            <div className="grid gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
              <div className="flex min-w-0 flex-col gap-2">
                <span className="text-xs font-medium text-theme-text-secondary">
                  {img.matching === img.findingsTotal ? 'Findings, worst first' : `${img.matching} of ${img.findingsTotal} findings match`}
                </span>
                <FindingsList findings={img.findings} />
                {img.matching > img.findings.length && <span className="text-xs text-theme-text-tertiary">{img.matching - img.findings.length} more in the report.</span>}
                <button type="button" onClick={() => onOpenReport(img)} className="self-start text-xs text-accent-text hover:underline">Open the scan report</button>
              </div>
              <div className="flex min-w-0 flex-col gap-2">
                <span className="text-xs font-medium text-theme-text-secondary">Running in {img.workloadsTotal} {img.workloadsTotal === 1 ? 'container' : 'containers'}{img.workloadsTotal > img.workloads.length ? ` (first ${img.workloads.length})` : ''}</span>
                <WorkloadLinks workloads={img.workloads} onOpen={onOpen} />
              </div>
            </div>
          </Disclosure>
        ))}
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

function FindingsList({ findings }: { findings: VulnerableImage['findings'] }) {
  if (findings.length === 0) return <span className="text-xs text-theme-text-tertiary">No findings match.</span>
  return (
    <div className="overflow-hidden rounded-md border border-theme-border">
      {findings.map((f) => (
        <div key={`${f.id}/${f.package}/${f.installedVersion}`} className="grid grid-cols-[150px_74px_minmax(0,1fr)_minmax(0,1.4fr)] items-center gap-3 border-b border-theme-border bg-theme-base px-3 py-1.5 text-xs last:border-0">
          {f.link ? (
            <a href={f.link} target="_blank" rel="noreferrer" className="truncate font-mono text-accent-text hover:underline" title={f.title}>{f.id}</a>
          ) : (
            <span className="truncate font-mono text-theme-text-primary" title={f.title}>{f.id}</span>
          )}
          <SeverityBadge severity={f.severity} />
          <span className="truncate font-mono text-theme-text-secondary">{f.package}</span>
          <span className="truncate font-mono text-theme-text-tertiary">
            {f.installedVersion} → {f.fixedVersion ? <span className="text-emerald-700 dark:text-emerald-400">{f.fixedVersion}</span> : 'no fix yet'}
          </span>
        </div>
      ))}
    </div>
  )
}

function CVETable({ cves, onOpen }: { cves: VulnerabilityCVE[]; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  return (
    <div className="flex shrink-0 flex-col gap-2">
      <p className="text-xs text-theme-text-tertiary">Worst first, then CVEs with a fix, then the ones the most running workloads carry: one base-image bump can clear a CVE everywhere it runs.</p>
      <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
        <div className="min-w-[900px]">
          <div className="grid grid-cols-[170px_80px_56px_minmax(0,1.4fr)_70px_80px_90px] gap-4 border-b border-theme-border py-2 pl-10 pr-3 text-[11px] font-semibold uppercase tracking-wider text-theme-text-tertiary">
            <span>CVE</span><span>Severity</span><span>Score</span><span>Package</span><span>Has a fix</span><span>Images</span><span>Workloads</span>
          </div>
          {cves.map((c) => (
            <Disclosure
              key={c.id}
              summary={
                <div className="grid flex-1 grid-cols-[170px_80px_56px_minmax(0,1.4fr)_70px_80px_90px] items-center gap-4 text-sm">
                  <span className="truncate font-mono text-theme-text-primary">{c.id}</span>
                  <SeverityBadge severity={c.severity} />
                  <span className="font-mono text-xs text-theme-text-secondary">{c.score?.toFixed(1) ?? '—'}</span>
                  <span className="truncate font-mono text-xs text-theme-text-secondary">{c.packages.join(', ')}</span>
                  <span className={clsx('text-xs', c.fixable ? 'text-emerald-700 dark:text-emerald-400' : 'text-theme-text-tertiary')}>{c.fixable ? 'Yes' : 'No'}</span>
                  <span className="font-mono text-xs text-theme-text-secondary">{c.images}</span>
                  <span className="font-mono text-xs font-medium text-theme-text-primary">{c.workloads}</span>
                </div>
              }
            >
              <div className="flex flex-col gap-2">
                {c.title && <p className="text-sm text-theme-text-secondary">{c.title}</p>}
                <div className="overflow-hidden rounded-md border border-theme-border">
                  {c.affected.map((a) => (
                    <div key={a.imageId} className="grid grid-cols-[minmax(0,2fr)_minmax(0,1.2fr)_minmax(0,2fr)] items-start gap-3 border-b border-theme-border bg-theme-base px-3 py-2 text-xs last:border-0">
                      <span className="truncate font-mono text-theme-text-primary" title={a.image}>{a.image}</span>
                      <span className="flex min-w-0 flex-col">
                        {a.packages.map((p) => (
                          <span key={`${p.package}/${p.installedVersion}`} className="truncate font-mono text-theme-text-tertiary">
                            {p.package} {p.installedVersion} → {p.fixedVersion ? <span className="text-emerald-700 dark:text-emerald-400">{p.fixedVersion}</span> : 'no fix yet'}
                          </span>
                        ))}
                      </span>
                      <span className="flex flex-wrap gap-x-2">
                        {a.workloads.map((w) => (
                          <button key={`${w.namespace}/${w.name}/${w.container ?? ''}`} type="button" onClick={() => onOpen(w)} className="text-accent-text hover:underline">{workloadLabel(w)}</button>
                        ))}
                        {a.workloadsTotal > a.workloads.length && <span className="text-theme-text-tertiary">and {a.workloadsTotal - a.workloads.length} more</span>}
                      </span>
                    </div>
                  ))}
                </div>
                {c.images > c.affected.length && <span className="text-xs text-theme-text-tertiary">{c.images - c.affected.length} more images.</span>}
                {c.link && <a href={c.link} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 self-start text-xs text-accent-text hover:underline">Advisory <ExternalLink className="h-3 w-3" aria-hidden /></a>}
              </div>
            </Disclosure>
          ))}
        </div>
      </div>
    </div>
  )
}

function WorkloadTable({ workloads, onOpen }: { workloads: VulnerabilityWorkload[]; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  return (
    <div className="shrink-0 overflow-x-auto overflow-y-hidden rounded-lg border border-theme-border bg-theme-surface">
      <div className="min-w-[760px]">
        <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,2fr)_minmax(0,2.4fr)] gap-4 border-b border-theme-border px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-theme-text-tertiary">
          <span>Workload</span><span>Findings across its images</span><span>Containers</span>
        </div>
        {workloads.map((w) => (
          <div key={`${w.namespace}/${w.kind}/${w.name}`} className="grid grid-cols-[minmax(0,2fr)_minmax(0,2fr)_minmax(0,2.4fr)] items-start gap-4 border-b border-theme-border px-3 py-2.5 text-sm last:border-0">
            <button type="button" onClick={() => onOpen(w)} className="flex min-w-0 flex-col items-start text-left">
              <span className="truncate text-accent-text hover:underline">{w.name}</span>
              <span className="text-xs text-theme-text-tertiary">{w.kind} · {w.namespace}</span>
            </button>
            <WorkloadFindings workload={w} />
            <ul className="flex min-w-0 flex-col gap-1">
              {w.containers.map((c) => (
                <li key={`${c.name}/${c.image}`} className="flex min-w-0 flex-col text-xs">
                  <span className="truncate"><span className="text-theme-text-secondary">{c.name}</span> <span className="font-mono text-theme-text-tertiary">{c.image}</span></span>
                  {c.scanned && c.counts ? <SeverityCounts counts={c.counts} /> : c.findingsUnread ? <span className="text-warning-text">Too many findings to read here</span> : <span className="text-warning-text">Not scanned yet</span>}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </div>
  )
}

// A workload's counts cover only its scanned containers: with none scanned
// they are unknown, never "no known CVEs".
function WorkloadFindings({ workload }: { workload: VulnerabilityWorkload }) {
  const unknown = workload.containers.filter((c) => !c.scanned || c.findingsUnread).length
  if (unknown === workload.containers.length) {
    return <span className="text-xs text-warning-text">No findings read for any container</span>
  }
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <SeverityBar counts={workload.counts} />
      {unknown > 0 && <span className="text-xs text-warning-text">{unknown} {unknown === 1 ? 'container' : 'containers'} not counted: not scanned yet or too many findings</span>}
    </div>
  )
}

function UnscannedList({ data, onOpen }: { data: VulnerabilitiesResponse; onOpen: (w: VulnerabilityWorkloadRef) => void }) {
  if (data.unscanned.length === 0) return null
  return (
    <div className="shrink-0 rounded-lg border border-theme-border bg-theme-surface">
      <Disclosure
        summary={
          <span className="flex items-center gap-2 text-sm text-theme-text-secondary">
            <CircleDashed className="h-4 w-4 text-theme-text-tertiary" aria-hidden />
            {data.summary.imagesUnscanned} running {data.summary.imagesUnscanned === 1 ? 'image has' : 'images have'} no report yet: unknown, not clean
          </span>
        }
      >
        <ul className="flex flex-col gap-2">
          {data.unscanned.map((u) => (
            <li key={u.image} className="flex flex-col gap-1">
              <span className="font-mono text-xs text-theme-text-primary">{u.image}</span>
              <WorkloadLinks workloads={u.workloads} onOpen={onOpen} />
              {u.workloadsTotal > u.workloads.length && <span className="text-xs text-theme-text-tertiary">and {u.workloadsTotal - u.workloads.length} more</span>}
            </li>
          ))}
        </ul>
      </Disclosure>
    </div>
  )
}
