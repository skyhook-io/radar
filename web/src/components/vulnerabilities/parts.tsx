import { clsx } from 'clsx'
import { WithTooltip } from '../ui/Tooltip'
import { CertaintyGlyph, VULN_SEVERITY_BADGE, VULN_SEVERITY_BAR, VULN_SEVERITY_TEXT, type Certainty } from '@skyhook-io/k8s-ui'
import type {
  VulnerabilitiesResponse,
  VulnerabilityCounts,
  VulnerabilityGap,
  VulnerabilitySeverity,
  VulnerabilityWorkloadRef,
} from '../../api/vulnerabilities'

export const SEVERITIES: { key: VulnerabilitySeverity; label: string; letter: string }[] = [
  { key: 'critical', label: 'Critical', letter: 'C' },
  { key: 'high', label: 'High', letter: 'H' },
  { key: 'medium', label: 'Medium', letter: 'M' },
  { key: 'low', label: 'Low', letter: 'L' },
]

const upper = (s: VulnerabilitySeverity) => s.toUpperCase()

export function totalFindings(c: VulnerabilityCounts): number {
  return c.critical + c.high + c.medium + c.low + c.unknown
}

/** Stacked bar of findings per severity; letters keep it readable without color. */
export function SeverityBar({ counts, className }: { counts: VulnerabilityCounts; className?: string }) {
  const total = totalFindings(counts)
  return (
    <div className={clsx('flex min-w-0 flex-col gap-1', className)}>
      <div className="flex h-1.5 overflow-hidden rounded-full bg-theme-elevated" aria-hidden>
        {total > 0 && [...SEVERITIES, { key: 'unknown' as const }].map(({ key }) => (
          counts[key] > 0 ? <span key={key} className={VULN_SEVERITY_BAR[upper(key)]} style={{ width: `${(100 * counts[key]) / total}%` }} /> : null
        ))}
      </div>
      <SeverityCounts counts={counts} />
    </div>
  )
}

export function SeverityCounts({ counts }: { counts: VulnerabilityCounts }) {
  if (totalFindings(counts) === 0) {
    return <span className="text-xs text-theme-text-tertiary">No known CVEs</span>
  }
  return (
    <span className="flex flex-wrap gap-x-3 font-mono text-xs">
      {SEVERITIES.map(({ key, letter, label }) => (
        <WithTooltip key={key} tip={`${counts[key]} ${label}`}>
          <span className={counts[key] > 0 ? VULN_SEVERITY_TEXT[upper(key)] : 'text-theme-text-disabled'}>
            {letter} {counts[key]}
          </span>
        </WithTooltip>
      ))}
    </span>
  )
}

export function SeverityBadge({ severity }: { severity: VulnerabilitySeverity }) {
  const label = SEVERITIES.find((s) => s.key === severity)?.label ?? 'Unknown'
  return <span className={clsx('badge-sm w-fit justify-self-start border', VULN_SEVERITY_BADGE[upper(severity)])}>{label}</span>
}

export function workloadLabel(w: VulnerabilityWorkloadRef): string {
  return `${w.namespace}/${w.name}`
}

/** "checkout, worker +4" for a list of workloads, each named once. */
export function workloadSummary(workloads: VulnerabilityWorkloadRef[], shown = 2): string {
  const names = [...new Set(workloads.map((w) => w.name))]
  const head = names.slice(0, shown).join(', ')
  return names.length > shown ? `${head} +${names.length - shown}` : head
}

export function distinctNamespaces(workloads: VulnerabilityWorkloadRef[]): number {
  return new Set(workloads.map((w) => w.namespace)).size
}

/** The sentence for coverage gaps, or null when none. */
export function gapSentence(gaps: VulnerabilityGap[]): string | null {
  const parts: string[] = []
  for (const gap of gaps) {
    const names = gap.namespaces?.names ?? []
    const count = gap.namespaces?.count ?? names.length
    const list = names.length > 0 ? ` (${names.slice(0, 5).join(', ')}${count > 5 ? ', …' : ''})` : ''
    const ns = count === 1 ? 'namespace' : 'namespaces'
    switch (gap.code) {
      case 'user_forbidden':
        parts.push(`Your access doesn't include the reports in ${count} ${ns}${list}.`)
        break
      case 'user_permission_check_failed':
        parts.push(count > 0
          ? `Radar couldn't check your access to the reports in ${count} ${ns}${list}; refresh to check again.`
          : "Radar couldn't check your access to reports; refresh to check again.")
        break
      case 'user_no_namespace_access':
        parts.push("You don't have access to any namespace.")
        break
      default:
        parts.push(`Some reports weren't read (${gap.code}).`)
    }
  }
  return parts.length > 0 ? `${parts.join(' ')} Counts cover the rest.` : null
}

/**
 * How exactly the finding totals are known. A running image without a
 * report, or reports the caller can't read, make the totals lower bounds;
 * with nothing scanned they are unknown, never zero.
 */
export function findingsCertainty(data: Pick<VulnerabilitiesResponse, 'summary' | 'coverage'>): Certainty {
  const partial = data.summary.imagesUnscanned > 0 || !data.coverage.complete
  if (data.summary.imagesScanned === 0 && partial) return 'unknown'
  return partial ? 'lower_bound' : 'exact'
}

/** A finding total under its certainty: "—" when unknown, "≥ n" when a bound. */
// plain renders the bound as text, for use inside a button where a
// focusable glyph would nest one control in another.
export function FindingTotal({ value, certainty, className, plain }: { value: number; certainty: Certainty; className?: string; plain?: boolean }) {
  if (certainty === 'unknown') {
    return <span className={clsx('font-mono text-theme-text-tertiary', className)}>—</span>
  }
  return (
    <span className={clsx('inline-flex items-baseline gap-1.5', className)}>
      {certainty === 'lower_bound' && (plain
        ? <span className="text-theme-text-tertiary">≥</span>
        : <CertaintyGlyph certainty="lower_bound" title="At least: some running images have no report, or some reports aren't readable" />)}
      <span className="font-mono">{value}</span>
    </span>
  )
}
