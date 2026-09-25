import { JobRenderer as BaseJobRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/JobRenderer'
import { JobSetRenderer as BaseJobSetRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/JobSetRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { KueueAdmission } from '../../execution/JobSetAdmission'

type Props = { data: any; onNavigate?: (ref: ResourceRef) => void }

export function JobRenderer({ data, onNavigate }: Props) {
  return <BaseJobRenderer data={data} admissionContent={
    data.apiVersion === 'batch/v1' && data.kind === 'Job' && <KueueAdmission presentation="drawer" resource={data} namespace={data.metadata.namespace} name={data.metadata.name} onNavigate={onNavigate} />
  } />
}

export function JobSetRenderer({ data, onNavigate }: Props) {
  return <BaseJobSetRenderer data={data} admissionContent={
    <KueueAdmission presentation="drawer" resource={data} namespace={data.metadata.namespace} name={data.metadata.name} onNavigate={onNavigate} />
  } />
}
