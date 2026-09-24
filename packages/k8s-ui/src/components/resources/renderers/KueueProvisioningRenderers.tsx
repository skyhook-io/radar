import { Boxes, Gauge, ListChecks, Settings } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { AlertBanner, ConditionsSection, Property, PropertyList, ResourceLink, Section, type ConditionTone } from '../../ui/drawer-components'
import { getAdmissionCheckStatus, getProvisioningRequestStatus, getProvisioningRequestMessage, getProvisioningRequestStatusCondition, isKueueConditionStale } from '../resource-utils-kueue'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function AdmissionCheckRenderer({ data, onNavigate }: Props) {
  const spec = data.spec ?? {}
  const condition = data.status?.conditions?.find((c: any) => c.type === 'Active')
  const status = getAdmissionCheckStatus(data)
  const parameters = spec.parameters
  const knownConfig = parameters?.apiGroup === 'kueue.x-k8s.io' && parameters?.kind === 'ProvisioningRequestConfig'
  return <>
    <Section title="Check Controller" icon={ListChecks} defaultExpanded>
      <PropertyList>
        <Property label="Reported State" value={<Badge colorClass={status.color}>{status.text}</Badge>} />
        <Property label="Controller" value={<span className="break-all">{spec.controllerName || 'Not specified'}</span>} />
        {condition?.reason && <Property label="Reason" value={condition.reason} />}
      </PropertyList>
      {condition?.message && <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{condition.message}</p>}
      <p className="mt-2 text-xs text-theme-text-tertiary">Active means the controller is ready to evaluate checks. Each Workload reports its own Pending, Ready, Retry or Rejected result.</p>
      {!condition && <p className="mt-2 text-sm text-theme-text-tertiary">Controller readiness has not been reported.</p>}
      {status.text === 'Stale' && <p className="mt-2 text-sm text-warning-text">Reported readiness describes an earlier generation.</p>}
    </Section>
    {parameters && <Section title="Check Configuration" icon={Settings} defaultExpanded>
      <PropertyList>
        <Property label="Kind" value={parameters.kind} />
        <Property label="API Group" value={parameters.apiGroup} />
        <Property label="Name" value={knownConfig && parameters.name ? <ResourceLink name={parameters.name} kind="ProvisioningRequestConfig" group={parameters.apiGroup} onNavigate={onNavigate} /> : parameters.name} />
      </PropertyList>
    </Section>}
    {data.apiVersion === 'kueue.x-k8s.io/v1beta1' && spec.retryDelayMinutes != null && <PropertyList><Property label="Retry Delay (deprecated)" value={`${spec.retryDelayMinutes} minutes`} /></PropertyList>}
    <ConditionsSection conditions={data.status?.conditions} getConditionTone={c => isKueueConditionStale(c, data.metadata?.generation) ? 'unknown' : c.type === 'Active' ? c.status === 'True' ? 'ok' : c.status === 'False' ? 'warning' : 'unknown' : 'unknown'} defaultExpanded />
  </>
}

export function provisioningConditionTone(condition: any): ConditionTone {
  if (condition.status !== 'True' && condition.status !== 'False') return 'unknown'
  if (['Failed', 'CapacityRevoked'].includes(condition.type)) return condition.status === 'True' ? 'fail' : 'ok'
  if (['Accepted', 'Provisioned'].includes(condition.type)) return condition.status === 'True' ? 'ok' : 'unknown'
  return 'unknown'
}

export function ProvisioningRequestRenderer({ data, onNavigate }: Props) {
  const spec = data.spec ?? {}
  const status = getProvisioningRequestStatus(data)
  const condition = getProvisioningRequestStatusCondition(data)
  const message = getProvisioningRequestMessage(data)
  const stale = isKueueConditionStale(condition, data.metadata?.generation)
  const namespace = data.metadata?.namespace ?? ''
  const owner = data.metadata?.ownerReferences?.find((ref: any) => ref.controller === true && ref.kind === 'Workload' && ['kueue.x-k8s.io/v1beta1', 'kueue.x-k8s.io/v1beta2'].includes(ref.apiVersion))
  const podSets = spec.podSets ?? []
  return <>
    {stale && <AlertBanner variant="info" title="Reported state describes an earlier generation" message="Inspect the conditions before treating this outcome as current." />}
    <Section title="Provisioning" icon={Gauge} defaultExpanded>
      <PropertyList>
        <Property label="Reported State" value={<Badge colorClass={status.color}>{status.text}</Badge>} />
        <Property label="Provisioning Class" value={<span className="break-all">{spec.provisioningClassName || 'Not specified'}</span>} />
        {condition?.reason && <Property label="Reason" value={condition.reason} />}
        {owner && <Property label="Owner Workload" value={<ResourceLink kind="workloads" group="kueue.x-k8s.io" namespace={namespace} name={owner.name} onNavigate={onNavigate} />} />}
      </PropertyList>
      {message && <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{message}</p>}
      {!condition && <p className="mt-2 text-sm text-theme-text-tertiary">No provisioning outcome has been reported. Inspect any conditions below.</p>}
      <p className="mt-2 text-xs text-theme-text-tertiary">Controller-reported provisioning outcome, not a live check of Nodes or running Pods. Accepted means the request was picked up; Provisioned does not mean the Workload is running.</p>
      {condition?.type === 'BookingExpired' && <p className="mt-2 text-xs text-theme-text-tertiary">The capacity booking expired. This alone does not establish a Workload failure; inspect its admission and execution state.</p>}
    </Section>
    <ConditionsSection conditions={data.status?.conditions} getConditionTone={c => isKueueConditionStale(c, data.metadata?.generation) ? 'unknown' : provisioningConditionTone(c)} defaultExpanded />
    <Section title={`Requested Pod Sets (${podSets.length})`} icon={Boxes} defaultExpanded>
      <p className="mb-2 text-xs text-theme-text-tertiary">Requested Pod counts and templates, not observed Pods or allocated devices.</p>
      <div className="space-y-2">
        {podSets.map((set: any, index: number) => <div className="card-inner break-words [&_button]:text-left" key={index}>
          <PropertyList>
            <Property label="Pod Template" value={set.podTemplateRef?.name ? <ResourceLink kind="PodTemplate" group="" namespace={namespace} name={set.podTemplateRef.name} onNavigate={onNavigate} /> : 'Not specified'} />
            <Property label="Requested Pods" value={set.count ?? 'Not specified'} />
          </PropertyList>
        </div>)}
        {podSets.length === 0 && <p className="text-sm text-theme-text-tertiary">No PodSets declared.</p>}
      </div>
    </Section>
    {Object.keys(spec.parameters ?? {}).length > 0 && <Section title="Provider Parameters" icon={Settings} defaultExpanded={false}>
      <PropertyList>{Object.entries(spec.parameters).map(([key, value]) => <Property key={key} label={key} value={<span className="whitespace-pre-wrap break-all">{String(value)}</span>} />)}</PropertyList>
    </Section>}
    {Object.keys(data.status?.provisioningClassDetails ?? {}).length > 0 && <Section title="Provider Details" icon={Settings} defaultExpanded={false}>
      <PropertyList>{Object.entries(data.status.provisioningClassDetails).map(([key, value]) => <Property key={key} label={key} value={<span className="whitespace-pre-wrap break-all">{String(value)}</span>} />)}</PropertyList>
    </Section>}
  </>
}
