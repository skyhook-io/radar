import { Server, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { getMachineSetStatus, getMachineClusterName } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CAPIMachineSetRenderer({ data, onNavigate }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const msStatus = getMachineSetStatus(data)
  const isFailed = msStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const clusterName = getMachineClusterName(data)
  const desired = spec.replicas ?? 0
  const ready = status.readyReplicas ?? 0
  const available = status.availableReplicas ?? 0
  const upToDate = status.upToDateReplicas ?? status.updatedReplicas ?? 0
  const deletePolicy = spec.deletePolicy || 'Random'
  const infraRef = spec.template?.spec?.infrastructureRef || {}
  const bootstrapRef = spec.template?.spec?.bootstrap?.configRef || {}

  return (
    <>
      {isFailed && (
        <AlertBanner
          variant="error"
          title="MachineSet Not Ready"
          message={readyCond?.message || 'MachineSet is not ready.'}
        />
      )}

      <Section title="Overview" icon={Server}>
        <PropertyList>
          <Property label="Cluster" value={clusterName} />
          <Property label="Delete Policy" value={deletePolicy} />
          {spec.minReadySeconds != null && (
            <Property label="Min Ready Seconds" value={String(spec.minReadySeconds)} />
          )}
          {readyCond?.lastTransitionTime && (
            <Property label="Since" value={formatAge(readyCond.lastTransitionTime)} />
          )}
        </PropertyList>
      </Section>

      <Section title="Replicas" icon={Server}>
        <PropertyList>
          <Property label="Desired" value={String(desired)} />
          <Property label="Ready" value={String(ready)} />
          <Property label="Available" value={String(available)} />
          <Property label="Up-to-date" value={String(upToDate)} />
        </PropertyList>
      </Section>

      {(infraRef.kind || bootstrapRef.kind) && (
        <Section title="Machine Template" icon={Settings}>
          <PropertyList>
            {infraRef.kind && (
              <Property label="Infrastructure" value={
                <ResourceLink
                  name={infraRef.name}
                  kind={kindToPlural(infraRef.kind)}
                  namespace={infraRef.namespace || data.metadata?.namespace}
                  group={infraRef.apiVersion?.split('/')?.[0]}
                  label={`${infraRef.kind}/${infraRef.name}`}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {bootstrapRef.kind && (
              <Property label="Bootstrap" value={
                <ResourceLink
                  name={bootstrapRef.name}
                  kind={kindToPlural(bootstrapRef.kind)}
                  namespace={bootstrapRef.namespace || data.metadata?.namespace}
                  group={bootstrapRef.apiVersion?.split('/')?.[0]}
                  label={`${bootstrapRef.kind}/${bootstrapRef.name}`}
                  onNavigate={onNavigate}
                />
              } />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Owned Machines hint */}
      <div className="px-3 py-1.5 text-xs text-theme-text-tertiary">
        Machines with label <code className="inline-code text-[10px] select-all">cluster.x-k8s.io/set-name={data.metadata?.name}</code>
      </div>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
