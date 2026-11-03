import { Boxes } from 'lucide-react'
import { KueueWorkloadRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KueueWorkloadRenderer'
import { getProvisioningRequestStatus, getProvisioningRequestMessage } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import { Badge, ResourceLink, Section, type Relationships, type ResourceRef } from '@skyhook-io/k8s-ui'
import { needsNamespaceLabels } from '@skyhook-io/k8s-ui/components/kueue/eligibility'
import { KUEUE_GROUP } from '@skyhook-io/k8s-ui/components/kueue/constants'
import { useKueueProvisioning } from '../../../api/client'
import { useRelatedObjects, useRelatedReads } from '../../kueue/useRelatedReads'

// Reads the Workload's admission path as the viewer: its LocalQueue, the
// ClusterQueue that LocalQueue names, the ClusterQueue holding a reservation
// when that differs, and the namespace's labels only when a selector needs them.
function useAdmissionPathReads(data: any) {
  const namespace: string = data.metadata?.namespace ?? ''
  const queueName: string = data.spec?.queueName ?? ''
  const reserved: string = data.status?.admission?.clusterQueue ?? ''
  const version: string | undefined = data.metadata?.resourceVersion
  const localQueue = useRelatedObjects('localqueues', KUEUE_GROUP, namespace, [queueName], !!queueName && !!namespace, version).byName[queueName]
  const configured: string = localQueue?.state === 'found' ? localQueue.object?.spec?.clusterQueue ?? '' : ''
  const names = [...new Set([configured, reserved].filter(Boolean))]
  const queues = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, names, names.length > 0, version)
  const flavorNames = [...new Set<string>((data.status?.admission?.podSetAssignments ?? []).flatMap((assignment: any) => Object.values(assignment?.flavors ?? {}).map(String)))]
  const flavors = useRelatedObjects('resourceflavors', KUEUE_GROUP, undefined, flavorNames, flavorNames.length > 0, version)
  const configuredRead = configured ? queues.byName[configured] : undefined
  // Eligibility is shown only while unreserved, so a reserved Workload skips the Namespace read.
  const needsLabels = !reserved && configuredRead?.state === 'found' && needsNamespaceLabels(configuredRead.object?.spec?.namespaceSelector)
  const [namespaceRead] = useRelatedReads([{ kind: 'namespaces', name: namespace }], needsLabels && !!namespace)
  return {
    localQueue: queueName ? localQueue : undefined,
    clusterQueues: queues.byName,
    flavors: flavors.byName,
    namespace: needsLabels ? namespaceRead : undefined,
  }
}

// Keyed by UID so moving the drawer to another Workload starts its reads (and their mount time) afresh.
export function KueueWorkloadRenderer(props: { data: any; onNavigate?: (ref: ResourceRef) => void; relationships?: Relationships }) {
  return <KueueWorkloadReads key={props.data.metadata?.uid} {...props} />
}

function KueueWorkloadReads({ data, onNavigate, relationships }: { data: any; onNavigate?: (ref: ResourceRef) => void; relationships?: Relationships }) {
  const reads = useAdmissionPathReads(data)
  const namespace = data.metadata?.namespace ?? ''
  const uid = data.metadata?.uid
  const hasChecks = (data.status?.admissionChecks?.length ?? 0) > 0
  const requests = useKueueProvisioning(namespace, data.metadata?.name ?? '', uid, hasChecks)
  const current = requests.data?.uid === uid ? requests.data : undefined
  const provisioning = hasChecks ? <Section variant="card" title="Provisioning Requests" icon={Boxes} defaultExpanded>
      {requests.error ? <p className="text-sm text-warning-text">Could not observe provisioning requests: {requests.error.message}</p>
        : !current ? <p className="text-sm text-theme-text-tertiary">Looking for provisioning requests…</p>
          : !current.installed ? <p className="text-sm text-theme-text-tertiary">The ProvisioningRequest API is not served by this cluster.</p>
            : current.requests.length === 0 ? <p className="text-sm text-theme-text-tertiary">No retained provisioning requests found on this cluster. A request may not be needed or may have been cleaned up. Check results above retain the controller’s explanation; requests on a MultiKueue worker cluster are not included.</p>
              : <div className="space-y-3">
                <p className="text-xs text-theme-text-tertiary">Currently retained requests owned by this Workload, not a retry history. Requests may be removed after completion or eviction.</p>
                {current.requests.map(request => {
                  const status = getProvisioningRequestStatus(request)
                  const message = getProvisioningRequestMessage(request)
                  return <div key={request.metadata.uid} className="border-t border-theme-border pt-3">
                    <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
                      <ResourceLink kind="provisioningrequests" group="autoscaling.x-k8s.io" namespace={namespace} name={request.metadata.name} onNavigate={onNavigate} />
                      <Badge colorClass={status.color} size="sm">{status.text}</Badge>
                      {request.metadata.deletionTimestamp && <Badge severity="neutral" size="sm">Deleting</Badge>}
                    </div>
                    <p className="mt-1 break-all text-xs text-theme-text-tertiary">{request.spec.provisioningClassName}</p>
                    {message && <p className="mt-1 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{message}</p>}
                  </div>
                })}
                {current.truncated && <p className="text-xs text-theme-text-tertiary">Showing {current.requests.length} of {current.total} retained requests, newest first.</p>}
              </div>}
    </Section> : undefined
  return <BaseRenderer data={data} onNavigate={onNavigate} provisioning={provisioning} relationships={relationships} reads={reads} />
}
