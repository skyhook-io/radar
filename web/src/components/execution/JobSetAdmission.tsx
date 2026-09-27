import { KueueAdmissionSection } from '@skyhook-io/k8s-ui/components/resources/KueueAdmissionSection'
import { ApiError, useKueueAdmission } from '../../api/client'

export function KueueAdmission({ resource, namespace, name, onNavigate, presentation }: {
  presentation?: 'card' | 'drawer'
  resource: any
  namespace: string
  name: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const isJob = resource.apiVersion === 'batch/v1' && resource.kind === 'Job'
  const isRayJob = resource.apiVersion === 'ray.io/v1' && resource.kind === 'RayJob'
  const labels = resource.metadata?.labels
  const annotations = resource.metadata?.annotations
  const externalExecution = Boolean(resource.spec?.managedBy && resource.spec.managedBy !== (isRayJob ? 'ray.io/kuberay-operator' : isJob ? 'kubernetes.io/job-controller' : 'jobset.sigs.k8s.io/jobset-controller'))
  const hinted = Boolean(labels?.['kueue.x-k8s.io/queue-name'] || labels?.['kueue.x-k8s.io/prebuilt-workload-name'] || annotations?.['kueue.x-k8s.io/prebuilt-workload-name'] || resource.spec?.managedBy === 'kueue.x-k8s.io/multikueue' || (!isJob && !isRayJob && externalExecution))
  const terminal = (isRayJob && ['Complete', 'Failed', 'ValidationFailed'].includes(resource.status?.jobDeploymentStatus)) || (resource.status?.conditions ?? []).some((c: any) => ['Complete', 'Failed'].includes(c.type) && c.status === 'True')
  const query = useKueueAdmission(namespace, name, resource.metadata?.uid, { isJob, isRayJob, hinted, terminal })
  const data = query.data?.uid === resource.metadata?.uid ? query.data : undefined
  const error = query.error?.message ?? (query.data && !data ? 'Admission evidence belongs to a different workload instance; waiting for a fresh observation.' : undefined)
  if ((isJob || isRayJob) && !hinted && !data?.workloads.length) return null
  const hasOwner = (resource.metadata?.ownerReferences ?? []).some((owner: any) => owner.controller === true && !(owner.apiVersion === 'batch/v1' && owner.kind === 'CronJob'))
  return <KueueAdmissionSection presentation={presentation} data={data} loading={query.isLoading} error={error} forbidden={query.error instanceof ApiError && query.error.status === 403} hinted={hinted} externalExecution={externalExecution} hasOwner={isJob && hasOwner} onRetry={() => { void query.refetch() }} onNavigate={onNavigate} />
}

export { KueueAdmission as JobSetAdmission }
