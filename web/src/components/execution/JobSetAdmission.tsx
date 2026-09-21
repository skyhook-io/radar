import { KueueAdmissionSection } from '@skyhook-io/k8s-ui/components/resources/KueueAdmissionSection'
import { ApiError, useKueueAdmission } from '../../api/client'

export function JobSetAdmission({ resource, namespace, name, onNavigate }: {
  resource: any
  namespace: string
  name: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const query = useKueueAdmission(namespace, name)
  const labels = resource.metadata?.labels
  const externalExecution = Boolean(resource.spec?.managedBy && resource.spec.managedBy !== 'jobset.sigs.k8s.io/jobset-controller')
  const hinted = Boolean(labels?.['kueue.x-k8s.io/queue-name'] || labels?.['kueue.x-k8s.io/prebuilt-workload-name'] || externalExecution)
  return <KueueAdmissionSection data={query.data} loading={query.isLoading} error={query.error?.message} forbidden={query.error instanceof ApiError && query.error.status === 403} hinted={hinted} externalExecution={externalExecution} onRetry={() => { void query.refetch() }} onNavigate={onNavigate} />
}
