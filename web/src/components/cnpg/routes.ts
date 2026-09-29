import type { SelectedResource } from '../../types'

export type CNPGScreen = 'overview' | 'protection' | 'declarations' | 'pooling' | 'operator'

export const CNPG_SCREENS: { id: CNPGScreen; label: string; path: string }[] = [
  { id: 'overview', label: 'Overview', path: '/cnpg' },
  { id: 'protection', label: 'Protection', path: '/cnpg/protection' },
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
// destination each one lives under.
export const CNPG_DETAIL_KINDS: Record<string, { group: string; kind: string; home: CNPGScreen; clusterScoped?: boolean }> = {
  clusters: { group: 'postgresql.cnpg.io', kind: 'Cluster', home: 'overview' },
  backups: { group: 'postgresql.cnpg.io', kind: 'Backup', home: 'protection' },
  scheduledbackups: { group: 'postgresql.cnpg.io', kind: 'ScheduledBackup', home: 'protection' },
  objectstores: { group: 'barmancloud.cnpg.io', kind: 'ObjectStore', home: 'protection' },
  databases: { group: 'postgresql.cnpg.io', kind: 'Database', home: 'declarations' },
  publications: { group: 'postgresql.cnpg.io', kind: 'Publication', home: 'declarations' },
  subscriptions: { group: 'postgresql.cnpg.io', kind: 'Subscription', home: 'declarations' },
  databaseroles: { group: 'postgresql.cnpg.io', kind: 'DatabaseRole', home: 'declarations' },
  poolers: { group: 'postgresql.cnpg.io', kind: 'Pooler', home: 'pooling' },
  imagecatalogs: { group: 'postgresql.cnpg.io', kind: 'ImageCatalog', home: 'operator' },
  clusterimagecatalogs: { group: 'postgresql.cnpg.io', kind: 'ClusterImageCatalog', home: 'operator', clusterScoped: true },
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

// Drawer identity in the URL: kind:group:namespace:name, chained with "~" for
// the in-drawer trail (last entry is the one shown). Kubernetes names and API
// groups cannot contain ":" or "~", and the group is mandatory — CNPG's Cluster
// and Backup collide with CAPI, KubeBlocks and Velero kinds.
export function encodeDrawerRef(r: SelectedResource): string {
  return [r.kind, r.group ?? '', r.namespace ?? '', r.name].join(':')
}

export function decodeDrawerRef(s: string): SelectedResource | null {
  const parts = s.split(':')
  if (parts.length !== 4 || !parts[0] || !parts[3]) return null
  return { kind: parts[0], group: parts[1], namespace: parts[2], name: parts[3] }
}

export function decodeDrawerTrail(param: string | null): SelectedResource[] {
  if (!param) return []
  return param.split('~').map(decodeDrawerRef).filter((r): r is SelectedResource => r !== null)
}

export function encodeDrawerTrail(trail: SelectedResource[]): string {
  return trail.map(encodeDrawerRef).join('~')
}

export function sameResource(a: SelectedResource | null | undefined, b: SelectedResource | null | undefined): boolean {
  if (!a || !b) return false
  return (
    a.kind.toLowerCase() === b.kind.toLowerCase() &&
    (a.group ?? '') === (b.group ?? '') &&
    (a.namespace ?? '') === (b.namespace ?? '') &&
    a.name === b.name
  )
}
