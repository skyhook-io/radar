import { Boxes } from 'lucide-react'
import { KueueWorkloadRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KueueWorkloadRenderer'
import { getProvisioningRequestStatus, getProvisioningRequestMessage } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import { Badge, ResourceLink, Section, type ResourceRef } from '@skyhook-io/k8s-ui'
import { useKueueProvisioning } from '../../../api/client'

export function KueueWorkloadRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace = data.metadata?.namespace ?? ''
  const uid = data.metadata?.uid
  const hasChecks = (data.status?.admissionChecks?.length ?? 0) > 0
  const requests = useKueueProvisioning(namespace, data.metadata?.name ?? '', uid, hasChecks)
  const current = requests.data?.uid === uid ? requests.data : undefined
  const provisioning = hasChecks ? <Section title="Provisioning Requests" icon={Boxes} defaultExpanded>
      {requests.error ? <p className="text-sm text-warning-text">Could not observe provisioning requests: {requests.error.message}</p>
        : !current ? <p className="text-sm text-theme-text-tertiary">Looking for provisioning requests…</p>
          : !current.installed ? <p className="text-sm text-theme-text-tertiary">The ProvisioningRequest API is not served by this cluster.</p>
            : current.requests.length === 0 ? <p className="text-sm text-theme-text-tertiary">No retained provisioning requests found on this cluster. A request may not be needed or may have been cleaned up. Check results above retain the controller’s explanation; requests on a MultiKueue worker cluster are not included.</p>
              : <div className="space-y-2">
                <p className="text-xs text-theme-text-tertiary">Currently retained requests owned by this Workload, not a retry history. Requests may be removed after completion or eviction.</p>
                {current.requests.map(request => {
                  const status = getProvisioningRequestStatus(request)
                  const message = getProvisioningRequestMessage(request)
                  return <div key={request.metadata.uid} className="card-inner">
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
  return <BaseRenderer data={data} onNavigate={onNavigate} provisioning={provisioning} />
}
