import type { SelectedResource } from '../../types'

export type CNPGScreen = 'overview' | 'protection' | 'declarations' | 'pooling' | 'operator'

export const CNPG_SCREENS: { id: CNPGScreen; label: string; path: string }[] = [
  { id: 'overview', label: 'Overview', path: '/cnpg' },
  { id: 'protection', label: 'Protection', path: '/cnpg/protection' },
  { id: 'declarations', label: 'Declarations', path: '/cnpg/declarations' },
  { id: 'pooling', label: 'Pooling', path: '/cnpg/pooling' },
  { id: 'operator', label: 'Operator', path: '/cnpg/operator' },
]

export interface CNPGRoute {
  screen: CNPGScreen
}

export function parseCNPGRoute(pathname: string): CNPGRoute {
  const seg = pathname.replace(/^\/+/, '').split('/')
  if (seg[0] !== 'cnpg') return { screen: 'overview' }
  const s = seg[1] ?? ''
  const match = CNPG_SCREENS.find((x) => x.id === s)
  if (match) return { screen: match.id }
  return { screen: 'overview' }
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
