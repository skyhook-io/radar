import { useMemo } from 'react'
import { LoggingFlowRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/LoggingRenderers'
import { LOGGING_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-logging'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useResources } from '../../../api/client'

// controlNamespacesOf is undefined unless every Logging was read: a partial or
// failed read must not mark a ClusterFlow "not counted". A complete read that
// found none is [], and then no ClusterFlow is counted.
export function controlNamespacesOf(items: any[] | undefined, failed: boolean): string[] | undefined {
  if (!items || failed) return undefined
  return [...new Set(items.map((l) => l?.spec?.controlNamespace).filter(Boolean))]
}

/** Host wrapper: tells Flow and ClusterFlow drawers where the Loggings' control namespaces are. */
export function LoggingFlowHostRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const loggings = useResources<any>('loggings', '', LOGGING_GROUP)
  const partial = loggings.coverage?.status === 'partial'
  const controlNamespaces = useMemo(() => controlNamespacesOf(loggings.data, loggings.isError || partial), [loggings.data, loggings.isError, partial])
  return <LoggingFlowRenderer data={data} onNavigate={onNavigate} controlNamespaces={controlNamespaces} loggingsUnreadable={!!loggings.error || partial} />
}
