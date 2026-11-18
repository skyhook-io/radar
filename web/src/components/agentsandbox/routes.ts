export const AGENT_SANDBOX_GROUP = 'agents.x-k8s.io'
export const AGENT_SANDBOX_EXTENSIONS_GROUP = 'extensions.agents.x-k8s.io'

export const AGENT_SANDBOX_OVERVIEW = { label: 'Overview', title: 'Agent Sandbox overview', path: '/agent-sandbox' }

export const SANDBOX_KIND = { name: 'sandboxes', kind: 'Sandbox', group: AGENT_SANDBOX_GROUP }
export const SANDBOX_CLAIM_KIND = { name: 'sandboxclaims', kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP }
export const SANDBOX_WARM_POOL_KIND = { name: 'sandboxwarmpools', kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP }
export const SANDBOX_TEMPLATE_KIND = { name: 'sandboxtemplates', kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP }

export type AgentSandboxKind = typeof SANDBOX_KIND | typeof SANDBOX_CLAIM_KIND | typeof SANDBOX_WARM_POOL_KIND | typeof SANDBOX_TEMPLATE_KIND

export function kindListPath(kind: { name: string; group: string }): string {
  return `/resources/${kind.name}?apiGroup=${encodeURIComponent(kind.group)}`
}

export const SANDBOXES_PATH = kindListPath(SANDBOX_KIND)
export const SANDBOX_CLAIMS_PATH = kindListPath(SANDBOX_CLAIM_KIND)
export const SANDBOX_WARM_POOLS_PATH = kindListPath(SANDBOX_WARM_POOL_KIND)

export function ownsAgentSandboxPath(pathname: string): boolean {
  return pathname === AGENT_SANDBOX_OVERVIEW.path || pathname.startsWith(`${AGENT_SANDBOX_OVERVIEW.path}/`)
}
