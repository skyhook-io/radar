import { Badge } from '@skyhook-io/k8s-ui/components/ui/Badge'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { RayJobRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/RayJobRenderer'
import { rayJobIsLocal, rayJobSubmissionMode } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { getJobStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { AlertBanner, ResourceLink } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { ApiError, useResource } from '../../../api/client'
import { KueueAdmission } from '../../execution/JobSetAdmission'

function Submitter({ root, onNavigate }: { root: any; onNavigate?: (ref: ResourceRef) => void }) {
  const query = useResource<any>('jobs', root.metadata.namespace, root.metadata.name, 'batch')
  if (query.error) return query.error instanceof ApiError && query.error.status === 404
    ? <p className="text-theme-text-secondary">No submitter Job observed. It may not have been created yet or may have been cleaned up.</p>
    : <AlertBanner variant="warning" title="Submitter evidence unavailable" message={query.error.message} />
  if (!query.data) return <p className="text-theme-text-secondary">Reading submitter Job…</p>
  const job = query.data
  const owned = job.apiVersion === 'batch/v1' && job.kind === 'Job' && job.metadata?.name === root.metadata.name && job.metadata?.namespace === root.metadata.namespace && job.metadata.ownerReferences?.some((owner: any) => owner.controller === true && apiVersionToGroup(owner.apiVersion) === 'ray.io' && owner.kind === 'RayJob' && owner.name === root.metadata.name && owner.uid === root.metadata.uid)
  if (!owned) return <AlertBanner variant="warning" title="Submitter ownership mismatch" message="The same-name Job does not report this RayJob incarnation as its controller. Its status is not attributed to this RayJob." />
  const status = getJobStatus(job)
  return <div className="space-y-2">
    <div className="flex flex-wrap items-center gap-2"><ResourceLink kind="jobs" group="batch" namespace={job.metadata.namespace} name={job.metadata.name} onNavigate={onNavigate} />{job.metadata.deletionTimestamp ? <Badge severity="alert">Deleting</Badge> : <span className={`badge ${status.color}`}>{status.text}</span>}</div>
    {job.metadata.creationTimestamp && <p className="text-xs text-theme-text-secondary">Created <time dateTime={job.metadata.creationTimestamp} title={job.metadata.creationTimestamp}>{formatRelativeAgeTime(job.metadata.creationTimestamp)}</time></p>}
    <p className="text-xs text-theme-text-secondary">Open this Job for submitter Pods and logs. Ownership identifies the RayJob, not a particular attempt.</p>
    {(job.metadata.deletionTimestamp || ['Retrying', 'Suspending'].includes(root.status?.jobDeploymentStatus)) && <p className="text-xs text-theme-text-secondary">The submitter may be part of the attempt being cleaned up.</p>}
  </div>
}

function SelectedCluster({ namespace, name }: { namespace: string; name: string }) {
  const query = useResource<any>('rayclusters', namespace, name, 'ray.io')
  if (query.error) return <AlertBanner variant="warning" title="Selected cluster unavailable" message={query.error instanceof ApiError && query.error.status === 404 ? 'The selected existing RayCluster was not found. RayJob initialization requires that cluster.' : query.error.message} />
  if (!query.data) return <p className="text-xs text-theme-text-secondary">Checking selected RayCluster…</p>
  return <p className="text-xs text-theme-text-secondary">Selected RayCluster is present. It may be shared; its health is separate from this application.</p>
}

export function RayJobRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const local = rayJobIsLocal(data)
  const selected = data.spec?.clusterSelector?.['ray.io/cluster']
  return <BaseRenderer data={data} onNavigate={onNavigate}
    admissionContent={<KueueAdmission resource={data} namespace={data.metadata.namespace} name={data.metadata.name} onNavigate={onNavigate} presentation="card" />}
    submitterEvidence={local && rayJobSubmissionMode(data) === 'K8sJobMode' && data.metadata.uid ? <Submitter root={data} onNavigate={onNavigate} /> : undefined}
    selectedClusterEvidence={local && selected ? <SelectedCluster namespace={data.metadata.namespace} name={selected} /> : undefined} />
}
