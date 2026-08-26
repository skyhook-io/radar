import { useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import {
  AlertCircle,
  AlertTriangle,
  BookOpen,
  CheckCircle2,
  CircleDashed,
  CircleMinus,
  ExternalLink,
  FileSearch,
  ShieldAlert,
  Wrench,
} from 'lucide-react'
import {
  Badge,
  Collapse,
  CollapseChevron,
  EmptyState,
  FreshnessControl,
  PageHeader,
  PaneLoader,
  SelectMenu,
} from '@skyhook-io/k8s-ui'
import {
  ApiError,
  useUpgradeReadiness,
  type UpgradeReadinessCheck,
  type UpgradeReadinessCheckStatus,
  type UpgradeReadinessFinding,
  type UpgradeReadinessResponse,
  type UpgradeReadinessVerdict,
} from '../../api/client'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { ChecksViewTabs } from './ChecksViewTabs'

interface UpgradeReadinessViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

const FINDING_CAP = 8
export const UPGRADE_IMPACT_DOCS_URL = 'https://radarhq.io/docs/features/upgrade-impact'
export const UPGRADE_IMPACT_MIN_RADAR_VERSION = 'v1.9.0'

const statusMeta: Record<UpgradeReadinessCheckStatus, {
  label: string
  icon: typeof CheckCircle2
  iconClass: string
  badgeSeverity: 'error' | 'warning' | 'info' | 'success' | 'neutral'
}> = {
  blocked: { label: 'Blocked', icon: AlertCircle, iconClass: 'text-red-600 dark:text-red-400', badgeSeverity: 'error' },
  warning: { label: 'Warning', icon: AlertTriangle, iconClass: 'text-amber-600 dark:text-amber-400', badgeSeverity: 'warning' },
  review: { label: 'Review', icon: FileSearch, iconClass: 'text-blue-600 dark:text-blue-400', badgeSeverity: 'info' },
  unknown: { label: 'Incomplete', icon: CircleDashed, iconClass: 'text-theme-text-tertiary', badgeSeverity: 'neutral' },
  passed: { label: 'Passed', icon: CheckCircle2, iconClass: 'text-emerald-600 dark:text-emerald-400', badgeSeverity: 'success' },
  not_applicable: { label: 'Not applicable', icon: CircleMinus, iconClass: 'text-theme-text-disabled', badgeSeverity: 'neutral' },
}

const statusOrder: Record<UpgradeReadinessCheckStatus, number> = {
  blocked: 0,
  warning: 1,
  review: 2,
  unknown: 3,
  passed: 4,
  not_applicable: 5,
}

const checkPriority: Record<string, number> = {
  'control-plane-upgrade-path': 0,
}

export function UpgradeReadinessView({ namespaces, onNavigateToResource }: UpgradeReadinessViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const requestedTarget = new URLSearchParams(location.search).get('target') ?? undefined
  const { data, isLoading, isFetching, isPlaceholderData, error, dataUpdatedAt, refreshScan } = useUpgradeReadiness(requestedTarget)
  const { connection } = useConnection()
  const targetOptions = useMemo(
    () => buildTargetOptions(data?.currentVersion, data?.reviewedThrough, requestedTarget ?? data?.targetVersion),
    [data?.currentVersion, data?.reviewedThrough, data?.targetVersion, requestedTarget],
  )
  const sortedChecks = useMemo(
    () => [...(data?.checks ?? [])].sort((a, b) =>
      statusOrder[a.status] - statusOrder[b.status]
      || (checkPriority[a.id] ?? 1) - (checkPriority[b.id] ?? 1)
      || a.category.localeCompare(b.category)
      || a.title.localeCompare(b.title),
    ),
    [data?.checks],
  )
  const resetTarget = () => {
    const params = new URLSearchParams(location.search)
    params.delete('target')
    navigate({ pathname: location.pathname, search: params.toString() }, { replace: true })
  }

  if (isLoading) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4">
        <ChecksViewTabs />
        <PaneLoader label="Analyzing upgrade impact…" className="flex-1" />
      </div>
    )
  }
  if (error || !data) {
    return (
      <div className="flex-1 flex flex-col min-h-0 p-4 gap-4">
        <ChecksViewTabs />
        <div className="flex flex-1 items-center justify-center">
          <UpgradeReadinessError error={error} onResetTarget={resetTarget} />
        </div>
      </div>
    )
  }

  const setTarget = (target: string) => {
    const params = new URLSearchParams(location.search)
    params.set('target', target)
    navigate({ pathname: location.pathname, search: params.toString() })
  }
  const unsupportedTarget = compareMinor(data.targetVersion, data.reviewedThrough) > 0
  const scopedKinds = Object.entries(data.coverage.scopedKinds ?? {})
  const showingPreviousTarget = Boolean(isPlaceholderData && requestedTarget && requestedTarget !== data.targetVersion)

  return (
    <div aria-busy={isFetching} className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <ChecksViewTabs />

      <PageHeader
        icon={ShieldAlert}
        title="Upgrade impact"
        description={`Find configuration and compatibility changes before upgrading Kubernetes ${data.currentVersion} → ${data.targetVersion}.`}
        actions={
          <>
            {data.coverage.state !== 'no_access' && (
              <div className="flex items-center gap-2">
                <span className="text-xs text-theme-text-tertiary">Target</span>
                {targetOptions.length > 1 ? (
                  <SelectMenu
                    value={requestedTarget ?? data.targetVersion}
                    options={targetOptions}
                    onChange={setTarget}
                    ariaLabel="Target Kubernetes version"
                    className="w-24"
                  />
                ) : (
                  <span className="rounded-md bg-theme-elevated px-2.5 py-1.5 text-xs font-medium tabular-nums text-theme-text-primary">
                    {data.targetVersion}
                  </span>
                )}
              </div>
            )}
            <FreshnessControl
              mode="snapshot"
              dataUpdatedAt={data.observedAt ? Date.parse(data.observedAt) : dataUpdatedAt}
              onRefresh={() => refreshScan()}
              connectionState={connection.state}
              isFetching={isFetching}
            />
          </>
        }
      />

      {showingPreviousTarget && (
        <CoverageNotice
          headline={`Analyzing Kubernetes ${requestedTarget}`}
          body={`Results for Kubernetes ${data.targetVersion} remain visible until the new scan completes.`}
        />
      )}

      {data.coverage.state !== 'no_access' && (
        <UpgradeSummary
          verdict={data.verdict}
          target={data.targetVersion}
          reviewedThrough={data.reviewedThrough}
          summary={data.summary}
          coverageState={data.coverage.state}
        />
      )}

      {unsupportedTarget && data.coverage.state !== 'no_access' && (
        <CoverageNotice
          headline={`Coverage ends at Kubernetes ${data.reviewedThrough}`}
          body={`Known results are shown, but checks added after ${data.reviewedThrough} are not included for ${data.targetVersion}. Update Radar when a release with ${data.targetVersion} coverage is available, then run this scan again.`}
        />
      )}
      {data.coverage.state !== 'no_access' && (data.coverage.scopedNamespaces?.length ?? 0) > 0 && (
        <CoverageNotice
          headline={`Scoped to ${data.coverage.scopedNamespaces!.length} ${data.coverage.scopedNamespaces!.length === 1 ? 'namespace' : 'namespaces'}`}
          body={`Results do not cover namespaced configuration outside ${formatNamespaceScope(data.coverage.scopedNamespaces!)}.`}
        />
      )}
      {namespaces.length > 0 && !data.coverage.scopedNamespaces?.length && scopedKinds.length === 0 && data.coverage.state !== 'no_access' && (
        <CoverageNotice
          headline="Cluster-wide upgrade scan"
          body="The current namespace browsing filter does not limit upgrade checks. Radar evaluates every namespace your identity can read."
        />
      )}
      {data.coverage.state !== 'no_access' && (data.coverage.unavailableKinds?.length ?? 0) > 0 && (
        <CoverageNotice
          headline="Some live resources were unavailable"
          body={`Radar could not inspect: ${(data.coverage.unavailableKinds ?? []).join(', ')}. Affected checks are marked incomplete.`}
        />
      )}
      {data.coverage.state !== 'no_access' && scopedKinds.length > 0 && (
        <CoverageNotice
          headline={`${scopedKinds.length} cached ${scopedKinds.length === 1 ? 'resource kind has' : 'resource kinds have'} narrower coverage`}
          body={`Informer evidence is namespace-limited for ${formatScopedKinds(scopedKinds)}. Radar does not treat absence outside those per-kind scopes as proof.`}
        />
      )}
      {data.coverage.state === 'partial' && !data.coverage.scopedNamespaces?.length && !data.coverage.unavailableKinds?.length && scopedKinds.length === 0 && (
        <CoverageNotice headline="Some evidence is incomplete" body="Rows marked Partial evidence explain what Radar could not verify." />
      )}
      {data.coverage.state === 'no_access' ? (
        <section className="shrink-0">
          <EmptyState
            tone="neutral"
            icon={FileSearch}
            headline="Upgrade checks need namespace access"
            body="Radar cannot inspect workload configuration in this cluster for your current identity. Ask a cluster administrator for read access, then refresh this page."
          />
        </section>
      ) : (
        <section className="shrink-0 overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
          <div className="flex items-center justify-between border-b-subtle px-4 py-3">
            <div>
              <h2 className="text-sm font-semibold text-theme-text-primary">Checks for Kubernetes {data.targetVersion}</h2>
              <p className="mt-0.5 text-xs text-theme-text-tertiary">Results are ordered by required action. Expand a row for evidence and remediation.</p>
            </div>
            <span className="text-right text-xs tabular-nums text-theme-text-tertiary">{upgradeEvaluationSummary(data.checks.length, data.summary, incompleteUpgradeCheckCount(data.checks))}</span>
          </div>
          <CoverageMethodology data={data} />
          <div className="hidden grid-cols-[minmax(230px,0.9fr)_minmax(320px,1.6fr)_160px] gap-4 border-b-subtle bg-theme-elevated px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary md:grid">
            <span>Check</span>
            <span>Result</span>
            <span>Status</span>
          </div>
          <div className="divide-y divide-theme-border">
            {sortedChecks.map((check) => (
              <CheckRow key={check.id} check={check} onNavigateToResource={onNavigateToResource} />
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

export function UpgradeReadinessError({ error, onResetTarget }: { error: unknown; onResetTarget?: () => void }) {
  const missingEndpoint = error instanceof ApiError
    && error.status === 404
    && error.message === 'Unknown error'
  if (missingEndpoint) {
    return (
      <EmptyState
        tone="neutral"
        icon={ShieldAlert}
        headline="Upgrade impact needs a newer Radar"
        body={
          <>
            This cluster&rsquo;s Radar predates Upgrade impact (added in Radar {UPGRADE_IMPACT_MIN_RADAR_VERSION}).
            <br />
            Upgrade the in-cluster Radar to enable it.
          </>
        }
      />
    )
  }

  return (
    <EmptyState
      tone="neutral"
      icon={AlertTriangle}
      headline="Unable to analyze upgrade impact"
      body={error instanceof ApiError ? error.message : 'Failed to analyze upgrade impact.'}
      action={error instanceof ApiError && error.status === 400 && onResetTarget ? (
        <button type="button" onClick={onResetTarget} className="btn-brand px-3 py-1.5 text-xs font-medium">
          Use next Kubernetes version
        </button>
      ) : undefined}
    />
  )
}

export function incompleteUpgradeCheckCount(checks: Pick<UpgradeReadinessCheck, 'status' | 'caveat'>[]) {
  return checks.filter((check) => check.status === 'unknown' || Boolean(check.caveat)).length
}

export function upgradeEvaluationSummary(total: number, summary: UpgradeReadinessResponse['summary'], incomplete = summary.unknown) {
  const applicable = Math.max(0, total - summary.notApplicable)
  const partialEvidenceLabel = incomplete > 0 ? ` · ${incomplete} with partial evidence` : ''
  return `${total} evaluated · ${applicable} applicable${partialEvidenceLabel} · ${summary.notApplicable} not applicable`
}

function CoverageMethodology({ data }: { data: UpgradeReadinessResponse }) {
  const [open, setOpen] = useState(false)
  const unavailable = data.coverage.unavailableKinds ?? []
  const scopedKinds = Object.entries(data.coverage.scopedKinds ?? {})
  return (
    <div className="border-b-subtle bg-theme-base/30">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center justify-between gap-4 px-4 py-2.5 text-left transition-colors hover:bg-theme-hover/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-theme-text-primary/20"
      >
        <span className="inline-flex min-w-0 items-center gap-2 text-xs font-medium text-theme-text-secondary">
          <FileSearch className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
          Coverage and methodology
        </span>
        <span className="flex min-w-0 items-center gap-2 text-xs text-theme-text-tertiary">
          <span className="hidden truncate sm:inline">Reviewed through Kubernetes {data.reviewedThrough}</span>
          <CollapseChevron open={open} className="h-3.5 w-3.5 shrink-0" />
        </span>
      </button>
      <Collapse open={open} mountLazily>
        <div id="upgrade-coverage-methodology" className="space-y-2 border-t border-theme-border px-4 py-3 text-xs leading-5 text-theme-text-secondary">
          <p>
            Radar evaluated the checks relevant to Kubernetes {data.currentVersion} → {data.targetVersion}. Release-specific checks outside this upgrade path are excluded instead of being counted as passed or not applicable.
          </p>
          <p>
            Results use live cluster resources and the row-specific evidence scope shown below. {unavailable.length > 0
              ? `Radar could not inspect ${unavailable.join(', ')}; affected checks are marked incomplete.`
              : scopedKinds.length > 0
                ? `Cached evidence has per-kind namespace ceilings for ${formatScopedKinds(scopedKinds)}.`
              : data.coverage.state === 'complete'
                ? 'Every required evidence source for the displayed checks was readable.'
                : 'Coverage is partial; affected rows explain which evidence was missing, unreadable, or outside the scan scope.'}
          </p>
          <a
            href={UPGRADE_IMPACT_DOCS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-accent-text hover:underline"
          >
            View the complete check catalog and evidence model <ExternalLink className="h-3 w-3" />
          </a>
        </div>
      </Collapse>
    </div>
  )
}

function UpgradeSummary({ verdict, target, reviewedThrough, summary, coverageState }: {
  verdict: UpgradeReadinessVerdict
  target: string
  reviewedThrough: string
  summary: { blocked: number; warnings: number; reviews: number; passed: number; unknown: number; notApplicable: number }
  coverageState: 'complete' | 'partial'
}) {
  const meta = summaryMeta(verdict, target, reviewedThrough, summary, coverageState)
  const Icon = meta.icon
  return (
    <div className={clsx('flex flex-col gap-3 rounded-xl border px-4 py-3 sm:flex-row sm:items-center sm:justify-between', meta.className)}>
      <div className="flex min-w-0 items-center gap-3">
        <div className="rounded-full bg-theme-surface p-2 shadow-theme-sm"><Icon className={clsx('h-5 w-5', meta.iconClass)} /></div>
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-theme-text-primary">{meta.headline}</h2>
          <p className="mt-0.5 text-xs text-theme-text-secondary">{meta.body}</p>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 pl-11 text-xs tabular-nums text-theme-text-secondary sm:pl-0">
        {summary.blocked > 0 && <SummaryCount tone="error" value={summary.blocked} label="blocked" />}
        {summary.warnings > 0 && <SummaryCount tone="warning" value={summary.warnings} label="warning" />}
        {summary.reviews > 0 && <SummaryCount tone="info" value={summary.reviews} label="review" />}
        {summary.passed > 0 && <SummaryCount tone="success" value={summary.passed} label="passed" />}
        {summary.unknown > 0 && <SummaryCount tone="neutral" value={summary.unknown} label="incomplete" />}
        {summary.notApplicable > 0 && <span>{summary.notApplicable} not applicable</span>}
      </div>
    </div>
  )
}

function SummaryCount({ tone, value, label }: { tone: 'error' | 'warning' | 'info' | 'success' | 'neutral'; value: number; label: string }) {
  const dot = { error: 'bg-red-500', warning: 'bg-amber-500', info: 'bg-blue-500', success: 'bg-emerald-500', neutral: 'bg-theme-text-tertiary' }[tone]
  return <span className="inline-flex items-center gap-1.5"><span className={clsx('h-1.5 w-1.5 rounded-full', dot)} />{value} {label}</span>
}

export function summaryMeta(
  verdict: UpgradeReadinessVerdict,
  target: string,
  reviewedThrough: string,
  summary: { blocked: number; warnings: number; reviews: number; unknown: number },
  coverageState: 'complete' | 'partial',
) {
  if (verdict === 'blocked') return {
    headline: `${summary.blocked} upgrade ${summary.blocked === 1 ? 'blocker' : 'blockers'} found`,
    body: `Resolve blocked checks before moving the control plane to Kubernetes ${target}.${coverageState === 'partial' ? ' Evidence coverage is also incomplete.' : ''}`,
    icon: AlertCircle, iconClass: 'text-red-600 dark:text-red-400', className: 'border-red-500/30 bg-red-500/5',
  }
  if (verdict === 'warning') return {
    headline: `${summary.warnings} upgrade ${summary.warnings === 1 ? 'warning needs' : 'warnings need'} attention`,
    body: `Resolve or explicitly accept these risks before scheduling Kubernetes ${target}.${coverageState === 'partial' ? ' Evidence coverage is also incomplete.' : ''}`,
    icon: AlertTriangle, iconClass: 'text-amber-600 dark:text-amber-400', className: 'border-amber-500/30 bg-amber-500/5',
  }
  if (verdict === 'review') return {
    headline: `${summary.reviews} upgrade ${summary.reviews === 1 ? 'item needs' : 'items need'} review`,
    body: `Verify the affected configuration before scheduling Kubernetes ${target}.${coverageState === 'partial' ? ' Evidence coverage is also incomplete.' : ''}`,
    icon: FileSearch, iconClass: 'text-blue-600 dark:text-blue-400', className: 'border-blue-500/30 bg-blue-500/5',
  }
  if (verdict === 'unknown') return {
    headline: 'Upgrade evidence is incomplete',
    body: summary.unknown > 0
      ? `${summary.unknown} ${summary.unknown === 1 ? 'check is' : 'checks are'} incomplete, so this is not a readiness guarantee.`
      : coverageState !== 'complete'
        ? 'No blocker was found in the evidence Radar could inspect; this is not a cluster-wide readiness guarantee.'
        : `Checks are reviewed through Kubernetes ${reviewedThrough}, not ${target}.`,
    icon: CircleDashed, iconClass: 'text-theme-text-tertiary', className: 'border-theme-border bg-theme-elevated',
  }
  return {
    headline: `No blockers found for Kubernetes ${target}`,
    body: 'Every applicable reviewed check passed. This result covers the evidence sources listed below.',
    icon: CheckCircle2, iconClass: 'text-emerald-600 dark:text-emerald-400', className: 'border-emerald-500/30 bg-emerald-500/5',
  }
}

function CoverageNotice({ headline, body }: { headline: string; body: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-xs">
      <FileSearch className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div><span className="font-medium text-theme-text-secondary">{headline}.</span> <span className="text-theme-text-tertiary">{body}</span></div>
    </div>
  )
}

function CheckRow({ check, onNavigateToResource }: { check: UpgradeReadinessCheck; onNavigateToResource: (resource: SelectedResource) => void }) {
  const meta = statusMeta[check.status]
  const Icon = meta.icon
  const [open, setOpen] = useState(false)
  const detailID = `upgrade-check-${check.id}`
  const findingGroups = groupFindings(check.findings)
  const label = evidenceLabel(check)
  const checkReferences = check.references ?? []
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="grid w-full cursor-pointer items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-theme-hover/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-theme-text-primary/20 md:grid-cols-[minmax(230px,0.9fr)_minmax(320px,1.6fr)_160px] md:gap-4"
      >
        <div className="flex min-w-0 items-center gap-2.5">
          <Icon className={clsx('h-4 w-4 shrink-0', meta.iconClass)} />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-theme-text-primary">{check.title}</div>
            <div className="text-[11px] text-theme-text-tertiary">{check.category}</div>
          </div>
        </div>
        <div className="min-w-0 pl-6 md:pl-0">
          <div className="text-xs leading-5 text-theme-text-secondary">{check.summary}</div>
        </div>
        <div className="flex min-w-0 items-center justify-between gap-2 pl-6 md:pl-0">
          <div className="min-w-0">
            <Badge severity={meta.badgeSeverity}>{meta.label}</Badge>
            {label && <div className="mt-1 text-[11px] text-theme-text-tertiary">{label}</div>}
            {check.caveat && (
              <div className="mt-1 inline-flex items-center gap-1 text-[11px] font-medium text-amber-700 dark:text-amber-300">
                <AlertTriangle className="h-3 w-3 shrink-0" />
                Partial evidence
              </div>
            )}
          </div>
          <CollapseChevron open={open} className="h-4 w-4" />
        </div>
      </button>
      <Collapse open={open} mountLazily>
        <div id={detailID} className="border-t border-theme-border bg-theme-base/40 px-4 py-3">
          {check.findings.length > 0 && (
            <div>
              <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-[11px] text-theme-text-tertiary">
                <span className="font-medium uppercase tracking-wide">Issue types</span>
                <span>{findingGroups.length} {findingGroups.length === 1 ? 'type' : 'types'} · {check.findings.length} {check.findings.length === 1 ? 'finding' : 'findings'}</span>
              </div>
              <div className="divide-y divide-theme-border overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
                {findingGroups.map((group, index) => (
                  <FindingGroupRow
                    key={group.key}
                    id={`${detailID}-issue-${index}`}
                    group={group}
                    checkReferences={checkReferences}
                    onNavigateToResource={onNavigateToResource}
                  />
                ))}
              </div>
            </div>
          )}
          <div className={clsx('flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-theme-text-tertiary', check.findings.length > 0 && 'mt-3 border-t-subtle pt-3')}>
            {check.caveat && <span className="inline-flex items-start gap-1.5 text-amber-700 dark:text-amber-300"><AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />Coverage note: {check.caveat}</span>}
            {check.evidenceNote && <span className="inline-flex items-start gap-1.5"><FileSearch className="mt-0.5 h-3.5 w-3.5 shrink-0" />Evidence scope: {check.evidenceNote}</span>}
            <span className="inline-flex items-center gap-1.5"><FileSearch className="h-3.5 w-3.5" />Check scope: {check.scope}</span>
            {checkReferences.map((reference) => <ReferenceLink key={reference.url} reference={reference} />)}
          </div>
        </div>
      </Collapse>
    </div>
  )
}

export function groupFindings(findings: UpgradeReadinessFinding[]) {
  const groups = new Map<string, UpgradeReadinessFinding[]>()
  for (const finding of findings) {
    const key = findingGroupKey(finding)
    const grouped = groups.get(key)
    if (grouped) grouped.push(finding)
    else groups.set(key, [finding])
  }
  return [...groups.entries()].map(([key, grouped]) => ({ key, findings: grouped, total: grouped.length }))
}

function FindingGroupRow({ id, group, checkReferences, onNavigateToResource }: {
  id: string
  group: ReturnType<typeof groupFindings>[number]
  checkReferences: NonNullable<UpgradeReadinessCheck['references']>
  onNavigateToResource: (resource: SelectedResource) => void
}) {
  const [open, setOpen] = useState(false)
  const [showAll, setShowAll] = useState(false)
  const finding = group.findings[0]
  const references = issueSpecificReferences(checkReferences, finding.references)
  const level = findingLevelMeta(finding.level)
  const preview = group.findings.slice(0, FINDING_CAP)
  const remaining = group.findings.slice(FINDING_CAP)
  return (
    <article>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left transition-colors hover:bg-theme-hover/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-theme-text-primary/20"
      >
        <span className="flex min-w-0 flex-wrap items-center gap-2">
          <Badge severity={level.severity}>{level.label}</Badge>
          <span className="truncate text-xs font-medium text-theme-text-primary">{finding.title}</span>
        </span>
        <span className="flex shrink-0 items-center gap-2 text-[11px] text-theme-text-tertiary">
          {group.total} {group.total === 1 ? 'finding' : 'findings'}
          <CollapseChevron open={open} className="h-3.5 w-3.5" />
        </span>
      </button>
      <Collapse open={open} mountLazily>
        <div id={id} className="border-t border-theme-border bg-theme-base/30 px-3 py-3">
          <div className="grid gap-3 lg:grid-cols-2">
            <FindingDetail icon={AlertTriangle} label="Impact">{finding.impact}</FindingDetail>
            <FindingDetail icon={Wrench} label="Remediation">{finding.remediation}</FindingDetail>
          </div>
          {(finding.appliesFrom || references.length > 0) && (
            <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 border-t-subtle pt-2 text-[11px] text-theme-text-tertiary">
              {finding.appliesFrom && <span>Applies from Kubernetes {finding.appliesFrom}</span>}
              {references.map((reference) => <ReferenceLink key={reference.url} reference={reference} />)}
            </div>
          )}
          <div className="mt-3 divide-y divide-theme-border/70 border-t-subtle">
            {preview.map((item, index) => (
              <CompactFindingRow key={findingKey(item, index)} finding={item} onNavigateToResource={onNavigateToResource} />
            ))}
            {remaining.length > 0 && (
              <Collapse open={showAll} mountLazily>
                <div id={`${id}-remaining`} className="divide-y divide-theme-border/70">
                  {remaining.map((item, index) => (
                    <CompactFindingRow key={findingKey(item, index + FINDING_CAP)} finding={item} onNavigateToResource={onNavigateToResource} />
                  ))}
                </div>
              </Collapse>
            )}
          </div>
          {remaining.length > 0 && (
            <button
              type="button"
              aria-expanded={showAll}
              onClick={() => setShowAll((value) => !value)}
              className="mt-2 inline-flex w-fit items-center gap-1 rounded px-1 py-1 text-xs font-medium text-accent-text hover:underline"
            >
              {showAll ? 'Show fewer findings' : `Show all ${group.total} findings`}
            </button>
          )}
        </div>
      </Collapse>
    </article>
  )
}

export function issueSpecificReferences(
  checkReferences: NonNullable<UpgradeReadinessCheck['references']>,
  findingReferences: UpgradeReadinessFinding['references'],
) {
  const checkReferenceURLs = new Set(checkReferences.map((reference) => reference.url))
  return findingReferences.filter((reference) => !checkReferenceURLs.has(reference.url))
}

function CompactFindingRow({ finding, onNavigateToResource }: {
  finding: UpgradeReadinessFinding
  onNavigateToResource: (resource: SelectedResource) => void
}) {
  const resourceLabel = finding.resource && `${finding.resource.namespace ? `${finding.resource.namespace}/` : ''}${finding.resource.name}`
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-[11px] text-theme-text-tertiary">
      {finding.resource && (
        <button
          type="button"
          onClick={() => onNavigateToResource({ ...finding.resource!, group: finding.resource!.group ?? '' })}
          className="flex min-w-0 items-center gap-1.5 rounded px-1 py-0.5 text-left transition-colors hover:bg-theme-hover"
        >
          <Badge kind={finding.resource.kind} size="sm">{finding.resource.kind}</Badge>
          <span className="max-w-[280px] truncate text-xs font-medium text-accent-text">{resourceLabel}</span>
        </button>
      )}
      <span className="inline-flex min-w-0 items-center gap-1"><FileSearch className="h-3 w-3 shrink-0" /><code className="truncate">{finding.evidence.path}</code>{finding.evidence.detail ? ` · ${finding.evidence.detail}` : ''}</span>
      {finding.managedBy && <span>Managed by {finding.managedBy.kind} {finding.managedBy.namespace ? `${finding.managedBy.namespace}/` : ''}{finding.managedBy.name}</span>}
    </div>
  )
}

function formatNamespaceScope(namespaces: string[]) {
  if (namespaces.length <= 3) return namespaces.join(', ')
  return `${namespaces.slice(0, 3).join(', ')} and ${namespaces.length - 3} more`
}

function formatScopedKinds(entries: [string, string[]][]) {
  const labels = entries
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([kind, namespaces]) => `${kind} (${formatNamespaceScope(namespaces)})`)
  if (labels.length <= 3) return labels.join(', ')
  return `${labels.slice(0, 3).join(', ')} and ${labels.length - 3} more`
}

function evidenceLabel(check: UpgradeReadinessCheck) {
  if (check.findings.length > 0) return `${check.findings.length} ${check.findings.length === 1 ? 'finding' : 'findings'}`
  if (check.inspected !== undefined && check.inspected > 0) return `${check.inspected} inspected`
  return undefined
}

function FindingDetail({ icon: Icon, label, children }: { icon: typeof AlertTriangle; label: string; children: React.ReactNode }) {
  return (
    <div className="flex gap-2">
      <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
      <div><div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">{label}</div><p className="mt-0.5 text-xs leading-5 text-theme-text-secondary">{children}</p></div>
    </div>
  )
}

function ReferenceLink({ reference }: { reference: { title: string; url: string } }) {
  return <a href={reference.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent-text hover:underline"><BookOpen className="h-3 w-3" />{reference.title}<ExternalLink className="h-2.5 w-2.5" /></a>
}

function findingKey(finding: UpgradeReadinessFinding, index: number) {
  const resource = finding.resource
  return `${finding.ruleID}:${resource?.group ?? ''}:${resource?.kind ?? ''}:${resource?.namespace ?? ''}:${resource?.name ?? ''}:${finding.evidence.path}:${index}`
}

function findingGroupKey(finding: UpgradeReadinessFinding) {
  return JSON.stringify([
    finding.ruleID,
    finding.title,
    finding.level,
    finding.impact,
    finding.remediation,
    finding.appliesFrom,
    finding.references,
  ])
}

function findingLevelMeta(level: UpgradeReadinessFinding['level']) {
  switch (level) {
    case 'blocker':
      return { label: 'Blocker', severity: 'error' as const }
    case 'warning':
      return { label: 'Warning', severity: 'warning' as const }
    default:
      return { label: 'Review', severity: 'info' as const }
  }
}

function buildTargetOptions(current?: string, reviewed?: string, selected?: string) {
  const values = new Set<string>()
  const currentParts = parseMinor(current)
  const reviewedParts = parseMinor(reviewed)
  if (currentParts && reviewedParts && currentParts.major === reviewedParts.major) {
    for (let minor = currentParts.minor + 1; minor <= reviewedParts.minor && minor <= currentParts.minor + 20; minor++) {
      values.add(`${currentParts.major}.${minor}`)
    }
  }
  if (selected) values.add(selected)
  return [...values].sort(compareMinor).map((value) => ({ value, label: value }))
}

function parseMinor(value?: string) {
  const match = value?.match(/^v?(\d+)\.(\d+)/)
  return match ? { major: Number(match[1]), minor: Number(match[2]) } : undefined
}

function compareMinor(a: string, b: string) {
  const av = parseMinor(a)
  const bv = parseMinor(b)
  if (!av || !bv) return a.localeCompare(b)
  return av.major === bv.major ? av.minor - bv.minor : av.major - bv.major
}
