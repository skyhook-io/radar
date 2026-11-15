import { useMemo } from 'react'
import { LoggingFlowRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/LoggingRenderers'
import { LOGGING_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-logging'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useResources } from '../../../api/client'

// controlNamespacesOf is undefined unless every Logging was read: a partial,
// failed or unknown read must not mark a ClusterFlow "not counted". A complete
// read that found none is [], and then no ClusterFlow is counted. A Logging that
// takes cluster resources from every namespace counts ClusterFlows anywhere, so
// it too leaves the drawer claiming nothing.
export function controlNamespacesOf(items: any[] | undefined, complete: boolean): string[] | undefined {
  if (!items || !complete || items.some((l) => l?.spec?.allowClusterResourcesFromAllNamespaces)) return undefined
  return [...new Set(items.map((l) => l?.spec?.controlNamespace).filter(Boolean))]
}

/** Only a read that says it covered everything (an older Radar says nothing). */
export function loggingsReadComplete(read: { isError: boolean; coverage?: { status: string } }): boolean {
  return !read.isError && read.coverage?.status === 'complete'
}

// missingOutputRefs names the refs the operator can't resolve, so it rejects
// the Flow: no output of the ref's kind with that name and the Flow's
// loggingRef. undefined outputs: that kind wasn't read completely, so its refs
// are never called missing.
export function missingOutputRefs(flow: any, clusterOutputs: any[] | undefined, outputs: any[] | undefined): string[] {
  const ref = flow?.spec?.loggingRef ?? ''
  const has = (items: any[], name: string) => items.some((o) => o?.metadata?.name === name && (o?.spec?.loggingRef ?? '') === ref)
  const missing: string[] = []
  if (clusterOutputs) for (const name of flow?.spec?.globalOutputRefs ?? []) if (!has(clusterOutputs, name)) missing.push(`ClusterOutput ${name}`)
  if (outputs) for (const name of flow?.spec?.localOutputRefs ?? []) if (!has(outputs.filter((o) => o?.metadata?.namespace === flow?.metadata?.namespace), name)) missing.push(`Output ${name}`)
  return missing
}

/** Host wrapper: tells Flow and ClusterFlow drawers where the Loggings' control namespaces are, and which output refs don't resolve. */
export function LoggingFlowHostRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const loggings = useResources<any>('loggings', '', LOGGING_GROUP)
  const clusterOutputs = useResources<any>('clusteroutputs', '', LOGGING_GROUP)
  const outputs = useResources<any>('outputs', data?.metadata?.namespace ?? '', LOGGING_GROUP, { enabled: data?.kind === 'Flow' })
  const complete = loggingsReadComplete(loggings)
  const controlNamespaces = useMemo(() => controlNamespacesOf(loggings.data, complete), [loggings.data, complete])
  const clusterOutputsRead = loggingsReadComplete(clusterOutputs) ? clusterOutputs.data : undefined
  const outputsRead = loggingsReadComplete(outputs) ? outputs.data : undefined
  const missingOutputs = useMemo(() => missingOutputRefs(data, clusterOutputsRead, outputsRead), [data, clusterOutputsRead, outputsRead])
  // A failed outputs read only withholds the "rejected" claim; say so rather than look checked.
  return (
    <>
      <LoggingFlowRenderer data={data} onNavigate={onNavigate} controlNamespaces={controlNamespaces} missingOutputs={missingOutputs} loggingsUnreadable={!!loggings.error || loggings.coverage?.status === 'partial'} />
      <LookupFailureNote errors={[clusterOutputs.error, data?.kind === 'Flow' ? outputs.error : null]} what="whether this flow's outputs exist" />
    </>
  )
}
