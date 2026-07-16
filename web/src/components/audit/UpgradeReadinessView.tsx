import { useMemo } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import {
  AlertTriangle,
  BookOpen,
  CheckCircle2,
  FileSearch,
  ShieldAlert,
  Wrench,
} from 'lucide-react'
import {
  Badge,
  CardBody,
  CardSection,
  EmptyState,
  FreshnessControl,
  PageHeader,
  PaneLoader,
  SelectMenu,
  SummaryTile,
} from '@skyhook-io/k8s-ui'
import { ApiError, useUpgradeReadiness, type UpgradeReadinessFinding, type UpgradeReadinessVerdict } from '../../api/client'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { ChecksViewTabs } from './ChecksViewTabs'

interface UpgradeReadinessViewProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

const verdictMeta: Record<UpgradeReadinessVerdict, { label: string; severity: 'error' | 'warning' | 'success' | 'neutral' }> = {
  blocked: { label: 'Blocked', severity: 'error' },
  review: { label: 'Review needed', severity: 'warning' },
  no_known_blockers: { label: 'No matches', severity: 'success' },
  unknown: { label: 'Unknown', severity: 'neutral' },
}

export function UpgradeReadinessView({ namespaces, onNavigateToResource }: UpgradeReadinessViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const requestedTarget = new URLSearchParams(location.search).get('target') ?? undefined
  const { data, isLoading, error, dataUpdatedAt, refetch } = useUpgradeReadiness(namespaces, requestedTarget)
  const { connection } = useConnection()
  const targetOptions = useMemo(
    () => buildTargetOptions(data?.currentVersion, data?.reviewedThrough, data?.targetVersion),
    [data?.currentVersion, data?.reviewedThrough, data?.targetVersion],
  )
  const resetTarget = () => {
    const params = new URLSearchParams(location.search)
    params.delete('target')
    navigate({ pathname: location.pathname, search: params.toString() }, { replace: true })
  }

  if (isLoading) return <PaneLoader label="Checking upgrade readiness…" className="flex-1" />
  if (error || !data) {
    return (
      <div className="flex-1 flex items-center justify-center p-4">
        <EmptyState
          tone="neutral"
          icon={AlertTriangle}
          headline="Unable to check upgrade readiness"
          body={error instanceof ApiError ? error.message : 'Failed to check upgrade readiness.'}
          action={error instanceof ApiError && error.status === 400 ? (
            <button type="button" onClick={resetTarget} className="btn-brand px-3 py-1.5 text-xs font-medium">
              Use next Kubernetes version
            </button>
          ) : undefined}
        />
      </div>
    )
  }

  const setTarget = (target: string) => {
    const params = new URLSearchParams(location.search)
    params.set('target', target)
    navigate({ pathname: location.pathname, search: params.toString() })
  }
  const blockers = data.findings.filter((finding) => finding.level === 'blocker')
  const warnings = data.findings.filter((finding) => finding.level === 'warning')
  const verdict = verdictMeta[data.verdict]
  const unsupportedTarget = compareMinor(data.targetVersion, data.reviewedThrough) > 0

  return (
    <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <PageHeader
        icon={ShieldAlert}
        title="Upgrade readiness"
        description={`Check live workload configuration before upgrading Kubernetes ${data.currentVersion} → ${data.targetVersion}.`}
        actions={
          <>
            <div className="flex items-center gap-2">
              <span className="text-xs text-theme-text-tertiary">Target</span>
              <SelectMenu
                value={data.targetVersion}
                options={targetOptions}
                onChange={setTarget}
                ariaLabel="Target Kubernetes version"
                className="w-24"
              />
            </div>
            <FreshnessControl
              mode="snapshot"
              dataUpdatedAt={dataUpdatedAt}
              onRefresh={() => refetch()}
              connectionState={connection.state}
            />
          </>
        }
      />

      <ChecksViewTabs />

      <div className="flex flex-wrap items-stretch gap-2">
        <div className="flex min-w-[190px] items-center gap-3 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
          <Badge severity={verdict.severity}>{verdict.label}</Badge>
          <span className="text-sm text-theme-text-secondary">Kubernetes {data.targetVersion}</span>
        </div>
        <SummaryTile label="Blockers" value={data.summary.blockers} tone={data.summary.blockers > 0 ? 'error' : 'neutral'} />
        <SummaryTile label="Review" value={data.summary.warnings} tone={data.summary.warnings > 0 ? 'warning' : 'neutral'} />
        <SummaryTile label="Resources inspected" value={data.summary.scanned} />
        <SummaryTile label="Rules checked" value={data.rulesEvaluated.length} />
      </div>

      {unsupportedTarget && (
        <div className="shrink-0">
          <EmptyState
            tone="neutral"
            variant="inline"
            icon={AlertTriangle}
            headline={`Rules are reviewed through Kubernetes ${data.reviewedThrough}`}
            body={`Known findings are shown, but Radar cannot make a complete claim for ${data.targetVersion}.`}
          />
        </div>
      )}

      {data.coverage.state === 'partial' && (
        <div className="shrink-0">
          <EmptyState
            tone="neutral"
            variant="inline"
            icon={FileSearch}
            headline="Live workload coverage is incomplete"
            body={`Radar could not inspect: ${(data.coverage.unavailableKinds ?? []).join(', ')}.`}
          />
        </div>
      )}

      {data.coverage.state === 'no_access' && (
        <div className="shrink-0">
          <EmptyState
            tone="neutral"
            variant="inline"
            icon={FileSearch}
            headline="No namespace access"
            body="You do not have access to any namespaces in this cluster."
          />
        </div>
      )}

      {blockers.length > 0 && <FindingSection title="Must fix" findings={blockers} onNavigateToResource={onNavigateToResource} />}
      {warnings.length > 0 && <FindingSection title="Review" findings={warnings} onNavigateToResource={onNavigateToResource} />}

      {data.findings.length === 0 && data.verdict === 'no_known_blockers' && (
        <EmptyState
          tone="healthy"
          icon={CheckCircle2}
          headline={`No matches found for Kubernetes ${data.targetVersion}`}
          body={`Radar checked ${data.rulesEvaluated.length} reviewed upgrade ${data.rulesEvaluated.length === 1 ? 'rule' : 'rules'} across ${data.summary.scanned} live workload resources.`}
        />
      )}

      {data.findings.length === 0 && data.verdict === 'unknown' && (
        <EmptyState
          tone="neutral"
          icon={FileSearch}
          headline="Radar cannot make a complete readiness claim"
          body={unknownVerdictBody(data.coverage.state, unsupportedTarget)}
        />
      )}

      <details className="rounded-md border border-theme-border bg-theme-surface px-3 py-2 text-sm">
        <summary className="cursor-pointer font-medium text-theme-text-secondary">Rules evaluated ({data.rulesEvaluated.length})</summary>
        <ul className="mt-2 flex flex-col gap-1 text-xs text-theme-text-tertiary">
          {data.rulesEvaluated.map((rule) => (
            <li key={rule.id}>{rule.title} · applies from Kubernetes {rule.appliesFrom}</li>
          ))}
          {data.rulesEvaluated.length === 0 && <li>No Radar rules apply to this target version.</li>}
        </ul>
      </details>
    </div>
  )
}

