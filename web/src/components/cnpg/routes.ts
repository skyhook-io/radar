import { CNPG_KIND_BY_KEY, type CNPGWorkspaceKey } from '@skyhook-io/k8s-ui'
import type { RadarFeatureSupport } from '../../api/radarFeatures'

export type CNPGScreen = 'overview' | 'protection' | 'declarations' | 'pooling' | 'operator'

export const CNPG_SCREENS: { id: CNPGScreen; label: string; path: string }[] = [
  { id: 'overview', label: 'Clusters', path: '/cnpg' },
  { id: 'protection', label: 'Backups', path: '/cnpg/protection' },
  { id: 'declarations', label: 'Declarations', path: '/cnpg/declarations' },
  { id: 'pooling', label: 'Pooling', path: '/cnpg/pooling' },
  { id: 'operator', label: 'Operator', path: '/cnpg/operator' },
]

export interface CNPGDetailTarget {
  plural: string
  group: string
  namespace: string
  name: string
}

export interface CNPGRoute {
  screen: CNPGScreen
  detail?: CNPGDetailTarget
}

// The CNPG kinds that have a CNPG-framed full detail, with the workspace
// destination each one lives under. Kind and group come from the workspace's
// kind table, so the two never disagree.
const CNPG_DETAIL_HOMES: Partial<Record<CNPGWorkspaceKey, CNPGScreen>> = {
  clusters: 'overview',
  backups: 'protection',
  scheduledBackups: 'protection',
  objectStores: 'protection',
  databases: 'declarations',
  publications: 'declarations',
  subscriptions: 'declarations',
  databaseRoles: 'declarations',
  poolers: 'pooling',
  imageCatalogs: 'operator',
  clusterImageCatalogs: 'operator',
}

export const CNPG_DETAIL_KINDS: Record<string, { group: string; kind: string; home: CNPGScreen; clusterScoped?: boolean }> = Object.fromEntries(
  (Object.entries(CNPG_DETAIL_HOMES) as [CNPGWorkspaceKey, CNPGScreen][]).map(([key, home]) => {
    const k = CNPG_KIND_BY_KEY[key]
    return [k.plural, { group: k.group, kind: k.kind, home, ...(key === 'clusterImageCatalogs' ? { clusterScoped: true } : {}) }]
  }),
)

// The kind each view is named after.
const CNPG_VIEW_NAMESAKES: Partial<Record<CNPGScreen, string>> = { overview: 'clusters', protection: 'backups', pooling: 'poolers' }

/**
 * Whether a kind is the one its home view is named after (Cluster in
 * Clusters, Backup in Backups, Pooler in Pooling): the crumb then already says
 * what the object is, so the kind badge is left out. Other kinds in the same
 * view, such as ScheduledBackup and ObjectStore under Backups, keep it.
 */
export function cnpgViewNamedForKind(plural: string): boolean {
  const home = CNPG_DETAIL_KINDS[plural]?.home
  return !!home && CNPG_VIEW_NAMESAKES[home] === plural
}

/** The CloudNativePG Cluster kind, whose list is the Clusters view. */
export function isCNPGClusterKind(kind: { name: string; group: string } | null | undefined): boolean {
  return !!kind && kind.name === 'clusters' && kind.group === 'postgresql.cnpg.io'
}

/**
 * What the CloudNativePG Cluster kind's Resources route shows: the Clusters
 * view when this Radar serves it; a loader while capabilities load for the
 * first time, so the table never flashes before it; otherwise the table (a
 * Radar older than the views, or capabilities that could not be read).
 */
export function cnpgClusterKindListMode(
  kind: { name: string; group: string } | null | undefined,
  support: RadarFeatureSupport,
  capabilitiesPending: boolean,
): 'view' | 'wait' | 'table' {
  if (!isCNPGClusterKind(kind)) return 'table'
  if (support === 'supported') return 'view'
  return support === 'unknown' && capabilitiesPending ? 'wait' : 'table'
}

export function cnpgDetailKindFor(plural: string, group: string | undefined): string | null {
  const p = plural.toLowerCase()
  const spec = CNPG_DETAIL_KINDS[p]
  return spec && spec.group === (group ?? '') ? p : null
}

export function parseCNPGRoute(pathname: string): CNPGRoute {
  const seg = pathname.replace(/^\/+/, '').split('/').map((s) => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  })
  if (seg[0] !== 'cnpg') return { screen: 'overview' }
  const s = seg[1] ?? ''
  const detailSpec = CNPG_DETAIL_KINDS[s]
  if (detailSpec && seg[2] && seg[3]) {
    return {
      screen: detailSpec.home,
      detail: { plural: s, group: detailSpec.group, namespace: seg[2] === '_' ? '' : seg[2], name: seg[3] },
    }
  }
  const match = CNPG_SCREENS.find((x) => x.id === s)
  if (match) return { screen: match.id }
  return { screen: 'overview' }
}

/** Full detail path; `ctx` pins the Kubernetes context the object belongs to. */
export function cnpgDetailPath(target: Omit<CNPGDetailTarget, 'group'>, ctx?: string, tab?: string): string {
  const params = new URLSearchParams()
  if (ctx) params.set('ctx', ctx)
  if (tab) params.set('tab', tab)
  const qs = params.toString()
  return `/cnpg/${target.plural}/${encodeURIComponent(target.namespace || '_')}/${encodeURIComponent(target.name)}${qs ? `?${qs}` : ''}`
}

export function cnpgScreenPath(screen: CNPGScreen): string {
  return CNPG_SCREENS.find((s) => s.id === screen)?.path ?? '/cnpg'
}
