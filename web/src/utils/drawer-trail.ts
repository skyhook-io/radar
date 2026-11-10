import type { SelectedResource } from '../types'

// Drawer identity in the URL: kind:group:namespace:name, chained with "~" for
// the in-drawer trail (last entry is the one shown). Kubernetes names and API
// groups cannot contain ":" or "~", and the group is mandatory: CRD kinds
// collide across groups (CloudNativePG's Cluster and Backup with CAPI,
// KubeBlocks and Velero kinds).
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

export function sameSelectedResource(a: SelectedResource | null | undefined, b: SelectedResource | null | undefined): boolean {
  if (!a || !b) return false
  return (
    a.kind.toLowerCase() === b.kind.toLowerCase() &&
    (a.group ?? '') === (b.group ?? '') &&
    (a.namespace ?? '') === (b.namespace ?? '') &&
    a.name === b.name
  )
}
