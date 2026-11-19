// imageTag returns an image reference's tag: after the last ':' of the name,
// before any '@digest'. A registry port (host:5000/repo) is not a tag.
export function imageTag(image: string): string | undefined {
  const name = image.split('@')[0]
  const colon = name.lastIndexOf(':')
  if (colon < 0 || name.indexOf('/', colon) >= 0) return undefined
  return name.slice(colon + 1) || undefined
}

// Flux Operator counts every unsuspended object as running, failing ones
// included, so rows carry the total (running + suspended) instead.
export interface FluxReconcilerRow {
  kind: string
  total: number
  failing: number
  suspended: number
  totalSize?: string
}

export interface FluxReportSummary {
  version?: string
  operatorVersion?: string
  managedBy?: string
  controllers: Array<{ name: string; tag?: string; ready: boolean }>
  // Undefined when the report lists no components / reconcilers (missing or empty): unknown, not zero.
  controllersReady?: number
  failing?: number
  rows: FluxReconcilerRow[]
  also: Array<{ kind: string; total: number }>
  needsInventoryNote: boolean
  total: number
}

// fluxVersionAtLeast compares major.minor of "v2.7.5" / "2.7.x"; null when
// the version doesn't parse, so callers never guess.
export function fluxVersionAtLeast(version: string | undefined, major: number, minor: number): boolean | null {
  const match = /^v?(\d+)\.(\d+)/.exec(version ?? '')
  if (!match) return null
  const [maj, min] = [Number(match[1]), Number(match[2])]
  return maj > major || (maj === major && min >= minor)
}

// pickFluxReport uses the first report by namespace/name; one is expected.
export function pickFluxReport<T extends { metadata?: { namespace?: string; name?: string } }>(reports: T[]): { report: T; total: number } | null {
  if (reports.length === 0) return null
  const key = (r: T) => `${r.metadata?.namespace ?? ''}/${r.metadata?.name ?? ''}`
  const sorted = [...reports].sort((a, b) => key(a).localeCompare(key(b)))
  return { report: sorted[0], total: reports.length }
}

// Kinds that only run a couple of objects fold into one "Also:" line.
const FOLD_BELOW = 3

export function summarizeFluxReport(report: any, total: number): FluxReportSummary {
  const spec = report?.spec ?? {}
  const controllers = (Array.isArray(spec.components) ? spec.components : []).map((c: any) => ({
    name: String(c?.name ?? ''),
    tag: c?.image ? imageTag(String(c.image)) : undefined,
    ready: c?.ready === true,
  }))
  const all: FluxReconcilerRow[] = (Array.isArray(spec.reconcilers) ? spec.reconcilers : []).map((r: any) => {
    const suspended = Number(r?.stats?.suspended) || 0
    return {
      kind: String(r?.kind ?? ''),
      total: (Number(r?.stats?.running) || 0) + suspended,
      failing: Number(r?.stats?.failing) || 0,
      suspended,
      totalSize: r?.stats?.totalSize || undefined,
    }
  })
  const counted = all.filter((r) => r.total + r.failing > 0)
  const folded = (r: FluxReconcilerRow) => r.failing === 0 && r.suspended === 0 && r.total < FOLD_BELOW
  const rows = counted
    .filter((r) => !folded(r))
    .sort((a, b) => b.failing - a.failing || b.suspended - a.suspended || b.total - a.total)
  const helmReleases = all.find((r) => r.kind === 'HelmRelease')
  return {
    version: spec.distribution?.version,
    operatorVersion: spec.operator?.version,
    managedBy: spec.distribution?.managedBy,
    controllers,
    controllersReady: controllers.length > 0 ? controllers.filter((c: { ready: boolean }) => c.ready).length : undefined,
    failing: all.length > 0 ? all.reduce((n, r) => n + r.failing, 0) : undefined,
    rows,
    also: counted.filter(folded).map((r) => ({ kind: r.kind, total: r.total })),
    // From 2.8 too: helm-controller records the inventory only at an install
    // or upgrade, so a release not upgraded since has none.
    needsInventoryNote: !!helmReleases && helmReleases.total + helmReleases.failing > 0,
    total,
  }
}
