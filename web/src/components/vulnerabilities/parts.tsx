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

/**
 * An image reference split for display: the last path segment with its tag
 * (what tells images apart) and the registry/repository path before it.
 */
export function imageParts(ref: string): { name: string; path: string } {
  const bare = ref.split('@')[0]
  const slash = bare.lastIndexOf('/')
  return slash < 0 ? { name: bare, path: '' } : { name: bare.slice(slash + 1), path: bare.slice(0, slash) }
}

/** An image's name and tag, with the full reference on hover. */
export function ImageName({ image, className }: { image: string; className?: string }) {
  const { name } = imageParts(image)
  return (
    <WithTooltip tip={image}>
      <span className={clsx('truncate font-mono', className)}>{name}</span>
    </WithTooltip>
  )
}

export function SeverityCounts({ counts }: { counts: VulnerabilityCounts }) {
  if (totalFindings(counts) === 0) {
    return <span className="text-xs text-theme-text-tertiary">No known CVEs</span>
  }
  // Zeros are left out: "H 1" reads faster than "C 0 H 1 M 0 L 0".
  return (
    <span className="flex flex-wrap gap-x-2.5 font-mono text-xs tabular-nums">
      {SEVERITIES.filter(({ key }) => counts[key] > 0).map(({ key, letter, label }) => (
        <WithTooltip key={key} tip={`${counts[key]} ${label}`}>
          <span className={VULN_SEVERITY_TEXT[upper(key)]}>{letter}&thinsp;{counts[key]}</span>
        </WithTooltip>
      ))}
      {counts.unknown > 0 && (
        <WithTooltip tip={`${counts.unknown} not yet rated by the scanner`}>
          <span className="text-theme-text-tertiary">? {counts.unknown}</span>
        </WithTooltip>
      )}
    </span>
  )
}

export function SeverityBadge({ severity }: { severity: VulnerabilitySeverity }) {
  const label = SEVERITIES.find((s) => s.key === severity)?.label ?? 'Unknown'
  return <span className={clsx('badge-sm w-fit justify-self-start border', VULN_SEVERITY_BADGE[upper(severity)])}>{label}</span>
}

/**
 * An installed version and the one to move to. The scanner lists one fix
 * per maintained branch ("v0.38.0, v0.36.1"); the target is the smallest
 * above the installed version, with the full list on hover. Without a
 * target the list stands as the scanner wrote it.
 */
export function FixVersion({ installed, fixed, target, showInstalled = true, className }: { installed: string; fixed?: string; target?: string; showInstalled?: boolean; className?: string }) {
  const to = target || fixed
  const tip = to ? `${installed} → ${to}${target && fixed && fixed !== target ? ` (fixed releases: ${fixed})` : ''}` : undefined
  return (
    <WithTooltip tip={tip}>
      <span className={clsx('min-w-0 truncate font-mono', className)}>
        {showInstalled && <span className="text-theme-text-tertiary">{installed} → </span>}
        {to ? <span className="text-emerald-700 dark:text-emerald-400">{to}</span> : <span className="text-theme-text-tertiary">no fix yet</span>}
      </span>
    </WithTooltip>
  )
}

const ECOSYSTEMS: Record<string, string> = {
  debian: 'Debian package', ubuntu: 'Ubuntu package', alpine: 'Alpine package', wolfi: 'Wolfi package', chainguard: 'Chainguard package',
  redhat: 'RPM package', centos: 'RPM package', rocky: 'RPM package', alma: 'RPM package', amazon: 'RPM package', oracle: 'RPM package',
  fedora: 'RPM package', suse: 'RPM package', 'cbl-mariner': 'RPM package', azurelinux: 'RPM package', photon: 'RPM package',
  gobinary: 'Go module, compiled in', gomod: 'Go module', npm: 'npm package', 'node-pkg': 'npm package', yarn: 'npm package', pnpm: 'npm package', bun: 'npm package',
  'python-pkg': 'Python package', pip: 'Python package', pipenv: 'Python package', poetry: 'Python package', uv: 'Python package',
  jar: 'Java library', pom: 'Java library', gradle: 'Java library', sbt: 'Java library',
  gemspec: 'Ruby gem', bundler: 'Ruby gem', cargo: 'Rust crate', 'rust-binary': 'Rust crate, compiled in',
  composer: 'PHP package', nuget: '.NET package', 'dotnet-core': '.NET package',
}

/** The scanner's package type in words ("gobinary" → "Go module, compiled in"). */
export function ecosystemLabel(packageType?: string): string | undefined {
  if (!packageType) return undefined
  return ECOSYSTEMS[packageType.toLowerCase()] ?? packageType
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
export function findingsCertainty(data: Pick<VulnerabilitiesResponse, 'summary' | 'coverage'>): 'exact' | 'lower_bound' | 'unknown' {
  const partial = data.summary.imagesUnscanned > 0 || !data.coverage.complete
  if (data.summary.imagesScanned === 0 && partial) return 'unknown'
  return partial ? 'lower_bound' : 'exact'
}

/** A finding total under its certainty: "—" when unknown, "≥ n" when a bound. */
export function FindingTotal({ value, certainty, className }: { value: number; certainty: Certainty; className?: string }) {
  if (certainty === 'unknown') {
    return <span className={clsx('font-mono text-theme-text-tertiary', className)}>—</span>
  }
  return (
    <span className={clsx('inline-flex items-baseline gap-1.5', className)}>
      {certainty === 'lower_bound' && <CertaintyGlyph certainty="lower_bound" title="At least: some running images have no report, or some reports aren't readable" />}
      <span className="font-mono">{value}</span>
    </span>
  )
}
