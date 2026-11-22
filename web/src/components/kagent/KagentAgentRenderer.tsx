import { KagentAgentRenderer as BaseKagentAgentRenderer, type KagentRelatedRead } from '@skyhook-io/k8s-ui/components/resources/renderers/KagentAgentRenderer'
import { getKagentAgentHarness, getKagentAgentTemplateRef } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { isForbiddenError, isNotFoundError, useResource } from '../../api/client'
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

/**
 * Host wrapper: reads, as the caller, the AgentTemplate and Harness a kagent
 * Agent references, so the drawer can show the models, MCP servers and worker
 * pool they declare.
 */
export function KagentAgentRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace: string = data?.metadata?.namespace ?? ''
  const templateName = data?.spec?.template?.inline ? undefined : getKagentAgentTemplateRef(data)
  const harnessName = getKagentAgentHarness(data)
  const template = useResource<any>('agenttemplates', namespace, templateName ?? '', KAGENT_GROUP, { enabled: !!templateName })
  const harness = useResource<any>('harnesses', namespace, harnessName ?? '', KAGENT_GROUP, { enabled: !!harnessName })
  return (
    <BaseKagentAgentRenderer
      data={data}
      onNavigate={onNavigate}
      related={{
        template: toRelatedRead(template, !!templateName),
        harness: toRelatedRead(harness, !!harnessName),
      }}
    />
  )
}
