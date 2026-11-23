import { KAGENT_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import type { KagentRef } from '../../api/kagent'
export { KAGENT_GROUP }
export const KAGENT_AGENT_KIND = { name: 'agents', kind: 'Agent', group: KAGENT_GROUP }
export const KAGENT_GENERIC_AGENTS_PATH = `/resources/agents?apiGroup=${encodeURIComponent(KAGENT_GROUP)}`
export const KAGENT_AGENTS_PATH = '/kagent/agents'
export const KAGENT_KINDS = {
  agents: { kind: 'Agent', label: 'Agents', listPath: KAGENT_AGENTS_PATH },
  remotemcpservers: { kind: 'RemoteMCPServer', label: 'MCP servers', listPath: '/kagent/mcpservers' },
  modelconfigs: { kind: 'ModelConfig', label: 'Models', listPath: '/kagent/models' },
} as const
export type KagentPlural = keyof typeof KAGENT_KINDS
export interface KagentTarget {
  plural: KagentPlural
  namespace: string
  name: string
}
export function kagentPath(ref: KagentRef, context?: string): string | null {
  const plural = (Object.keys(KAGENT_KINDS) as KagentPlural[]).find((p) => KAGENT_KINDS[p].kind === ref.kind)
  if (ref.group !== KAGENT_GROUP || !plural) return null
  return `/kagent/${plural}/${encodeURIComponent(ref.namespace)}/${encodeURIComponent(ref.name)}${context ? `?ctx=${encodeURIComponent(context)}` : ''}`
}
export function parseKagentRoute(pathname: string): { screen: 'overview' | 'agents' | 'mcpservers' | 'models'; detail?: KagentTarget } {
  const [, , section, ns, name] = pathname.split('/')
  if (Object.hasOwn(KAGENT_KINDS, section) && ns && name)
    return {
      screen: 'overview',
      detail: { plural: section as KagentPlural, namespace: decodeURIComponent(ns), name: decodeURIComponent(name) },
    }
  return { screen: section === 'agents' || section === 'mcpservers' || section === 'models' ? section : 'overview' }
}
