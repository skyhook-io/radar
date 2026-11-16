import { clsx } from 'clsx'
import { WithTooltip } from '../ui/Tooltip'
import { CertaintyGlyph, gapReasonText, VULN_SEVERITY_BADGE, VULN_SEVERITY_BAR, VULN_SEVERITY_TEXT, type Certainty } from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
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
    return <span className="text-xs text-theme-text-tertiary">No CVEs reported</span>
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
        {to ? <span className={SEVERITY_TEXT.success}>{to}</span> : <span className="text-theme-text-tertiary">no fix yet</span>}
      </span>
    </WithTooltip>
  )
}

// Keys are the scanner's package types and, for reports without them, the
// ecosystem Radar reads from the package URL (deb, pypi, golang, …).
const ECOSYSTEMS: Record<string, string> = {
  deb: 'OS package (deb)', apk: 'OS package (apk)', rpm: 'OS package (rpm)',
  pypi: 'Python package', gem: 'Ruby gem', cocoapods: 'CocoaPods pod', maven: 'Java library', golang: 'Go module',
  cargo: 'Rust crate', pub: 'Dart package', hex: 'Elixir package', swift: 'Swift package', rustbinary: 'Rust crate, compiled in',
  debian: 'Debian package', ubuntu: 'Ubuntu package', alpine: 'Alpine package', wolfi: 'Wolfi package', chainguard: 'Chainguard package',
  redhat: 'RPM package', centos: 'RPM package', rocky: 'RPM package', alma: 'RPM package', amazon: 'RPM package', oracle: 'RPM package',
  fedora: 'RPM package', suse: 'RPM package', 'cbl-mariner': 'RPM package', azurelinux: 'RPM package', photon: 'RPM package',
  gobinary: 'Go module, compiled in', gomod: 'Go module', npm: 'npm package', 'node-pkg': 'npm package', yarn: 'npm package', pnpm: 'npm package', bun: 'npm package',
  'python-pkg': 'Python package', pip: 'Python package', pipenv: 'Python package', poetry: 'Python package', uv: 'Python package',
  jar: 'Java library', pom: 'Java library', gradle: 'Java library', sbt: 'Java library',
  gemspec: 'Ruby gem', bundler: 'Ruby gem', 'rust-binary': 'Rust crate, compiled in',
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

type KindName = { kind: string; group?: string }

const REPORT_KIND = 'VulnerabilityReport'

// What a gap left unread: the reports when it names no kind.
function gapKinds(gap: VulnerabilityGap): KindName[] {
  return gap.kinds?.names?.length ? gap.kinds.names : [{ kind: REPORT_KIND }]
}

const kindWords = (kinds: KindName[]) => kinds.map((k) => (k.kind === REPORT_KIND ? 'reports' : `${k.kind}s`)).join(', ')

// Codes for a kind Radar doesn't hold, rather than one the caller may not read.
const COLLECTION_CODES = new Set(['radar_kind_not_collected', 'radar_namespace_not_collected', 'kind_sync_pending', 'kind_sync_timed_out'])

/**
 * A gap that leaves the finding counts exact: where Radar doesn't hold
 * ReplicaSets, a Deployment's containers are named, and counted, by their
 * ReplicaSets, but their findings are read. A ReplicaSet the caller may not
 * read is withheld.
 */
function namesOnly(gap: VulnerabilityGap): boolean {
  return COLLECTION_CODES.has(gap.code) && gapKinds(gap).every((k) => k.kind === 'ReplicaSet')
}

/** Why a build of a moved tag stays unscanned, and the ways out: for a tooltip. */
export const OTHER_BUILD_TIP = "These Pods pulled the tag before it moved. Trivy scans only the build the tag points to now, so this one won't be scanned. Pin the image by digest to keep scans matched, or redeploy so the Pods pull the current build."

/** Whether a Deployment may count once per ReplicaSet in the workload totals. */
export function workloadsByReplicaSet(coverage: VulnerabilitiesResponse['coverage']): boolean {
  return coverage.gaps.some(namesOnly)
}

/** Whether what Radar read covers the scope: unlisted rows don't count. */
export function readsComplete(coverage: VulnerabilitiesResponse['coverage']): boolean {
  return !coverage.truncated && coverage.gaps.every(namesOnly)
}

function gapWhere(gap: VulnerabilityGap): string {
  const ns = gap.namespaces
  if (!ns) return ''
  // A kind not collected or not loaded at all has no namespaces to name.
  if (ns.extent === 'unknown') return gap.code === 'radar_namespace_not_collected' ? ' in some namespaces' : ''
  const names = ns.names ?? []
  const count = ns.count ?? names.length
  if (count === 0) return ''
  const list = names.length > 0 ? ` (${names.slice(0, 5).join(', ')}${count > 5 ? ', …' : ''})` : ''
  return ` in ${count} ${count === 1 ? 'namespace' : 'namespaces'}${list}`
}

function gapPart(gap: VulnerabilityGap): string {
  if (gap.code === 'user_no_namespace_access') return "You don't have access to any namespace."
  const kinds = gapKinds(gap)
  if (kinds.some((k) => k.kind === 'Namespace')) {
    return "Radar can't list namespaces, so it checked only those that hold reports."
  }
  const words = kindWords(kinds)
  const where = gapWhere(gap)
  let part: string
  switch (gap.code) {
    case 'user_forbidden':
      part = `Your access doesn't include ${kinds[0].kind === REPORT_KIND ? 'the reports' : `some ${words}`}${where}`
      break
    case 'user_permission_check_failed':
      part = `Radar couldn't check your access to ${words}${where}; refresh to check again`
      break
    case 'radar_kind_not_collected':
    case 'radar_namespace_not_collected':
      part = `Radar doesn't collect ${words}${where}`
      break
    case 'kind_sync_pending':
      part = `${capitalize(words)}${where} are still loading`
      break
    case 'kind_sync_timed_out':
      part = `${capitalize(words)}${where} didn't finish loading`
      break
    default:
      part = `Radar couldn't read ${words}${where} (${gapReasonText(gap.code)})`
  }
  const kind = kinds[0].kind
  if (kind === REPORT_KIND) return `${part}.`
  const collection = COLLECTION_CODES.has(gap.code) && kinds.length === 1
  if (collection && kind === 'Pod') return `${part}, so what runs there is unknown and its reports aren't counted.`
  if (namesOnly(gap)) return `${part}, so Deployments there are named and counted by their ReplicaSets.`
  if (collection && kind === 'CronJob') return `${part}, so CronJob scans there aren't counted.`
  // A Job's Pods can't be traced to the CronJob Trivy scanned them through.
  if (collection && kind === 'Job') return `${part}, so scans of Jobs and of the CronJobs they ran there aren't counted.`
  // Objects the caller may not read: their scans are withheld.
  return `${part}; their scans aren't counted.`
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

/** The sentence for coverage gaps, or null when none. */
export function gapSentence(gaps: VulnerabilityGap[], { counts = true } = {}): string | null {
  if (gaps.length === 0) return null
  const parts = [...new Set(gaps.map(gapPart))]
  if (counts && !gaps.every(namesOnly)) parts.push('Counts cover the rest.')
  return parts.join(' ')
}

/**
 * How exactly the finding totals are known. A running image without a
 * report, or reports the caller can't read, make the totals lower bounds;
 * with nothing scanned they are unknown, never zero. Rows the response
 * doesn't list don't: the totals are counted before rows are capped.
 */
export function findingsCertainty(data: Pick<VulnerabilitiesResponse, 'summary' | 'coverage'>): 'exact' | 'lower_bound' | 'unknown' {
  const partial = data.summary.imagesUnscanned > 0 || !readsComplete(data.coverage)
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