function FindingSection({ title, findings, onNavigateToResource }: { title: string; findings: UpgradeReadinessFinding[]; onNavigateToResource: (resource: SelectedResource) => void }) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm font-semibold text-theme-text-primary">{title} <span className="font-normal text-theme-text-tertiary">({findings.length})</span></h2>
      {findings.map((finding) => (
        <FindingCard key={`${finding.ruleID}:${finding.resource.group ?? ''}:${finding.resource.kind}:${finding.resource.namespace}:${finding.resource.name}:${finding.evidence.path}`} finding={finding} onNavigateToResource={onNavigateToResource} />
      ))}
    </section>
  )
}

function FindingCard({ finding, onNavigateToResource }: { finding: UpgradeReadinessFinding; onNavigateToResource: (resource: SelectedResource) => void }) {
  const resourceLabel = `${finding.resource.namespace ? `${finding.resource.namespace}/` : ''}${finding.resource.name}`
  return (
    <article className="rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex items-center gap-2 border-b-subtle px-4 py-3">
        <Badge severity={finding.level === 'blocker' ? 'error' : 'warning'}>{finding.level === 'blocker' ? 'Blocker' : 'Review'}</Badge>
        <h3 className="min-w-0 flex-1 truncate text-sm font-semibold text-theme-text-primary">{finding.title}</h3>
        <button
          type="button"
          onClick={() => onNavigateToResource({ ...finding.resource, group: finding.resource.group ?? '' })}
          className="flex min-w-0 items-center gap-1.5 rounded px-1.5 py-1 text-left transition-colors hover:bg-theme-hover"
        >
          <Badge kind={finding.resource.kind} size="sm">{finding.resource.kind}</Badge>
          <span className="max-w-[320px] truncate text-xs font-medium text-accent-text">{resourceLabel}</span>
        </button>
      </div>
      <div className="grid gap-4 px-4 py-3 lg:grid-cols-2">
        <CardSection icon={AlertTriangle} label="What breaks" tone="warn">
          <CardBody>{finding.impact}</CardBody>
        </CardSection>
        <CardSection icon={Wrench} label="How to fix" tone="fix">
          <CardBody>{finding.remediation}</CardBody>
        </CardSection>
        <CardSection icon={FileSearch} label="Evidence">
          <div className="flex flex-col gap-1 text-xs text-theme-text-secondary">
            <code className="w-fit rounded bg-theme-elevated px-1.5 py-1 font-mono">{finding.evidence.path}</code>
            <span>Live object · breaks from Kubernetes {finding.appliesFrom}</span>
            {finding.managedBy && <span>Managed by {finding.managedBy.kind} {finding.managedBy.namespace ? `${finding.managedBy.namespace}/` : ''}{finding.managedBy.name}</span>}
          </div>
        </CardSection>
        <CardSection icon={BookOpen} label="References">
          <div className="flex flex-col items-start gap-1">
            {finding.references.map((reference) => (
              <a key={reference.url} href={reference.url} target="_blank" rel="noreferrer" className="text-xs text-accent-text hover:underline">
                {reference.title} ↗
              </a>
            ))}
          </div>
        </CardSection>
      </div>
    </article>
  )
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

function unknownVerdictBody(coverage: 'complete' | 'partial' | 'no_access', unsupportedTarget: boolean) {
  if (unsupportedTarget) return 'Choose a target covered by the reviewed rules before scheduling the upgrade.'
  if (coverage === 'no_access') return 'Ask for namespace access before scheduling the upgrade.'
  return 'Review the coverage notes above before scheduling the upgrade.'
}
