import { clsx } from 'clsx'
import { Flame, Globe } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { WithTooltip } from '../ui/Tooltip'
import { Badge, CertaintyGlyph, gapReasonText, VULN_SEVERITY_BADGE, VULN_SEVERITY_BAR, VULN_SEVERITY_TEXT, type Certainty } from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import type {
  ExposedWorkload,
  VulnerabilitiesResponse,
  VulnerabilityCounts,
  VulnerabilityEPSS,
  VulnerabilityFeedStatus,
  VulnerabilityGap,
  VulnerabilityIntel,
  VulnerabilityKEV,
  VulnerabilityRoute,
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

// fromScore: the scanner gave no severity; Radar rated the CVSS score.
export function SeverityBadge({ severity, fromScore, score }: { severity: VulnerabilitySeverity; fromScore?: boolean; score?: number }) {
  const label = SEVERITIES.find((s) => s.key === severity)?.label ?? 'Unknown'
  const badge = <span className={clsx('badge-sm w-fit justify-self-start border', VULN_SEVERITY_BADGE[upper(severity)])}>{label}</span>
  return fromScore ? <WithTooltip tip={`Trivy gave no severity; rated from its CVSS score${score !== undefined ? ` (${score.toFixed(1)})` : ''}`}>{badge}</WithTooltip> : badge
}

/**
 * An installed version and the one to move to. The scanner lists one fix
 * per maintained branch ("v0.38.0, v0.36.1"); the target is the smallest
 * above the installed version, with the full list on hover. When Radar
 * couldn't pick a target, the scanner's list stands as written, without
 * an arrow or the color of a recommendation.
 */
export function FixVersion({ installed, fixed, target, showInstalled = true, className }: { installed: string; fixed?: string; target?: string; showInstalled?: boolean; className?: string }) {
  if (!target && fixed) {
    return (
      <WithTooltip tip={`Installed ${installed}. The scanner lists ${fixed} as fixed, but Radar couldn't confirm it's above the installed version.`}>
        <span className={clsx('min-w-0 truncate font-mono text-theme-text-secondary', className)}>
          {showInstalled && <span className="text-theme-text-tertiary">{installed} · </span>}fixed in {fixed}
        </span>
      </WithTooltip>
    )
  }
  const tip = target ? `${installed} → ${target}${fixed && fixed !== target ? ` (fixed releases: ${fixed})` : ''}` : undefined
  return (
    <WithTooltip tip={tip}>
      {/* The installed side gives way first: the target is what to act on. */}
      <span className={clsx('flex min-w-0 items-baseline font-mono', className)}>
        {showInstalled && <span className="min-w-0 shrink-[100] truncate text-theme-text-tertiary">{installed}</span>}
        {showInstalled && <span className="shrink-0 whitespace-pre text-theme-text-tertiary"> → </span>}
        {target ? <span className={clsx('min-w-0 truncate', SEVERITY_TEXT.success)}>{target}</span> : <span className="min-w-0 truncate text-theme-text-tertiary">no fix yet</span>}
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

type WorkloadKey = { kind: string; namespace: string; name: string }

/** A row's exposed workloads by kind/namespace/name, so any container of one matches. */
export type ExposureLookup = Map<string, ExposedWorkload>

const exposureKey = (w: WorkloadKey) => `${w.kind}/${w.namespace}/${w.name}`

export function exposureLookup(exposed: ExposedWorkload[] | undefined): ExposureLookup {
  return new Map((exposed ?? []).filter((w) => w.routes.length > 0).map((w) => [exposureKey(w), w]))
}

export function routesFor(exposure: ExposureLookup | undefined, w: WorkloadKey): ExposedWorkload | undefined {
  return exposure?.get(exposureKey(w))
}

export function routeText(r: VulnerabilityRoute): string {
  return `${r.via} ${r.namespace}/${r.name}`
}

/** "A, B, C and 197 more": the routes listed, and how many the list leaves out. */
export function routeListText(routes: VulnerabilityRoute[], total = routes.length): string {
  const more = total - routes.length
  return `${routes.map(routeText).join(', ')}${more > 0 ? ` and ${more} more` : ''}`
}

export function routesTip(routes: VulnerabilityRoute[], total = routes.length): string {
  return `Exposed via ${routeListText(routes, total)}`
}

/**
 * The words and mark for exposure, in one place. "Exposed" is the term
 * container security tools use for a workload reachable from outside the
 * cluster (Sysdig: "publicly or ingress exposed"); it doesn't claim the
 * internet, which tools keep for "internet-facing"; the tooltips say it may
 * be internal only. The globe stays as the mark people recognise for exposure.
 */
export const EXPOSURE = {
  icon: Globe as LucideIcon,
  chip: 'Exposed',
  column: 'Exposed via',
  unknown: 'Exposure unknown',
  chipTip: "Only rows affecting a workload reachable from outside the cluster: a LoadBalancer or NodePort Service, or an Ingress or Gateway API route with an address. It may be internal only; firewalls, security groups and NetworkPolicies aren't checked. Not counted yet: hostNetwork and hostPort Pods, service meshes and custom routers.",
  unknownTip: "Radar couldn't read every route kind that could reach this workload, so it may be exposed.",
}

/** The mark on a workload with a route from outside the cluster; absence gets none. */
export function ExposedMark({ exposed, tip }: { exposed?: ExposedWorkload; tip?: string }) {
  const text = tip ?? (exposed ? routesTip(exposed.routes, exposed.routesTotal) : undefined)
  const Icon = EXPOSURE.icon
  return (
    <WithTooltip tip={text}>
      <Icon className="h-3 w-3 shrink-0 self-center text-theme-text-secondary" role="img" aria-label={text} />
    </WithTooltip>
  )
}

/**
 * Up to `shown` distinct workload names for a collapsed row, exposed ones
 * first, and how many more names there are.
 */
export function summaryNames(workloads: VulnerabilityWorkloadRef[], exposure?: ExposureLookup, shown = 2): { names: { name: string; exposed?: ExposedWorkload }[]; rest: number } {
  const byName = new Map<string, ExposedWorkload | undefined>()
  for (const w of workloads) {
    const exposed = routesFor(exposure, w)
    if (!byName.has(w.name) || (exposed && !byName.get(w.name))) byName.set(w.name, exposed)
  }
  const all = [...byName].map(([name, exposed]) => ({ name, exposed }))
  const ordered = [...all.filter((n) => n.exposed), ...all.filter((n) => !n.exposed)]
  return { names: ordered.slice(0, shown), rest: Math.max(0, ordered.length - shown) }
}

/** "checkout, worker +4" for a list of workloads, each named once, exposed ones first with the mark. */
export function WorkloadSummary({ workloads, exposure, suffix }: { workloads: VulnerabilityWorkloadRef[]; exposure?: ExposureLookup; suffix?: string }) {
  const { names, rest } = summaryNames(workloads, exposure)
  return (
    <span className="flex min-w-0 items-baseline text-theme-text-secondary">
      <span className="truncate">
        {names.map((n, i) => (
          <span key={n.name}>
            {i > 0 && ', '}
            {n.exposed && <span className="mr-1 inline-flex align-[-1px]"><ExposedMark exposed={n.exposed} /></span>}
            {n.name}
          </span>
        ))}
        {rest > 0 && ` +${rest}`}
        {suffix}
      </span>
    </span>
  )
}

/**
 * The tooltip for a count of workloads that lists no names: how many are
 * externally exposed, and the first few with their routes.
 */
export function exposedCountTip(exposed: ExposedWorkload[], exposedTotal: number, workloads?: number, listed = 3): string {
  const shown = exposed.slice(0, listed).map((w) => `${w.name} (${routeListText(w.routes, w.routesTotal)})`)
  const more = exposedTotal - shown.length
  // By image the row counts containers, which can't be set against workloads.
  const lead = workloads === undefined
    ? `${exposedTotal} ${exposedTotal === 1 ? 'workload running it is' : 'workloads running it are'}`
    : `${exposedTotal} of ${workloads} ${workloads === 1 ? 'workload is' : 'workloads are'}`
  return `${lead} exposed: ${shown.join(', ')}${more > 0 ? ` and ${more} more` : ''}`
}

/**
 * "3 exposed" with the mark, under a row's workload count ("1 exposed
 * workload" where the row counts containers);
 * "Exposure unknown" when none is known exposed but some may be; nothing
 * otherwise.
 */
export function ExposedCount({ exposed, total, workloads, unknown }: { exposed?: ExposedWorkload[]; total?: number; workloads?: number; unknown?: boolean }) {
  const Icon = EXPOSURE.icon
  if (!total) {
    if (!unknown) return null
    return (
      <WithTooltip tip={EXPOSURE.unknownTip}>
        <span className="whitespace-nowrap text-xs text-theme-text-tertiary">{EXPOSURE.unknown}</span>
      </WithTooltip>
    )
  }
  const tip = exposedCountTip(exposed ?? [], total, workloads)
  // Under a workload count the total is already on screen, so the line
  // says only how many are exposed; the tooltip says "N of M".
  const text = workloads === undefined ? `${total} exposed ${total === 1 ? 'workload' : 'workloads'}` : `${total} exposed`
  return (
    <WithTooltip tip={tip}>
      <span className="inline-flex items-center gap-1 whitespace-nowrap text-xs text-theme-text-secondary" aria-label={tip}>
        <Icon className="h-3 w-3 shrink-0" aria-hidden />
        {text}
      </span>
    </WithTooltip>
  )
}

/** Whether an exposure gap covers this namespace, so its routes are unknown. */
export function exposureUnknownIn(gaps: VulnerabilityGap[] | undefined, namespace: string): boolean {
  return (gaps ?? []).some((g) => {
    const ns = g.namespaces
    if (!ns) return true
    if (ns.extent !== undefined) return true
    // Past the name cap the set only counts: any namespace may be in it.
    return (ns.names ?? []).includes(namespace) || (ns.names ?? []).length < ns.count
  })
}

const pluralKind = (kind: string) => (/(s|x|ch|sh)$/.test(kind) ? `${kind}es` : `${kind}s`)

function listWords(words: string[]): string {
  return words.length <= 1 ? words.join('') : `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
}

/** The sentence for route kinds Radar couldn't read, or null when none. */
export function exposureGapSentence(gaps: VulnerabilityGap[] | undefined): string | null {
  if (!gaps || gaps.length === 0) return null
  const describe = (list: VulnerabilityGap[]) => listWords([...new Set(list.map((g) => {
    const kinds = g.kinds?.names?.length ? listWords(g.kinds.names.map((k) => pluralKind(k.kind))) : 'routes'
    const ns = g.namespaces
    let where = ''
    if (ns?.extent === 'unknown') where = ' in some namespaces'
    else if (ns && ns.extent === undefined) where = ` in ${ns.count} ${ns.count === 1 ? 'namespace' : 'namespaces'}`
    return `${kinds}${where}`
  }))])
  // user_forbidden: route kinds the caller can't read for these workloads' namespaces.
  const hidden = gaps.filter((g) => g.code === 'user_forbidden')
  const unread = gaps.filter((g) => g.code !== 'user_forbidden')
  const sentences: string[] = []
  if (unread.length) sentences.push(`Radar couldn't read ${describe(unread)}, so some workloads may be exposed without the mark.`)
  if (hidden.length) {
    const kinds = listWords([...new Set(hidden.flatMap((g) => (g.kinds?.names ?? []).map((k) => pluralKind(k.kind))))])
    const namespaces = new Set(hidden.flatMap((g) => g.namespaces?.names ?? []))
    const where = namespaces.size ? `workloads in ${namespaces.size} ${namespaces.size === 1 ? 'namespace' : 'namespaces'}` : 'these workloads'
    sentences.push(`Your access doesn't cover all ${kinds || 'routes'} that could reach ${where}, so those without a route you can see read Exposure unknown.`)
  }
  return sentences.join(' ')
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

/**
 * How many CVEs an upgrade clears at one severity, right-aligned for a
 * column of them; a dash for none. Upgrade lists show Critical and High
 * only: those decide which upgrade comes first.
 */
export function ClearedCount({ count, severity }: { count: number; severity: 'critical' | 'high' }) {
  return <span className={clsx('text-right font-mono tabular-nums', count > 0 ? VULN_SEVERITY_TEXT[upper(severity)] : 'text-theme-text-tertiary')}>{count > 0 ? count : '–'}</span>
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

// The installed side of an upgrade: two versions read fine, more push the
// version to move to out of the cell, so they collapse to a count (the
// full list is in the row's tooltip and expanded detail).
export function installedLabel(from: string[]): string {
  return from.length > 2 ? `${from.length} versions` : from.join(', ')
}

const UNCHECKED_NAMES: Record<string, string> = {
  'networking.istio.io/VirtualService': 'Istio VirtualServices',
  'networking.istio.io/Gateway': 'Istio Gateways',
  'traefik.io/IngressRoute': 'Traefik IngressRoutes',
  'traefik.containo.us/IngressRoute': 'Traefik IngressRoutes',
  'traefik.io/IngressRouteTCP': 'Traefik IngressRouteTCPs',
  'traefik.containo.us/IngressRouteTCP': 'Traefik IngressRouteTCPs',
  'traefik.io/IngressRouteUDP': 'Traefik IngressRouteUDPs',
  'traefik.containo.us/IngressRouteUDP': 'Traefik IngressRouteUDPs',
  'projectcontour.io/HTTPProxy': 'Contour HTTPProxies',
  'serving.knative.dev/Service': 'Knative Services',
}

/** The sentence for route kinds this cluster serves that Radar doesn't follow, or null. */
export function uncheckedRoutesSentence(unchecked: { kind: string; group?: string }[] | undefined): string | null {
  if (!unchecked || unchecked.length === 0) return null
  const names = [...new Set(unchecked.map((k) => UNCHECKED_NAMES[`${k.group ?? ''}/${k.kind}`] ?? pluralKind(k.kind)))]
  return `This cluster has ${listWords(names)}, which Radar doesn't follow yet, so workloads they route to aren't marked.`
}

// CISA's Known Exploited Vulnerabilities catalog and FIRST's EPSS, as the
// page and the CSV name them.
export const KEV = {
  icon: Flame as LucideIcon,
  chip: 'Known exploited',
  badge: 'KEV',
  chipTip: "Only CVEs in CISA's Known Exploited Vulnerabilities catalog (KEV): exploited in the wild. US federal agencies must fix these by the catalog's due date (BOD 22-01).",
  noData: 'no data',
  loading: 'Loading KEV data',
}

/** A feed's copy is usable: entries missing from it are not listed. */
export function feedReady(feed?: VulnerabilityFeedStatus): boolean {
  return feed?.state === 'ready'
}

function dateLabel(iso: string): string {
  const d = new Date(`${iso}T00:00:00Z`)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' })
}

export function kevTip(kev: VulnerabilityKEV): string {
  return [
    "In CISA's Known Exploited Vulnerabilities catalog.",
    `Added ${dateLabel(kev.dateAdded)}.`,
    `Federal due date ${dateLabel(kev.dueDate)}.`,
    kev.ransomwareUse === 'known' ? 'Known to be used in ransomware campaigns.' : 'Ransomware use: not known.',
    kev.requiredAction ? `Required action: ${kev.requiredAction}` : '',
  ].filter(Boolean).join(' ')
}

/** The KEV mark on a CVE; absence gets none, and the page says when the catalog is missing. */
export function KevBadge({ kev }: { kev?: VulnerabilityKEV }) {
  if (!kev) return null
  return (
    <WithTooltip tip={kevTip(kev)}>
      <span className="inline-flex shrink-0" tabIndex={0} aria-label={`Known exploited. ${kevTip(kev)}`}>
        <Badge severity="error" size="sm">{KEV.badge}{kev.ransomwareUse === 'known' ? ' · ransomware' : ''}</Badge>
      </span>
    </WithTooltip>
  )
}

/** How many of a row's CVEs KEV lists, as a mark beside its counts. */
export function KevCount({ count, what = 'of these CVEs' }: { count?: number; what?: string }) {
  if (!count) return null
  const tip = `${count} ${what} ${count === 1 ? 'is' : 'are'} in CISA's Known Exploited Vulnerabilities catalog.`
  return (
    <WithTooltip tip={tip}>
      <span className="inline-flex shrink-0" tabIndex={0} aria-label={tip}>
        <Badge severity="error" size="sm">{count} {KEV.badge}</Badge>
      </span>
    </WithTooltip>
  )
}

function ordinal(n: number): string {
  const v = n % 100
  const suffix = v >= 11 && v <= 13 ? 'th' : ['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'
  return `${n}${suffix}`
}

/** EPSS as a percent with its percentile; "?" when the scores couldn't be loaded. */
export function EpssValue({ epss, feed }: { epss?: VulnerabilityEPSS; feed?: VulnerabilityFeedStatus }) {
  if (!feedReady(feed)) {
    return <WithTooltip tip={feedNote('EPSS', feed) ?? undefined}><span className="text-theme-text-tertiary" tabIndex={0}>?</span></WithTooltip>
  }
  if (!epss) return <WithTooltip tip="FIRST scores CVE IDs only, and not this one."><span className="text-theme-text-tertiary" tabIndex={0}>—</span></WithTooltip>
  const pct = epss.score * 100
  const label = pct >= 10 ? `${pct.toFixed(0)}%` : pct >= 1 ? `${pct.toFixed(1)}%` : `${pct.toFixed(2)}%`
  const percentile = ordinal(Math.min(99, Math.floor(epss.percentile * 100)))
  const tip = `EPSS by FIRST: a ${label} chance of exploitation activity in the next 30 days, scored at or above ${Math.floor(epss.percentile * 100)}% of CVEs.`
  return (
    <WithTooltip tip={tip}>
      <span className="flex flex-col items-end tabular-nums" tabIndex={0} aria-label={tip}>
        <span className="text-theme-text-primary">{label}</span>
        <span className="text-[11px] text-theme-text-tertiary">{percentile} pct</span>
      </span>
    </WithTooltip>
  )
}

/** What a feed that isn't ready means for the page, or null when it is ready or turned off. */
export function feedNote(name: 'KEV' | 'EPSS', feed?: VulnerabilityFeedStatus): string | null {
  const flag = name === 'KEV' ? '--kev-feed' : '--epss-feed'
  const what = name === 'KEV' ? "CISA's Known Exploited Vulnerabilities catalog" : "FIRST's EPSS scores"
  if (!feed) return null
  switch (feed.state) {
    case 'ready':
      return feed.stale ? `${name} data may be out of date: Radar couldn't refresh ${what}${feed.error ? ` (${feed.error})` : ''}.` : null
    case 'loading':
      return `Radar is loading ${what}.`
    default:
      return noFeedNote(name, flag, what, feed)
  }
}

// Without a feed the page is complete without it, so the note says what the
// feed would add and exactly what to set, not that something failed. A mirror
// or file the operator set up that can't be read does say why, so they can
// fix it.
function noFeedNote(name: 'KEV' | 'EPSS', flag: string, what: string, feed: VulnerabilityFeedStatus): string {
  const adds = name === 'KEV' ? 'mark the CVEs that are known to be exploited' : 'show how likely each CVE is to be exploited'
  const setting = `${name === 'KEV' ? 'vulnerabilities.kevFeed' : 'vulnerabilities.epssFeed'} (Helm) or ${flag}`
  if (feed.state === 'disabled' && feed.downloadsOff) return `Feed downloads are turned off on this Radar. To ${adds}, put a copy of ${what} in a file and set ${setting} to its path.`
  if (feed.state === 'disabled') return `${name} is turned off on this Radar. To ${adds}, set ${setting} to the public URL, an https mirror or a file.`
  if (feed.origin === 'mirror' || feed.origin === 'file') {
    return `Radar couldn't read ${what} from its configured ${feed.origin}${feed.error ? ` (${feed.error})` : ''}. Check ${setting}.`
  }
  return `This Radar can't reach the internet to download ${what}. To ${adds}, set ${setting} to an https mirror inside your network, or a file.`
}

/** The docs section on providing the feeds, which the no-data chip links to. */
export const FEEDS_DOCS_URL = 'https://github.com/skyhook-io/radar/blob/main/docs/integrations.md#known-exploited-cves-and-epss'

/**
 * Which copy of the feeds Radar holds and when it last checked, for the
 * Known exploited chip's tooltip: "CISA catalog 2026.11.20, checked 3m ago.
 * EPSS scores from 2026-11-22." A stale copy says why it is stale.
 */
export function feedStatusText(intel: VulnerabilityIntel, age: (iso: string) => string): string {
  const parts: string[] = []
  const kev = intel.kev
  if (kev.state === 'ready') {
    const checked = kev.checkedAt ?? kev.loadedAt
    parts.push(`CISA catalog ${kev.version ?? 'loaded'}${checked ? `, checked ${age(checked)}` : ''}.`)
    if (kev.stale) parts.push(`Out of date: Radar couldn't refresh it${kev.error ? ` (${kev.error})` : ''}.`)
  }
  const epss = intel.epss
  if (epss.state === 'ready' && epss.version) parts.push(`EPSS scores from ${epss.version}.`)
  return parts.join(' ')
}

/** Refreshing could change what Radar holds: a feed loads, or comes from a mirror or file someone may have fixed. Not for feeds that are off, or that the public download can't reach. */
export function refreshCanHelp(intel: VulnerabilityIntel): boolean {
  return [intel.kev, intel.epss].some((f) => f.state === 'ready' || f.state === 'loading' || (f.state === 'unavailable' && (f.origin === 'mirror' || f.origin === 'file')))
}

/**
 * The page's notes about the feeds, for the coverage line: loading, and a
 * copy that couldn't be refreshed. A feed this Radar doesn't have gets no
 * note here; the Known exploited chip says so calmly, and EPSS is simply
 * not offered.
 */
export function intelNotes(intel?: VulnerabilityIntel): { label: string; tip: string; warn: boolean }[] {
  if (!intel) return []
  const out: { label: string; tip: string; warn: boolean }[] = []
  if (intel.kev.state === 'loading') out.push({ label: KEV.loading, tip: feedNote('KEV', intel.kev) ?? '', warn: false })
  if (intel.kev.state === 'ready' && intel.kev.stale) out.push({ label: 'KEV data may be out of date', tip: feedNote('KEV', intel.kev) ?? '', warn: true })
  if (intel.epss.state === 'ready' && intel.epss.stale) out.push({ label: 'EPSS data may be out of date', tip: feedNote('EPSS', intel.epss) ?? '', warn: true })
  return out
}
