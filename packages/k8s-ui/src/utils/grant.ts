/**
 * One RBAC permission a read or action needs, as the server sends it. An
 * empty namespace means cluster-wide (a cluster-scoped resource, or a list
 * across every namespace).
 */
export interface Grant {
  verb: string
  group?: string
  resource: string
  subresource?: string
  namespace?: string
}

function grantResource(g: Grant): string {
  return g.subresource ? `${g.resource}/${g.subresource}` : g.resource
}

/**
 * The grant's two halves for display: what ("patch clusters/status
 * (postgresql.cnpg.io)") and where (" in namespace pg", " cluster-wide").
 * Worded like the server's Grant.String so the two never disagree.
 */
export function grantParts(g: Grant): { what: string; scope: string } {
  if (g.namespace) {
    return { what: `${g.verb} ${grantResource(g)}${g.group ? ` (${g.group})` : ''}`, scope: ` in namespace ${g.namespace}` }
  }
  return { what: `${g.verb} ${grantResource(g)}${g.group ? `.${g.group}` : ''}`, scope: ' cluster-wide' }
}

export function formatGrant(g: Grant): string
export function formatGrant(g: Grant | undefined): string | undefined
export function formatGrant(g: Grant | undefined): string | undefined {
  if (!g) return undefined
  const { what, scope } = grantParts(g)
  return what + scope
}
