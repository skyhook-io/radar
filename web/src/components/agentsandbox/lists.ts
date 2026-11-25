import type { Relationships, ResourceRef } from '@skyhook-io/k8s-ui/types/core'
import { getSandboxClaimWarmPool } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import { AGENT_SANDBOX_EXTENSIONS_GROUP } from './routes'

// What the kind lists join across their rows, within one namespace.

type Ref = { kind: string; group: string; namespace: string; name: string }

const trimmed = (value: unknown): string | undefined => (typeof value === 'string' && value.trim() ? value.trim() : undefined)

/** The object that controls a Sandbox, from its controller owner reference: a warm pool, or a claim. */
export function sandboxController(o: any): Ref | undefined {
  const refs: any[] = Array.isArray(o?.metadata?.ownerReferences) ? o.metadata.ownerReferences : []
  const owner = refs.find((r) => r?.controller) ?? refs[0]
  if (!owner?.name || !['SandboxWarmPool', 'SandboxClaim'].includes(owner.kind)) return undefined
  return { kind: owner.kind, group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: o?.metadata?.namespace ?? '', name: owner.name }
}

/** The claims that name the pool in spec.warmPoolRef. */
export function poolClaims(pool: any, claims: any[]): any[] {
  const namespace = pool?.metadata?.namespace ?? ''
  const name = pool?.metadata?.name ?? ''
  return claims.filter((c) => (c?.metadata?.namespace ?? '') === namespace && getSandboxClaimWarmPool(c) === name)
}

/** The Sandbox a claim is bound to, from status.sandbox.name. */
export function claimSandboxName(claim: any): string | undefined {
  return trimmed(claim?.status?.sandbox?.name)
}

export type PodAnswer =
  | { state: 'loading' }
  | { state: 'found'; pods: ResourceRef[] }
  /** Every Pod the viewer may read was checked, and none names this Sandbox as its owner. */
  | { state: 'none' }
  | { state: 'unknown' }

/** The Sandbox's Pod from its owner-reference children, never from its name. */
export function sandboxPod(relationships: Relationships | undefined, loading: boolean): PodAnswer {
  if (!relationships) return loading ? { state: 'loading' } : { state: 'unknown' }
  const pods = (relationships.children ?? []).filter((r) => r.kind === 'Pod' && !r.group)
  if (pods.length > 0) return { state: 'found', pods }
  return relationships.ownership?.children.complete ? { state: 'none' } : { state: 'unknown' }
}
