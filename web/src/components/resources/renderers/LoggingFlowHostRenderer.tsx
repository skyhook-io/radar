import { useMemo } from 'react'
import { LoggingFlowRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/LoggingRenderers'
import { LOGGING_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-logging'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useResources } from '../../../api/client'

// controlNamespacesOf lists the control namespaces of the Loggings with the
// flow's loggingRef, the only ones that process it. Undefined unless every
// Logging was read: a partial, failed or unknown read must not mark a
// ClusterFlow "not counted". A complete read that found none is [], and then no
// ClusterFlow is counted. A Logging that takes cluster resources from every
// namespace counts ClusterFlows anywhere, so it too leaves the drawer claiming
// nothing.
export function controlNamespacesOf(items: any[] | undefined, complete: boolean, loggingRef = ''): string[] | undefined {
  if (!items || !complete) return undefined
  const own = items.filter((l) => (l?.spec?.loggingRef ?? '') === loggingRef)
  if (own.some((l) => l?.spec?.allowClusterResourcesFromAllNamespaces)) return undefined
  return [...new Set(own.map((l) => l?.spec?.controlNamespace).filter(Boolean))]
}

/** Only a read that says it covered everything (an older Radar says nothing). */
export function loggingsReadComplete(read: { isError: boolean; coverage?: { status: string } }): boolean {
  return !read.isError && read.coverage?.status === 'complete'
}

// ownClusterOutputs are the ClusterOutputs the operator resolves a flow's refs
// among: its loggingRef, in its Logging's control namespace (anywhere when that
// is unknown or the Logging allows cluster resources from every namespace).
function ownClusterOutputs(flow: any, clusterOutputs: any[], controlNamespaces: string[] | undefined): any[] {
  const ref = flow?.spec?.loggingRef ?? ''
  return clusterOutputs.filter((o) => (o?.spec?.loggingRef ?? '') === ref && (!controlNamespaces || controlNamespaces.includes(o?.metadata?.namespace)))
}

// missingOutputRefs names the refs the operator can't resolve, so it can't
// render the Flow: no output of the ref's kind with that name for the Flow's
// Logging. undefined outputs: that kind wasn't read completely, so its refs are
// never called missing.
export function missingOutputRefs(flow: any, clusterOutputs: any[] | undefined, outputs: any[] | undefined, controlNamespaces?: string[]): string[] {
  const ref = flow?.spec?.loggingRef ?? ''
  const has = (items: any[], name: string) => items.some((o) => o?.metadata?.name === name && (o?.spec?.loggingRef ?? '') === ref)
  const missing: string[] = []
  if (clusterOutputs) for (const name of flow?.spec?.globalOutputRefs ?? []) if (!has(ownClusterOutputs(flow, clusterOutputs, controlNamespaces), name)) missing.push(`ClusterOutput ${name}`)
  if (outputs) for (const name of flow?.spec?.localOutputRefs ?? []) if (!has(outputs.filter((o) => o?.metadata?.namespace === flow?.metadata?.namespace), name)) missing.push(`Output ${name}`)
  return missing
}

// protectedOutputRefs names the protected ClusterOutputs a namespaced Flow
// names; only ClusterFlows may use them.
export function protectedOutputRefs(flow: any, clusterOutputs: any[] | undefined, controlNamespaces?: string[]): string[] {
  if (flow?.kind !== 'Flow' || !clusterOutputs) return []
  const own = ownClusterOutputs(flow, clusterOutputs, controlNamespaces)
  return (flow?.spec?.globalOutputRefs ?? []).filter((name: string) => own.some((o) => o?.metadata?.name === name && o?.spec?.protected === true))
}

/** Host wrapper: tells Flow and ClusterFlow drawers where the Loggings' control namespaces are, and which output refs don't resolve. */
export function LoggingFlowHostRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const loggings = useResources<any>('loggings', '', LOGGING_GROUP)
  const clusterOutputs = useResources<any>('clusteroutputs', '', LOGGING_GROUP)
  const outputs = useResources<any>('outputs', data?.metadata?.namespace ?? '', LOGGING_GROUP, { enabled: data?.kind === 'Flow' })
  const complete = loggingsReadComplete(loggings)
  const loggingRef = data?.spec?.loggingRef ?? ''
  const controlNamespaces = useMemo(() => controlNamespacesOf(loggings.data, complete, loggingRef), [loggings.data, complete, loggingRef])
  const clusterOutputsRead = loggingsReadComplete(clusterOutputs) ? clusterOutputs.data : undefined
  const outputsRead = loggingsReadComplete(outputs) ? outputs.data : undefined
  const missingOutputs = useMemo(() => missingOutputRefs(data, clusterOutputsRead, outputsRead, controlNamespaces), [data, clusterOutputsRead, outputsRead, controlNamespaces])
  const protectedOutputs = useMemo(() => protectedOutputRefs(data, clusterOutputsRead, controlNamespaces), [data, clusterOutputsRead, controlNamespaces])
  // A failed outputs read only withholds the "invalid" claim; say so rather than look checked.
  return (
    <>
      <LoggingFlowRenderer data={data} onNavigate={onNavigate} controlNamespaces={controlNamespaces} missingOutputs={missingOutputs} protectedOutputs={protectedOutputs} loggingsUnreadable={!!loggings.error || loggings.coverage?.status === 'partial'} />
      <LookupFailureNote errors={[clusterOutputs.error, data?.kind === 'Flow' ? outputs.error : null]} what="whether this flow's outputs exist" />
    </>
  )
}
