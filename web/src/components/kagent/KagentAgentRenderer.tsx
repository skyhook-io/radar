import { useQueries } from '@tanstack/react-query'
import { KAGENT_AGENT_LIST_CAP, KagentAgentRenderer as BaseKagentAgentRenderer, type KagentRelatedRead } from '@skyhook-io/k8s-ui/components/resources/renderers/KagentAgentRenderer'
import { getKagentAgentHarness, getKagentAgentTemplateRef, getKagentTemplateDeclarations, isKagentServerRef, kagentRefKey, type KagentRef } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import type { ResourceWithRelationships } from '@skyhook-io/k8s-ui/types/core'
import { isForbiddenError, isNotFoundError, resourceQueryOptions, useResource } from '../../api/client'
import { KAGENT_GROUP } from './routes'

/** How a read of one referenced object came out, as the drawer shows it. */
export function toRelatedRead(query: { data?: unknown; error: unknown; isLoading: boolean }, requested: boolean): KagentRelatedRead | undefined {
  if (!requested) return undefined
  if (query.data) return { state: 'read', object: query.data }
  if (isNotFoundError(query.error)) return { state: 'notFound' }
  if (isForbiddenError(query.error)) return { state: 'denied' }
  if (query.error) return { state: 'failed', message: query.error instanceof Error ? query.error.message : undefined }
  return { state: 'loading' }
}

/** The distinct kagent RemoteMCPServers a template binds, as many as the drawer lists. */
export function kagentServersToRead(refs: (KagentRef | undefined)[]): KagentRef[] {
  const seen = new Set<string>()
  const out: KagentRef[] = []
  for (const ref of refs) {
    if (!ref || !isKagentServerRef(ref) || seen.has(kagentRefKey(ref))) continue
    seen.add(kagentRefKey(ref))
    out.push(ref)
    if (out.length === KAGENT_AGENT_LIST_CAP) break
  }
  return out
}

/**
 * Host wrapper: reads, as the caller, the AgentTemplate and Harness a kagent
 * Agent references, so the drawer can show the models, MCP servers and worker
 * pool they declare, and the RemoteMCPServers it binds tools from: kagent
 * drops a server's tools when its discovery fails and still reports the Agent
 * Ready. Each is an ordinary resource read, gated as the caller.
 */
export function KagentAgentRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace: string = data?.metadata?.namespace ?? ''
  const templateName = data?.spec?.template?.inline ? undefined : getKagentAgentTemplateRef(data)
  const harnessName = getKagentAgentHarness(data)
  const template = useResource<any>('agenttemplates', namespace, templateName ?? '', KAGENT_GROUP, { enabled: !!templateName })
  const harness = useResource<any>('harnesses', namespace, harnessName ?? '', KAGENT_GROUP, { enabled: !!harnessName })
  const templateSpec = data?.spec?.template?.inline ?? template.data?.spec
  const servers = templateSpec ? kagentServersToRead(getKagentTemplateDeclarations(templateSpec, namespace).tools.map(t => t.server)) : []
  const serverReads = useQueries({
    queries: servers.map(ref => resourceQueryOptions<any>('remotemcpservers', ref.namespace, ref.name, KAGENT_GROUP)),
  })
  const mcpServers = Object.fromEntries(servers.map((ref, i) => {
    const q = serverReads[i]
    const data = (q.data as ResourceWithRelationships<any> | undefined)?.resource
    return [kagentRefKey(ref), toRelatedRead({ data, error: q.error, isLoading: q.isLoading }, true)!]
  }))
  return (
    <BaseKagentAgentRenderer
      data={data}
      onNavigate={onNavigate}
      related={{
        template: toRelatedRead(template, !!templateName),
        harness: toRelatedRead(harness, !!harnessName),
        mcpServers,
      }}
    />
  )
}
