import { Server, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown} from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { getMachinePoolStatus } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CAPIMachinePoolRenderer({ data, onNavigate }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const mpStatus = getMachinePoolStatus(data)
  const isFailed = mpStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const clusterName = spec.clusterName || data.metadata?.labels?.['cluster.x-k8s.io/cluster-name'] || '-'
  const phase = status.phase || 'Unknown'
  const desired = spec.replicas ?? 0
  const ready = status.readyReplicas ?? 0
  const infraRef = spec.template?.spec?.infrastructureRef || {}
  const bootstrapRef = spec.template?.spec?.bootstrap?.configRef || {}

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="MachinePool Not Ready"
          message={readyCond?.message || `MachinePool is in ${phase} state.`}
        />
      )}

      <Section title="Overview" icon={Server}>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Cluster" value={clusterName} />
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

      <ConditionsSection conditions={conditions} />
    </>
  )
}
