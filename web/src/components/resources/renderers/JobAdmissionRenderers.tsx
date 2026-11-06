import { JobRenderer as BaseJobRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/JobRenderer'
import { JobSetRenderer as BaseJobSetRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/JobSetRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { KueueAdmission } from '../../execution/JobSetAdmission'

type Props = { data: any; onNavigate?: (ref: ResourceRef) => void }

export function JobRenderer({ data, onNavigate }: Props) {
  if (data.apiVersion !== 'batch/v1' || data.kind !== 'Job') return <BaseJobRenderer data={data} />
  return <KueueAdmission presentation="drawer" resource={data} namespace={data.metadata.namespace} name={data.metadata.name} onNavigate={onNavigate}>
    {(admissionContent, kueueManaged) => <BaseJobRenderer data={data} admissionContent={admissionContent} kueueManaged={kueueManaged} />}
  </KueueAdmission>
}

export function JobSetRenderer({ data, onNavigate }: Props) {
  return <BaseJobSetRenderer data={data} admissionContent={
    <KueueAdmission presentation="drawer" resource={data} namespace={data.metadata.namespace} name={data.metadata.name} onNavigate={onNavigate} />
  } />
}
